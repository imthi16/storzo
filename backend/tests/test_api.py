import io
import os
from datetime import UTC, datetime

from PIL import Image

os.environ.setdefault('ADMIN_PASSWORD', 'StrongPasswordForTests12345')
os.environ.setdefault('SESSION_SECRET', 'some-long-session-secret-for-tests-change-this-1234')
os.environ.setdefault('S3_ACCESS_KEY', 'testingkey')
os.environ.setdefault('S3_SECRET_KEY', 'testingsecret')
os.environ.setdefault('AUDIT_DB_PATH', '/tmp/storzo-test-audit.db')

from fastapi.testclient import TestClient

from app import config, main, s3, security


class Body(io.BytesIO):
    def iter_chunks(self, chunk_size=65536):
        while chunk := self.read(chunk_size):
            yield chunk


class FakeS3:
    def __init__(self):
        self.buckets = {'demo-images': {}}
        self.modified = {}

    def list_buckets(self):
        return {'Buckets': [{'Name': name, 'CreationDate': datetime.now(UTC)} for name in self.buckets]}

    def create_bucket(self, Bucket):
        self.buckets[Bucket] = {}

    def delete_bucket(self, Bucket):
        if self.buckets[Bucket]:
            from botocore.exceptions import ClientError
            raise ClientError({'Error': {'Code': 'BucketNotEmpty'}}, 'DeleteBucket')
        del self.buckets[Bucket]

    def list_objects_v2(self, Bucket, Prefix='', Delimiter=None, MaxKeys=1000, ContinuationToken=None):
        data = sorted((k, v) for k,v in self.buckets[Bucket].items() if k.startswith(Prefix))
        folders = set()
        contents=[]
        for key, value in data:
            remaining = key[len(Prefix):]
            if Delimiter and '/' in remaining:
                folders.add(Prefix + remaining.split('/')[0] + '/')
            else:
                contents.append({'Key': key, 'Size': len(value[0]),
                                 'LastModified': self.modified.get((Bucket, key), datetime.now(UTC)),
                                 'ETag': '"abc"'})
        offset = int(ContinuationToken or 0)
        result = {'Contents': contents[offset:offset + MaxKeys],
                  'CommonPrefixes': [{'Prefix': x} for x in sorted(folders)]}
        if offset + MaxKeys < len(contents):
            result['NextContinuationToken'] = str(offset + MaxKeys)
        return result

    def upload_fileobj(self, file, Bucket, Key, ExtraArgs=None):
        self.buckets[Bucket][Key] = (file.read(), (ExtraArgs or {}).get('ContentType', 'application/octet-stream'))

    def get_object(self, Bucket, Key):
        data, mime = self.buckets[Bucket][Key]
        return {'Body': Body(data), 'ContentType': mime, 'ContentLength': len(data)}

    def delete_object(self, Bucket, Key):
        self.buckets[Bucket].pop(Key, None)

    def generate_presigned_url(self, operation, Params, ExpiresIn):
        return f"http://localhost:8333/{Params['Bucket']}/{Params['Key']}?fake=true"


def setup_client(monkeypatch, tmp_path):
    fake = FakeS3()
    monkeypatch.setattr(s3, 's3_client', lambda public=False: fake)
    monkeypatch.setattr(config, 'DB_PATH', tmp_path / 'audit.db')
    c = TestClient(main.app)
    c.__enter__()
    return c, fake


def login(c):
    r = c.post('/api/auth/login', json={'username':'admin','password':config.ADMIN_PASSWORD})
    assert r.status_code == 200, r.text
    return r.json()['csrf']


def test_auth_and_csrf(monkeypatch, tmp_path):
    c, _fake = setup_client(monkeypatch, tmp_path)
    try:
        assert c.get('/api/buckets').status_code == 401
        assert c.post('/api/auth/login', json={'username':'admin','password':'wrong'}).status_code == 401
        csrf = login(c)
        assert c.get('/api/buckets').status_code == 200
        assert c.post('/api/buckets', json={'name':'new-bucket'}).status_code == 403
        assert c.post('/api/buckets', json={'name':'new-bucket'}, headers={'X-CSRF-Token':csrf}).status_code == 201
        assert any(b['name'] == 'new-bucket' for b in c.get('/api/buckets').json()['items'])
        assert c.delete('/api/buckets/new-bucket', headers={'X-CSRF-Token':csrf}).status_code == 200
        assert c.post('/api/auth/logout', headers={'X-CSRF-Token':csrf}).status_code == 200
        assert c.get('/api/buckets').status_code == 401
    finally: c.__exit__(None, None, None)


