"""Audit list filters and the uploads-by-day gateway write count."""
from datetime import UTC, datetime, timedelta

import pytest
from test_api import login, setup_client

from app import db


def _gw(method, status, decision='allow', kid='kid', nbytes=8):
    return f'method={method} bytes={nbytes} kid={kid} decision={decision} status={status}'


def _add(action, detail=None, *, day=None, key='obj'):
    db.audit(action, 'demo-images', key, detail)
    if day is not None:
        with db.connect() as conn:
            conn.execute(
                'UPDATE audit SET ts=? WHERE id=(SELECT MAX(id) FROM audit)',
                (f'{day}T12:00:00+00:00',),
            )


def _ids(payload):
    return [row['id'] for row in payload['items']]


@pytest.fixture
def client(monkeypatch, tmp_path):
    c, _fake = setup_client(monkeypatch, tmp_path)
    try:
        yield c, login(c)
    finally:
        c.__exit__(None, None, None)


def _seed_mixed():
    """Login row already exists. Returns allow, deny, console, and the decision=allowed id."""
    with db.connect() as conn:
        login_id = conn.execute("SELECT id FROM audit WHERE action='login'").fetchone()[0]
    console_ids = [login_id]
    _add('create_bucket')
    console_ids.append(_latest())
    allow_ids = []
    deny_ids = []
    _add('gateway.request', _gw('GET', 200, 'allow'), key='g-allow')
    allow_ids.append(_latest())
    _add('gateway.request', _gw('PUT', 403, 'deny'), key='g-deny')
    deny_ids.append(_latest())
    # Longer value must not match the spaced decision=allow token.
    _add('gateway.request', _gw('GET', 200, 'allowed'), key='g-allowed')
    neither = _latest()
    _add('upload', 'bytes=1 decision=allow note', key='console-token')
    console_ids.append(_latest())
    _add('gateway.request', _gw('DELETE', 204, 'deny'), key='g-deny-2')
    deny_ids.append(_latest())
    _add('gateway.request', _gw('PUT', 201, 'allow', kid=''), key='g-allow-empty-kid')
    allow_ids.append(_latest())
    return allow_ids, deny_ids, console_ids, neither


def _latest():
    with db.connect() as conn:
        return conn.execute('SELECT MAX(id) FROM audit').fetchone()[0]


def test_source_filter_rows_and_total(client):
    """protects=source all/gateway/console returns only those actions and the filtered total; fails_when=a gateway row appears under console, a console row under gateway, or total counts the other set; no_claim=decision matching."""
    c, _csrf = client
    allow_ids, deny_ids, console_ids, neither = _seed_mixed()
    everyone = console_ids + allow_ids + deny_ids + [neither]

    implicit = c.get('/api/audit').json()
    explicit = c.get('/api/audit?source=all').json()
    assert implicit['total'] == explicit['total'] == len(everyone)
    assert _ids(explicit) == sorted(everyone, reverse=True)
    assert explicit['limit'] == 50 and explicit['offset'] == 0

    gateway = c.get('/api/audit?source=gateway').json()
    assert gateway['total'] == len(allow_ids) + len(deny_ids) + 1
    assert _ids(gateway) == sorted(allow_ids + deny_ids + [neither], reverse=True)
    assert {row['action'] for row in gateway['items']} == {'gateway.request'}

    console = c.get('/api/audit?source=console').json()
    assert console['total'] == len(console_ids)
    assert _ids(console) == sorted(console_ids, reverse=True)
    assert all(row['action'] != 'gateway.request' for row in console['items'])


def test_decision_filter_rows_and_total(client):
    """protects=decision=allow/deny keeps gateway rows with that spaced token and drops console rows, including source=console which is empty; fails_when=decision=allowed matches, a console detail containing the token is returned, or total includes the other decision; no_claim=uploads-by-day."""
    c, _csrf = client
    allow_ids, deny_ids, console_ids, _neither = _seed_mixed()
    assert console_ids  # login, create_bucket, and the console row whose detail contains the token

    allow = c.get('/api/audit?decision=allow').json()
    assert allow['total'] == len(allow_ids)
    assert _ids(allow) == sorted(allow_ids, reverse=True)
    assert all(' decision=allow ' in row['detail'] for row in allow['items'])

    deny = c.get('/api/audit?source=all&decision=deny').json()
    assert deny['total'] == len(deny_ids)
    assert _ids(deny) == sorted(deny_ids, reverse=True)

    gateway_allow = c.get('/api/audit?source=gateway&decision=allow').json()
    assert gateway_allow['total'] == len(allow_ids) and _ids(gateway_allow) == _ids(allow)
    gateway_deny = c.get('/api/audit?source=gateway&decision=deny').json()
    assert gateway_deny['total'] == len(deny_ids) and _ids(gateway_deny) == _ids(deny)

    for decision in ('allow', 'deny'):
        empty = c.get('/api/audit', params={'source': 'console', 'decision': decision}).json()
        assert empty['items'] == [] and empty['total'] == 0
        assert empty['limit'] == 50 and empty['offset'] == 0


