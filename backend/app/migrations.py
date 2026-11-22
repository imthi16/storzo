"""Cloud connect Phase 1: one-time copy from AWS S3 / S3-compatible into Storzo via rclone.

Source credentials are held only in memory (job thread / subprocess env) and are
never written to SQLite, the audit trail, or logs.
"""
import ipaddress
import json
import os
import shutil
import socket
import subprocess
import threading
import uuid
from datetime import UTC, datetime
from urllib.parse import urlparse

import boto3
from botocore.config import Config
from fastapi import HTTPException

from app import config, db

SCAN_LIMIT = int(os.getenv('MIGRATION_SCAN_LIMIT', '3000'))
EGRESS_USD_PER_GB = float(os.getenv('MIGRATION_EGRESS_USD_PER_GB', '0.09'))
ALLOW_PRIVATE = os.getenv('MIGRATION_ALLOW_PRIVATE_ENDPOINTS', 'false').lower() == 'true'
MAX_ERRORS = 20

_lock = threading.Lock()
_procs: dict[str, subprocess.Popen] = {}
_cancelled: set[str] = set()


def _now() -> str:
    return datetime.now(UTC).isoformat()


def init_migrations() -> None:
    with db.connect() as conn:
        conn.execute('''CREATE TABLE IF NOT EXISTS migrations (
            id TEXT PRIMARY KEY, state TEXT NOT NULL, verify_status TEXT,
            source_bucket TEXT NOT NULL, source_prefix TEXT NOT NULL,
            source_endpoint TEXT, source_region TEXT,
            dest_bucket TEXT NOT NULL, dest_prefix TEXT NOT NULL,
            bytes_done INTEGER NOT NULL DEFAULT 0, objects_done INTEGER NOT NULL DEFAULT 0,
            total_bytes INTEGER NOT NULL DEFAULT 0, total_objects INTEGER NOT NULL DEFAULT 0,
            errors TEXT NOT NULL DEFAULT '[]',
            created_at TEXT NOT NULL, updated_at TEXT NOT NULL)''')


def _row_to_job(row) -> dict:
    d = dict(row)
    d['errors'] = json.loads(d.get('errors') or '[]')
    return d


def create_job(source: dict, dest: dict, total_objects: int = 0, total_bytes: int = 0) -> dict:
    init_migrations()
    job = {
        'id': uuid.uuid4().hex[:16], 'state': 'queued', 'verify_status': None,
        'source_bucket': source['bucket'], 'source_prefix': source.get('prefix', ''),
        'source_endpoint': source.get('endpoint') or None, 'source_region': source.get('region') or None,
        'dest_bucket': dest['bucket'], 'dest_prefix': dest.get('prefix', ''),
        'bytes_done': 0, 'objects_done': 0, 'total_bytes': total_bytes, 'total_objects': total_objects,
        'errors': '[]', 'created_at': _now(), 'updated_at': _now(),
    }
    with db.connect() as conn:
        conn.execute('''INSERT INTO migrations(id,state,verify_status,source_bucket,source_prefix,
            source_endpoint,source_region,dest_bucket,dest_prefix,bytes_done,objects_done,
            total_bytes,total_objects,errors,created_at,updated_at)
            VALUES (:id,:state,:verify_status,:source_bucket,:source_prefix,:source_endpoint,
            :source_region,:dest_bucket,:dest_prefix,:bytes_done,:objects_done,:total_bytes,
            :total_objects,:errors,:created_at,:updated_at)''', job)
    out = dict(job)
    out['errors'] = []
    return out


def update_job(job_id: str, **fields) -> None:
    if 'errors' in fields and not isinstance(fields['errors'], str):
        fields['errors'] = json.dumps(list(fields['errors'])[:MAX_ERRORS])
    fields['updated_at'] = _now()
    cols = ', '.join(f'{k}=?' for k in fields)
    with db.connect() as conn:
        conn.execute(f'UPDATE migrations SET {cols} WHERE id=?', (*fields.values(), job_id))


def get_job(job_id: str) -> dict | None:
    init_migrations()
    with db.connect() as conn:
        conn.row_factory = __import__('sqlite3').Row
        row = conn.execute('SELECT * FROM migrations WHERE id=?', (job_id,)).fetchone()
    return _row_to_job(row) if row else None


def list_jobs(limit: int = 50) -> list[dict]:
    init_migrations()
    with db.connect() as conn:
        conn.row_factory = __import__('sqlite3').Row
        rows = conn.execute('SELECT * FROM migrations ORDER BY created_at DESC LIMIT ?', (limit,)).fetchall()
    return [_row_to_job(r) for r in rows]


def active_job() -> dict | None:
    return next((j for j in list_jobs(20) if j['state'] in ('queued', 'running', 'verifying')), None)


# --- SSRF guard -------------------------------------------------------------

def _ip_is_blocked(ip: str) -> bool:
    try:
        addr = ipaddress.ip_address(ip)
    except ValueError:
        return True
    return bool(addr.is_private or addr.is_loopback or addr.is_link_local or addr.is_reserved or addr.is_multicast)


