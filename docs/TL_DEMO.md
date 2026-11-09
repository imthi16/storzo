# TL handoff — working prototype demo (November 10, 2026)

## What to say

> We built a self-hosted, image-aware S3 storage prototype using our existing Docker/Kubernetes model, with no requirement for GCS. The underlying object engine is SeaweedFS; our work is a separate React/FastAPI console, object management API, image preview, persistent deployment, and integration tests. This is a staging proof-of-concept, not a production-cleared replacement or a new RustFS competitor yet. Before replacing MinIO, we still need SDK compatibility, security and restore validation, and sizing/cost assessment.

## Five-minute path

1. Run `bash scripts/demo_verify.sh` (or equivalent manual checks), then `docker compose ps`, show storage/backend/frontend running and `curl -f http://localhost:8088/api/health/ready` returns `ready`.
2. Open `http://localhost:8088`, log in, show dashboard and `demo-images` bucket. Show that the browser UI never exposes S3 secret keys.
3. Upload an example image (synthetic test data), browse folder prefixes, open preview and download original. Note that the thumbnail WebP does not replace the source image.
4. Demonstrate AWS S3 SDK direct access via `python3 scripts/smoke_test.py`: create bucket, PUT, GET, SHA-256 match, list and cleanup.
5. Restart containers via `docker compose restart storage backend frontend`; verify the *same previously uploaded test image* still appears and downloads intact. This is a restart-persistence demonstration, not disaster-recovery proof.
6. Explain Docker volumes/Kubernetes PVC and the next needed validation for production: redundancy, backups, restore, security and app SDK compatibility.

## Demo acceptance gates (fill in on your own machine)

| Test | Result |
|---|---|
| `docker compose up -d --build` completes | ☐ Pass / ☐ Fail |
| UI works and admin login succeeds | ☐ Pass / ☐ Fail |
| Bucket create/list/delete | ☐ Pass / ☐ Fail |
| PNG/JPG upload, browse, preview, download | ☐ Pass / ☐ Fail |
| Boto3 smoke test full pass | ☐ Pass / ☐ Fail |
| Restart, re-download same image, compare hash | ☐ Pass / ☐ Fail |
| Kubernetes chart install in staging | ☐ Pass / ☐ Not attempted |

**Do not claim a checked result without running it.** Record command output/screenshots for TL review.

## If something goes wrong

- **Backend ready returns 503:** `docker compose logs --tail=120 storage backend`; confirm S3 secrets match both services; inspect storage volume.
- **UI 502:** inspect `frontend` logs and `backend` health; Nginx template must resolve `BACKEND_UPSTREAM=backend:8000`.
- **Build dependencies fail:** check Docker DNS and outbound network access to image registries/npm/PyPI; commit a reviewed package lock for reproducibility later.
- **Signed links work only on server:** set `S3_PUBLIC_ENDPOINT` to the hostname/port reachable from the browser, restart the backend. For local Kubernetes use a second port-forward on 8333.
- **Kubernetes PVC Pending:** ensure StorageClass and capacity exist; fix storage provisioning before claiming Kubernetes compatibility.