def test_filtered_pagination_uses_the_filtered_set(client):
    """protects=limit and offset page the filtered id-DESC set and total stays the filtered count; fails_when=offset skips unfiltered rows or a later page repeats an id; no_claim=the default page size."""
    c, _csrf = client
    allow_ids = []
    for i in range(6):
        _add('gateway.request', _gw('GET', 200, 'allow', kid=str(i)), key=f'a{i}')
        allow_ids.append(_latest())
        _add('gateway.request', _gw('GET', 403, 'deny', kid=str(i)), key=f'd{i}')
    _add('upload', 'bytes=9', key='console')
    desc = list(reversed(allow_ids))
    seen = []
    for offset, expected in ((0, desc[0:2]), (2, desc[2:4]), (4, desc[4:6]), (6, [])):
        page = c.get('/api/audit', params={'source': 'gateway', 'decision': 'allow', 'limit': 2, 'offset': offset}).json()
        assert page['total'] == 6 and page['limit'] == 2 and page['offset'] == offset
        assert _ids(page) == expected
        seen.extend(expected)
    assert seen == desc and len(set(seen)) == 6


@pytest.mark.parametrize('params', [
    {'source': "gateway' OR '1'='1"},
    {'source': 'gateway; DROP TABLE audit;--'},
    {'source': '%'},
    {'source': '_'},
    {'source': ''},
    {'source': 'ALL'},
    {'source': 'both'},
    {'decision': "allow' OR '1'='1"},
    {'decision': '%'},
    {'decision': 'allow_'},
    {'decision': ''},
    {'decision': 'ALLOW'},
    {'decision': 'granted'},
    {'source': 'console', 'decision': "deny' OR 1=1 --"},
])
def test_invalid_source_or_decision_is_422(client, params):
    """protects=source and decision are an allow-list, so SQL metacharacters never reach the query; fails_when=any other value is not 422 or the audit table is gone afterwards; no_claim=SQLite itself rejects the string."""
    c, _csrf = client
    bad = c.get('/api/audit', params=params)
    assert bad.status_code == 422
    ok = c.get('/api/audit?source=console')
    assert ok.status_code == 200 and ok.json()['total'] >= 1


def test_filters_require_login(client):
    """protects=filtered audit and uploads-by-day stay behind require_login; fails_when=an anonymous request returns 200; no_claim=CSRF, these routes are reads."""
    c, _csrf = client
    c.cookies.clear()
    assert c.get('/api/audit?source=gateway&decision=allow').status_code == 401
    assert c.get('/api/audit/uploads-by-day').status_code == 401


def test_uploads_by_day_sums_console_and_gateway_writes(client):
    """protects=count is console uploads plus PUT 2xx gateway writes, with console and gateway fields and zero-filled UTC days; fails_when=a read, delete, denied write, 5xx write, or non-PUT changes a day, a day is missing, or count != console + gateway; no_claim=a multipart upload is one object (each part is its own PUT)."""
    c, _csrf = client
    today = datetime.now(UTC).date()
    day_2 = str(today - timedelta(days=2))
    day_1 = str(today - timedelta(days=1))
    day_10 = str(today - timedelta(days=10))
    today_s = str(today)

    _add('upload', 'bytes=1')
    _add('upload', 'bytes=2')
    _add('gateway.request', _gw('PUT', 200, 'allow'))
    _add('gateway.request', _gw('PUT', 201, 'allow', kid=''))
    _add('gateway.request', _gw('PUT', 299, 'allow'))
    _add('gateway.request', _gw('GET', 200, 'allow'))
    _add('gateway.request', _gw('DELETE', 204, 'allow'))
    _add('gateway.request', _gw('PUT', 403, 'deny'))
    _add('gateway.request', _gw('PUT', 500, 'allow'))
    _add('gateway.request', _gw('PUT', 300, 'allow'))
    _add('gateway.request', _gw('PUTTY', 200, 'allow'))
    _add('gateway.request', 'method=GET bytes=1 kid=method=PUT x decision=allow status=200')
    _add('create_bucket')
    _add('gateway.request', _gw('GET', 200, 'allow'), day=day_1)
    _add('upload', 'bytes=3', day=day_2)
    _add('gateway.request', _gw('PUT', 204, 'allow'), day=day_2)
    _add('upload', 'bytes=4', day=day_10)
    _add('gateway.request', _gw('PUT', 200, 'allow'), day=day_10)

    payload = c.get('/api/audit/uploads-by-day?days=14').json()
    items = payload['items']
    assert [row['day'] for row in items] == [str(today - timedelta(days=i)) for i in range(13, -1, -1)]
    by_day = {row['day']: row for row in items}
    assert by_day[today_s] == {'day': today_s, 'count': 5, 'console': 2, 'gateway': 3}
    assert by_day[day_1] == {'day': day_1, 'count': 0, 'console': 0, 'gateway': 0}
    assert by_day[day_2] == {'day': day_2, 'count': 2, 'console': 1, 'gateway': 1}
    assert by_day[day_10] == {'day': day_10, 'count': 2, 'console': 1, 'gateway': 1}
    quiet = str(today - timedelta(days=5))
    assert by_day[quiet] == {'day': quiet, 'count': 0, 'console': 0, 'gateway': 0}
    assert all(row['count'] == row['console'] + row['gateway'] for row in items)
    assert sum(row['count'] for row in items) == 9

    c.cookies.clear()
    assert c.get('/api/audit/uploads-by-day').status_code == 401
