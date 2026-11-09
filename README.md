# Storzo 0.1.0 — Self-hosted S3 image storage console

**Working-source MVP for a MinIO-to-local-storage evaluation. No GCS or AWS account required.**

Storzo combines a custom React management console and FastAPI image-management backend with the open-source **SeaweedFS** S3 storage engine. The application objects live in local Docker volumes or Kubernetes PersistentVolumes, **not** in a SaaS cloud. Applications can use the SeaweedFS S3 endpoint directly with AWS SDKs.

> **Honest status:** This is a *prototype implementation*, not a fully verified production MinIO replacement or a new independent Rust storage engine. The management API unit tests run locally. Docker image builds, full end-to-end storage tests, Kubernetes deployment, durability and security assessments must still be run in your environment **before** handling company data.

## What exists

- Responsive React console: login, overview, bucket browser, nested folder navigation, drag-and-drop upload, file download, delete, image thumbnails/preview, short-lived presigned links, activity audit and status page.
- FastAPI REST admin backend: auth with HttpOnly signed session cookie and CSRF protection, bucket/object management, SHA-256 for UI uploads, capped thumbnail transformation into WebP **without altering originals**, basic SQLite audit trail.
- Official SeaweedFS S3 API provides object data plane, application SDK access and filesystem-backed persistence. SeaweedFS features are **not reimplemented** in Storzo.
- Dockerfiles for backend and frontend, Docker Compose deployment (with persistent volumes), local generated secrets, simple smoke test, starter Helm chart, Kubernetes demo bootstrap script, GitHub CI workflow.
- No mandatory external object-store bills. Your server/disk, backup, network, maintenance and power **still have costs**.

## Run locally (Docker Compose)

**Requirements:** Docker Engine + Docker Compose v2, Python 3.10+ to generate credentials, available Docker disk space, internet to pull base images/build Node dependencies.

```bash
# Inside the extracted storzo directory
python3 scripts/setup_env.py      # Creates randomized admin and S3 credentials in .env

docker compose up -d --build

docker compose ps
curl -f http://localhost:8088/api/health/live
curl -f http://localhost:8088/api/health/ready
```

Open **http://localhost:8088**. Username: `admin`. Retrieve the generated password on **your machine**:

```bash
grep '^ADMIN_PASSWORD=' .env
```

The Docker Compose demo defaults to the upstream SeaweedFS `4.53_full` image tag (verified on Docker Hub, November 9, 2026); for production, validate and pin an immutable image digest. The `demo-images` bucket is automatically initialized by SeaweedFS. You can create other buckets in the console.

If startup is slow, inspect logs:

```bash
docker compose logs --tail=100 storage backend frontend
```

**Don't run `docker compose down -v` on data you care about.** `-v` deletes named persistent volumes. Normal `docker compose stop`, `up`, `restart` and `down` (without `-v`) retain them.

For a single-command local Docker build + S3 smoke + actual storage restart/checksum test (requires `boto3` installed on the host):

```bash
bash scripts/demo_verify.sh
```

## Access the S3 API from an existing application

The Docker Compose development S3 endpoint is `http://localhost:8333` on the host (loopback-bound by default), or `http://storage:8333` inside the Compose network. Credentials are in `.env`.

Example for Python `boto3`:

```python
import boto3
from botocore.config import Config

s3 = boto3.client(
    's3', endpoint_url='http://localhost:8333',
    aws_access_key_id='YOUR_S3_ACCESS_KEY',
    aws_secret_access_key='YOUR_S3_SECRET_KEY',
    region_name='us-east-1',
    config=Config(signature_version='s3v4', s3={'addressing_style': 'path'}),
)
s3.upload_file('example.png', 'demo-images', 'photos/example.png')
print(s3.list_objects_v2(Bucket='demo-images'))
```

Replace placeholders with your locally generated secrets. This is a native S3 SDK call to SeaweedFS, **not** to the FastAPI management API.

## Run the automated checks

```bash
# FastAPI tests using a fake S3 store; no Docker required
cd backend
python3 -m pip install -r requirements-dev.txt
python3 -m pytest -q
cd ..

# Full data-plane smoke test against a live Docker stack
python3 -m pip install boto3
python3 scripts/smoke_test.py

# Restart persisted containers and verify a test image is still there
bash scripts/verify_restart.sh
```

To compile the React app locally:

```bash
cd frontend
npm install
npm run build
```

**Test results bundled by author:** 3 backend test cases passed with a fake S3 adapter, TSX syntax parsing passed. Docker, React dependency install, real SeaweedFS smoke test and Helm installation were *not available to run in this authoring environment*, so those are still acceptance gates.

