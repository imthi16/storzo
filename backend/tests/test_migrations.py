import json
import os
import stat
import time

os.environ.setdefault('ADMIN_PASSWORD', 'StrongPasswordForTests12345')
os.environ.setdefault('SESSION_SECRET', 'some-long-session-secret-for-tests-change-this-1234')
os.environ.setdefault('S3_ACCESS_KEY', 'testingkey')
os.environ.setdefault('S3_SECRET_KEY', 'testingsecret')
os.environ.setdefault('AUDIT_DB_PATH', '/tmp/storzo-test-audit.db')

import pytest
from fastapi.testclient import TestClient

from app import config, main, migrations, s3

SECRET = 'SOURCE-SECRET-DO-NOT-LOG-12345'
AKID = 'AKIAFAKE12345'

FAKE_RCLONE = r'''#!/bin/sh
# Fail if any secret reaches argv.
for a in "$@"; do
  case "$a" in *SOURCE-SECRET-DO-NOT-LOG*) echo "secret-in-argv" >&2; exit 42;; esac
done
if [ -z "$RCLONE_CONFIG_SRC_SECRET_ACCESS_KEY" ]; then echo "missing env secret" >&2; exit 43; fi
mode=$(cat "$(dirname "$0")/mode" 2>/dev/null || echo ok)
cmd="$1"
if [ "$cmd" = "copy" ]; then
  if [ "$mode" = "fail" ]; then
    printf '%s\n' '{"level":"error","msg":"fake copy failure","stats":{"bytes":0,"totalBytes":10,"transfers":0,"totalTransfers":2,"errors":1}}' >&2
    exit 1
  fi
  if [ "$mode" = "slow" ]; then sleep 3; fi
  if [ "$mode" = "quick" ]; then exit 0; fi
  printf '%s\n' '{"level":"notice","msg":"stats","stats":{"bytes":5,"totalBytes":10,"transfers":1,"totalTransfers":2,"errors":0}}' >&2
  printf '%s\n' '{"level":"notice","msg":"stats","stats":{"bytes":10,"totalBytes":10,"transfers":2,"totalTransfers":2,"errors":0}}' >&2
  exit 0
fi
if [ "$cmd" = "size" ]; then printf '%s\n' '{"count":3,"bytes":7}'; exit 0; fi
if [ "$cmd" = "check" ]; then
  if [ "$mode" = "mismatch" ]; then
    printf '%s\n' '{"level":"error","msg":"1 files differ"}' >&2
    exit 1
  fi
  exit 0
fi
exit 44
'''


class FakeS3:
    def __init__(self):
        self.buckets = {'demo-images': {}}

    def head_bucket(self, Bucket):
        if Bucket not in self.buckets:
            from botocore.exceptions import ClientError
            raise ClientError({'Error': {'Code': '404'}}, 'HeadBucket')

    def list_buckets(self):
        return {'Buckets': [{'Name': n} for n in self.buckets]}


class FakeSource:
    def list_objects_v2(self, **kwargs):
        return {'Contents': [{'Key': 'a.png', 'Size': 1073741824}, {'Key': 'b.png', 'Size': 1073741824}]}


def setup(monkeypatch, tmp_path):
    fake = FakeS3()
    monkeypatch.setattr(s3, 's3_client', lambda public=False: fake)
    monkeypatch.setattr(config, 'DB_PATH', tmp_path / 'audit.db')
    migrations.init_migrations()
    bindir = tmp_path / 'bin'
    bindir.mkdir(exist_ok=True)
    script = bindir / 'rclone'
    script.write_text(FAKE_RCLONE)
    global MODE_FILE
    MODE_FILE = bindir / 'mode'
    script.chmod(script.stat().st_mode | stat.S_IEXEC)
    monkeypatch.setenv('PATH', str(bindir) + os.pathsep + os.environ['PATH'])
    c = TestClient(main.app)
    c.__enter__()
    return c, fake


def login(c):
    r = c.post('/api/auth/login', json={'username': 'admin', 'password': config.ADMIN_PASSWORD})
    assert r.status_code == 200, r.text
    return r.json()['csrf']


