import asyncio
import hashlib
import hmac
import json
import time
from contextlib import closing

import pytest
from fastapi import HTTPException, Request
from test_api import setup_client

from app import config, db
from app.routers.internal import ingest_audit

KEY = 'k' * 40


def _event(i=0):
    return {'kid': f'STZ{i}', 'method': 'GET', 'bucket': 'demo-images', 'key': f'run-42/{i}.txt',
            'status': 200, 'bytes': 12, 'decision': 'allow'}


def _body(events):
    return json.dumps({'events': events}).encode()


def _sign(body: bytes, key: str = KEY, timestamp: int | None = None) -> dict:
    ts = str(int(time.time()) if timestamp is None else timestamp)
    digest = hmac.new(key.encode(), ts.encode() + b'.' + body, hashlib.sha256).hexdigest()
    return {'x-storzo-timestamp': ts, 'x-storzo-signature': digest, 'content-type': 'application/json'}


def _gateway_rows():
    rows, _total = db.list_audit(500, 0)
    return [row for row in rows if row['action'] == 'gateway.request']


@pytest.fixture
def client(monkeypatch, tmp_path):
    monkeypatch.setattr(config, 'CRED_SIGNING_KEY', KEY)
    c, _fake = setup_client(monkeypatch, tmp_path)
    return c


def test_valid_signature_writes_rows(client):
    """protects=a signed batch becomes one gateway.request row each; fails_when=count or method/bytes/kid/decision/status detail is wrong; no_claim=nginx still hides the route."""
    body = _body([_event(0), _event(1)])
    response = client.post('/api/internal/audit', content=body, headers=_sign(body))
    assert response.status_code == 200, response.text
    rows = _gateway_rows()
    assert len(rows) == 2
    details = {row['detail'] for row in rows}
    assert details == {
        'method=GET bytes=12 kid=STZ0 decision=allow status=200',
        'method=GET bytes=12 kid=STZ1 decision=allow status=200',
    }
    assert {(row['bucket'], row['object_key']) for row in rows} == {
        ('demo-images', 'run-42/0.txt'),
        ('demo-images', 'run-42/1.txt'),
    }


def test_bad_signature_writes_nothing(client):
    """protects=HMAC rejects a wrong signature when the timestamp is fresh; fails_when=401 is not returned or a row is written; no_claim=a missing key is safe."""
    body = _body([_event()])
    headers = _sign(body)
    headers['x-storzo-signature'] = '00' * 32
    response = client.post('/api/internal/audit', content=body, headers=headers)
    assert response.status_code == 401
    assert _gateway_rows() == []


def test_tampered_body_is_rejected(client):
    """protects=signature covers the timestamp prefix and the raw body; fails_when=a body swapped after signing is stored; no_claim=header tampering beyond the signature."""
    body = _body([_event(0)])
    headers = _sign(body)
    response = client.post('/api/internal/audit', content=_body([_event(9)]), headers=headers)
    assert response.status_code == 401
    assert _gateway_rows() == []


def test_too_many_events_is_422(client):
    """protects=500-event cap; fails_when=501 events are accepted or any row is written; no_claim=smaller batches are bounded."""
    body = _body([_event(i) for i in range(501)])
    response = client.post('/api/internal/audit', content=body, headers=_sign(body))
    assert response.status_code == 422
    assert _gateway_rows() == []


@pytest.mark.parametrize('signing_key', ['', 'x' * 31])
def test_unset_or_short_key_returns_503(monkeypatch, tmp_path, signing_key):
    """protects=unset or short CRED_SIGNING_KEY returns 503 before signature work and writes nothing; fails_when=a missing signature is 401 or a signature computed from that key is accepted."""
    monkeypatch.setattr(config, 'CRED_SIGNING_KEY', signing_key)
    c, _fake = setup_client(monkeypatch, tmp_path)
    body = _body([_event()])
    unsigned = c.post('/api/internal/audit', content=body, headers={'content-type': 'application/json'})
    assert unsigned.status_code == 503
    signed = c.post('/api/internal/audit', content=body, headers=_sign(body, signing_key))
    assert signed.status_code == 503
    assert _gateway_rows() == []


def test_stored_row_contains_method_and_bytes(client):
    """protects=stored detail keeps method and bytes; fails_when=GET and PUT of the same object look identical; no_claim=other detail fields are omitted."""
    body = _body([
        {**_event(0), 'method': 'GET', 'bytes': 12},
        {**_event(0), 'method': 'PUT', 'bytes': 4096},
    ])
    response = client.post('/api/internal/audit', content=body, headers=_sign(body))
    assert response.status_code == 200, response.text
    assert {row['detail'] for row in _gateway_rows()} == {
        'method=GET bytes=12 kid=STZ0 decision=allow status=200',
        'method=PUT bytes=4096 kid=STZ0 decision=allow status=200',
    }


