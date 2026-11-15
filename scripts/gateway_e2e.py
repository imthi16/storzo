#!/usr/bin/env python3
"""End-to-end acceptance checks for a running Storzo agent gateway.

The script does not start the stack. Targets come from the environment:
CONSOLE_URL (default http://localhost:8088), S3_URL (default http://localhost:8333),
ADMIN_USER, ADMIN_PASSWORD, S3_ACCESS_KEY, S3_SECRET_KEY, BUCKET (default demo-images).
S3 keys are filled from the repo .env when that file exists and the variable is unset.
Secrets and tokens are never printed.
"""
from __future__ import annotations

import argparse
import base64
import hashlib
import hmac
import http.client
import json
import os
import secrets
import sys
import time
import urllib.error
import urllib.request
import uuid
import xml.etree.ElementTree as ET
from datetime import UTC, datetime
from pathlib import Path
from urllib.parse import unquote, urlencode, urlsplit

import boto3
from botocore.auth import S3SigV4Auth
from botocore.awsrequest import AWSRequest
from botocore.config import Config
from botocore.credentials import Credentials
from botocore.exceptions import BotoCoreError, ClientError

S3_CONFIG = Config(
    signature_version='s3v4',
    s3={'addressing_style': 'path'},
    retries={'max_attempts': 1, 'mode': 'standard'},
    connect_timeout=5,
    read_timeout=60,
)


class Report:
    def __init__(self) -> None:
        self.passed = 0
        self.failed = 0
        self.skipped = 0

    def add(self, status: str, name: str, observed: str) -> None:
        if status == 'PASS':
            self.passed += 1
        elif status == 'SKIPPED':
            self.skipped += 1
        else:
            status = 'FAIL'
            self.failed += 1
        print(f'{status} {name}: {observed}', flush=True)

    def ok(self, name: str, observed: str, good: bool) -> None:
        self.add('PASS' if good else 'FAIL', name, observed)

    def finish(self) -> int:
        print(
            f'RESULT pass={self.passed} fail={self.failed} skipped={self.skipped}',
            flush=True,
        )
        return 1 if self.failed else 0


def load_env() -> None:
    path = Path(__file__).resolve().parents[1] / '.env'
    if not path.is_file():
        return
    for line in path.read_text().splitlines():
        if not line or line.startswith('#') or '=' not in line:
            continue
        key, value = line.split('=', 1)
        os.environ.setdefault(key.strip(), value.strip())


def require_env(name: str) -> str:
    value = os.environ.get(name, '').strip()
    if not value:
        print(f'FAIL setup: {name} is not set', file=sys.stderr)
        raise SystemExit(2)
    return value


def probe(url: str) -> str | None:
    request = urllib.request.Request(url, method='GET')
    try:
        with urllib.request.urlopen(request, timeout=3) as response:
            response.read(128)
    except urllib.error.HTTPError as exc:
        exc.close()
        return None
    except urllib.error.URLError as exc:
        return str(exc.reason)
    return None


def xml_code(body: bytes) -> str:
    try:
        root = ET.fromstring(body)
    except ET.ParseError:
        return ''
    for element in root.iter():
        if element.tag.endswith('Code') and element.text:
            return element.text
    return ''


class Console:
    def __init__(self, base: str) -> None:
        self.base = base.rstrip('/')
        self.csrf = ''
        opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor())
        self._open = opener.open

    def call(self, method: str, path: str, body: dict | None = None, *, csrf: bool = True) -> tuple[int, bytes]:
        data = None if body is None else json.dumps(body).encode()
        request = urllib.request.Request(self.base + path, data=data, method=method)
        if data is not None:
            request.add_header('Content-Type', 'application/json')
        if csrf and self.csrf:
            request.add_header('x-csrf-token', self.csrf)
        try:
            with self._open(request, timeout=15) as response:
                return response.status, response.read()
        except urllib.error.HTTPError as exc:
            payload = exc.read()
            exc.close()
            return exc.code, payload

    def login(self, username: str, password: str) -> tuple[int, dict]:
        status, raw = self.call('POST', '/api/auth/login', {'username': username, 'password': password}, csrf=False)
        try:
            payload = json.loads(raw) if raw else {}
        except json.JSONDecodeError:
            payload = {}
        if status == 200 and isinstance(payload, dict):
            self.csrf = str(payload.get('csrf') or '')
        return status, payload if isinstance(payload, dict) else {}