def test_upload_list_download_thumbnail_delete(monkeypatch, tmp_path):
    c, fake = setup_client(monkeypatch, tmp_path)
    try:
        csrf = login(c)
        img = Image.new('RGB', (32, 20), (20, 100, 200))
        b = io.BytesIO(); img.save(b, format='PNG')
        r = c.post('/api/objects/upload', data={'bucket':'demo-images','key':'photos/test.png'},
                   files={'file':('test.png', b.getvalue(), 'image/png')}, headers={'X-CSRF-Token':csrf})
        assert r.status_code == 201, r.text
        assert len(r.json()['sha256']) == 64
        ls = c.get('/api/buckets/demo-images/objects').json()
        assert ls['folders'] == ['photos/']
        ls = c.get('/api/buckets/demo-images/objects?prefix=photos/').json()
        assert ls['items'][0]['key'] == 'photos/test.png'
        r = c.get('/api/objects/download?bucket=demo-images&key=photos%2Ftest.png')
        assert r.content == b.getvalue()
        assert 'attachment' in r.headers['content-disposition']
        thumb = c.get('/api/objects/thumbnail?bucket=demo-images&key=photos%2Ftest.png')
        assert thumb.status_code == 200, thumb.text
        assert thumb.headers['content-type'] == 'image/webp'
        assert c.get('/api/objects/presign?bucket=demo-images&key=photos%2Ftest.png').status_code == 200
        audit = c.get('/api/audit').json()['items']
        assert any(e['action'] == 'upload' for e in audit)
        assert c.delete('/api/objects?bucket=demo-images&key=photos%2Ftest.png', headers={'X-CSRF-Token':csrf}).status_code == 200
        assert not fake.buckets['demo-images']
    finally: c.__exit__(None, None, None)


def test_bad_keys_and_health(monkeypatch, tmp_path):
    c, _fake = setup_client(monkeypatch, tmp_path)
    try:
        csrf = login(c)
        assert c.get('/api/health/live').status_code == 200
        assert c.get('/api/health/ready').status_code == 200
        assert c.post('/api/buckets', json={'name':'INVALID_Bucket'}, headers={'X-CSRF-Token':csrf}).status_code == 400
        assert c.post('/api/objects/upload', data={'bucket':'demo-images','key':'../a.png'},
                files={'file':('a.png', b'data', 'image/png')}, headers={'X-CSRF-Token':csrf}).status_code == 400
        assert c.get('/api/objects/download?bucket=demo-images&key=..%2Fa').status_code == 400
    finally: c.__exit__(None, None, None)


def test_unicode_credentials(monkeypatch, tmp_path):
    monkeypatch.setattr(config, 'ADMIN_USER', 'நிர்வாகி')
    monkeypatch.setattr(config, 'ADMIN_PASSWORD', 'பாதுகாப்பான-password-12345')
    c, _ = setup_client(monkeypatch, tmp_path)
    try:
        for username, password in [
            ('wrong', config.ADMIN_PASSWORD),
            (config.ADMIN_USER, 'தவறு'),
        ]:
            assert c.post('/api/auth/login', json={
                'username': username, 'password': password,
            }).status_code == 401
        response = c.post('/api/auth/login', json={
            'username': config.ADMIN_USER, 'password': config.ADMIN_PASSWORD,
        })
        assert response.status_code == 200
        assert c.get('/api/auth/me').json()['username'] == config.ADMIN_USER
    finally:
        c.__exit__(None, None, None)


def test_unicode_download_filename(monkeypatch, tmp_path):
    from urllib.parse import quote
    c, fake = setup_client(monkeypatch, tmp_path)
    try:
        login(c)
        name = 'படம் café "one".png'
        key = 'photos/' + name
        fake.buckets['demo-images'][key] = (b'image', 'image/png')
        for route, disposition in [('download', 'attachment'), ('preview', 'inline')]:
            response = c.get('/api/objects/' + route, params={
                'bucket': 'demo-images', 'key': key,
            })
            assert response.status_code == 200
            assert response.content == b'image'
            header = response.headers['content-disposition']
            assert header.startswith(disposition + '; filename="')
            assert header.endswith("filename*=UTF-8''" + quote(name, safe=''))
    finally:
        c.__exit__(None, None, None)