def test_route_works_without_session_cookie(client):
    """protects=gateway ingest without a session or CSRF header; fails_when=the call is 401/403 for lack of require_write; no_claim=a forged signature would succeed."""
    client.cookies.clear()
    body = _body([_event()])
    response = client.post('/api/internal/audit', content=body, headers=_sign(body))
    assert response.status_code == 200, response.text
    assert 'storzo_session' not in response.request.headers.get('cookie', '')
    assert len(_gateway_rows()) == 1


@pytest.mark.parametrize('delta', [-301, 301])
def test_stale_or_future_timestamp_is_401(client, monkeypatch, delta):
    """protects=timestamps more than 300s from the server; fails_when=a stale or future signed batch is stored; no_claim=a replay inside the window is rejected."""
    now = 1_700_000_000
    monkeypatch.setattr('app.routers.internal.time.time', lambda: float(now))
    body = _body([_event()])
    response = client.post('/api/internal/audit', content=body, headers=_sign(body, timestamp=now + delta))
    assert response.status_code == 401
    assert _gateway_rows() == []


@pytest.mark.parametrize('stamp', [None, '', 'abc', '12.5', '1e2', '+1700000000', ' 1700000000'])
def test_missing_or_garbage_timestamp_is_401(client, stamp):
    """protects=timestamp must be present and an integer; fails_when=a missing or non-integer timestamp is accepted; no_claim=clock skew inside 300s is rejected."""
    body = _body([_event()])
    mac_ts = '' if stamp is None else stamp
    digest = hmac.new(KEY.encode(), mac_ts.encode() + b'.' + body, hashlib.sha256).hexdigest()
    headers = {'x-storzo-signature': digest, 'content-type': 'application/json'}
    if stamp is not None:
        headers['x-storzo-timestamp'] = stamp
    response = client.post('/api/internal/audit', content=body, headers=headers)
    assert response.status_code == 401
    assert _gateway_rows() == []


def test_oversized_timestamp_is_401(client):
    """protects=a timestamp longer than 12 digits is rejected before int(); fails_when=a 5000-digit timestamp returns 500 or writes a row; no_claim=a 12-digit timestamp inside the window is rejected."""
    body = _body([_event()])
    stamp = '1' * 5000
    digest = hmac.new(KEY.encode(), stamp.encode() + b'.' + body, hashlib.sha256).hexdigest()
    headers = {'x-storzo-timestamp': stamp, 'x-storzo-signature': digest, 'content-type': 'application/json'}
    response = client.post('/api/internal/audit', content=body, headers=headers)
    assert response.status_code == 401
    assert _gateway_rows() == []


def test_body_only_signature_is_rejected(client):
    """protects=signature covers the timestamp prefix plus the body; fails_when=the previous body-only HMAC is accepted; no_claim=a timestamp-prefixed signature of a tampered body succeeds."""
    body = _body([_event()])
    ts = str(int(time.time()))
    digest = hmac.new(KEY.encode(), body, hashlib.sha256).hexdigest()
    headers = {'x-storzo-timestamp': ts, 'x-storzo-signature': digest, 'content-type': 'application/json'}
    response = client.post('/api/internal/audit', content=body, headers=headers)
    assert response.status_code == 401
    assert _gateway_rows() == []


@pytest.mark.parametrize('field,value', [
    ('kid', 'k' * 65),
    ('method', 'M' * 17),
    ('bucket', 'b' * 64),
    ('key', 'k' * 1025),
    ('decision', 'd' * 17),
    ('status', 99),
    ('status', 600),
    ('bytes', -1),
])
def test_field_over_limit_is_422(client, field, value):
    """protects=kid, method, bucket, key, decision, status, and bytes limits; fails_when=an over-limit field is accepted or a row is written; no_claim=values at the limit are rejected."""
    body = _body([{**_event(), field: value}])
    response = client.post('/api/internal/audit', content=body, headers=_sign(body))
    assert response.status_code == 422
    assert _gateway_rows() == []



@pytest.mark.parametrize('signature', [b'\xff', b'\xff' * 64, b'', b'0' * 63, b'0' * 65, b'A' * 64, b'g' * 64])
def test_invalid_signature_header_is_401(client, signature):
    # protects=non-ASCII, wrong-length and non-lowercase-hex signatures return 401;
    # fails_when=invalid headers raise an exception, return another status, or write rows;
    # no_claim=a well-formed signature authenticates the body.
    body = _body([_event()])
    headers = {key.encode(): value.encode() for key, value in _sign(body).items()}
    headers[b'x-storzo-signature'] = signature
    response = client.post('/api/internal/audit', content=body, headers=headers)
    assert response.status_code == 401
    assert _gateway_rows() == []


def _stream_request(headers, chunks):
    consumed = []

    async def stream():
        for chunk in chunks:
            consumed.append(len(chunk))
            yield chunk

    iterator = stream()

    async def receive():
        try:
            chunk = await anext(iterator)
        except StopAsyncIteration:
            return {'type': 'http.request', 'body': b'', 'more_body': False}
        return {'type': 'http.request', 'body': chunk, 'more_body': True}

    scope = {
        'type': 'http', 'method': 'POST', 'path': '/api/internal/audit',
        'headers': [(key.encode(), value.encode()) for key, value in headers.items()],
    }
    return Request(scope, receive), consumed


