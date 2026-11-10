"""Sessions, CSRF, input validation and S3 error mapping."""
import hmac
from typing import Annotated

from botocore.exceptions import BotoCoreError, ClientError, EndpointConnectionError
from fastapi import Cookie, Depends, HTTPException, Request
from itsdangerous import BadSignature, SignatureExpired, URLSafeTimedSerializer

from app import config

S3_ERRORS = (ClientError, BotoCoreError, EndpointConnectionError)


def signed_serializer() -> URLSafeTimedSerializer:
    if not config.SESSION_SECRET or len(config.SESSION_SECRET) < 32:
        raise RuntimeError('SESSION_SECRET must have at least 32 characters')
    return URLSafeTimedSerializer(config.SESSION_SECRET, salt='storzo-session-v1')


def session_payload(session: str | None) -> dict:
    if not session:
        raise HTTPException(401, 'Login required')
    try:
        data = signed_serializer().loads(session, max_age=config.SESSION_TTL)
    except (BadSignature, SignatureExpired):
        raise HTTPException(401, 'Session expired')
    if data.get('user') != config.ADMIN_USER or not data.get('csrf'):
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
    if not config.BUCKET_RE.fullmatch(bucket) or '..' in bucket or '.-' in bucket or '-.' in bucket:
        raise HTTPException(400, 'Invalid S3 bucket name: use 3-63 lowercase letters, numbers, hyphens or dots, starting and ending with a letter or number.')
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
