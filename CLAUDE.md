# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Storzo is a self-hosted S3 image-storage console: a React UI plus a FastAPI **management plane** over an upstream **SeaweedFS** S3 server (the data plane). It is a prototype for evaluating a MinIO replacement, not a custom storage engine — don't reimplement S3 features in the backend. Apps talk to SeaweedFS directly with S3 SDKs (`:8333`); only the browser console goes through the FastAPI API.

## Commands

```bash
# Backend tests (fake S3 adapter, no Docker needed)
cd backend && python3 -m pip install -r requirements-dev.txt
python3 -m pytest -q                                  # all
python3 -m pytest -q tests/test_api.py -k <name>      # single test

# Frontend (type-check + build; there is no test or lint script)
cd frontend && npm install && npm run build
npm run dev                                           # vite dev server

# Full stack
python3 scripts/setup_env.py        # writes .env with random secrets; never overwrites an existing .env
docker compose up -d --build        # UI at http://localhost:8088, S3 at http://localhost:8333 (loopback-bound)
grep '^ADMIN_PASSWORD=' .env        # admin login password
python3 scripts/smoke_test.py       # S3 data-plane smoke test against live stack (needs boto3)
bash scripts/verify_restart.sh      # restart containers, check data survives
bash scripts/demo_verify.sh         # build + smoke + restart/checksum in one go
bash scripts/k8s_demo_install.sh    # Kubernetes demo (needs BACKEND_IMAGE / FRONTEND_IMAGE env vars, kubectl, helm)
```

CI (`.github/workflows/ci.yml`) runs backend pytest, the frontend build, and `setup_env.py` + compose config validation. Never run `docker compose down -v` on data you care about — it deletes the named volumes.

## Architecture

- **`backend/app/`** — layered FastAPI package: `main.py` (app factory + startup checks), `config.py` (env constants, read at import time), `security.py` (signed session cookie + CSRF, `safe_bucket`/`safe_key`, `s3_failure`), `s3.py` (cached boto3 clients), `db.py` (SQLite audit, WAL), `stats.py` (bounded dashboard scan), `schemas.py`, and `routers/` (`health`, `auth`, `buckets`, `objects`, `audit`, `dashboard`).
  - Single admin user, no user table. Login issues a signed (`itsdangerous`) HttpOnly `storzo_session` cookie containing a CSRF token. Read endpoints depend on `require_login`; every mutating endpoint depends on `require_write`, which also requires the `x-csrf-token` header to match. New mutating routes must use `require_write`.
  - Startup fails closed: `ADMIN_PASSWORD` ≥16 chars, `SESSION_SECRET` ≥32 chars, and S3 keys must be set, or the app refuses to start.
  - Two cached boto3 clients via `s3.s3_client(public)`: the internal endpoint for server-side calls, and the public endpoint (`S3_PUBLIC_ENDPOINT`) used only to sign presigned URLs the browser will open. Always call it as `s3.s3_client()` (module attribute) so tests can swap it.
  - All user-supplied bucket/key input goes through `safe_bucket` / `safe_key`; S3 errors are mapped to HTTP codes in `s3_failure`. SeaweedFS deletes non-empty buckets, so `delete_bucket` enforces emptiness itself (409).
  - Audit trail is SQLite (`audit` table, WAL) written via `db.audit()`. Single writer → the Helm chart pins the backend to one replica.
  - Thumbnails are generated on demand to WebP via Pillow with pixel/size caps; originals are never modified. Dashboard scans at most 3,000 objects and reports partial results.
- **`backend/tests/test_api.py`** — sets env vars *before* importing `app`, then swaps `s3.s3_client` for a `FakeS3`. Extend `FakeS3` when a new route calls a new boto3 method.
- **`frontend/src/`** — React 19 + Vite + TypeScript console. `main.tsx` mounts `App.tsx` (state, effects, handlers); `pages/` (Login, Overview, Buckets, ActivityList, ActivityEvent, Settings), `components/` (AppDialog, Pager, RefRow, ActivityDetail, TypeDonut, UploadSpark), `api/` (`client.ts` fetch + friendly `ApiError`, `upload.ts` XHR progress), `lib/` (format helpers, hash routes), `types.ts`, `constants.ts`, `style.css`. Icons from `lucide-react`. nginx (`frontend/nginx.conf`) serves the build and proxies `/api/` to `BACKEND_UPSTREAM`. E2E specs live in `frontend/e2e` (`npm run e2e`, needs `E2E_ADMIN_PASSWORD`).
- **Deploy**: `compose.yaml` (storage + backend + frontend, volumes `storage-data` and `audit-data`, ports bound to loopback by default); `deploy/helm/storzo` is the chart for backend/frontend only — SeaweedFS itself is installed from its official chart with `deploy/seaweedfs-values-demo.yaml` by `scripts/k8s_demo_install.sh`.
- **`docs/`** — `ARCHITECTURE.md`, `SECURITY_CHECKLIST.md`, `MIGRATION.md`, `FEATURE_MATRIX.md`, `TL_DEMO.md`. README is deliberately conservative about production-readiness claims; keep new docs/claims consistent with that.

## gstack (recommended)

This project uses [gstack](https://github.com/garrytan/gstack) for AI-assisted workflows.
Install it for the best experience:

```bash
git clone --depth 1 https://github.com/garrytan/gstack.git ~/.claude/skills/gstack
cd ~/.claude/skills/gstack && ./setup --team
```

Skills like /qa, /ship, /review, /investigate, and /browse become available after install.
Use /browse for all web browsing (Aside first, the bundled gstack browser as fallback). Use ~/.claude/skills/gstack/... for gstack file paths.
