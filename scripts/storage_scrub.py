#!/usr/bin/env python3
"""Read-only S3 SHA-256 scrub for Storzo.

Walks buckets with the master S3 credentials, recomputes SHA-256 for every
object that carries the console's `sha256` upload metadata, and compares.
Reports match / mismatch / no-metadata / unreadable. Never writes to S3.

Bounded: --max-objects caps one run. Resumable: --state keeps a per-bucket
cursor (StartAfter) plus cumulative counts; a resumed run skips keys already
processed. Objects inserted *before* the cursor after a run are not re-checked
until you start a fresh state file.

Exit codes: 0 clean (no-metadata objects are reported but are not failures),
1 any mismatch, 2 any unreadable (no mismatch), 3 configuration/usage error.

A scrub over objects without stored sha256 metadata proves nothing about
their content; they are counted, not downloaded.
"""
import argparse
import hashlib
import json
import os
import sys
from pathlib import Path

import boto3
from botocore.config import Config

ROOT = Path(__file__).resolve().parents[1]
CHUNK = 1024 * 1024
CATEGORIES = ('match', 'mismatch', 'no-metadata', 'unreadable')
MAX_PROBLEMS = 100


def classify(stored, actual):
    """Pure comparison: stored metadata value vs recomputed hex digest."""
    if stored is None or not stored.strip():
        return 'no-metadata'
    return 'match' if stored.strip().lower() == actual.strip().lower() else 'mismatch'


def sha256_stream(body, chunk_size=CHUNK):
    digest = hashlib.sha256()
    while True:
        block = body.read(chunk_size)
        if not block:
            break
        digest.update(block)
    return digest.hexdigest()


def load_env(path):
    """Read KEY=VALUE lines into os.environ without overriding real env vars."""
    path = Path(path)
    if not path.is_file():
        return
    for line in path.read_text().splitlines():
        if '=' in line and not line.startswith('#'):
            key, value = line.split('=', 1)
            os.environ.setdefault(key.strip(), value.strip())


def make_client(endpoint, access_key, secret_key):
    return boto3.client(
        's3', endpoint_url=endpoint, aws_access_key_id=access_key,
        aws_secret_access_key=secret_key, region_name='us-east-1',
        config=Config(signature_version='s3v4', s3={'addressing_style': 'path'},
                      retries={'max_attempts': 2, 'mode': 'standard'},
                      connect_timeout=5, read_timeout=120))


def iter_keys(client, bucket, start_after=None):
    kwargs = {'Bucket': bucket}
    if start_after:
        kwargs['StartAfter'] = start_after
    while True:
        resp = client.list_objects_v2(**kwargs)
        for obj in resp.get('Contents', []):
            yield obj['Key']
        if not resp.get('IsTruncated'):
            return
        kwargs = {'Bucket': bucket, 'ContinuationToken': resp['NextContinuationToken']}


def check_one(client, bucket, key):
    """Returns (category, detail). Read-only: HEAD then GET."""
    try:
        head = client.head_object(Bucket=bucket, Key=key)
    except Exception as exc:
        return 'unreadable', f'HEAD failed: {type(exc).__name__}'
    # SeaweedFS returns metadata keys capitalized (e.g. "Sha256"); match case-insensitively.
    meta = head.get('Metadata') or {}
    stored = next((v for k, v in meta.items() if k.lower() == 'sha256'), None)
    if stored is None or not stored.strip():
        return 'no-metadata', ''
    try:
        got = client.get_object(Bucket=bucket, Key=key)
    except Exception as exc:
        return 'unreadable', f'GET failed: {type(exc).__name__}'
    if got.get('ETag') != head.get('ETag'):
        return 'unreadable', 'object changed between HEAD and GET'
    try:
        actual = sha256_stream(got['Body'])
    except Exception as exc:
        return 'unreadable', f'read failed: {type(exc).__name__}'
    return classify(stored, actual), actual


class State:
    """JSON checkpoint: per-bucket last-processed key + cumulative counts."""

    def __init__(self, path=None):
        self.path = Path(path) if path else None
        self.done = {}
        self.counts = {c: 0 for c in CATEGORIES}
        if self.path and self.path.is_file():
            data = json.loads(self.path.read_text())
            if data.get('version') != 1:
                raise ValueError(f'unsupported state version in {self.path}')
            self.done = data.get('done', {})
            for cat in CATEGORIES:
                self.counts[cat] = data.get('counts', {}).get(cat, 0)

    def save(self):
        if not self.path:
            return
        tmp = self.path.with_suffix('.tmp')
        tmp.write_text(json.dumps({'version': 1, 'done': self.done, 'counts': self.counts}))
        tmp.replace(self.path)


def run_scrub(client, buckets, state, max_objects=0):
    problems = []
    processed = 0
    bounded = False
    for bucket in buckets:
        for key in iter_keys(client, bucket, state.done.get(bucket)):
            category, detail = check_one(client, bucket, key)
            state.counts[category] += 1
            if category in ('mismatch', 'unreadable') and len(problems) < MAX_PROBLEMS:
                problems.append({'bucket': bucket, 'key': key,
                                 'category': category, 'detail': detail})
            state.done[bucket] = key
            processed += 1
            if processed % 100 == 0:
                state.save()
            if max_objects and processed >= max_objects:
                bounded = True
                break
        if bounded:
            break
    state.save()
    return {'processed': processed, 'bounded': bounded,
            'counts': dict(state.counts), 'problems': problems}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--endpoint', default=os.getenv('SMOKE_S3_ENDPOINT', 'http://localhost:8333'))
    parser.add_argument('--env-file', default=str(ROOT / '.env'),
                        help='KEY=VALUE file for S3 keys; real env vars win')
    parser.add_argument('--state', help='checkpoint file for bounded/resumable runs')
    parser.add_argument('--max-objects', type=int, default=0, help='cap objects per run (0 = all)')
    parser.add_argument('--bucket', help='scrub only this bucket (default: all)')
    args = parser.parse_args(argv)

    load_env(args.env_file)
    access_key, secret_key = os.getenv('S3_ACCESS_KEY'), os.getenv('S3_SECRET_KEY')
    if not access_key or not secret_key:
        print('ERROR: S3_ACCESS_KEY / S3_SECRET_KEY not set (env or --env-file)', file=sys.stderr)
        return 3
    client = make_client(args.endpoint, access_key, secret_key)
    try:
        buckets = [args.bucket] if args.bucket else [
            b['Name'] for b in client.list_buckets().get('Buckets', [])]
    except Exception as exc:
        print(f'ERROR: cannot list buckets at {args.endpoint}: {exc}', file=sys.stderr)
        return 3

    try:
        state = State(args.state)
    except (ValueError, json.JSONDecodeError) as exc:
        print(f'ERROR: bad state file: {exc}', file=sys.stderr)
        return 3
    result = run_scrub(client, buckets, state, args.max_objects)
    print(json.dumps(result, indent=2))
    if result['counts']['mismatch']:
        print(f"FAIL: {result['counts']['mismatch']} SHA-256 mismatch(es)", file=sys.stderr)
        return 1
    if result['counts']['unreadable']:
        print(f"FAIL: {result['counts']['unreadable']} unreadable object(s)", file=sys.stderr)
        return 2
    if result['counts']['no-metadata']:
        print(f"NOTE: {result['counts']['no-metadata']} object(s) had no stored sha256; "
              'their content was not verified', file=sys.stderr)
    if result['bounded']:
        print('NOTE: run was bounded by --max-objects; resume with the same --state', file=sys.stderr)
    return 0


if __name__ == '__main__':
    sys.exit(main())