# Exercise the real routing/dependency chain with an isolated fake data plane.
import pytest
from fastapi.routing import APIRoute


@pytest.fixture
def security_client(monkeypatch, tmp_path):
    c, fake = setup_client(monkeypatch, tmp_path)
    try:
        yield c, fake
    finally:
        c.__exit__(None, None, None)


READ_ROUTES = [route.path for route in main.app.routes
               if isinstance(route, APIRoute) and 'GET' in route.methods
               and not route.path.startswith('/api/health/')]


@pytest.mark.parametrize('route', READ_ROUTES)
def test_every_read_route_requires_login(security_client, route):
    c, _ = security_client
    response = c.get(route.replace('{bucket}', 'demo-images'),
                     params={'bucket': 'demo-images', 'key': 'image.png'})
    assert response.status_code == 401, (route, response.text)
    assert response.json()['detail'] == 'Login required'


@pytest.mark.parametrize('route', ['/api/health/live', '/api/health/ready'])
def test_health_routes_intentionally_public(security_client, route):
    c, _ = security_client
    assert c.get(route).status_code == 200


@pytest.mark.parametrize('token', [None, 'wrong-token'])
@pytest.mark.parametrize('operation', ['create_bucket', 'delete_bucket', 'upload', 'delete_object', 'logout'])
def test_all_writes_require_login_csrf(security_client, token, operation):
    c, fake = security_client
    login(c)
    fake.buckets['demo-images']['keep.png'] = (b'keep', 'image/png')
    before = {name: dict(objects) for name, objects in fake.buckets.items()}
    headers = {} if token is None else {'x-csrf-token': token}
    if operation == 'create_bucket':
        response = c.post('/api/buckets', json={'name': 'new-bucket'}, headers=headers)
    elif operation == 'delete_bucket':
        response = c.delete('/api/buckets/demo-images', headers=headers)
    elif operation == 'upload':
        response = c.post('/api/objects/upload', data={'bucket': 'demo-images', 'key': 'new.png'},
                          files={'file': ('new.png', b'data', 'image/png')}, headers=headers)
    elif operation == 'delete_object':
        response = c.delete('/api/objects', params={'bucket': 'demo-images', 'key': 'keep.png'}, headers=headers)
    else:
        response = c.post('/api/auth/logout', headers=headers)
    assert response.status_code == 403, response.text
    assert response.json()['detail'] == 'CSRF validation failed'
    assert fake.buckets == before
    assert c.get('/api/auth/me').status_code == 200
    assert [row['action'] for row in c.get('/api/audit').json()['items']] == ['login']


BAD_BUCKETS = ['../demo', 'demo/../other', '/demo', r'demo\other', '..',
               'demo..other', 'demo\x00other', '%2e%2e', 'demo.-other', 'demo-.other']
BAD_KEYS = ['', '../secret', 'images/../../secret', 'images/../secret', '/etc/passwd',
            r'images\..\secret', 'images/\x00secret', 'a' * 1025, 'é' * 513]


@pytest.mark.parametrize('value', BAD_BUCKETS)
def test_safe_bucket_rejects_traversal(value):
    from fastapi import HTTPException
    with pytest.raises(HTTPException) as exc:
        security.safe_bucket(value)
    assert exc.value.status_code == 400


@pytest.mark.parametrize('value', BAD_KEYS)
def test_safe_key_rejects_traversal(value):
    from fastapi import HTTPException
    with pytest.raises(HTTPException) as exc:
        security.safe_key(value)
    assert exc.value.status_code == 400


@pytest.mark.parametrize('bucket', BAD_BUCKETS)
def test_bucket_creation_rejects_traversal_before_s3(security_client, bucket):
    c, fake = security_client
    csrf = login(c)
    response = c.post('/api/buckets', json={'name': bucket}, headers={'x-csrf-token': csrf})
    assert response.status_code == 400, response.text
    assert set(fake.buckets) == {'demo-images'}


