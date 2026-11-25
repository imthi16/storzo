"""Verify real Cloud Connect copies in the disposable GitHub Actions stack."""

import hashlib
import json
import os
import time
import urllib.error
import urllib.request
from uuid import uuid4

import boto3
from botocore.config import Config
from botocore.exceptions import BotoCoreError, ClientError
from gateway_e2e import Console, require_env

MIGRATIONS = '/api/migrations'
TERMINAL = {'done', 'failed', 'cancelled'}
FIXTURE = 'http://127.0.0.1:18334/fixture'


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def s3(endpoint, access_key_var, secret_key_var):
    return boto3.client(
        's3', endpoint_url=endpoint, region_name='us-east-1',
        aws_access_key_id=require_env(access_key_var),
        aws_secret_access_key=require_env(secret_key_var),
        config=Config(s3={'addressing_style': 'path'}, signature_version='s3v4',
                      connect_timeout=2, read_timeout=10, retries={'total_max_attempts': 1}),
    )


def wait_for(check, message, seconds=45):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        result = check()
        if result:
            return result
        time.sleep(0.25)
    raise RuntimeError(message)


def source_ready(client):
    try:
        client.list_buckets()
    except (ClientError, BotoCoreError):
        return False
    return True


def gate_received():
    try:
        with urllib.request.urlopen(FIXTURE + '/received', timeout=2) as response:
            return response.status == 204
    except urllib.error.HTTPError as exc:
        if exc.code == 404:
            exc.close()
            return False
        raise


def snapshot(client, bucket, prefix):
    objects = {}
    for page in client.get_paginator('list_objects_v2').paginate(Bucket=bucket, Prefix=prefix):
        for item in page.get('Contents', []):
            key = item['Key']
            with client.get_object(Bucket=bucket, Key=key)['Body'] as body:
                objects[key] = hashlib.file_digest(body, 'sha256').hexdigest()
    return objects


def expected(prefix, objects):
    return {prefix + key: hashlib.sha256(value).hexdigest() for key, value in objects.items()}


def run():
    console = Console('http://127.0.0.1:8088')
    status, _ = console.login(os.getenv('ADMIN_USER', 'admin'), require_env('ADMIN_PASSWORD'))
    require(status == 200 and console.csrf, f'login failed (HTTP {status})')
    source = s3('http://127.0.0.1:18333', 'MIGRATION_SOURCE_ACCESS_KEY', 'MIGRATION_SOURCE_SECRET_KEY')
    dest = s3('http://127.0.0.1:8333', 'S3_ACCESS_KEY', 'S3_SECRET_KEY')
    wait_for(lambda: source_ready(source), 'S3 source fixture did not become ready')
    suffix = uuid4().hex[:12]
    source_bucket = 'migration-source-' + suffix
    dest_bucket = 'migration-destination-' + suffix
    source.create_bucket(Bucket=source_bucket)
    dest.create_bucket(Bucket=dest_bucket)

    copy_objects = {'one.bin': b'first', 'nested/two.bin': b'second', 'three.bin': b'third'}
    followup = {'one.bin': b'after cancellation'}
    fixtures = {'outside.bin': b'excluded', 'cancel/held.bin': b'held response'}
    fixtures.update({'copy/' + key: data for key, data in copy_objects.items()})
    fixtures.update({'followup/' + key: data for key, data in followup.items()})
    for key, data in fixtures.items():
        source.put_object(Bucket=source_bucket, Key=key, Body=data)

    def request(source_prefix, dest_prefix):
        return {
            'source': {
                'endpoint': 'http://migration-proxy:8080', 'region': 'us-east-1',
                'bucket': source_bucket, 'prefix': source_prefix,
                'access_key_id': require_env('MIGRATION_SOURCE_ACCESS_KEY'),
                'secret_access_key': require_env('MIGRATION_SOURCE_SECRET_KEY'),
            },
            'destination': {'bucket': dest_bucket, 'prefix': dest_prefix},
        }

    def api(method, path, status, body=None):
        observed, raw = console.call(method, path, body)
        require(observed == status, f'{method} {path}: expected HTTP {status}, got {observed}')
        return json.loads(raw)

    def start(scenario, body):
        job_id = api('POST', MIGRATIONS, 201, body)['id']
        print(f'{scenario}: started job {job_id}', flush=True)
        return f'{MIGRATIONS}/{job_id}'

    def poll(path, accept, seconds=45):
        last = {}

        def check():
            nonlocal last
            last = api('GET', path, 200)
            return last if accept(last) or last['state'] in TERMINAL else None

        try:
            job = wait_for(check, f'{path}: job timed out', seconds)
        finally:
            print(f"{path}: state={last.get('state')} verify={last.get('verify_status')}", flush=True)
        require(accept(job), f'{path}: unexpected terminal state')
        return job

    def verify_copy(path, prefix, objects):
        poll(path, lambda j: j['state'] == 'done' and j['verify_status'] == 'verified')
        require(snapshot(dest, dest_bucket, prefix) == expected(prefix, objects),
                f'{path}: destination keys or SHA-256 digests differ')

    print('Estimate and copy', flush=True)
    body = request('copy/', 'imported/')
    estimate = api('POST', MIGRATIONS + '/estimate', 200, body)
    require(estimate['object_count'] == len(copy_objects)
            and estimate['total_bytes'] == sum(map(len, copy_objects.values()))
            and estimate['partial'] is False, 'Estimate count, bytes or partial flag differ')
    require(not snapshot(dest, dest_bucket, ''), 'Estimate wrote destination objects')
    verify_copy(start('Copy', body), 'imported/', copy_objects)

    print('Cancel in-flight copy', flush=True)
    try:
        path = start('Cancel', request('cancel/', 'cancelled/'))
        poll(path, lambda j: j['state'] == 'running' and gate_received(), seconds=30)
        api('POST', path + '/cancel', 200)
        poll(path, lambda j: j['state'] == 'cancelled', seconds=15)
    finally:
        with urllib.request.urlopen(urllib.request.Request(FIXTURE + '/release', method='POST'),
                                   timeout=5):
            pass
    require(not snapshot(dest, dest_bucket, 'cancelled/'), 'Cancelled job wrote a complete object')

    verify_copy(start('Copy after cancellation', request('followup/', 'followup/')),
                'followup/', followup)
    require(snapshot(source, source_bucket, '') == expected('', fixtures), 'Source objects changed')
    print('Cloud Connect acceptance passed', flush=True)


if __name__ == '__main__':
    try:
        run()
    except RuntimeError as exc:
        raise SystemExit(str(exc)) from None
    except (BotoCoreError, ClientError, OSError, ValueError, KeyError) as exc:
        raise SystemExit(f'Cloud Connect acceptance failed ({type(exc).__name__})') from None