def anonymous_post(base: str, path: str, body: dict) -> int:
    data = json.dumps(body).encode()
    request = urllib.request.Request(
        base.rstrip('/') + path,
        data=data,
        method='POST',
        headers={'Content-Type': 'application/json'},
    )
    try:
        with urllib.request.urlopen(request, timeout=15) as response:
            response.read()
            return response.status
    except urllib.error.HTTPError as exc:
        exc.close()
        return exc.code


def s3_client(endpoint: str, access: str, secret: str, token: str | None = None):
    # A private session keeps the response hook off the process-wide emitter.
    # Registering on the default session would record each call once per client.
    session = boto3.session.Session()
    client = session.client(
        's3',
        endpoint_url=endpoint,
        aws_access_key_id=access,
        aws_secret_access_key=secret,
        aws_session_token=token,
        region_name='us-east-1',
        config=S3_CONFIG,
    )
    watch_client(client)
    return client


def invoke(fn):
    try:
        value = fn()
    except ClientError as exc:
        value = exc.response
    except BotoCoreError as exc:
        return None, type(exc).__name__, None
    meta = value.get('ResponseMetadata', {}) if isinstance(value, dict) else {}
    err = value.get('Error', {}) if isinstance(value, dict) else {}
    return meta.get('HTTPStatusCode'), err.get('Code') or '', value


def raw_request(endpoint: str, method: str, path: str, access: str, secret: str, token: str) -> tuple[int, str]:
    # boto3 collapses ".." and "%2e%2e" before signing. The bytes below are the request target.
    split = urlsplit(endpoint)
    url = f'{split.scheme}://{split.netloc}{path}'
    aws_request = AWSRequest(method=method, url=url)
    S3SigV4Auth(Credentials(access, secret, token), 's3', 'us-east-1').add_auth(aws_request)
    connection_cls = http.client.HTTPSConnection if split.scheme == 'https' else http.client.HTTPConnection
    connection = connection_cls(split.hostname, split.port, timeout=15)
    try:
        headers = {str(key): str(value) for key, value in aws_request.headers.items()}
        connection.request(method, path, headers=headers)
        response = connection.getresponse()
        body = response.read()
        record_observed(method, path, response.status)
        return response.status, xml_code(body)
    finally:
        connection.close()


def issue_past_token(key: str, bucket: str, prefix: str) -> dict[str, str]:
    """Sign a token the HTTP API cannot issue. Layout matches backend/app/credentials.py."""
    iat = int(time.time()) - 3600
    kid = 'STZ' + base64.b32encode(secrets.token_bytes(10)).decode().rstrip('=')
    claims = {
        'bucket': bucket,
        'exp': iat + 30,
        'iat': iat,
        'jti': str(uuid.uuid4()),
        'kid': kid,
        'mode': 'ro',
        'prefix': prefix,
    }
    body = base64.urlsafe_b64encode(json.dumps(claims, separators=(',', ':')).encode()).rstrip(b'=').decode()
    mac = hmac.new(key.encode(), body.encode(), hashlib.sha256).digest()
    signature = base64.urlsafe_b64encode(mac).rstrip(b'=').decode()
    secret = hmac.new(key.encode(), b'secret|' + kid.encode(), hashlib.sha256).hexdigest()
    return {'access_key_id': kid, 'secret_access_key': secret, 'session_token': body + '.' + signature}


def flip_token(token: str) -> str:
    chars = list(token)
    index = len(chars) - 1
    chars[index] = 'A' if chars[index] != 'A' else 'B'
    return ''.join(chars)


def read_body(value: dict) -> bytes:
    body = value.get('Body')
    if body is None:
        return b''
    try:
        return body.read()
    finally:
        body.close()