@pytest.mark.parametrize('key', BAD_KEYS)
@pytest.mark.parametrize('route', ['download', 'preview', 'thumbnail', 'presign', 'delete', 'upload', 'list'])
def test_object_routes_reject_traversal_before_s3(security_client, key, route):
    c, fake = security_client
    csrf = login(c)
    params = {'bucket': 'demo-images', 'key': key}
    headers = {'x-csrf-token': csrf}
    if route == 'upload':
        response = c.post('/api/objects/upload', data=params,
                          files={'file': ('a.png', b'image', 'image/png')}, headers=headers)
        # Multipart empty required strings are rejected by request validation.
        expected = 422 if not key else 400
    elif route == 'delete':
        response = c.delete('/api/objects', params=params, headers=headers)
        expected = 400
    elif route == 'list':
        # Empty prefix intentionally means list bucket root.
        response = c.get('/api/buckets/demo-images/objects', params={'prefix': key})
        expected = 200 if not key else 400
    else:
        response = c.get('/api/objects/' + route, params=params)
        expected = 400
    assert response.status_code == expected, response.text
    assert not fake.buckets['demo-images']
    assert [row['action'] for row in c.get('/api/audit').json()['items']] == ['login']


def test_upload_limit_rejects_before_s3(security_client, monkeypatch):
    c, fake = security_client
    csrf = login(c)
    monkeypatch.setattr(config, 'MAX_UPLOAD_MB', 1)
    content = b'x' * (config.MAX_UPLOAD_MB * 1024 * 1024)
    response = c.post('/api/objects/upload', data={'bucket': 'demo-images', 'key': 'too-big.bin'},
                      files={'file': ('large.bin', content + b'x', 'application/octet-stream')},
                      headers={'x-csrf-token': csrf})
    assert response.status_code == 413, response.text
    assert response.json()['detail'] == 'Upload exceeds 1 MB limit'
    assert not fake.buckets['demo-images']
    assert [row['action'] for row in c.get('/api/audit').json()['items']] == ['login']
    response = c.post('/api/objects/upload', data={'bucket': 'demo-images', 'key': 'at-limit.bin'},
                      files={'file': ('large.bin', content, 'application/octet-stream')},
                      headers={'x-csrf-token': csrf})
    assert response.status_code == 201, response.text
    assert fake.buckets['demo-images']['at-limit.bin'][0] == content


def test_dashboard_insight_shapes_sorting_and_existing_fields(monkeypatch, tmp_path):
    c, fake = setup_client(monkeypatch, tmp_path)
    try:
        assert c.get('/api/dashboard').status_code == 401
        login(c)
        fake.buckets = {'small-bucket': {}, 'empty-bucket': {}, 'large-bucket': {}}
        samples = [('a.JPG', 10), ('b.jpeg', 20), ('c.PNG', 30), ('d.webp', 40),
                   ('e.gif', 50), ('f.avif', 60), ('g.MP4', 100), ('h.mp3', 90),
                   ('i.PDF', 80), ('j.TAR.GZ', 70), ('k.unknown', 120),
                   ('README', 130), ('folder.png/plain', 140)]
        expected_objects = []
        for index, (key, size) in enumerate(samples):
            bucket = 'small-bucket' if index < 3 else 'large-bucket'
            modified = datetime(2026, 1, index + 1, tzinfo=UTC)
            fake.buckets[bucket][key] = (b'x' * size, 'application/octet-stream')
            fake.modified[bucket, key] = modified
            expected_objects.append({'bucket': bucket, 'key': key, 'size': size,
                                     'modified': modified.isoformat()})
        calls = []
        original_list = fake.list_objects_v2

        def listing(**kwargs):
            calls.append(kwargs)
            return original_list(**kwargs)

        monkeypatch.setattr(fake, 'list_objects_v2', listing)
        response = c.get('/api/dashboard')
        assert response.status_code == 200, response.text
        data = response.json()
        existing = {'bucket_count': 3, 'object_count': 13, 'total_bytes': 940,
                    'image_count': 6, 'sampled': False, 'message': ''}
        assert {key: data[key] for key in existing} == existing
        assert {key: type(data[key]) for key in existing} == {key: type(value) for key, value in existing.items()}
        assert set(data) == set(existing) | {'buckets', 'by_type', 'largest', 'recent'}
        assert data['buckets'] == [
            {'name': 'large-bucket', 'object_count': 10, 'total_bytes': 880},
            {'name': 'small-bucket', 'object_count': 3, 'total_bytes': 60},
            {'name': 'empty-bucket', 'object_count': 0, 'total_bytes': 0},
        ]
        assert data['by_type'] == [
            {'type': 'other', 'count': 3, 'bytes': 390},
            {'type': 'image', 'count': 6, 'bytes': 210},
            {'type': 'video', 'count': 1, 'bytes': 100},
            {'type': 'audio', 'count': 1, 'bytes': 90},
            {'type': 'document', 'count': 1, 'bytes': 80},
            {'type': 'archive', 'count': 1, 'bytes': 70},
        ]
        for row in data['buckets']:
            assert {key: type(value) for key, value in row.items()} == {
                'name': str, 'object_count': int, 'total_bytes': int,
            }
        for row in data['by_type']:
            assert {key: type(value) for key, value in row.items()} == {
                'type': str, 'count': int, 'bytes': int,
            }
        assert data['largest'] == sorted(expected_objects, key=lambda row: row['size'], reverse=True)[:5]
        assert data['recent'] == list(reversed(expected_objects))[:8]
        assert len(data['largest']) == 5
        assert len(data['recent']) == 8
        for field in ('largest', 'recent'):
            for row in data[field]:
                assert set(row) == {'bucket', 'key', 'size', 'modified'}
                assert type(row['bucket']) is str and type(row['key']) is str
                assert type(row['size']) is int and type(row['modified']) is str
                datetime.fromisoformat(row['modified'])
        # Only the existing listing calls: one per bucket, including the empty one.
        assert calls == [{'Bucket': bucket, 'MaxKeys': 500} for bucket in fake.buckets]
    finally:
        c.__exit__(None, None, None)