def validate_source_endpoint(endpoint: str | None) -> None:
    """Refuse private/loopback/link-local source endpoints unless explicitly allowed."""
    if not endpoint or ALLOW_PRIVATE:
        return
    parsed = urlparse(endpoint)
    if parsed.scheme not in ('http', 'https') or not parsed.hostname:
        raise HTTPException(400, 'Source endpoint must be an http(s) URL')
    host = parsed.hostname
    ips: list[str] = []
    try:
        ipaddress.ip_address(host)
        ips = [host]
    except ValueError:
        try:
            infos = socket.getaddrinfo(host, None)
        except OSError:
            raise HTTPException(400, 'Source endpoint hostname could not be resolved') from None
        ips = sorted({str(info[4][0]) for info in infos})
    if any(_ip_is_blocked(ip) for ip in ips):
        raise HTTPException(400, 'Source endpoint points at a private, loopback or link-local address; '
                                   'refused (set MIGRATION_ALLOW_PRIVATE_ENDPOINTS=true to allow it)')


# --- Estimate ---------------------------------------------------------------

def make_source_client(source: dict):
    kwargs: dict = {
        'aws_access_key_id': source['access_key_id'],
        'aws_secret_access_key': source['secret_access_key'],
        'region_name': source.get('region') or 'us-east-1',
        'config': Config(signature_version='s3v4', s3={'addressing_style': 'path'},
                         retries={'max_attempts': 2, 'mode': 'standard'}),
    }
    if source.get('endpoint'):
        kwargs['endpoint_url'] = source['endpoint']
    return boto3.client('s3', **kwargs)


def estimate(source: dict) -> dict:
    client = make_source_client(source)
    count, total, scanned = 0, 0, 0
    token = None
    partial = False
    while scanned < SCAN_LIMIT:
        args: dict = {'Bucket': source['bucket'], 'Prefix': source.get('prefix', ''),
                      'MaxKeys': min(500, SCAN_LIMIT - scanned)}
        if token:
            args['ContinuationToken'] = token
        res = client.list_objects_v2(**args)
        contents = res.get('Contents', [])
        for obj in contents:
            count += 1
            total += int(obj.get('Size', 0))
        scanned += len(contents)
        token = res.get('NextContinuationToken')
        if not token:
            break
        if scanned >= SCAN_LIMIT:
            partial = True
    egress_gb = total / (1024 ** 3)
    return {
        'object_count': count, 'total_bytes': total, 'partial': partial, 'scan_limit': SCAN_LIMIT,
        'egress_usd': round(egress_gb * EGRESS_USD_PER_GB, 4),
        'egress_price_per_gb_usd': EGRESS_USD_PER_GB,
        'price_note': ('Estimate only: priced at the configured per-GB rate '
                       f'(MIGRATION_EGRESS_USD_PER_GB, default ${EGRESS_USD_PER_GB:.2f}/GB, the AWS '
                       'internet egress list price at time of writing). Prices change; check your '
                       'provider. Intra-region / same-provider transfers may cost less or nothing.'),
    }


# --- rclone runner ----------------------------------------------------------

def rclone_path() -> str | None:
    return shutil.which('rclone')


def parse_stats_line(line: str) -> dict | None:
    """Parse one rclone --use-json-log stderr line; return progress dict or None."""
    try:
        obj = json.loads(line)
    except (ValueError, TypeError):
        return None
    if not isinstance(obj, dict):
        return None
    stats = obj.get('stats')
    if not isinstance(stats, dict):
        return None
    return {
        'bytes_done': int(stats.get('bytes', 0)),
        'objects_done': int(stats.get('transfers', 0)),
        'total_bytes': int(stats.get('totalBytes', 0)),
        'total_objects': int(stats.get('totalTransfers', 0)),
        'errors': int(stats.get('errors', 0)),
    }


def _remote(prefix: str, bucket: str, obj_prefix: str) -> str:
    path = f'{prefix}:{bucket}'
    if obj_prefix:
        path += '/' + obj_prefix.strip('/')
    return path


def build_env(source: dict) -> dict:
    # Minimal environment: rclone must not inherit ADMIN_PASSWORD, SESSION_SECRET or CRED_SIGNING_KEY.
    env = {k: v for k, v in os.environ.items() if k in ('PATH', 'HOME', 'TMPDIR', 'SSL_CERT_FILE', 'SSL_CERT_DIR', 'LANG')}
    env.update({
        'RCLONE_CONFIG_SRC_TYPE': 's3',
        'RCLONE_CONFIG_SRC_PROVIDER': 'Other',
        'RCLONE_CONFIG_SRC_ACCESS_KEY_ID': source['access_key_id'],
        'RCLONE_CONFIG_SRC_SECRET_ACCESS_KEY': source['secret_access_key'],
        'RCLONE_CONFIG_SRC_REGION': source.get('region') or 'us-east-1',
        'RCLONE_CONFIG_DST_TYPE': 's3',
        'RCLONE_CONFIG_DST_PROVIDER': 'Other',
        'RCLONE_CONFIG_DST_ACCESS_KEY_ID': config.S3_ACCESS_KEY,
        'RCLONE_CONFIG_DST_SECRET_ACCESS_KEY': config.S3_SECRET_KEY,
        'RCLONE_CONFIG_DST_REGION': 'us-east-1',
        'RCLONE_CONFIG_DST_ENDPOINT': config.S3_ENDPOINT,
        'RCLONE_CONFIG_DST_FORCE_PATH_STYLE': 'true',
    })
    if source.get('endpoint'):
        env['RCLONE_CONFIG_SRC_ENDPOINT'] = source['endpoint']
        env['RCLONE_CONFIG_SRC_FORCE_PATH_STYLE'] = 'true'
    return env