def delete_prefix(client, bucket: str, prefix: str) -> str | None:
    token = None
    while True:
        kwargs = {'Bucket': bucket, 'Prefix': prefix}
        if token:
            kwargs['ContinuationToken'] = token
        status, code, page = invoke(lambda kw=dict(kwargs): client.list_objects_v2(**kw))
        if status != 200 or not isinstance(page, dict):
            return f'status={status} code={code}'
        objects = [{'Key': item['Key']} for item in page.get('Contents', [])]
        if objects:
            status, code, deleted = invoke(lambda objs=objects: client.delete_objects(
                Bucket=bucket, Delete={'Objects': objs, 'Quiet': True},
            ))
            if status != 200 or (isinstance(deleted, dict) and deleted.get('Errors')):
                return f'status={status} code={code}'
        if not page.get('IsTruncated'):
            break
        token = page.get('NextContinuationToken')
    status, code, page = invoke(lambda: client.list_multipart_uploads(Bucket=bucket, Prefix=prefix))
    if status != 200 or not isinstance(page, dict):
        return f'multipart status={status} code={code}'
    for upload in page.get('Uploads') or []:
        key, upload_id = upload['Key'], upload['UploadId']
        status, code, _ = invoke(lambda key=key, upload_id=upload_id: client.abort_multipart_upload(
            Bucket=bucket, Key=key, UploadId=upload_id,
        ))
        if code and status not in (200, 204):
            return f'abort status={status} code={code}'
    return None


def detail_map(detail: str) -> dict[str, str]:
    found = {}
    for part in (detail or '').split():
        key, sep, value = part.partition('=')
        if sep:
            found[key] = value
    return found


def written_since(row: dict, started: datetime) -> bool:
    # GET /api/audit returns ts. Denied gateway.request rows have an empty kid.
    raw = row.get('ts')
    if not isinstance(raw, str):
        return False
    try:
        seen = datetime.fromisoformat(raw)
    except ValueError:
        return False
    if seen.tzinfo is None:
        seen = seen.replace(tzinfo=UTC)
    return seen >= started


# method, bucket, key, status, decision. One entry per HTTP response the gateway returned.
LEDGER: list[tuple[str, str, str, str, str]] = []


def observed_decision(status: int) -> str:
    # 2xx/3xx passed the gateway. 4xx and 501 are the denials it writes itself.
    if 200 <= status < 400:
        return 'allow'
    return 'deny'


def gateway_target(url: str) -> tuple[str, str]:
    # The gateway splits the decoded URL path on the first slash. Bucket-level
    # calls and ListBuckets leave the key empty.
    path = unquote(urlsplit(url).path).removeprefix('/')
    bucket, _sep, key = path.partition('/')
    return bucket, key


def record_observed(method: str, url: str, status: int) -> None:
    bucket, key = gateway_target(url)
    LEDGER.append((method, bucket, key, str(status), observed_decision(status)))


def watch_client(client) -> None:
    pending: list = []

    def before_send(request, **_kwargs):
        pending.append(request)

    def on_response(**kwargs):
        request = pending.pop(0) if pending else None
        if request is None or kwargs.get('exception') is not None:
            return
        response_dict = kwargs.get('response_dict')
        if not isinstance(response_dict, dict):
            return
        status = response_dict.get('status_code')
        if not isinstance(status, int):
            return
        record_observed(getattr(request, 'method', '') or '', getattr(request, 'url', '') or '', status)

    client.meta.events.register('before-send.s3', before_send)
    client.meta.events.register('response-received.s3', on_response)


def audit_coverage(ledger, rows, started: datetime) -> list:
    """Ledger entries that have no distinct gateway.request row since `started`."""
    pool = []
    for row in rows:
        if not isinstance(row, dict) or row.get('action') != 'gateway.request':
            continue
        if not written_since(row, started):
            continue
        parsed = detail_map(str(row.get('detail') or ''))
        pool.append((
            parsed.get('method', ''),
            row.get('bucket') or '',
            row.get('object_key') or '',
            parsed.get('status', ''),
            parsed.get('decision', ''),
        ))
    missing = []
    for event in ledger:
        try:
            pool.remove(event)
        except ValueError:
            missing.append(event)
    return missing


