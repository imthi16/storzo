"""Storzo: self-hosted S3 object/image management API.

This is a management plane over SeaweedFS, not a custom production S3 engine.
The S3 data plane is reached directly by S3 SDKs.
"""
from __future__ import annotations

import hashlib
import hmac
import io
import os
import re
import secrets
import sqlite3
import time
from datetime import datetime, timezone
from functools import lru_cache
from pathlib import Path
from typing import Annotated
from urllib.parse import quote

import boto3
from botocore.config import Config
from botocore.exceptions import BotoCoreError, ClientError, EndpointConnectionError
from fastapi import Cookie, Depends, FastAPI, File, Form, Header, HTTPException, Query, Request, Response, UploadFile
from fastapi.responses import JSONResponse, StreamingResponse
from itsdangerous import BadSignature, SignatureExpired, URLSafeTimedSerializer
from PIL import Image, ImageOps, UnidentifiedImageError
from pydantic import BaseModel

APP_NAME = 'Storzo'
S3_ENDPOINT = os.getenv('S3_ENDPOINT', 'http://127.0.0.1:8333')
S3_PUBLIC_ENDPOINT = os.getenv('S3_PUBLIC_ENDPOINT', 'http://127.0.0.1:8333')
S3_ACCESS_KEY = os.getenv('S3_ACCESS_KEY', '')
S3_SECRET_KEY = os.getenv('S3_SECRET_KEY', '')
ADMIN_USER = os.getenv('ADMIN_USER', 'admin')
ADMIN_PASSWORD = os.getenv('ADMIN_PASSWORD', '')
SESSION_SECRET = os.getenv('SESSION_SECRET', '')
COOKIE_SECURE = os.getenv('COOKIE_SECURE', 'false').lower() == 'true'
DB_PATH = Path(os.getenv('AUDIT_DB_PATH', '/tmp/storzo-audit.db'))
MAX_UPLOAD_MB = min(500, max(1, int(os.getenv('MAX_UPLOAD_MB', '50'))))
THUMB_MAX_MB = 12
SESSION_TTL = 8 * 60 * 60
BUCKET_RE = re.compile(r'^(?!\d+\.\d+\.\d+\.\d+$)[a-z0-9](?:[a-z0-9.-]{1,61})[a-z0-9]$')
Image.MAX_IMAGE_PIXELS = 25_000_000

app = FastAPI(title=APP_NAME + ' Management API', version='0.1.0', docs_url=None, redoc_url=None)


def signed_serializer() -> URLSafeTimedSerializer:
    if not SESSION_SECRET or len(SESSION_SECRET) < 32:
        raise RuntimeError('SESSION_SECRET must have at least 32 characters')
    return URLSafeTimedSerializer(SESSION_SECRET, salt='storzo-session-v1')


@lru_cache(maxsize=2)
def s3_client(public: bool = False):
    if not S3_ACCESS_KEY or not S3_SECRET_KEY:
        raise RuntimeError('S3 credentials not configured')
    return boto3.client(
        's3', endpoint_url=S3_PUBLIC_ENDPOINT if public else S3_ENDPOINT,
        aws_access_key_id=S3_ACCESS_KEY, aws_secret_access_key=S3_SECRET_KEY,
        region_name='us-east-1',
        config=Config(signature_version='s3v4', s3={'addressing_style': 'path'},
                      retries={'max_attempts': 3, 'mode': 'standard'}),
    )


def init_db():
    DB_PATH.parent.mkdir(parents=True, exist_ok=True)
    with sqlite3.connect(DB_PATH, timeout=10) as db:
        db.execute('PRAGMA journal_mode=WAL')
        db.execute('PRAGMA busy_timeout=5000')
        db.execute('''CREATE TABLE IF NOT EXISTS audit (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            ts TEXT NOT NULL, actor TEXT NOT NULL, action TEXT NOT NULL,
            bucket TEXT, object_key TEXT, detail TEXT
        )''')


@app.on_event('startup')
def startup():
    # Fail closed: no default admin credentials in a deployed container.
    if not ADMIN_PASSWORD or len(ADMIN_PASSWORD) < 16:
        raise RuntimeError('ADMIN_PASSWORD must have at least 16 characters')
    signed_serializer()
    if not S3_ACCESS_KEY or not S3_SECRET_KEY:
        raise RuntimeError('S3 credentials must be set')
    init_db()


def audit(action: str, bucket: str | None = None, key: str | None = None, detail: str | None = None):
    with sqlite3.connect(DB_PATH, timeout=10) as db:
        db.execute('INSERT INTO audit(ts,actor,action,bucket,object_key,detail) VALUES (?,?,?,?,?,?)',
                   (datetime.now(timezone.utc).isoformat(), ADMIN_USER, action, bucket, key, detail))


