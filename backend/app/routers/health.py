from fastapi import APIRouter, HTTPException

from app import config, s3

router = APIRouter(prefix='/api/health', tags=['health'])


@router.get('/live')
def live():
    return {'status': 'alive', 'service': config.APP_NAME}


@router.get('/ready')
def ready():
    try:
        s3.s3_client().list_buckets()
        return {'status': 'ready', 'storage': 'reachable'}
    except Exception as exc:  # any storage failure means "not ready"; the cause is chained, not leaked
        raise HTTPException(503, 'Storage not reachable') from exc
