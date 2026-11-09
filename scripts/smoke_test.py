#!/usr/bin/env python3
"""S3 smoke test: create, PUT, GET, verify SHA-256, delete.
Requires boto3, .env, and running docker compose stack.
"""
import hashlib
import os
import sys
import uuid
from pathlib import Path

import boto3
from botocore.config import Config

root = Path(__file__).resolve().parents[1]
for line in (root / '.env').read_text().splitlines():
    if '=' in line and not line.startswith('#'):
        k, v = line.split('=',1); os.environ.setdefault(k,v)
client = boto3.client('s3', endpoint_url=os.getenv('SMOKE_S3_ENDPOINT', 'http://localhost:8333'),
    aws_access_key_id=os.environ['S3_ACCESS_KEY'], aws_secret_access_key=os.environ['S3_SECRET_KEY'],
    region_name='us-east-1', config=Config(signature_version='s3v4', s3={'addressing_style':'path'}))
bucket = 'storzo-smoke-' + uuid.uuid4().hex[:12]
key = 'images/integrity-check.txt'
data = b'Storzo disk-backed object integrity smoke test\n' * 500
try:
    client.create_bucket(Bucket=bucket)
    client.put_object(Bucket=bucket, Key=key, Body=data, ContentType='text/plain')
    received = client.get_object(Bucket=bucket, Key=key)['Body'].read()
    assert hashlib.sha256(received).digest() == hashlib.sha256(data).digest(), 'Checksum mismatch'
    items = client.list_objects_v2(Bucket=bucket).get('Contents', [])
    assert any(o['Key'] == key for o in items), 'List missing uploaded file'
    print('PASS: create bucket, put, get, SHA256 match, list')
finally:
    try:
        client.delete_object(Bucket=bucket, Key=key)
        client.delete_bucket(Bucket=bucket)
        print('PASS: cleanup')
    except Exception as e:
        print('Cleanup incomplete:',e,file=sys.stderr)