def session_payload(session: str | None) -> dict:
    if not session:
        raise HTTPException(401, 'Login required')
    try:
        data = signed_serializer().loads(session, max_age=SESSION_TTL)
    except (BadSignature, SignatureExpired):
        raise HTTPException(401, 'Session expired')
    if data.get('user') != ADMIN_USER or not data.get('csrf'):
        raise HTTPException(401, 'Invalid session')
    return data


def require_login(storzo_session: Annotated[str | None, Cookie()] = None):
    return session_payload(storzo_session)


def require_write(request: Request, sess: dict = Depends(require_login)):
    token = request.headers.get('x-csrf-token', '')
    if not hmac.compare_digest(token, sess['csrf']):
        raise HTTPException(403, 'CSRF validation failed')
    return sess


def safe_key(key: str):
    if (not key or key.startswith('/') or '\\' in key or '\x00' in key or
            '..' in key.split('/') or len(key.encode('utf-8')) > 1024):
        raise HTTPException(400, 'Invalid object key')
    return key


def safe_bucket(bucket: str):
    if not BUCKET_RE.fullmatch(bucket) or '..' in bucket or '.-' in bucket or '-.' in bucket:
        raise HTTPException(400, 'Invalid S3 bucket name')
    return bucket


def s3_failure(e: Exception):
    if isinstance(e, ClientError):
        code = e.response.get('Error', {}).get('Code', '')
        if code in ('NoSuchBucket', 'NoSuchKey', '404', 'NotFound'):
            raise HTTPException(404, 'Object or bucket not found')
        if code in ('BucketAlreadyExists', 'BucketAlreadyOwnedByYou'):
            raise HTTPException(409, 'Bucket already exists')
        if code in ('BucketNotEmpty',):
            raise HTTPException(409, 'Bucket is not empty')
        if code in ('AccessDenied', '403'):
            raise HTTPException(403, 'Storage access denied')
    raise HTTPException(503, 'Storage service unavailable or request failed')


class Login(BaseModel):
    username: str
    password: str


class BucketCreate(BaseModel):
    name: str


@app.get('/api/health/live')
def live():
    return {'status': 'alive', 'service': APP_NAME}


@app.get('/api/health/ready')
def ready():
    try:
        s3_client().list_buckets()
        return {'status': 'ready', 'storage': 'reachable'}
    except Exception:
        raise HTTPException(503, 'Storage not reachable')


@app.post('/api/auth/login')
def login(body: Login, response: Response):
    if not (hmac.compare_digest(body.username.encode("utf-8"), ADMIN_USER.encode("utf-8")) and
            hmac.compare_digest(body.password.encode("utf-8"), ADMIN_PASSWORD.encode("utf-8"))):
        # Fixed-delay protection is better enforced by a gateway rate limit.
        raise HTTPException(401, 'Invalid credentials')
    csrf = secrets.token_urlsafe(24)
    token = signed_serializer().dumps({'user': ADMIN_USER, 'csrf': csrf})
    response.set_cookie('storzo_session', token, httponly=True, secure=COOKIE_SECURE,
                        samesite='strict', max_age=SESSION_TTL, path='/api')
    audit('login')
    return {'username': ADMIN_USER, 'csrf': csrf}


@app.get('/api/auth/me')
def me(sess: dict = Depends(require_login)):
    return {'username': ADMIN_USER, 'csrf': sess['csrf']}


@app.post('/api/auth/logout')
def logout(response: Response, _sess: dict = Depends(require_write)):
    response.delete_cookie('storzo_session', path='/api', secure=COOKIE_SECURE, samesite='strict')
    return {'ok': True}


@app.get('/api/buckets')
def buckets(_sess: dict = Depends(require_login)):
    try:
        result = s3_client().list_buckets()
        return {'items': [{'name': b['Name'], 'created': b.get('CreationDate').isoformat() if b.get('CreationDate') else ''}
                          for b in result.get('Buckets', [])]}
    except (ClientError, BotoCoreError, EndpointConnectionError) as exc:
        s3_failure(exc)


@app.post('/api/buckets', status_code=201)
def create_bucket(body: BucketCreate, _sess: dict = Depends(require_write)):
    name = safe_bucket(body.name)
    try:
        s3_client().create_bucket(Bucket=name)
        audit('create_bucket', name)
        return {'name': name}
    except (ClientError, BotoCoreError, EndpointConnectionError) as exc:
        s3_failure(exc)


