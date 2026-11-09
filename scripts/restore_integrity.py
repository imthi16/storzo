#!/usr/bin/env python3
"""Backup -> restore drill against Docker Compose (non-destructive).

Uploads a random object to a scratch bucket, stops ONLY the storage container,
tars its data volume, restarts it, then restores the tar into a brand-new
throwaway volume, boots a second SeaweedFS on 127.0.0.1:18333 from that copy and
SHA-256-verifies the object. Reports backup/restore timings (rough RPO/RTO inputs).
The live volume is only read; throwaway volume/container/bucket are always removed.
"""
import hashlib
import os
import secrets
import subprocess
import tempfile
import time
from pathlib import Path

import boto3
from botocore.config import Config

root = Path(__file__).resolve().parents[1]
for line in (root / '.env').read_text().splitlines():
    if '=' in line and not line.startswith('#'):
        key, value = line.split('=', 1)
        os.environ.setdefault(key, value)


def sh(*args, **kw):
    return subprocess.run(args, cwd=root, check=True, text=True, capture_output=True, **kw).stdout.strip()


def client(endpoint):
    return boto3.client('s3', endpoint_url=endpoint, aws_access_key_id=os.environ['S3_ACCESS_KEY'],
        aws_secret_access_key=os.environ['S3_SECRET_KEY'], region_name='us-east-1',
        config=Config(signature_version='s3v4', s3={'addressing_style': 'path'},
                      retries={'max_attempts': 2, 'mode': 'standard'}))


cid = sh('docker', 'compose', 'ps', '-q', 'storage')
assert cid, 'storage container not running; run docker compose up -d first'
image = sh('docker', 'inspect', '-f', '{{.Config.Image}}', cid)
volume = sh('docker', 'inspect', '-f', '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}', cid)
suffix = secrets.token_hex(4)
tmp_volume, tmp_container = f'storzo-restore-{suffix}', f'storzo-restore-{suffix}'
backup_dir = tempfile.mkdtemp(prefix='storzo-backup-')
archive = 'data.tar'

live = client(os.getenv('SMOKE_S3_ENDPOINT', 'http://localhost:8333'))
bucket = 'storzo-restore-' + suffix
obj_key = 'test/restore.bin'
data = secrets.token_bytes(256 * 1024)
expected = hashlib.sha256(data).hexdigest()
created = False
try:
    live.create_bucket(Bucket=bucket)
    created = True
    live.put_object(Bucket=bucket, Key=obj_key, Body=data)
    print('TEST: uploaded 256 KiB random object, SHA256:', expected)

    # Stop storage so the tar is a consistent copy (cold backup).
    t0 = time.time()
    sh('docker', 'compose', 'stop', 'storage')
    try:
        sh('docker', 'run', '--rm', '-v', f'{volume}:/data:ro', '-v', f'{backup_dir}:/backup',
           'alpine', 'tar', 'cf', f'/backup/{archive}', '-C', '/data', '.')
    finally:
        sh('docker', 'compose', 'start', 'storage')
    print(f'TEST: cold backup of volume {volume} took {time.time() - t0:.1f}s')

    t1 = time.time()
    sh('docker', 'volume', 'create', tmp_volume)
    sh('docker', 'run', '--rm', '-v', f'{tmp_volume}:/data', '-v', f'{backup_dir}:/backup:ro',
       'alpine', 'tar', 'xf', f'/backup/{archive}', '-C', '/data')
    sh('docker', 'run', '-d', '--name', tmp_container, '-v', f'{tmp_volume}:/data',
       '-p', '127.0.0.1:18333:8333',
       '-e', f'AWS_ACCESS_KEY_ID={os.environ["S3_ACCESS_KEY"]}',
       '-e', f'AWS_SECRET_ACCESS_KEY={os.environ["S3_SECRET_KEY"]}',
       '-e', f'WEED_ADMIN_PASSWORD={os.environ["WEED_ADMIN_PASSWORD"]}', image)
    restored = client('http://127.0.0.1:18333')
    for attempt in range(30):
        try:
            got = restored.get_object(Bucket=bucket, Key=obj_key)['Body'].read()
            break
        except Exception as exc:
            if attempt == 29:
                raise RuntimeError('Restored copy did not serve the object within ~90 s') from exc
            time.sleep(3)
    actual = hashlib.sha256(got).hexdigest()
    assert actual == expected, f'RESTORE INTEGRITY FAILED: expected={expected}, actual={actual}'
    print(f'PASS: restored copy served exact bytes (SHA-256 match); restore took {time.time() - t1:.1f}s')
finally:
    subprocess.run(['docker', 'rm', '-f', tmp_container], capture_output=True)
    subprocess.run(['docker', 'volume', 'rm', '-f', tmp_volume], capture_output=True)
    subprocess.run(['docker', 'run', '--rm', '-v', f'{backup_dir}:/backup', 'alpine', 'rm', '-rf', f'/backup/{archive}'],
                   capture_output=True)
    Path(backup_dir).rmdir() if not any(Path(backup_dir).iterdir()) else None
    if created:
        try:
            live.delete_object(Bucket=bucket, Key=obj_key)
            live.delete_bucket(Bucket=bucket)
            print('Scratch data cleaned up')
        except Exception as exc:
            print('WARNING: scratch bucket cleanup failed:', exc)