def body(dest_bucket='demo-images', endpoint=None):
    return {
        'source': {'endpoint': endpoint, 'region': 'us-east-1', 'bucket': 'src-bucket', 'prefix': '',
                   'access_key_id': AKID, 'secret_access_key': SECRET},
        'destination': {'bucket': dest_bucket, 'prefix': 'imported/'},
    }


def wait_state(c, job_id, states, timeout=10):
    deadline = time.time() + timeout
    job = None
    while time.time() < deadline:
        job = c.get(f'/api/migrations/{job_id}').json()
        if job['state'] in states:
            return job
        time.sleep(0.05)
    return job


def test_estimate(monkeypatch, tmp_path):
    c, _ = setup(monkeypatch, tmp_path)
    try:
        csrf = login(c)
        monkeypatch.setattr(migrations, 'make_source_client', lambda src: FakeSource())
        monkeypatch.setattr(migrations, 'EGRESS_USD_PER_GB', 0.09)
        r = c.post('/api/migrations/estimate', json=body(), headers={'X-CSRF-Token': csrf})
        assert r.status_code == 200, r.text
        d = r.json()
        assert d['object_count'] == 2 and d['total_bytes'] == 2147483648 and d['partial'] is False
        assert d['egress_usd'] == pytest.approx(2 * 0.09)
        assert 'Estimate only' in d['price_note'] and SECRET not in r.text
    finally:
        c.__exit__(None, None, None)


def test_estimate_requires_write(monkeypatch, tmp_path):
    c, _ = setup(monkeypatch, tmp_path)
    try:
        login(c)
        assert c.post('/api/migrations/estimate', json=body()).status_code == 403
    finally:
        c.__exit__(None, None, None)


def test_ssrf_refusal(monkeypatch, tmp_path):
    c, _ = setup(monkeypatch, tmp_path)
    try:
        csrf = login(c)
        for ep in ('http://127.0.0.1:9000', 'http://169.254.169.254/latest', 'http://10.0.0.5:9000', 'http://[::1]:9000'):
            r = c.post('/api/migrations/estimate', json=body(endpoint=ep), headers={'X-CSRF-Token': csrf})
            assert r.status_code == 400, (ep, r.text)
    finally:
        c.__exit__(None, None, None)


def test_start_progress_verify_and_no_secret_leak(monkeypatch, tmp_path):
    c, _ = setup(monkeypatch, tmp_path)
    try:
        csrf = login(c)
        set_mode('ok')
        r = c.post('/api/migrations', json=body(), headers={'X-CSRF-Token': csrf})
        assert r.status_code == 201, r.text
        job = wait_state(c, r.json()['id'], {'done'})
        assert job['state'] == 'done' and job['verify_status'] == 'verified'
        assert job['bytes_done'] == 10 and job['objects_done'] == 2
        assert c.get('/api/migrations').json()['items']
        audit = c.get('/api/audit').json()['items']
        blob = json.dumps(audit) + json.dumps(job)
        assert SECRET not in blob and AKID not in blob
        actions = [a['action'] for a in audit]
        assert 'migration.start' in actions and 'migration.finish' in actions
    finally:
        c.__exit__(None, None, None)


def test_verify_mismatch(monkeypatch, tmp_path):
    c, _ = setup(monkeypatch, tmp_path)
    try:
        csrf = login(c)
        set_mode('mismatch')
        r = c.post('/api/migrations', json=body(), headers={'X-CSRF-Token': csrf})
        job = wait_state(c, r.json()['id'], {'done'})
        assert job['verify_status'] == 'mismatched'
    finally:
        c.__exit__(None, None, None)


def test_copy_failure(monkeypatch, tmp_path):
    c, _ = setup(monkeypatch, tmp_path)
    try:
        csrf = login(c)
        set_mode('fail')
        r = c.post('/api/migrations', json=body(), headers={'X-CSRF-Token': csrf})
        job = wait_state(c, r.json()['id'], {'failed'})
        assert job['state'] == 'failed' and job['errors']
        assert SECRET not in json.dumps(job)
    finally:
        c.__exit__(None, None, None)


