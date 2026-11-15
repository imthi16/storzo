import hashlib
import hmac
import json

import pytest
from test_api import setup_client

from app import config, db

KEY = 'k' * 40


def _event(i=0):
    return {'kid': f'STZ{i}', 'method': 'GET', 'bucket': 'demo-images', 'key': f'run-42/{i}.txt',
            'status': 200, 'bytes': 12, 'decision': 'allow'}


def _body(events):
    return json.dumps({'events': events}).encode()


def _sign(body: bytes, key: str = KEY) -> dict:
    digest = hmac.new(key.encode(), body, hashlib.sha256).hexdigest()
    return {'x-storzo-signature': digest, 'content-type': 'application/json'}


def _gateway_rows():
    rows, _total = db.list_audit(500, 0)
    return [row for row in rows if row['action'] == 'gateway.request']


@pytest.fixture
def client(monkeypatch, tmp_path):
    monkeypatch.setattr(config, 'CRED_SIGNING_KEY', KEY)
    c, _fake = setup_client(monkeypatch, tmp_path)
    return c


def test_valid_signature_writes_rows(client):
    """protects=a signed batch becomes one gateway.request row each; fails_when=count or kid/decision/status detail is wrong; no_claim=nginx still hides the route."""
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
    """protects=HMAC rejects a wrong signature; fails_when=401 is not returned or a row is written; no_claim=a missing key is safe."""
    body = _body([_event()])
    headers = _sign(body)
    headers['x-storzo-signature'] = '00' * 32
    response = client.post('/api/internal/audit', content=body, headers=headers)
    assert response.status_code == 401
    assert _gateway_rows() == []


def test_tampered_body_is_rejected(client):
    """protects=signature covers the raw body; fails_when=a body swapped after signing is stored; no_claim=header tampering beyond the signature."""
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
    """protects=unset or short CRED_SIGNING_KEY returns 503 and writes nothing; fails_when=a signature computed from that key is accepted or a row is stored."""
    monkeypatch.setattr(config, 'CRED_SIGNING_KEY', signing_key)
    c, _fake = setup_client(monkeypatch, tmp_path)
    body = _body([_event()])
    response = c.post('/api/internal/audit', content=body, headers=_sign(body, signing_key))
    assert response.status_code == 503
    assert _gateway_rows() == []


def test_stored_row_contains_method_and_bytes(client):
    """protects=stored detail keeps method and bytes; fails_when=GET and PUT of the same object look identical."""
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
