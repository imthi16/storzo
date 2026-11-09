#!/usr/bin/env python3
"""Cold storage + SQLite audit backup/restore drill; live volumes are mounted read-only.

Requires a running Compose stack and Docker access, but no host Python packages or
.env/credential reads. S3 operations run inside the existing backend. All copies,
helper containers and archives are removed in finally, including injected failures.
"""
import argparse
import json
import secrets
import subprocess
import tempfile
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
STEPS = ('storage-stopped', 'storage-backup', 'storage-restored',
         'audit-stopped', 'audit-backup', 'audit-restored')


def sh(*args):
    result = subprocess.run(args, cwd=ROOT, text=True, capture_output=True)
    if result.returncode:
        # Docker errors may contain container environment values. Do not echo them.
        raise RuntimeError(f'{args[0]} {args[1]} failed (exit {result.returncode})')
    return result.stdout.strip()


def mounted_volume(cid, destination):
    volume = sh('docker', 'inspect', '-f',
                '{{range .Mounts}}{{if eq .Destination "' + destination + '"}}{{.Name}}{{end}}{{end}}', cid)
    if not volume:
        raise RuntimeError(f'No named volume mounted at {destination}')
    return volume


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--fail-after', choices=STEPS, help='Inject failure to exercise finally cleanup')
    args = parser.parse_args(argv)
    storage = sh('docker', 'compose', 'ps', '-q', 'storage')
    backend = sh('docker', 'compose', 'ps', '-q', 'backend')
    if not storage or not backend:
        raise RuntimeError('storage and backend must be running; run docker compose up -d first')
    storage_image = sh('docker', 'inspect', '-f', '{{.Image}}', storage)
    backend_image = sh('docker', 'inspect', '-f', '{{.Image}}', backend)
    storage_volume = mounted_volume(storage, '/data')
    audit_volume = mounted_volume(backend, '/app/state')
    suffix = secrets.token_hex(6)
    name = f'storzo-restore-{suffix}'
    volumes = [name + '-storage', name + '-audit']
    containers = [name + '-' + role for role in ('storage', 'archive', 'sqlite', 'verify')]
    backup_dir = Path(tempfile.mkdtemp(prefix=name + '-'))
    bucket, key = name, 'test/restore.bin'
    created = False
    cleanup_errors = []

    def checkpoint(step):
        print(f'STEP: {step}', flush=True)
        if args.fail_after == step:
            raise RuntimeError(f'Injected failure after {step}')

    def live(code):
        return sh('docker', 'exec', backend, 'python', '-c',
                  'from app.main import s3_client, audit, DB_PATH\n'
                  'import hashlib, secrets, sqlite3, json\n'
                  f'bucket={bucket!r}; key={key!r}\n' + code)

    def archive(source, filename, restore=False):
        sh('docker', 'run', '--rm', '--name', containers[1],
           '-v', f'{source}:/data' + ('' if restore else ':ro'),
           '-v', f'{backup_dir}:/backup' + (':ro' if restore else ''),
           'alpine', 'tar', 'xf' if restore else 'cf', f'/backup/{filename}', '-C', '/data', '.')

    try:
        live('s3_client().create_bucket(Bucket=bucket)')
        created = True
        expected = live("data=secrets.token_bytes(256*1024)\n"
                        "s3_client().put_object(Bucket=bucket, Key=key, Body=data)\n"
                        "print(hashlib.sha256(data).hexdigest())")
        print('TEST: uploaded 256 KiB random object; SHA-256:', expected, flush=True)
        t0 = time.monotonic()
        # Include stop itself in try: a partially successful stop still needs start.
        try:
            sh('docker', 'stop', storage)
            checkpoint('storage-stopped')
            archive(storage_volume, 'storage.tar')
            checkpoint('storage-backup')
        finally:
            sh('docker', 'start', storage)
        print(f'TEST: cold storage backup took {time.monotonic() - t0:.1f}s', flush=True)
        restore_started = time.monotonic()
        sh('docker', 'volume', 'create', volumes[0])
        archive(volumes[0], 'storage.tar', restore=True)
        # Fresh credentials apply only to this loopback-bound throwaway copy.
        sh('docker', 'run', '-d', '--name', containers[0], '-v', f'{volumes[0]}:/data',
           '-p', '127.0.0.1:18333:8333', '-e', 'AWS_ACCESS_KEY_ID=restore-drill',
           '-e', 'AWS_SECRET_ACCESS_KEY=restore-drill-copy-only',
           '-e', 'WEED_ADMIN_PASSWORD=restore-drill-copy-only', storage_image)
        checkpoint('storage-restored')
        verify = (
            'import boto3, hashlib, time\n'
            'from botocore.config import Config\n'
            "s=boto3.client('s3', endpoint_url='http://127.0.0.1:18333', "
            "aws_access_key_id='restore-drill', aws_secret_access_key='restore-drill-copy-only', "
            "config=Config(connect_timeout=2, read_timeout=2, retries={'max_attempts': 0}))\n"
            'for attempt in range(30):\n'
            ' try:\n'
            f'  data=s.get_object(Bucket={bucket!r},Key={key!r})["Body"].read()\n'
            '  break\n'
            ' except Exception:\n'
            '  if attempt == 29: raise RuntimeError("Restored S3 unavailable") from None\n'
            '  time.sleep(3)\n'
            f'assert hashlib.sha256(data).hexdigest() == {expected!r}, "SHA-256 mismatch"\n'
            'print("PASS: restored storage SHA-256 matches")'
        )
        print(sh('docker', 'run', '--rm', '--name', containers[3], '--network', 'host',
                 backend_image, 'python', '-c', verify), flush=True)

        print(f'TEST: storage restore + verification took {time.monotonic() - restore_started:.1f}s', flush=True)

        # Record a unique row through the app's SQLite writer. Cold-copy the entire
        # volume (including WAL/SHM), with the backend stopped to exclude writers.
        row = live("audit('restore_probe', bucket, key, 'backup/restore verification')\n"
                   "with sqlite3.connect(DB_PATH) as db:\n"
                   " print(json.dumps(db.execute('SELECT * FROM audit WHERE action=? AND bucket=?', "
                   "('restore_probe', bucket)).fetchone()))")
        t1 = time.monotonic()
        try:
            sh('docker', 'stop', backend)
            checkpoint('audit-stopped')
            archive(audit_volume, 'audit.tar')
            checkpoint('audit-backup')
        finally:
            sh('docker', 'start', backend)
        sh('docker', 'volume', 'create', volumes[1])
        archive(volumes[1], 'audit.tar', restore=True)
        checkpoint('audit-restored')
        verify_db = (
            'import sqlite3, json\n'
            'with sqlite3.connect("file:/restore/audit.db?mode=ro", uri=True) as db:\n'
            ' assert db.execute("PRAGMA integrity_check").fetchone() == ("ok",)\n'
            ' actual=db.execute("SELECT * FROM audit WHERE action=? AND bucket=?", '
            f'("restore_probe", {bucket!r})).fetchone()\n'
            f' assert list(actual or ()) == {json.loads(row)!r}, "Audit row mismatch"\n'
            'print("PASS: restored SQLite integrity_check and exact audit row match")'
        )
        print(sh('docker', 'run', '--rm', '--name', containers[2],
                 '-v', f'{volumes[1]}:/restore', backend_image, 'python', '-c', verify_db), flush=True)
        print(f'TEST: audit backup + restore + verification took {time.monotonic() - t1:.1f}s', flush=True)
    finally:
        # Only remove resources with this run's unpredictable names. Verify removal
        # instead of swallowing cleanup errors and reporting a false success.
        for container in containers:
            subprocess.run(['docker', 'rm', '-f', container], capture_output=True)
        for volume in volumes:
            subprocess.run(['docker', 'volume', 'rm', volume], capture_output=True)
        try:
            # Archives were created by root in Docker; remove only these two files.
            sh('docker', 'run', '--rm', '--name', containers[1], '-v', f'{backup_dir}:/backup',
               'alpine', 'rm', '-f', '/backup/storage.tar', '/backup/audit.tar')
            backup_dir.rmdir()
        except Exception:
            cleanup_errors.append('temporary archives/directory')
        if created:
            for attempt in range(30):
                try:
                    live('s3_client().delete_object(Bucket=bucket, Key=key)\n'
                         's3_client().delete_bucket(Bucket=bucket)')
                    print('PASS: live scratch bucket cleaned up', flush=True)
                    break
                except Exception:
                    if attempt == 29:
                        cleanup_errors.append('live scratch bucket')
                    else:
                        time.sleep(3)
        remaining_containers = set(sh('docker', 'ps', '-a', '--format', '{{.Names}}').splitlines())
        remaining_volumes = set(sh('docker', 'volume', 'ls', '--format', '{{.Name}}').splitlines())
        cleanup_errors.extend(sorted(remaining_containers.intersection(containers)))
        cleanup_errors.extend(sorted(remaining_volumes.intersection(volumes)))
        if cleanup_errors:
            raise RuntimeError('Cleanup failed: ' + ', '.join(cleanup_errors))
        print(f'PASS: cleanup verified for {name} (containers, volumes, temporary directory)', flush=True)


if __name__ == '__main__':
    main()