def issue_tally(kids: set[str], rows) -> tuple[int, int, bool]:
    """credential.issue rows for kids this run issued. Each kid needs exactly one."""
    wanted = {kid for kid in kids if kid}
    counts = dict.fromkeys(wanted, 0)
    for row in rows:
        if not isinstance(row, dict) or row.get('action') != 'credential.issue':
            continue
        kid = detail_map(str(row.get('detail') or '')).get('kid', '')
        if kid in counts:
            counts[kid] += 1
    matched = sum(counts.values())
    ok = all(count == 1 for count in counts.values()) and matched == len(wanted)
    return matched, len(wanted), ok


def fetch_audit(console: Console) -> tuple[int, list]:
    items: list = []
    offset = 0
    while True:
        status, raw = console.call('GET', f'/api/audit?limit=200&offset={offset}', csrf=False)
        if status != 200:
            return status, items
        payload = json.loads(raw)
        batch = payload.get('items') or []
        items.extend(batch)
        total = int(payload.get('total') or 0)
        offset += 200
        if len(batch) < 200 or offset >= total:
            break
    return 200, items


def report_audit(report: Report, console: Console, kids: set[str], started: datetime) -> None:
    deadline = time.monotonic() + 20
    audit_status = 0
    missing = list(LEDGER)
    matched = 0
    issued = len({kid for kid in kids if kid})
    issues_ok = False
    while True:
        audit_status, rows = fetch_audit(console)
        if audit_status == 200:
            missing = audit_coverage(LEDGER, rows, started)
            matched, issued, issues_ok = issue_tally(kids, rows)
            if not missing and issues_ok:
                break
        if time.monotonic() >= deadline:
            break
        time.sleep(0.5)
    sample = '; '.join(
        f'{method} bucket={bucket} key={key} status={status}'
        for method, bucket, key, status, _decision in missing[:3]
    )
    observed = f'status={audit_status} missing={len(missing)} issue={matched}/{issued}'
    if sample:
        observed = f'{observed} {sample}'
    report.ok('audit-gateway-and-issue', observed, audit_status == 200 and not missing and issues_ok)


