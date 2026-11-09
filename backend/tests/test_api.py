import io
import os
from datetime import datetime, timezone

from PIL import Image

os.environ.setdefault('ADMIN_PASSWORD', 'StrongPasswordForTests12345')
os.environ.setdefault('SESSION_SECRET', 'some-long-session-secret-for-tests-change-this-1234')
os.environ.setdefault('S3_ACCESS_KEY', 'testingkey')
os.environ.setdefault('S3_SECRET_KEY', 'testingsecret')
os.environ.setdefault('AUDIT_DB_PATH', '/tmp/storzo-test-audit.db')

from fastapi.testclient import TestClient
from app import main


class Body(io.BytesIO):
    def iter_chunks(self, chunk_size=65536):
        while chunk := self.read(chunk_size):
            yield chunk


class FakeS3:
    def __init__(self):
        self.buckets = {'demo-images': {}}

    def list_buckets(self):
        return {'Buckets': [{'Name': name, 'CreationDate': datetime.now(timezone.utc)} for name in self.buckets]}

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
                contents.append({'Key': key, 'Size': len(value[0]), 'LastModified': datetime.now(timezone.utc), 'ETag': '"abc"'})
        return {'Contents': contents[:MaxKeys], 'CommonPrefixes': [{'Prefix': x} for x in sorted(folders)]}

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
    monkeypatch.setattr(main, 's3_client', lambda public=False: fake)
    monkeypatch.setattr(main, 'DB_PATH', tmp_path / 'audit.db')
    c = TestClient(main.app)
    c.__enter__()
    return c, fake


def login(c):
    r = c.post('/api/auth/login', json={'username':'admin','password':main.ADMIN_PASSWORD})
    assert r.status_code == 200, r.text
    return r.json()['csrf']


def test_auth_and_csrf(monkeypatch, tmp_path):
    c, fake = setup_client(monkeypatch, tmp_path)
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
    monkeypatch.setattr(main, 'ADMIN_USER', 'நிர்வாகி')
    monkeypatch.setattr(main, 'ADMIN_PASSWORD', 'பாதுகாப்பான-password-12345')
    c, _ = setup_client(monkeypatch, tmp_path)
    try:
        for username, password in [
            ('wrong', main.ADMIN_PASSWORD),
            (main.ADMIN_USER, 'தவறு'),
        ]:
            assert c.post('/api/auth/login', json={
                'username': username, 'password': password,
            }).status_code == 401
        response = c.post('/api/auth/login', json={
            'username': main.ADMIN_USER, 'password': main.ADMIN_PASSWORD,
        })
        assert response.status_code == 200
        assert c.get('/api/auth/me').json()['username'] == main.ADMIN_USER
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
        main.safe_bucket(value)
    assert exc.value.status_code == 400


@pytest.mark.parametrize('value', BAD_KEYS)
def test_safe_key_rejects_traversal(value):
    from fastapi import HTTPException
    with pytest.raises(HTTPException) as exc:
        main.safe_key(value)
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
    monkeypatch.setattr(main, 'MAX_UPLOAD_MB', 1)
    content = b'x' * (main.MAX_UPLOAD_MB * 1024 * 1024)
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
