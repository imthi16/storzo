from fastapi import APIRouter, Depends, HTTPException

from app import migrations, s3
from app.db import audit
from app.schemas import MigrationRequest
from app.security import S3_ERRORS, require_login, require_write, s3_failure, safe_bucket, safe_key

router = APIRouter(prefix='/api/migrations', tags=['migrations'])


def _validate(body: MigrationRequest) -> tuple[dict, dict]:
    safe_bucket(body.source.bucket)
    safe_bucket(body.destination.bucket)
    if body.source.prefix:
        safe_key(body.source.prefix)
    if body.destination.prefix:
        safe_key(body.destination.prefix)
    migrations.validate_source_endpoint(body.source.endpoint)
    return body.source.model_dump(), body.destination.model_dump()


@router.post('/estimate', dependencies=[Depends(require_write)])
def estimate(body: MigrationRequest):
    source, _dest = _validate(body)
    try:
        return migrations.estimate(source)
    except HTTPException:
        raise
    except Exception:  # noqa: BLE001
        raise HTTPException(503, 'Could not list the source bucket. Check the endpoint, bucket and credentials.') from None


@router.post('', status_code=201, dependencies=[Depends(require_write)])
def start(body: MigrationRequest):
    source, dest = _validate(body)
    if not migrations.rclone_path():
        raise HTTPException(503, 'rclone is not available in this backend image; rebuild it with rclone installed (see docs/CLOUD_CONNECT.md)')
    if migrations.active_job():
        raise HTTPException(409, 'A migration is already running. Only one migration at a time is supported; cancel or wait for it first.')
    try:
        s3.s3_client().head_bucket(Bucket=dest['bucket'])
    except S3_ERRORS as exc:
        s3_failure(exc)
    audit('migration.start', dest['bucket'], dest['prefix'] or None,
          f"source_bucket={source['bucket']} source_prefix={source['prefix']}")
    return migrations.start_job(source, dest)


@router.get('', dependencies=[Depends(require_login)])
def list_migrations():
    return {'items': migrations.list_jobs()}


@router.get('/{job_id}', dependencies=[Depends(require_login)])
def get_migration(job_id: str):
    job = migrations.get_job(job_id)
    if not job:
        raise HTTPException(404, 'Migration not found')
    return job


@router.post('/{job_id}/cancel', dependencies=[Depends(require_write)])
def cancel(job_id: str):
    job = migrations.get_job(job_id)
    if not job:
        raise HTTPException(404, 'Migration not found')
    if job['state'] in ('done', 'failed', 'cancelled'):
        return job
    migrations.cancel_job(job_id)
    audit('migration.cancel', job['dest_bucket'], job['dest_prefix'] or None, f'id={job_id}')
    return migrations.get_job(job_id) or job
