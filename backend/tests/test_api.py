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
