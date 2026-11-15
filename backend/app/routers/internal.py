"""Gateway callbacks. Not exposed by the public nginx frontend.

This route deliberately does not use require_write. require_write demands a
browser session cookie and a matching CSRF header; the gateway has neither.
Authentication is HMAC-SHA256 over the bytes `<timestamp>.` + raw body with
CRED_SIGNING_KEY. The timestamp is unix seconds and must be within 300 s.
"""
import hashlib
import hmac
import json
import re
import time

from fastapi import APIRouter, HTTPException, Request
from pydantic import BaseModel, Field, ValidationError
from starlette.concurrency import run_in_threadpool

from app import config
from app.db import audit

router = APIRouter(prefix='/api/internal', tags=['internal'])

MAX_EVENTS = 500
MAX_SKEW_SECONDS = 300
_TIMESTAMP = re.compile(r'-?[0-9]+')


class AuditEvent(BaseModel):
    kid: str = Field(max_length=64)
    method: str = Field(max_length=16)
    bucket: str = Field(max_length=63)
    key: str = Field(max_length=1024)
    status: int = Field(ge=100, le=599)
    bytes: int = Field(ge=0)
    decision: str = Field(max_length=16)


class AuditBatch(BaseModel):
    events: list[AuditEvent] = Field(max_length=MAX_EVENTS)


def _write_events(events: list[AuditEvent]) -> None:
    for event in events:
        audit('gateway.request', event.bucket, event.key,
              f'method={event.method} bytes={event.bytes} kid={event.kid} decision={event.decision} status={event.status}')


def _fresh_timestamp(header: str | None, now: int) -> str | None:
    # Replay is bounded by the 300 s window only; there is no nonce store
    # (shortcut: a replay inside the window is accepted, upgrade when the audit trail needs exactly-once).
    if header is None or _TIMESTAMP.fullmatch(header) is None:
        return None
    if abs(now - int(header)) > MAX_SKEW_SECONDS:
        return None
    return header


@router.post('/audit')
async def ingest_audit(request: Request):
    raw = await request.body()
    if len(config.CRED_SIGNING_KEY) < 32:
        raise HTTPException(503, 'Agent credentials are not configured (set CRED_SIGNING_KEY, 32+ characters)')
    timestamp = _fresh_timestamp(request.headers.get('x-storzo-timestamp'), int(time.time()))
    if timestamp is None:
        raise HTTPException(401, 'bad timestamp')
    supplied = request.headers.get('x-storzo-signature', '')
    expected = hmac.new(config.CRED_SIGNING_KEY.encode(), timestamp.encode() + b'.' + raw, hashlib.sha256).hexdigest()
    if not supplied or not hmac.compare_digest(supplied, expected):
        raise HTTPException(401, 'bad signature')
    try:
        payload = json.loads(raw)
        batch = AuditBatch.model_validate(payload)
    except (json.JSONDecodeError, ValidationError, TypeError):
        raise HTTPException(422, 'invalid audit batch') from None
    await run_in_threadpool(_write_events, batch.events)
    return {'accepted': len(batch.events)}
