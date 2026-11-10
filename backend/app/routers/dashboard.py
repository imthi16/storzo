from fastapi import APIRouter, Depends

from app.security import S3_ERRORS, require_login, s3_failure
from app.stats import collect_dashboard

router = APIRouter(prefix='/api', tags=['dashboard'])


@router.get('/dashboard')
def dashboard(_sess: dict = Depends(require_login)):
    try:
        return collect_dashboard()
    except S3_ERRORS as exc:
        s3_failure(exc)