def test_cancel(monkeypatch, tmp_path):
    c, _ = setup(monkeypatch, tmp_path)
    try:
        csrf = login(c)
        set_mode('slow')
        r = c.post('/api/migrations', json=body(), headers={'X-CSRF-Token': csrf})
        job_id = r.json()['id']
        wait_state(c, job_id, {'running'})
        r2 = c.post(f'/api/migrations/{job_id}/cancel', headers={'X-CSRF-Token': csrf})
        assert r2.status_code == 200
        job = wait_state(c, job_id, {'cancelled'})
        assert job['state'] == 'cancelled'
    finally:
        c.__exit__(None, None, None)


def test_missing_rclone_503_and_missing_dest_and_unknown_job(monkeypatch, tmp_path):
    c, _ = setup(monkeypatch, tmp_path)
    try:
        csrf = login(c)
        monkeypatch.setattr(migrations, 'rclone_path', lambda: None)
        assert c.post('/api/migrations', json=body(), headers={'X-CSRF-Token': csrf}).status_code == 503
        monkeypatch.setattr(migrations, 'rclone_path', lambda: '/bin/true')
        assert c.post('/api/migrations', json=body(dest_bucket='no-such-bucket'), headers={'X-CSRF-Token': csrf}).status_code == 404
        assert c.get('/api/migrations/nope').status_code == 404
        assert c.post('/api/migrations/nope/cancel', headers={'X-CSRF-Token': csrf}).status_code == 404
    finally:
        c.__exit__(None, None, None)


def test_parse_stats_line():
    assert migrations.parse_stats_line('{"stats":{"bytes":3,"totalBytes":9,"transfers":1,"totalTransfers":4}}') == {
        'bytes_done': 3, 'objects_done': 1, 'total_bytes': 9, 'total_objects': 4, 'errors': 0}
    assert migrations.parse_stats_line('not json') is None
    assert migrations.parse_stats_line('{"msg":"hi"}') is None


def test_build_env_uses_env_not_argv():
    env = migrations.build_env({'access_key_id': AKID, 'secret_access_key': SECRET, 'region': 'eu-west-1', 'endpoint': 'https://s3.example.com'})
    assert env['RCLONE_CONFIG_SRC_SECRET_ACCESS_KEY'] == SECRET
    assert env['RCLONE_CONFIG_DST_ENDPOINT'] == config.S3_ENDPOINT


def test_rclone_env_does_not_inherit_app_secrets(monkeypatch):
    from app import migrations
    for name in ('ADMIN_PASSWORD', 'SESSION_SECRET', 'CRED_SIGNING_KEY'):
        monkeypatch.setenv(name, 'must-not-leak-into-rclone')
    env = migrations.build_env({'access_key_id': 'a', 'secret_access_key': 's', 'bucket': 'b'})
    assert not {'ADMIN_PASSWORD', 'SESSION_SECRET', 'CRED_SIGNING_KEY'} & set(env)
    assert 'must-not-leak-into-rclone' not in env.values()


MODE_FILE = None


def set_mode(mode):
    # A file, not an env var: build_env deliberately drops non-allowlisted variables.
    MODE_FILE.write_text(mode)


def test_fast_copy_without_progress_still_reports_counts(monkeypatch, tmp_path):
    # Found live: a copy that finishes inside one stats interval emitted no progress and the job showed 0 of 0.
    c, _ = setup(monkeypatch, tmp_path)
    try:
        csrf = login(c)
        set_mode('quick')
        r = c.post('/api/migrations', json=body(), headers={'X-CSRF-Token': csrf})
        job = wait_state(c, r.json()['id'], {'done'})
        assert job['verify_status'] == 'verified'
        assert job['objects_done'] == 3 and job['bytes_done'] == 7 and job['total_objects'] == 3
    finally:
        c.__exit__(None, None, None)
