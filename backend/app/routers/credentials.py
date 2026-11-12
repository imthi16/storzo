from datetime import UTC, datetime
from typing import Literal

from fastapi import APIRouter, Depends, HTTPException
from pydantic import BaseModel, Field

from app import config, credentials, s3
from app.db import audit
from app.security import S3_ERRORS, require_write, s3_failure, safe_bucket, safe_key

router = APIRouter(prefix='/api/credentials', tags=['credentials'])


class CredentialRequest(BaseModel):
    bucket: str
    prefix: str
    mode: Literal['ro', 'rw']
    ttl_seconds: int = Field(900, ge=1, le=credentials.MAX_TTL)
    label: str | None = Field(None, max_length=100)


@router.post('', status_code=201)
def issue_credential(body: CredentialRequest, _sess: dict = Depends(require_write)):
    if len(config.CRED_SIGNING_KEY) < 32:
        raise HTTPException(503, 'Agent credentials are not configured (set CRED_SIGNING_KEY, 32+ characters)')
    safe_bucket(body.bucket)
    safe_key(body.prefix)
    try:
        s3.s3_client().head_bucket(Bucket=body.bucket)
    except S3_ERRORS as exc:
        s3_failure(exc)
    cred = credentials.issue(body.bucket, body.prefix, body.mode, body.ttl_seconds)
    expires = datetime.fromtimestamp(cred['expires_at'], UTC).isoformat()
    audit('credential.issue', body.bucket, body.prefix,
          f"kid={cred['access_key_id']} mode={body.mode} exp={cred['expires_at']}" + (f' label={body.label}' if body.label else ''))
    return {'access_key_id': cred['access_key_id'], 'secret_access_key': cred['secret_access_key'],
            'session_token': cred['session_token'], 'expires_at': expires, 'endpoint': config.S3_PUBLIC_ENDPOINT}