## Build Docker images for Kubernetes

```bash
# Replace the sample registry paths with your company-accessible container registry.
docker build -t registry.example.com/team/storzo-backend:0.1.0 ./backend
docker build -t registry.example.com/team/storzo-frontend:0.1.0 ./frontend

docker push registry.example.com/team/storzo-backend:0.1.0
docker push registry.example.com/team/storzo-frontend:0.1.0

export BACKEND_IMAGE=registry.example.com/team/storzo-backend:0.1.0
export FRONTEND_IMAGE=registry.example.com/team/storzo-frontend:0.1.0
bash scripts/k8s_demo_install.sh
```

The Kubernetes bootstrap script creates a **new namespace** `storzo-demo`, installs the official SeaweedFS chart with demo-sized PVCs, creates a Kubernetes Secret with the generated credentials and deploys our management UI/API Helm chart.

Prerequisites: `kubectl`, `helm`, a working cluster with a default StorageClass and permissions to install resources. Use a nonproduction namespace. The Helm chart's values and generated secrets are **demo-oriented** and must be reviewed before exposing externally.

After installation:

```bash
kubectl -n storzo-demo port-forward svc/storzo-storzo-frontend 8088:80
# New terminal: for signed S3 links in the browser
kubectl -n storzo-demo port-forward svc/seaweedfs-s3 8333:8333
```

Open `http://localhost:8088` and sign in. **Never** copy production MinIO data into the demo until backup/restore and policy checks pass. For production replace port-forward with TLS ingress, external secret management and a properly sized, redundant SeaweedFS deployment. The backend uses one SQLite audit writer and is set to one replica.

## Feature scope and important limitations

| Domain | MVP implementation | Not yet production-ready / not included |
| --- | --- | --- |
| S3 data plane | SeaweedFS S3 gateway with authenticated SDK access | Feature-by-feature compatibility against existing MinIO SDK usages needs validation |
| Storage | Persisted Docker volume; Kubernetes PVC example | Multi-node HA, disaster recovery, proven automatic repair for company workload |
| Admin web app | Login, bucket/objects CRUD, uploads, previews, audit | Full multi-user IAM, retention UI, lifecycle UI, unlimited object analytics |
| Image features | On-demand WebP thumbnail preview, SHA-256 metadata on UI uploads | Automatic tiering, storage deduplication, production image transformation queue |
| Security | Randomized secrets, signed HttpOnly cookie, CSRF checks, loopback ports by default | TLS ingress setup, rate limiting, enterprise auth, external vulnerability assessment |
| Operations | Health checks, Compose, Helm example, CI definitions | Real load/chaos testing, verified backup/restore runbook, SLO evidence |

The dashboard scans at most 3,000 objects; it labels results as partial when capped. It is not an authoritative disk/billing meter. Deleting objects in versioned buckets may create delete markers rather than immediately reclaim space. Underlying provider feature support must be tested.

## Read these before the office demonstration

- [`docs/TL_DEMO.md`](docs/TL_DEMO.md): five-minute demonstration and acceptance script.
- [`docs/FEATURE_MATRIX.md`](docs/FEATURE_MATRIX.md): implemented vs delegated vs future features.
- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md): storage/data/control plane and trust boundaries.
- [`docs/MIGRATION.md`](docs/MIGRATION.md): staged MinIO migration (do not cut over without validation).
- [`docs/SECURITY_CHECKLIST.md`](docs/SECURITY_CHECKLIST.md): production blocking requirements.
- [`docs/BUILD_PLAN.md`](docs/BUILD_PLAN.md): Claude Code / Codex / Muse Code ownership and prompts.

## License and attribution

SeaweedFS is an independent upstream project; see its upstream repository for its license and notices. Storzo is a new demonstration/control-plane codebase and is **not** affiliated with MinIO, RustFS, or SeaweedFS. Before publishing the Storzo repository, choose and record a license and preserve required third-party notices. This archive does not imply that any third-party code has been relicensed.

## Safe migration posture

Keep the existing MinIO installation untouched. Test with synthetic images first. Even after this demo works, obtain a staging compatibility matrix, full backups, restore drill, bucket permissions review, cutover/rollback plan, and TL approval before production migration.

## Contributing

Use separate feature branches for Claude Code (backend), Meta Muse Code (frontend) and Codex (independent testing/review); merge to `main` only when tests pass. Do not commit secrets, `.env`, office data, or customer files.