def run(report: Report) -> None:
    load_env()
    started = datetime.now(UTC)
    console_url = os.environ.get('CONSOLE_URL', 'http://localhost:8088').rstrip('/')
    s3_url = os.environ.get('S3_URL', 'http://localhost:8333').rstrip('/')
    bucket = os.environ.get('BUCKET', 'demo-images').strip() or 'demo-images'
    admin_user = require_env('ADMIN_USER')
    admin_password = require_env('ADMIN_PASSWORD')
    master_access = require_env('S3_ACCESS_KEY')
    master_secret = require_env('S3_SECRET_KEY')

    console_error = probe(console_url + '/api/auth/me')
    if console_error is not None:
        print(f'Console is unreachable at {console_url}: {console_error}', file=sys.stderr)
        raise SystemExit(2)
    s3_error = probe(s3_url + '/')
    if s3_error is not None:
        print(f'S3 gateway is unreachable at {s3_url}: {s3_error}', file=sys.stderr)
        raise SystemExit(2)

    prefix = f'e2e-{uuid.uuid4().hex[:12]}/'
    sibling = prefix[:-1] + '0/a.txt'
    shorter = prefix[:-2]
    other_bucket = f'e2e-other-{uuid.uuid4().hex[:8]}'
    seed_key = prefix + 'a.txt'
    seed = b'gateway-e2e-seed\n'
    kids: set[str] = set()
    LEDGER.clear()
    master = s3_client(s3_url, master_access, master_secret)
    console = Console(console_url)
    expect_audit = False

    def issue(mode: str, ttl: int | None) -> tuple[int, dict]:
        body = {'bucket': bucket, 'prefix': prefix, 'mode': mode}
        if ttl is not None:
            body['ttl_seconds'] = ttl
        status, raw = console.call('POST', '/api/credentials', body)
        try:
            payload = json.loads(raw) if raw else {}
        except json.JSONDecodeError:
            payload = {}
        return status, payload if isinstance(payload, dict) else {}

    try:
        status, _payload = console.login(admin_user, admin_password)
        report.ok('login', f'status={status}', status == 200 and bool(console.csrf))
        if status != 200 or not console.csrf:
            return

        anon = anonymous_post(console_url, '/api/credentials', {'bucket': bucket, 'prefix': prefix, 'mode': 'ro'})
        report.ok('credentials-without-login', f'status={anon}', anon == 401)

        saved = console.csrf
        console.csrf = ''
        status, _ = issue('ro', 900)
        console.csrf = saved
        report.ok('credentials-without-csrf', f'status={status}', status == 403)

        for name, ttl in (('ttl-0', 0), ('ttl-negative', -1), ('ttl-3601', 3601)):
            status, _ = issue('ro', ttl)
            report.ok(name, f'status={status}', status == 422)

        before = time.time()
        status, issued = issue('ro', None)
        expires = issued.get('expires_at')
        delta = None
        if isinstance(expires, str):
            try:
                parsed = datetime.fromisoformat(expires)
                if parsed.tzinfo is None:
                    parsed = parsed.replace(tzinfo=UTC)
                delta = parsed.timestamp() - before
            except ValueError:
                delta = None
        report.ok('ttl-default-900', f'status={status} delta={delta}', status == 201 and delta is not None and abs(delta - 900) <= 2)
        if status != 201:
            return
        kids.add(str(issued.get('access_key_id') or ''))

        status, code, _ = invoke(lambda: master.put_object(Bucket=bucket, Key=seed_key, Body=seed))
        got_status, got_code, got = invoke(lambda: master.get_object(Bucket=bucket, Key=seed_key))
        same = isinstance(got, dict) and read_body(got) == seed
        report.ok('master-put-get', f'put={status}/{code} get={got_status}/{got_code}', status == 200 and got_status == 200 and same)
        status, code, listed = invoke(lambda: master.list_buckets())
        count = len(listed.get('Buckets', [])) if isinstance(listed, dict) else 0
        report.ok('master-list-buckets', f'status={status} code={code} buckets={count}', status == 200 and isinstance(listed, dict))

        status, ro = issue('ro', 900)
        report.ok('issue-ro', f'status={status}', status == 201 and bool(ro.get('access_key_id')))
        if status != 201:
            return
        kids.add(str(ro['access_key_id']))
        ro_client = s3_client(s3_url, ro['access_key_id'], ro['secret_access_key'], ro['session_token'])

        status, code, got = invoke(lambda: ro_client.get_object(Bucket=bucket, Key=seed_key))
        same = isinstance(got, dict) and read_body(got) == seed
        report.ok('ro-get', f'status={status} code={code}', status == 200 and same)
        status, code, _ = invoke(lambda: ro_client.head_object(Bucket=bucket, Key=seed_key))
        report.ok('ro-head', f'status={status} code={code}', status == 200)
        status, code, _ = invoke(lambda: ro_client.put_object(Bucket=bucket, Key=seed_key, Body=b'nope'))
        report.ok('ro-put', f'status={status} code={code}', status == 403 and code == 'AccessDenied')

        denied = (
            ('ro-other-prefix', lambda: ro_client.get_object(Bucket=bucket, Key='outside-prefix/a.txt')),
            ('ro-sibling-prefix', lambda: ro_client.get_object(Bucket=bucket, Key=sibling)),
            ('ro-other-bucket', lambda: ro_client.get_object(Bucket=other_bucket, Key=seed_key)),
            ('ro-list-no-prefix', lambda: ro_client.list_objects_v2(Bucket=bucket)),
            ('ro-list-shorter-prefix', lambda: ro_client.list_objects_v2(Bucket=bucket, Prefix=shorter)),
            ('ro-list-buckets', lambda: ro_client.list_buckets()),
            ('ro-create-bucket', lambda: ro_client.create_bucket(Bucket=other_bucket)),
            ('ro-delete-bucket', lambda: ro_client.delete_bucket(Bucket=other_bucket)),
        )
        for name, fn in denied:
            status, code, _ = invoke(fn)
            report.ok(name, f'status={status} code={code}', status == 403 and code == 'AccessDenied')

        status, rw = issue('rw', 900)
        report.ok('issue-rw', f'status={status}', status == 201 and bool(rw.get('access_key_id')))
        if status != 201:
            return
        kids.add(str(rw['access_key_id']))
        rw_client = s3_client(s3_url, rw['access_key_id'], rw['secret_access_key'], rw['session_token'])
        rw_key = prefix + 'rw.txt'
        rw_body = b'rw-round-trip'
        status, code, _ = invoke(lambda: rw_client.put_object(Bucket=bucket, Key=rw_key, Body=rw_body))
        got_status, got_code, got = invoke(lambda: rw_client.get_object(Bucket=bucket, Key=rw_key))
        same = isinstance(got, dict) and read_body(got) == rw_body
        report.ok('rw-put-get', f'put={status}/{code} get={got_status}/{got_code}', status == 200 and got_status == 200 and same)
        status, code, _ = invoke(lambda: rw_client.delete_object(Bucket=bucket, Key=rw_key))
        report.ok('rw-delete', f'status={status} code={code}', status in (200, 204))

        blob = b'm' * (20 * 1024 * 1024)
        part_size = 8 * 1024 * 1024
        multi_key = prefix + 'multi.bin'
        status, code, created = invoke(lambda: rw_client.create_multipart_upload(Bucket=bucket, Key=multi_key))
        upload_id = created.get('UploadId') if isinstance(created, dict) else None
        parts = []
        part_error = ''
        if upload_id:
            for number, start in enumerate((0, part_size, part_size * 2), start=1):
                chunk = blob[start:start + part_size]
                status, code, part = invoke(lambda number=number, chunk=chunk: rw_client.upload_part(
                    Bucket=bucket, Key=multi_key, UploadId=upload_id, PartNumber=number, Body=chunk,
                ))
                if status != 200 or not isinstance(part, dict):
                    part_error = f'part {number} status={status} code={code}'
                    break
                parts.append({'ETag': part['ETag'], 'PartNumber': number})
        if upload_id and not part_error:
            status, code, _ = invoke(lambda: rw_client.complete_multipart_upload(
                Bucket=bucket, Key=multi_key, UploadId=upload_id,
                MultipartUpload={'Parts': parts},
            ))
            got_status, got_code, got = invoke(lambda: rw_client.get_object(Bucket=bucket, Key=multi_key))
            read_back = read_body(got) if isinstance(got, dict) else b''
            match = hashlib.sha256(read_back).digest() == hashlib.sha256(blob).digest()
            report.ok(
                'rw-multipart-sha256',
                f'complete={status}/{code} get={got_status}/{got_code} bytes={len(read_back)} parts={len(parts)}',
                status == 200 and got_status == 200 and match and len(blob) == 20 * 1024 * 1024 and len(parts) == 3,
            )
        else:
            report.ok('rw-multipart-sha256', part_error or f'status={status} code={code}', False)

        status, code, _ = invoke(lambda: rw_client.copy_object(
            Bucket=bucket, Key=prefix + 'copy.txt', CopySource={'Bucket': bucket, 'Key': seed_key},
        ))
        report.ok('rw-copy-object', f'status={status} code={code}', status == 501 and code == 'NotImplemented')

        fancy_key = prefix + 'a file 日.txt'
        fancy = 'storzo 日'.encode()
        status, code, _ = invoke(lambda: rw_client.put_object(Bucket=bucket, Key=fancy_key, Body=fancy))
        got_status, got_code, got = invoke(lambda: rw_client.get_object(Bucket=bucket, Key=fancy_key))
        same = isinstance(got, dict) and read_body(got) == fancy
        report.ok('rw-space-unicode', f'put={status}/{code} get={got_status}/{got_code}', status == 200 and got_status == 200 and same)

        for name, path in (
            ('key-dotdot', f'/{bucket}/{prefix}../other/a.txt'),
            ('key-encoded-dotdot', f'/{bucket}/{prefix}%2e%2e/other/a.txt'),
        ):
            status, code = raw_request(s3_url, 'GET', path, ro['access_key_id'], ro['secret_access_key'], ro['session_token'])
            report.ok(name, f'status={status} code={code}', status == 400 and code == 'InvalidArgument')

        bad_secret = ro['secret_access_key'][:-1] + ('A' if ro['secret_access_key'][-1:] != 'A' else 'B')
        bad_client = s3_client(s3_url, ro['access_key_id'], bad_secret, ro['session_token'])
        status, code, _ = invoke(lambda: bad_client.get_object(Bucket=bucket, Key=seed_key))
        report.ok('wrong-secret', f'status={status} code={code}', status == 403 and code == 'SignatureDoesNotMatch')

        flipped = s3_client(s3_url, ro['access_key_id'], ro['secret_access_key'], flip_token(ro['session_token']))
        status, code, _ = invoke(lambda: flipped.get_object(Bucket=bucket, Key=seed_key))
        report.ok('flipped-token', f'status={status} code={code}', status == 403 and code == 'SignatureDoesNotMatch')

        signing_key = os.environ.get('CRED_SIGNING_KEY', '').strip()
        if len(signing_key) < 32:
            report.add('SKIPPED', 'expired-token', 'CRED_SIGNING_KEY unavailable; API ttl minimum is 1s')
        else:
            past = issue_past_token(signing_key, bucket, prefix)
            past_client = s3_client(s3_url, past['access_key_id'], past['secret_access_key'], past['session_token'])
            status, code, _ = invoke(lambda: past_client.get_object(Bucket=bucket, Key=seed_key))
            report.ok('expired-token', f'status={status} code={code}', status == 403 and code == 'ExpiredToken')

        query = urlencode({'bucket': bucket, 'key': seed_key, 'expires': 60})
        status, raw = console.call('GET', '/api/objects/presign?' + query, csrf=False)
        url = ''
        try:
            url = str(json.loads(raw).get('url') or '')
        except json.JSONDecodeError:
            url = ''
        fetched = b''
        fetch_status = None
        host = urlsplit(url).netloc if url else ''
        if url:
            try:
                with urllib.request.urlopen(url, timeout=15) as response:
                    fetch_status = response.status
                    fetched = response.read()
            except urllib.error.HTTPError as exc:
                fetch_status = exc.code
                exc.close()
            except urllib.error.URLError as exc:
                fetch_status = str(exc.reason)
            if isinstance(fetch_status, int):
                record_observed('GET', url, fetch_status)
        report.ok(
            'presigned-download',
            f'issue={status} fetch={fetch_status} host={host} bytes={len(fetched)}',
            status == 200 and fetch_status == 200 and fetched == seed,
        )

        expect_audit = True
    finally:
        error = None
        try:
            error = delete_prefix(master, bucket, prefix)
        except BotoCoreError as exc:
            error = type(exc).__name__
        report.ok('cleanup', 'deleted prefix' if error is None else error, error is None)
        if expect_audit:
            report_audit(report, console, kids, started)