@pytest.mark.parametrize('declared_length', [None, str(8 * 1024 * 1024)])
@pytest.mark.parametrize('failure,status', [('key', 503), ('timestamp', 401), ('signature', 401)])
def test_header_rejection_does_not_consume_body(client, monkeypatch, declared_length, failure, status):
    # protects=configuration and invalid auth headers reject an 8 MiB upload without reading any chunk;
    # fails_when=body chunks are consumed or Content-Length rejection precedes header authentication;
    # no_claim=an incorrect but well-formed HMAC can be checked without reading its message.
    headers = _sign(b'')
    if failure == 'key':
        monkeypatch.setattr(config, 'CRED_SIGNING_KEY', '')
    elif failure == 'timestamp':
        headers['x-storzo-timestamp'] = '0'
    else:
        headers['x-storzo-signature'] = 'bad-signature'
    if declared_length is not None:
        headers['content-length'] = declared_length
    request, consumed = _stream_request(headers, [b'x' * 65536] * 128)
    with pytest.raises(HTTPException) as exc:
        asyncio.run(ingest_audit(request))
    assert exc.value.status_code == status
    assert consumed == []
    assert _gateway_rows() == []


@pytest.mark.parametrize('declare_length', [True, False])
def test_signed_oversized_body_is_413(client, declare_length):
    # protects=the 4 MiB cap rejects correctly signed oversized bodies before parsing or writing;
    # fails_when=a declared oversized body is read, a stream is fully consumed, or a row is written;
    # no_claim=ASGI servers allocate bounded individual receive chunks.
    cap = 4 * 1024 * 1024
    chunk = b'x' * 65536
    body = chunk * 128
    headers = _sign(body)
    if declare_length:
        headers['content-length'] = str(len(body))
    request, consumed = _stream_request(headers, [chunk] * 128)
    with pytest.raises(HTTPException) as exc:
        asyncio.run(ingest_audit(request))
    assert exc.value.status_code == 413
    assert len(consumed) == (0 if declare_length else cap // len(chunk) + 1)
    assert _gateway_rows() == []


def test_exact_body_cap_is_accepted(client):
    # protects=a valid signed body exactly at 4 MiB remains accepted;
    # fails_when=the size boundary is exclusive or streaming changes the authenticated bytes;
    # no_claim=oversized batches or oversized event fields are accepted.
    cap = 4 * 1024 * 1024
    body = _body([_event()])
    body += b' ' * (cap - len(body))
    response = client.post('/api/internal/audit', content=body, headers=_sign(body))
    assert response.status_code == 200, response.text
    assert response.json() == {'accepted': 1}
    assert len(_gateway_rows()) == 1


def test_maximum_field_batch_is_accepted(client):
    # protects=500 events with maximum-length fields and UTF-8 Unicode fit the byte cap and persist;
    # fails_when=a permitted full batch returns 413/422 or loses rows;
    # no_claim=unbounded integer lengths or arbitrary ignored JSON fields fit under the cap.
    event = {
        'kid': '\U0001f600' * 64, 'method': '\U0001f600' * 16,
        'bucket': '\U0001f600' * 63, 'key': '\U0001f600' * 1024,
        'status': 599, 'bytes': 2**63 - 1, 'decision': '\U0001f600' * 16,
    }
    # Raw UTF-8 matches the gateway's JSON representation.
    body = json.dumps({'events': [event] * 500}, ensure_ascii=False).encode()
    assert len(body) <= 4 * 1024 * 1024
    response = client.post('/api/internal/audit', content=body, headers=_sign(body))
    assert response.status_code == 200, response.text
    assert response.json() == {'accepted': 500}
    rows = _gateway_rows()
    assert len(rows) == 500
    assert all(row['bucket'] == event['bucket'] and row['object_key'] == event['key'] for row in rows)


def test_revocation_feed_filters_since_and_expiry(client):
    """protects=authenticated active feed; fails_when=bad or stale HMAC passes or filtering is wrong; no_claim=nginx."""
    now = int(time.time())
    kids = ['STZ' + letter * 16 for letter in 'ABC']
    with closing(db.connect()) as conn, conn:
        conn.executemany('INSERT INTO revoked_kids VALUES (?, ?, ?)', [
            (kids[0], now - 2, now + 5),
            (kids[1], now - 3, now + 5),
            (kids[2], now - 2, now - 121),
        ])
    url = f'/api/internal/revoked?since={now - 2}'
    response = client.get(url, headers=_sign(b'', timestamp=now))
    assert response.status_code == 200
    assert response.json()['kids'] == kids[:1]
    assert response.json()['now'] >= now
    assert response.headers['Cache-Control'] == 'no-store'
    assert client.get(url, headers=_sign(b'wrong', timestamp=now)).status_code == 401
    assert client.get(url, headers=_sign(b'', timestamp=now - 301)).status_code == 401