@app.delete('/api/buckets/{bucket}')
def delete_bucket(bucket: str, _sess: dict = Depends(require_write)):
    safe_bucket(bucket)
    try:
        s3_client().delete_bucket(Bucket=bucket)
        audit('delete_bucket', bucket)
        return {'ok': True}
    except (ClientError, BotoCoreError, EndpointConnectionError) as exc:
        s3_failure(exc)


@app.get('/api/buckets/{bucket}/objects')
def objects(bucket: str, prefix: str = '', continuation: str = '', limit: int = Query(100, ge=1, le=500),
            _sess: dict = Depends(require_login)):
    safe_bucket(bucket)
    if prefix:
        safe_key(prefix)
    args = {'Bucket': bucket, 'Prefix': prefix, 'MaxKeys': limit, 'Delimiter': '/'}
    if continuation:
        args['ContinuationToken'] = continuation
    try:
        result = s3_client().list_objects_v2(**args)
        items = [{'key': item['Key'], 'size': item['Size'],
                  'modified': item['LastModified'].isoformat() if item.get('LastModified') else '',
                  'etag': item.get('ETag', '').strip('"')}
                 for item in result.get('Contents', []) if item['Key'] != prefix]
        return {'items': items, 'folders': [p['Prefix'] for p in result.get('CommonPrefixes', [])],
                'continuation': result.get('NextContinuationToken'), 'prefix': prefix}
    except (ClientError, BotoCoreError, EndpointConnectionError) as exc:
        s3_failure(exc)


def get_checked_object(bucket: str, key: str):
    safe_bucket(bucket)
    safe_key(key)
    try:
        return s3_client().get_object(Bucket=bucket, Key=key)
    except (ClientError, BotoCoreError, EndpointConnectionError) as exc:
        s3_failure(exc)


def stream_object(bucket: str, key: str, preview: bool):
    obj = get_checked_object(bucket, key)
    mime = obj.get('ContentType', 'application/octet-stream')
    if preview and mime not in {'image/jpeg', 'image/png', 'image/webp', 'image/gif', 'image/avif'}:
        obj['Body'].close()
        raise HTTPException(415, 'Preview allowed only for raster images')
    def generator():
        try:
            for chunk in obj['Body'].iter_chunks(chunk_size=65536):
                if chunk:
                    yield chunk
        finally:
            obj['Body'].close()
    name = key.rsplit('/', 1)[-1].replace('\r', '').replace('\n', '')
    ascii_name = ''.join(c if c.isascii() and c not in '"\\' and ord(c) >= 32 else '_' for c in name)
    # The ASCII fallback prevents response header encoding failures for Unicode file names.
    disposition = (
        f'{"inline" if preview else "attachment"}; filename="{ascii_name}"; '
        f"filename*=UTF-8''{quote(name, safe='')}"
    )
    headers = {'X-Content-Type-Options': 'nosniff',
               'Content-Disposition': disposition,
               'Cache-Control': 'private, max-age=60' if preview else 'no-store'}
    if obj.get('ContentLength') is not None:
        headers['Content-Length'] = str(obj['ContentLength'])
    return StreamingResponse(generator(), media_type=mime, headers=headers)


@app.get('/api/objects/download')
def download(bucket: str, key: str, _sess: dict = Depends(require_login)):
    return stream_object(bucket, key, preview=False)


@app.get('/api/objects/preview')
def preview(bucket: str, key: str, _sess: dict = Depends(require_login)):
    return stream_object(bucket, key, preview=True)


@app.get('/api/objects/thumbnail')
def thumbnail(bucket: str, key: str, width: int = Query(256, ge=32, le=1024),
              _sess: dict = Depends(require_login)):
    obj = get_checked_object(bucket, key)
    body = obj['Body']
    try:
        if obj.get('ContentLength', THUMB_MAX_MB * 1024 * 1024 + 1) > THUMB_MAX_MB * 1024 * 1024:
            raise HTTPException(413, 'Image too large for thumbnail')
        raw = body.read(THUMB_MAX_MB * 1024 * 1024 + 1)
        if len(raw) > THUMB_MAX_MB * 1024 * 1024:
            raise HTTPException(413, 'Image too large for thumbnail')
    finally:
        body.close()
    try:
        with Image.open(io.BytesIO(raw)) as img:
            if img.format not in {'JPEG', 'PNG', 'WEBP', 'GIF', 'AVIF'}:
                raise HTTPException(415, 'Unsupported image format')
            out = ImageOps.exif_transpose(img)
            out.thumbnail((width, width))
            out = out.convert('RGB')
            stream = io.BytesIO()
            out.save(stream, format='WEBP', quality=78, method=4)
            stream.seek(0)
            return Response(content=stream.getvalue(), media_type='image/webp',
                            headers={'Cache-Control': 'private, max-age=120', 'X-Content-Type-Options': 'nosniff'})
    except (UnidentifiedImageError, OSError, Image.DecompressionBombError, Image.DecompressionBombWarning):
        raise HTTPException(415, 'Invalid or unsafe image')


