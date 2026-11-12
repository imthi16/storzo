import time

import pytest
from test_api import login, setup_client

from app import config, credentials

KEY = 'k' * 40
BODY = {'bucket': 'demo-images', 'prefix': 'run-42/', 'mode': 'ro'}


@pytest.fixture
def client(monkeypatch, tmp_path):
    monkeypatch.setattr(config, 'CRED_SIGNING_KEY', KEY)
    c, _fake = setup_client(monkeypatch, tmp_path)
    return c, login(c)


def issue(c, csrf, **over):
    return c.post('/api/credentials', json={**BODY, **over}, headers={'X-CSRF-Token': csrf})


def test_requires_login_and_csrf(client):
    """protects=admin-only issuing; fails_when=anonymous or CSRF-less call returns 201; no_claim=nothing about gateway enforcement."""
    c, csrf = client
    assert c.post('/api/credentials', json=BODY, headers={'X-CSRF-Token': csrf}).status_code == 201
    assert c.post('/api/credentials', json=BODY).status_code == 403
    c.cookies.clear()
    assert c.post('/api/credentials', json=BODY, headers={'X-CSRF-Token': csrf}).status_code == 401


@pytest.mark.parametrize('ttl', [0, -5, 3601])
def test_ttl_out_of_range_is_422(client, ttl):
    """protects=60 min TTL ceiling; fails_when=an over-long or non-positive TTL is issued; no_claim=none."""
    c, csrf = client
    assert issue(c, csrf, ttl_seconds=ttl).status_code == 422


def test_default_ttl_is_15_minutes(client):
    """protects=default 900 s; fails_when=expires_at drifts from now+900 by more than 2 s; no_claim=wall-clock only, no fake clock."""
    c, csrf = client
    body = issue(c, csrf).json()
    from datetime import datetime
    delta = datetime.fromisoformat(body['expires_at']).timestamp() - time.time()
    assert 898 <= delta <= 902
    assert body['endpoint'] == config.S3_PUBLIC_ENDPOINT


@pytest.mark.parametrize('over,code', [
    ({'bucket': 'BAD_Bucket'}, 400), ({'prefix': '../x/'}, 400), ({'prefix': ''}, 400),
    ({'prefix': '/abs/'}, 400), ({'bucket': 'no-such-bucket'}, 404), ({'mode': 'admin'}, 422)])
def test_invalid_input_rejected(client, over, code):
    """protects=safe_bucket/safe_key at the trust boundary and bucket existence; fails_when=a bad scope is signed; no_claim=FakeS3 stands in for SeaweedFS."""
    c, csrf = client
    assert issue(c, csrf, **over).status_code == code


def test_token_roundtrip_tamper_and_expiry(client):
    """protects=token integrity and expiry; fails_when=a changed byte or an expired token verifies; no_claim=Go side not tested here (cross-language vectors come with the gateway)."""
    c, csrf = client
    cred = issue(c, csrf).json()
    claims = credentials.verify(cred['session_token'])
    assert (claims['bucket'], claims['prefix'], claims['mode'], claims['kid']) == ('demo-images', 'run-42/', 'ro', cred['access_key_id'])
    assert cred['secret_access_key'] == credentials._mac(b'secret|' + cred['access_key_id'].encode()).hex()
    body, sig = cred['session_token'].split('.')
    with pytest.raises(ValueError):
        credentials.verify(body[:-1] + ('A' if body[-1] != 'A' else 'B') + '.' + sig)
    credentials.verify(cred['session_token'], now=claims['exp'] + credentials.SKEW)
    with pytest.raises(ValueError):
        credentials.verify(cred['session_token'], now=claims['exp'] + credentials.SKEW + 1)


def test_issue_is_audited_without_secret(client):
    """protects=audit of every issue; fails_when=no row or the secret/token leaks into the log; no_claim=gateway request audit not built yet."""
    c, csrf = client
    cred = issue(c, csrf, label='nightly').json()
    row = next(e for e in c.get('/api/audit').json()['items'] if e['action'] == 'credential.issue')
    assert cred['access_key_id'] in row['detail'] and 'mode=ro' in row['detail'] and 'label=nightly' in row['detail']
    assert (row['bucket'], row['object_key']) == ('demo-images', 'run-42/')
    assert cred['secret_access_key'] not in str(row) and cred['session_token'] not in str(row)


def test_unconfigured_key_returns_503(monkeypatch, tmp_path):
    """protects=fail-closed without a signing key; fails_when=a credential is signed with an empty/short key; no_claim=none."""
    monkeypatch.setattr(config, 'CRED_SIGNING_KEY', '')
    c, _fake = setup_client(monkeypatch, tmp_path)
    assert issue(c, login(c)).status_code == 503