def self_test() -> int:
    started = datetime(2026, 11, 15, tzinfo=UTC)
    later = '2026-11-15T00:00:01+00:00'

    def row(method, bucket, key, status, decision, ts=later):
        return {
            'ts': ts,
            'action': 'gateway.request',
            'bucket': bucket,
            'object_key': key,
            'detail': f'method={method} bytes=0 kid= decision={decision} status={status}',
        }

    one = [('GET', 'b', 'k', '200', 'allow')]
    covered = [row('GET', 'b', 'k', '200', 'allow')]
    checks = (
        ('full coverage', audit_coverage(one, covered, started) == []),
        ('missing row', len(audit_coverage(one, [], started)) == 1),
        ('wrong status', len(audit_coverage(one, [row('GET', 'b', 'k', '403', 'allow')], started)) == 1),
        ('twice needs two rows', len(audit_coverage(one + one, covered, started)) == 1),
        ('extra rows ignored', audit_coverage(one, covered + [
            row('PUT', 'other', 'z', '201', 'allow'),
            row('GET', 'b', 'k', '200', 'allow', ts='2020-01-01T00:00:00+00:00'),
        ], started) == []),
    )
    failed = [name for name, ok in checks if not ok]
    if failed:
        print('self-test failed: ' + ', '.join(failed), flush=True)
        return 1
    print('self-test passed', flush=True)
    return 0


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--self-test', action='store_true', help=argparse.SUPPRESS)
    args = parser.parse_args()
    if args.self_test:
        raise SystemExit(self_test())
    report = Report()
    run(report)
    raise SystemExit(report.finish())


if __name__ == '__main__':
    main()