@app.post('/api/objects/upload', status_code=201)
def upload(bucket: str = Form(...), key: str = Form(...), file: UploadFile = File(...),
           _sess: dict = Depends(require_write)):
    safe_bucket(bucket)
    safe_key(key)
    max_bytes = MAX_UPLOAD_MB * 1024 * 1024
    size, digest = 0, hashlib.sha256()
    # UploadFile is spooled to disk by Starlette; check limit before transfer.
    while data := file.file.read(1024 * 1024):
        size += len(data)
        if size > max_bytes:
            raise HTTPException(413, f'Upload exceeds {MAX_UPLOAD_MB} MB limit')
        digest.update(data)
    file.file.seek(0)
    if size == 0:
        raise HTTPException(400, 'Empty files are not supported in management UI')
    ctype = file.content_type or 'application/octet-stream'
    if ctype in {'image/svg+xml', 'text/html', 'application/javascript'}:
        raise HTTPException(415, 'Potentially executable file type not allowed via UI')
    try:
        # sha256 stored as object metadata to make verification easier.
        s3_client().upload_fileobj(file.file, bucket, key,
                                   ExtraArgs={'ContentType': ctype, 'Metadata': {'sha256': digest.hexdigest()}})
        audit('upload', bucket, key, f'bytes={size};sha256={digest.hexdigest()}')
        return {'bucket': bucket, 'key': key, 'bytes': size, 'sha256': digest.hexdigest()}
    except (ClientError, BotoCoreError, EndpointConnectionError) as exc:
        s3_failure(exc)
    finally:
        file.file.close()


@app.delete('/api/objects')
def delete_object(bucket: str, key: str, _sess: dict = Depends(require_write)):
    safe_bucket(bucket)
    safe_key(key)
    try:
        # S3 delete is idempotent; caller should confirm existence in UI first.
        s3_client().delete_object(Bucket=bucket, Key=key)
        audit('delete', bucket, key)
        return {'ok': True}
    except (ClientError, BotoCoreError, EndpointConnectionError) as exc:
        s3_failure(exc)


@app.get('/api/objects/presign')
def presign(bucket: str, key: str, expires: int = Query(300, ge=60, le=3600),
            _sess: dict = Depends(require_login)):
    safe_bucket(bucket)
    safe_key(key)
    try:
        url = s3_client(public=True).generate_presigned_url('get_object',
                 Params={'Bucket': bucket, 'Key': key}, ExpiresIn=expires)
        audit('presign', bucket, key, f'expires={expires}')
        return {'url': url, 'expires_seconds': expires}
    except (ClientError, BotoCoreError, EndpointConnectionError) as exc:
        s3_failure(exc)


@app.get('/api/audit')
def audit_logs(limit: int = Query(50, ge=1, le=200), _sess: dict = Depends(require_login)):
    with sqlite3.connect(DB_PATH, timeout=10) as db:
        db.row_factory = sqlite3.Row
        rows = db.execute('SELECT id,ts,actor,action,bucket,object_key,detail FROM audit ORDER BY id DESC LIMIT ?', (limit,)).fetchall()
    return {'items': [dict(row) for row in rows]}


@app.get('/api/dashboard')
def dashboard(_sess: dict = Depends(require_login)):
    try:
        buckets_ = s3_client().list_buckets().get('Buckets', [])
        total, bytes_, images, scanned, capped = 0, 0, 0, 0, False
        for bucket in buckets_:
            token = None
            while scanned < 3000:
                args = {'Bucket': bucket['Name'], 'MaxKeys': min(500, 3000 - scanned)}
                if token:
                    args['ContinuationToken'] = token
                res = s3_client().list_objects_v2(**args)
                for obj in res.get('Contents', []):
                    total += 1
                    bytes_ += obj.get('Size', 0)
                    images += int(obj['Key'].lower().endswith(('.jpg', '.jpeg', '.png', '.webp', '.gif', '.avif')))
                scanned += len(res.get('Contents', []))
                token = res.get('NextContinuationToken')
                if not token:
                    break
                if scanned >= 3000:
                    capped = True
            if scanned >= 3000:
                capped = True
                break
        return {'bucket_count': len(buckets_), 'object_count': total, 'total_bytes': bytes_,
                'image_count': images, 'sampled': capped,
                'message': 'Statistics scan is capped at 3000 objects; not full billing metrics.' if capped else ''}
    except (ClientError, BotoCoreError, EndpointConnectionError) as exc:
        s3_failure(exc)
