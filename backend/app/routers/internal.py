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

from fastapi import APIRouter, HTTPException, Query, Request, Response
from pydantic import BaseModel, Field, ValidationError
from starlette.concurrency import run_in_threadpool

from app import config, credentials
from app.db import audit

router = APIRouter(prefix='/api/internal', tags=['internal'])

MAX_EVENTS = 500
MAX_BODY_BYTES = 4 * 1024 * 1024
MAX_SKEW_SECONDS = 300
# 12 digits max: int() raises ValueError past ~4300 digits, which would be a 500.
_TIMESTAMP = re.compile(r'[0-9]{1,12}')
_SIGNATURE = re.compile(r'[0-9a-f]{64}')


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


async def _authenticated_body(request: Request) -> bytearray:
    if len(config.CRED_SIGNING_KEY) < 32:
        raise HTTPException(503, 'Agent credentials are not configured (set CRED_SIGNING_KEY, 32+ characters)')
    timestamp = _fresh_timestamp(request.headers.get('x-storzo-timestamp'), int(time.time()))
    if timestamp is None:
        raise HTTPException(401, 'bad timestamp')
    supplied = request.headers.get('x-storzo-signature', '')
    if _SIGNATURE.fullmatch(supplied) is None:
        raise HTTPException(401, 'bad signature')

    # Header checks must precede all body reads, including size-limit handling.
    try:
        content_length = int(request.headers.get('content-length', '0'))
    except ValueError:
        # The transport validates framing; still enforce our cap while streaming.
        content_length = 0
    if content_length > MAX_BODY_BYTES:
        raise HTTPException(413, 'audit batch too large')
    raw = bytearray()
    async for chunk in request.stream():
        if len(raw) + len(chunk) > MAX_BODY_BYTES:
            raise HTTPException(413, 'audit batch too large')
        raw.extend(chunk)

    expected = hmac.new(config.CRED_SIGNING_KEY.encode(), timestamp.encode() + b'.' + raw, hashlib.sha256).hexdigest()
    if not hmac.compare_digest(supplied, expected):
        raise HTTPException(401, 'bad signature')
    return raw


@router.post('/audit')
async def ingest_audit(request: Request):
    raw = await _authenticated_body(request)
    try:
        payload = json.loads(raw)
        batch = AuditBatch.model_validate(payload)
    except (json.JSONDecodeError, ValidationError, TypeError):
        raise HTTPException(422, 'invalid audit batch') from None
    await run_in_threadpool(_write_events, batch.events)
    return {'accepted': len(batch.events)}


@router.get('/revoked')
async def list_revoked(request: Request, response: Response, since: int = Query(ge=0, le=2**63 - 1)):
    if await _authenticated_body(request):
        raise HTTPException(400, 'Expected an empty body')
    now = int(time.time())
    kids = await run_in_threadpool(credentials.revoked_since, since, now)
    response.headers['Cache-Control'] = 'no-store'
    return {'kids': kids, 'now': now}
