# MinIO -> Storzo/SeaweedFS staging migration

**Never overwrite the running production MinIO endpoint during a demonstration.** This workflow applies first to a synthetic bucket on a nonproduction deployment.

## Before cutover

1. Inventory bucket names, object counts/bytes, last-modified dates, versioning, object lock/retention, legal holds, policies, service users, presigned URLs, multipart uploads, encryption and lifecycle rules. Check compliance obligations.
2. Confirm the exact current S3 SDK requests and configuration (path-style versus virtual-hosted, region handling, ETags, conditional requests, range reads, SSE, redirects, CORS). Build a compatibility test per required operation.
3. Run SeaweedFS S3 smoke tests and ensure independent backups and a restore drill for both storage and metadata.
4. Ensure candidate data have no confidential customer information until permissions and legal approvals are in place.

## Staging copies

Using a *separate* host/container with MinIO Client `mc` installed:

```bash
mc alias set oldminio http://OLD_MINIO_HOST:9000 OLD_ACCESS_KEY OLD_SECRET_KEY
mc alias set newlocal http://localhost:8333 NEW_S3_ACCESS_KEY NEW_S3_SECRET_KEY
mc mb --ignore-existing newlocal/staging-images
mc mirror oldminio/staging-images newlocal/staging-images
mc diff oldminio/staging-images newlocal/staging-images
```

The commands above are a **runbook sketch**, not a migration executed by the author. Depending on versioning/retention semantics, a simple mirror may not preserve all versions, tags, legal holds, ACLs or policies. Review the MinIO Client version and options before any production copy.

Validate a sample or (preferably) all objects by streaming downloads from both stores and comparing SHA-256 checksums. Do not treat multipart ETags as universally equivalent to an MD5. Check metadata/content-type, file size, and application behavior.

## Staged cutover

1. Application staging only: point a separate S3 client to the new S3 endpoint; use the same SDK and exercise upload, download, presigned URL, overwrite, concurrent requests and deletes.
2. Run a second incremental sync after final write freeze, verify count/checksum differences, record timing, and retain original MinIO as rollback source.
3. Obtain a documented TL approval. Switch only a controlled deployment, monitor errors, keep rollback DNS/endpoint/config ready.
4. After proven validation and backups, consider production migration. **Never delete MinIO data during first demo or immediately after cutover.**