@pytest.mark.parametrize('empty_buckets', [True, False])
def test_dashboard_empty_storage(monkeypatch, tmp_path, empty_buckets):
    c, fake = setup_client(monkeypatch, tmp_path)
    try:
        login(c)
        fake.buckets = {'empty-bucket': {}} if empty_buckets else {}
        assert c.get('/api/dashboard').json() == {
            'bucket_count': int(empty_buckets), 'object_count': 0, 'total_bytes': 0,
            'image_count': 0, 'sampled': False, 'message': '',
            'buckets': [{'name': 'empty-bucket', 'object_count': 0, 'total_bytes': 0}] if empty_buckets else [],
            'by_type': [], 'largest': [], 'recent': [],
        }
    finally:
        c.__exit__(None, None, None)


@pytest.mark.parametrize('object_count', [2999, 3000, 3001])
def test_dashboard_pagination_cap_and_unreached_buckets(monkeypatch, tmp_path, object_count):
    c, fake = setup_client(monkeypatch, tmp_path)
    try:
        login(c)
        fake.buckets = {
            'first-bucket': {f'{index:04}.png': (b'x', 'image/png') for index in range(object_count)},
            'later-bucket': {'later.mp4': (b'xx', 'video/mp4')},
            'empty-bucket': {},
        }
        calls = []
        original_list = fake.list_objects_v2

        def listing(**kwargs):
            calls.append(kwargs)
            return original_list(**kwargs)

        monkeypatch.setattr(fake, 'list_objects_v2', listing)
        data = c.get('/api/dashboard').json()
        reached_later = object_count < 3000
        assert data['bucket_count'] == 3
        assert data['object_count'] == 3000
        assert data['total_bytes'] == (3001 if reached_later else 3000)
        assert data['image_count'] == min(3000, object_count)
        assert data['sampled'] is True
        assert data['message'] == 'Statistics scan is capped at 3000 objects; not full billing metrics.'
        assert data['buckets'] == [
            {'name': 'first-bucket', 'object_count': min(3000, object_count), 'total_bytes': min(3000, object_count)},
            {'name': 'later-bucket', 'object_count': int(reached_later), 'total_bytes': 2 * int(reached_later)},
            {'name': 'empty-bucket', 'object_count': 0, 'total_bytes': 0},
        ]
        assert sum(row['count'] for row in data['by_type']) == 3000
        assert sum(row['bytes'] for row in data['by_type']) == data['total_bytes']
        assert len(data['largest']) == 5 and len(data['recent']) == 8
        assert len(calls) == 6 + int(reached_later)
        assert [call.get('ContinuationToken') for call in calls[:6]] == [None, '500', '1000', '1500', '2000', '2500']
        assert calls[5]['MaxKeys'] == 500
        if reached_later:
            assert calls[6] == {'Bucket': 'later-bucket', 'MaxKeys': 1}
        else:
            assert all(call['Bucket'] == 'first-bucket' for call in calls)
    finally:
        c.__exit__(None, None, None)


