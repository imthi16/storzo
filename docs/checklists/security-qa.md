# Security and restore QA

Run date: 2026-11-09. Statuses describe this local checkout and running Docker Compose stack, not a
production-readiness certification. No backend application logic was changed.

## Backend checks

Run from `backend/` with the existing dependency environment:

```sh
python3 -m pytest -q
```

Result: **119 passed**, **0 xfailed**, 3 dependency/lifespan deprecation warnings.
The restricted sandbox hung in TestClient; the completed run used Docker/test
execution permission outside that sandbox. No dependencies were added.

| Check | Status | Evidence (test name) |
| --- | --- | --- |
| Existing login, upload, thumbnail, download, deletion and Unicode regressions | PASS | Existing five tests in `backend/tests/test_api.py` |
| Every protected GET route rejects an unauthenticated request with 401 | PASS | `test_every_read_route_requires_login` (9 routes, discovered from FastAPI APIRoutes) |
| Liveness and readiness intentionally remain public | PASS | `test_health_routes_intentionally_public` |
| Logged-in writes without CSRF header are rejected before changes | PASS | `test_all_writes_require_login_csrf` (create/delete bucket, upload/delete object, logout) |
| Logged-in writes with an incorrect CSRF token are rejected before changes | PASS | `test_all_writes_require_login_csrf` |
| Bucket validator rejects traversal, slash, backslash, NUL and malformed names | PASS | `test_safe_bucket_rejects_traversal` |
| Object-key validator rejects traversal, absolute paths, backslash, NUL and oversized UTF-8 keys | PASS | `test_safe_key_rejects_traversal` |
| Malicious bucket creation is rejected without creating a bucket | PASS | `test_bucket_creation_rejects_traversal_before_s3` |
| Object routes reject malicious keys before reads, writes or presign audit entries | PASS | `test_object_routes_reject_traversal_before_s3` (download, preview, thumbnail, presign, delete, upload, listing prefix) |
| Upload one byte over configured limit is rejected with 413 and no S3/audit write | PASS | `test_upload_limit_rejects_before_s3` (cap set to 1 MiB in test) |
| Upload exactly at configured limit succeeds with exact bytes | PASS | `test_upload_limit_rejects_before_s3` |

Empty listing prefix is intentionally allowed. An empty multipart upload key
receives request validation status 422; other invalid keys receive 400.
Health endpoints and the framework-generated OpenAPI schema are outside the
protected management GET routes covered by the authentication matrix.

## Live backup/restore checks

Prerequisites: running storage and backend Compose services, Docker access,
Linux Docker host networking, a local Alpine image (Docker may pull it), and free loopback port 18333. The drill
uses the backend image's existing boto3/SQLite modules. It does not open `.env`
or export live credentials. The restored storage copy uses separate drill-only
credentials, and Docker errors are not echoed with environment values.

```sh
python3 scripts/restore_integrity.py
```

Final successful run: storage backup **2.8 s**, storage restore/verification
**4.4 s**, audit backup/restore/verification **1.5 s**; exit 0.

The script briefly stops/restarts storage, then separately backend for a cold
audit copy. Both original volumes are mounted read-only in backup helpers.
Only random per-run scratch S3 objects/buckets and throwaway resources are
removed; the live audit `restore_probe` row is deliberately retained as evidence.
Never use `docker compose down -v`.

| Check | Status | Evidence |
| --- | --- | --- |
| Storage cold backup restores to a throwaway volume and serves the exact 256 KiB object | PASS | `python3 scripts/restore_integrity.py`: SHA-256 match |
| Audit volume cold backup restores to a separate throwaway volume | PASS | Same command: `PASS: restored SQLite integrity_check and exact audit row match` |
| Complete audit probe row survives restore, including ID/time/actor/action/bucket/key/detail | PASS | Same command: exact row comparison and `PRAGMA integrity_check` |
| Successful drill removes scratch S3 bucket, all helper containers, both restored volumes and archive directory | PASS | Same command: `PASS: live scratch bucket cleaned up` and `PASS: cleanup verified` |
| Failure immediately after storage stop restarts storage and cleans up | PASS | `python3 scripts/restore_integrity.py --fail-after storage-stopped` |
| Failure after storage archive creation cleans up archive/directory | PASS | `python3 scripts/restore_integrity.py --fail-after storage-backup` |
| Failure after restored storage container starts cleans up container/volume | PASS | `python3 scripts/restore_integrity.py --fail-after storage-restored` |
| Failure immediately after backend stop restarts backend and cleans up | PASS | `python3 scripts/restore_integrity.py --fail-after audit-stopped` |
| Failure after audit archive creation cleans up both archives | PASS | `python3 scripts/restore_integrity.py --fail-after audit-backup` |
| Failure after both restored volumes exist cleans up both volumes | PASS | `python3 scripts/restore_integrity.py --fail-after audit-restored` |
| No remaining `storzo-restore-*` containers, volumes or temporary directories | PASS | Independent census: containers `[]`, volumes `[]`, temporary directories `[]`; legacy `storzo-backup-*` directories also `[]` |
| Live stack healthy again after all drills | PASS | `docker compose ps`: all three running, backend healthy; `/api/health/ready`: HTTP 200, `status=ready`, `storage=reachable` |

Each injected failure must exit **1**, include `Injected failure after <step>`
in stderr, and include `PASS: cleanup verified` in stdout. A nonzero exit alone
is insufficient evidence. Run injections sequentially because they restart the
same live services and reuse port 18333.

Independent resource census (expect all lists empty):

```sh
docker ps -a --filter name=storzo-restore- --format '{{.Names}}'
docker volume ls --filter name=storzo-restore- --format '{{.Name}}'
python3 -c 'from pathlib import Path; print(list(Path("/tmp").glob("storzo-restore-*")))'
docker compose ps
curl --fail http://127.0.0.1:8088/api/health/ready
```

No backend defects were exposed by these checks; no xfails were needed. Abrupt
SIGKILL, host failure, Docker daemon failure and external writers to these
volumes were not simulated. This verifies exception cleanup, not crash recovery.
