"""Gateway callbacks. Not exposed by the public nginx frontend.

This route deliberately does not use require_write. require_write demands a
browser session cookie and a matching CSRF header; the gateway has neither.
Authentication is HMAC-SHA256 over the raw body with CRED_SIGNING_KEY.
"""
import hashlib
import hmac
import json

from fastapi import APIRouter, HTTPException, Request
from pydantic import BaseModel, Field, ValidationError

from app import config
from app.db import audit

router = APIRouter(prefix='/api/internal', tags=['internal'])

MAX_EVENTS = 500


class AuditEvent(BaseModel):
    kid: str
    method: str
    bucket: str
    key: str
    status: int
    bytes: int
    decision: str


class AuditBatch(BaseModel):
    events: list[AuditEvent] = Field(max_length=MAX_EVENTS)


@router.post('/audit')
async def ingest_audit(request: Request):
    raw = await request.body()
    supplied = request.headers.get('x-storzo-signature', '')
    expected = hmac.new(config.CRED_SIGNING_KEY.encode(), raw, hashlib.sha256).hexdigest()
    if not supplied or not hmac.compare_digest(supplied, expected):
        raise HTTPException(401, 'bad signature')
    try:
        payload = json.loads(raw)
        batch = AuditBatch.model_validate(payload)
    except (json.JSONDecodeError, ValidationError, TypeError):
        raise HTTPException(422, 'invalid audit batch') from None
    for event in batch.events:
        audit('gateway.request', event.bucket, event.key,
              f'kid={event.kid} decision={event.decision} status={event.status}')
    return {'accepted': len(batch.events)}
