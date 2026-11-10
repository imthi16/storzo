from fastapi import APIRouter, Depends, HTTPException, Query

from app import s3
from app.db import audit
from app.schemas import BucketCreate
from app.security import S3_ERRORS, require_login, require_write, s3_failure, safe_bucket, safe_key

router = APIRouter(prefix='/api/buckets', tags=['buckets'])


@router.get('')
def buckets(_sess: dict = Depends(require_login)):
    try:
        result = s3.s3_client().list_buckets()
        return {'items': [{'name': b['Name'], 'created': b.get('CreationDate').isoformat() if b.get('CreationDate') else ''}
                          for b in result.get('Buckets', [])]}
    except S3_ERRORS as exc:
        s3_failure(exc)


@router.post('', status_code=201)
def create_bucket(body: BucketCreate, _sess: dict = Depends(require_write)):
    name = safe_bucket(body.name)
    try:
        s3.s3_client().create_bucket(Bucket=name)
        audit('create_bucket', name)
        return {'name': name}
    except S3_ERRORS as exc:
        s3_failure(exc)


@router.delete('/{bucket}')
def delete_bucket(bucket: str, _sess: dict = Depends(require_write)):
    safe_bucket(bucket)
    try:
        # SeaweedFS deletes non-empty buckets (AWS S3 returns BucketNotEmpty), so enforce it here.
        if s3.s3_client().list_objects_v2(Bucket=bucket, MaxKeys=1).get('Contents'):
            raise HTTPException(409, 'Bucket is not empty. Delete its objects first.')
        s3.s3_client().delete_bucket(Bucket=bucket)
        audit('delete_bucket', bucket)
        return {'ok': True}
    except S3_ERRORS as exc:
        s3_failure(exc)


@router.get('/{bucket}/objects')
def objects(bucket: str, prefix: str = '', continuation: str = '', limit: int = Query(100, ge=1, le=500),
            _sess: dict = Depends(require_login)):
    safe_bucket(bucket)
    if prefix:
        safe_key(prefix)
    args = {'Bucket': bucket, 'Prefix': prefix, 'MaxKeys': limit, 'Delimiter': '/'}
    if continuation:
        args['ContinuationToken'] = continuation
    try:
        result = s3.s3_client().list_objects_v2(**args)
        items = [{'key': item['Key'], 'size': item['Size'],
                  'modified': item['LastModified'].isoformat() if item.get('LastModified') else '',
                  'etag': item.get('ETag', '').strip('"')}
                 for item in result.get('Contents', []) if item['Key'] != prefix]
        return {'items': items, 'folders': [p['Prefix'] for p in result.get('CommonPrefixes', [])],
                'continuation': result.get('NextContinuationToken'), 'prefix': prefix}
    except S3_ERRORS as exc:
        s3_failure(exc)