def _collect_errors(line: str, errors: list[str]) -> None:
    try:
        obj = json.loads(line)
    except (ValueError, TypeError):
        return
    if isinstance(obj, dict) and obj.get('level') == 'error' and obj.get('msg'):
        errors.append(str(obj['msg'])[:500])


def run_job(job_id: str, source: dict, dest: dict) -> None:
    """Run copy then `rclone check --one-way`. Credentials live only in `env` here."""
    from app.db import audit
    rclone = rclone_path()
    if not rclone:
        update_job(job_id, state='failed', errors=['rclone is not available'])
        return
    src = _remote('src', source['bucket'], source.get('prefix', ''))
    dst = _remote('dst', dest['bucket'], dest.get('prefix', ''))
    env = build_env(source)
    errors: list[str] = []
    try:
        update_job(job_id, state='running')
        proc = subprocess.Popen(  # fixed argv, credentials via env only
            [rclone, 'copy', src, dst, '--use-json-log', '--stats=1s', '--stats-one-line'],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, env=env)
        with _lock:
            _procs[job_id] = proc
        assert proc.stderr is not None
        for line in proc.stderr:
            progress = parse_stats_line(line)
            if progress:
                update_job(job_id, bytes_done=progress['bytes_done'],
                           objects_done=progress['objects_done'],
                           total_bytes=progress['total_bytes'] or _existing_total(job_id, 'total_bytes'),
                           total_objects=progress['total_objects'] or _existing_total(job_id, 'total_objects'))
            else:
                _collect_errors(line, errors)
        rc = proc.wait()
        with _lock:
            _procs.pop(job_id, None)
        if job_id in _cancelled:
            update_job(job_id, state='cancelled', errors=errors[:MAX_ERRORS])
            audit('migration.cancel', dest['bucket'], dest.get('prefix') or None, f'id={job_id}')
            return
        if rc != 0:
            errors.append(f'rclone copy exited with code {rc}')
            update_job(job_id, state='failed', verify_status='failed', errors=errors[:MAX_ERRORS])
            audit('migration.finish', dest['bucket'], dest.get('prefix') or None,
                  f'id={job_id} state=failed verify=failed')
            return
        update_job(job_id, state='verifying', errors=errors[:MAX_ERRORS])
        check = subprocess.run(
            [rclone, 'check', src, dst, '--one-way', '--use-json-log'],
            capture_output=True, text=True, env=env, timeout=3600)
        if job_id in _cancelled:
            update_job(job_id, state='cancelled', errors=errors[:MAX_ERRORS])
            return
        verify = 'verified' if check.returncode == 0 else 'mismatched'
        for line in (check.stderr or '').splitlines():
            _collect_errors(line, errors)
        update_job(job_id, state='done', verify_status=verify, errors=errors[:MAX_ERRORS])
        job = get_job(job_id) or {}
        audit('migration.finish', dest['bucket'], dest.get('prefix') or None,
              f"id={job_id} state=done verify={verify} objects={job.get('objects_done', 0)} bytes={job.get('bytes_done', 0)}")
    except Exception as exc:  # noqa: BLE001 - keep thread alive; message carries no credentials
        update_job(job_id, state='failed', verify_status='failed',
                   errors=([*errors, f'{type(exc).__name__}: copy/verify failed'])[:MAX_ERRORS])
        try:
            audit('migration.finish', dest['bucket'], dest.get('prefix') or None, f'id={job_id} state=failed verify=failed')
        except Exception:  # noqa: BLE001
            pass
    finally:
        _cancelled.discard(job_id)
        with _lock:
            _procs.pop(job_id, None)


def _existing_total(job_id: str, field: str) -> int:
    job = get_job(job_id)
    return int(job[field]) if job else 0


def cancel_job(job_id: str) -> bool:
    with _lock:
        proc = _procs.get(job_id)
        if proc:
            _cancelled.add(job_id)
            proc.terminate()
            return True
    job = get_job(job_id)
    if job and job['state'] in ('queued', 'running', 'verifying'):
        _cancelled.add(job_id)
        update_job(job_id, state='cancelled')
        return True
    return False


def start_job(source: dict, dest: dict) -> dict:
    job = create_job(source, dest)
    thread = threading.Thread(target=run_job, args=(job['id'], dict(source), dict(dest)), daemon=True)
    thread.start()
    return job
