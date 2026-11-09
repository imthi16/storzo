#!/usr/bin/env python3
"""Real S3 restart-integrity test against Docker Compose.

Creates a uniquely named scratch bucket, uploads a randomly generated object,
restarts ONLY the storage container, then downloads and SHA256-verifies the exact
same bytes. Cleans up its own bucket. Never touches any existing bucket.
"""
import hashlib
import os
import secrets
import subprocess
import time
from pathlib import Path

import boto3
from botocore.config import Config

root = Path(__file__).resolve().parents[1]
for line in (root / '.env').read_text().splitlines():
    if '=' in line and not line.startswith('#'):
        key, value = line.split('=', 1)
        os.environ.setdefault(key, value)

s3 = boto3.client('s3', endpoint_url=os.getenv('SMOKE_S3_ENDPOINT', 'http://localhost:8333'),
    aws_access_key_id=os.environ['S3_ACCESS_KEY'], aws_secret_access_key=os.environ['S3_SECRET_KEY'],
    region_name='us-east-1', config=Config(signature_version='s3v4', s3={'addressing_style':'path'},
                                          retries={'max_attempts':2,'mode':'standard'}))

bucket = 'storzo-restart-' + secrets.token_hex(6)
key = 'test/persist.bin'
data = secrets.token_bytes(256 * 1024)
expected = hashlib.sha256(data).hexdigest()
created = False
try:
    s3.create_bucket(Bucket=bucket)
    created = True
    s3.put_object(Bucket=bucket, Key=key, Body=data)
    print('TEST: uploaded 256 KiB of random data, SHA256:', expected)
    subprocess.run(['docker','compose','restart','storage'], cwd=root, check=True)
    print('TEST: restarted storage container; polling for S3 recovery')
    for attempt in range(30):
        try:
            returned = s3.get_object(Bucket=bucket, Key=key)['Body'].read()
            actual = hashlib.sha256(returned).hexdigest()
            assert actual == expected, f'INTEGRITY FAILED: expected={expected}, actual={actual}'
            print('PASS: object survived storage restart; exact SHA-256 match')
            break
        except AssertionError:
            raise
        except Exception as exc:
            if attempt == 29:
                raise RuntimeError('Storage did not recover within approximately 90 s') from exc
            time.sleep(3)
finally:
    if created:
        try:
            s3.delete_object(Bucket=bucket, Key=key)
            s3.delete_bucket(Bucket=bucket)
            print('Scratch data cleaned up')
        except Exception as exc:
            print('WARNING: scratch bucket cleanup failed:', exc)