def test_dashboard_recent_uses_instants_and_includes_zero_byte_types(monkeypatch, tmp_path):
    c, fake = setup_client(monkeypatch, tmp_path)
    try:
        login(c)
        fake.buckets = {'demo-images': {'older.txt': (b'', 'text/plain'), 'newer.zip': (b'', 'application/zip')}}
        # Lexical ISO ordering is opposite to chronological ordering across offsets.
        fake.modified['demo-images', 'older.txt'] = datetime.fromisoformat('2026-01-01T10:00:00+02:00')
        fake.modified['demo-images', 'newer.zip'] = datetime.fromisoformat('2026-01-01T09:00:00+00:00')
        data = c.get('/api/dashboard').json()
        assert [row['key'] for row in data['recent']] == ['newer.zip', 'older.txt']
        assert {row['type'] for row in data['by_type']} == {'document', 'archive'}
        assert all(row['count'] == 1 and row['bytes'] == 0 for row in data['by_type'])
        assert data['object_count'] == 2 and data['total_bytes'] == 0
        assert len(data['largest']) == 2 and len(data['recent']) == 2
    finally:
        c.__exit__(None, None, None)


def test_delete_bucket_refuses_non_empty_even_if_backend_would_allow_it(monkeypatch, tmp_path):
    c, fake = setup_client(monkeypatch, tmp_path)
    try:
        csrf = login(c)
        # SeaweedFS-like: DeleteBucket succeeds even when the bucket still holds objects.
        fake.delete_bucket = lambda Bucket: fake.buckets.pop(Bucket)
        fake.buckets['demo-images']['a.txt'] = (b'x', {})
        r = c.delete('/api/buckets/demo-images', headers={'X-CSRF-Token': csrf})
        assert r.status_code == 409, r.text
        assert fake.buckets.get('demo-images')
        fake.buckets['demo-images'].clear()
        assert c.delete('/api/buckets/demo-images', headers={'X-CSRF-Token': csrf}).status_code == 200
        assert 'demo-images' not in fake.buckets
    finally: c.__exit__(None, None, None)


def test_audit_pagination_total_and_detail(monkeypatch, tmp_path):
    c, _fake = setup_client(monkeypatch, tmp_path)
    try:
        csrf = login(c)
        for name in ('aa1', 'bb2', 'cc3'):
            assert c.post('/api/buckets', json={'name': name}, headers={'X-CSRF-Token': csrf}).status_code == 201
        page1 = c.get('/api/audit?limit=2&offset=0').json()
        page2 = c.get('/api/audit?limit=2&offset=2').json()
        assert page1['total'] == page2['total'] >= 4 and len(page1['items']) == 2
        assert page1['limit'] == 2 and page2['offset'] == 2
        ids = [r['id'] for r in page1['items'] + page2['items']]
        assert ids == sorted(ids, reverse=True) and len(set(ids)) == len(ids)
        one = c.get(f"/api/audit/{page1['items'][0]['id']}")
        assert one.status_code == 200 and one.json() == page1['items'][0]
        assert c.get('/api/audit/99999').status_code == 404
        assert c.get('/api/audit?offset=-1').status_code == 422
        c.cookies.clear()
        assert c.get('/api/audit/1').status_code == 401
    finally: c.__exit__(None, None, None)


def test_uploads_by_day_counts_full_window_not_last_200_events(monkeypatch, tmp_path):
    c, _fake = setup_client(monkeypatch, tmp_path)
    try:
        login(c)
        from app import db
        for _ in range(3):
            db.audit('upload', 'demo-images', 'a.png')
        for _ in range(250):
            db.audit('delete', 'demo-images', 'a.png')
        items = c.get('/api/audit/uploads-by-day?days=14').json()['items']
        assert len(items) == 14
        assert items[-1]['count'] == 3 and sum(i['count'] for i in items) == 3
        c.cookies.clear()
        assert c.get('/api/audit/uploads-by-day').status_code == 401
    finally: c.__exit__(None, None, None)
