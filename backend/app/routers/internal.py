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
from starlette.concurrency import run_in_threadpool

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


def _write_events(events: list[AuditEvent]) -> None:
    for event in events:
        audit('gateway.request', event.bucket, event.key,
              f'method={event.method} bytes={event.bytes} kid={event.kid} decision={event.decision} status={event.status}')


@router.post('/audit')
async def ingest_audit(request: Request):
    raw = await request.body()
    if len(config.CRED_SIGNING_KEY) < 32:
        raise HTTPException(503, 'Agent credentials are not configured (set CRED_SIGNING_KEY, 32+ characters)')
    supplied = request.headers.get('x-storzo-signature', '')
    expected = hmac.new(config.CRED_SIGNING_KEY.encode(), raw, hashlib.sha256).hexdigest()
    if not supplied or not hmac.compare_digest(supplied, expected):
        raise HTTPException(401, 'bad signature')
    try:
        payload = json.loads(raw)
        batch = AuditBatch.model_validate(payload)
    except (json.JSONDecodeError, ValidationError, TypeError):
        raise HTTPException(422, 'invalid audit batch') from None
    await run_in_threadpool(_write_events, batch.events)
    return {'accepted': len(batch.events)}
