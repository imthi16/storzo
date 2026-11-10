import hashlib
import io
from urllib.parse import quote

from fastapi import APIRouter, Depends, File, Form, HTTPException, Query, Response, UploadFile
from fastapi.responses import StreamingResponse
from PIL import Image, ImageOps, UnidentifiedImageError

from app import config, s3
from app.db import audit
from app.security import S3_ERRORS, require_login, require_write, s3_failure, safe_bucket, safe_key

Image.MAX_IMAGE_PIXELS = 25_000_000
router = APIRouter(prefix='/api/objects', tags=['objects'])
PREVIEW_TYPES = {'image/jpeg', 'image/png', 'image/webp', 'image/gif', 'image/avif'}


def get_checked_object(bucket: str, key: str):
    safe_bucket(bucket)
    safe_key(key)
    try:
        return s3.s3_client().get_object(Bucket=bucket, Key=key)
    except S3_ERRORS as exc:
        s3_failure(exc)


def stream_object(bucket: str, key: str, preview: bool):
    obj = get_checked_object(bucket, key)
    mime = obj.get('ContentType', 'application/octet-stream')
    if preview and mime not in PREVIEW_TYPES:
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


@router.get('/download')
def download(bucket: str, key: str, _sess: dict = Depends(require_login)):
    return stream_object(bucket, key, preview=False)


@router.get('/preview')
def preview(bucket: str, key: str, _sess: dict = Depends(require_login)):
    return stream_object(bucket, key, preview=True)


@router.get('/thumbnail')
def thumbnail(bucket: str, key: str, width: int = Query(256, ge=32, le=1024),
              _sess: dict = Depends(require_login)):
    obj = get_checked_object(bucket, key)
    body = obj['Body']
    limit = config.THUMB_MAX_MB * 1024 * 1024
    try:
        if obj.get('ContentLength', limit + 1) > limit:
            raise HTTPException(413, 'Image too large for thumbnail')
        raw = body.read(limit + 1)
        if len(raw) > limit:
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


@router.post('/upload', status_code=201)
def upload(bucket: str = Form(...), key: str = Form(...), file: UploadFile = File(...),
           _sess: dict = Depends(require_write)):
    safe_bucket(bucket)
    safe_key(key)
    max_bytes = config.MAX_UPLOAD_MB * 1024 * 1024
    size, digest = 0, hashlib.sha256()
    # UploadFile is spooled to disk by Starlette; check limit before transfer.
    while data := file.file.read(1024 * 1024):
        size += len(data)
        if size > max_bytes:
            raise HTTPException(413, f'Upload exceeds {config.MAX_UPLOAD_MB} MB limit')
        digest.update(data)
    file.file.seek(0)
    if size == 0:
        raise HTTPException(400, 'Empty files are not supported in management UI')
    ctype = file.content_type or 'application/octet-stream'
    if ctype in {'image/svg+xml', 'text/html', 'application/javascript'}:
        raise HTTPException(415, 'Potentially executable file type not allowed via UI')
    try:
        # sha256 stored as object metadata to make verification easier.
        s3.s3_client().upload_fileobj(file.file, bucket, key,
                                      ExtraArgs={'ContentType': ctype, 'Metadata': {'sha256': digest.hexdigest()}})
        audit('upload', bucket, key, f'bytes={size};sha256={digest.hexdigest()}')
        return {'bucket': bucket, 'key': key, 'bytes': size, 'sha256': digest.hexdigest()}
    except S3_ERRORS as exc:
        s3_failure(exc)
    finally:
        file.file.close()


@router.delete('')
def delete_object(bucket: str, key: str, _sess: dict = Depends(require_write)):
    safe_bucket(bucket)
    safe_key(key)
    try:
        # S3 delete is idempotent; caller should confirm existence in UI first.
        s3.s3_client().delete_object(Bucket=bucket, Key=key)
        audit('delete', bucket, key)
        return {'ok': True}
    except S3_ERRORS as exc:
        s3_failure(exc)


@router.get('/presign')
def presign(bucket: str, key: str, expires: int = Query(300, ge=60, le=3600),
            _sess: dict = Depends(require_login)):
    safe_bucket(bucket)
    safe_key(key)
    try:
        url = s3.s3_client(public=True).generate_presigned_url(
            'get_object', Params={'Bucket': bucket, 'Key': key}, ExpiresIn=expires)
        audit('presign', bucket, key, f'expires={expires}')
        return {'url': url, 'expires_seconds': expires}
    except S3_ERRORS as exc:
        s3_failure(exc)
