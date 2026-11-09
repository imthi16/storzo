# Storzo feature matrix — honest MVP status

Status meanings: **Implemented** = source code exists, subject to end-to-end validation; **Delegated** = provided by SeaweedFS S3 engine, not authored by Storzo; **Planned** = not implemented and must not be demonstrated as existing.

| Component | Feature | Status | Evidence / notes |
|---|---|---|---|
| React UI | Admin sign-in / sign-out | Implemented | Cookie session via FastAPI, CSRF on writes |
| React UI | Overview and bucket stats | Implemented | Capped 3000-object scan |
| React UI | Bucket create/list/delete (empty) | Implemented | Backend S3 calls |
| React UI | Object and folder browsing | Implemented | Prefix/Delimiter, first-page pagination |
| React UI | Drag-and-drop image upload | Implemented | 50 MB default backend limit |
| React UI | Image thumbnail and full preview | Implemented | Original unchanged, on-demand WebP thumbnail |
| React UI | Download / delete | Implemented | Requires authenticated session; CSRF on delete |
| React UI | Presigned download link | Implemented | Requires public S3 endpoint / local port-forward |
| React UI | Activity / Settings / Health | Implemented | Single admin only |
| Backend | Audit logging | Implemented | Local SQLite database with persistent volume |
| Backend | SHA-256 of uploaded object | Implemented | Added to S3 object metadata for UI uploads |
| Backend | S3 SigV4/S3 SDK support | Delegated | AWS SDK speaks directly to SeaweedFS gateway |
| Backend | Multipart upload protocol | Delegated | SeaweedFS implementation; verify SDK workload |
| Backend | Versioning and object lock | Delegated/Needs validation | Do not assume MinIO parity without test |
| Backend | IAM and bucket policies | Delegated/Needs validation | Current UI only has one administrative identity |
| Backend | Replication and erasure coding | Delegated/Needs validation | Demo is SINGLE-NODE without replication |
| Backend | Per-tenant quota | Planned | Requires policy/identity model |
| Backend | Content-addressable dedupe | Planned | Metadata/references and race safety needed |
| Backend | Automatic original-preserving variants | Planned | Needs async queue and cache invalidation |
| Backend | Intelligent tiering | Planned | Outside scope; no paid cloud required |
| Backend | Full-text / visual search | Planned | Optional future feature |
| Ops | Docker Compose | Implemented as config | Not run by authoring environment |
| Ops | Frontend/backend Dockerfiles | Implemented as source | Local image build still required |
| Ops | Helm chart and SeaweedFS chart values | Implemented as config | Unvalidated live deployment; adjust cluster needs |
| Ops | Secrets generator | Implemented | Random generated secret; local .env only |
| Ops | Backend unit tests | Implemented and passed | Three cases with fake S3 |
| Ops | Real S3 integrity smoke script | Implemented as script | Run only against live stack |
| Ops | Verified restart persistence | Manual validation required | Do not mark complete based on PVC configuration alone |
| Ops | TLS / enterprise auth / MFA | Planned | Must be implemented before internet exposure |
| Ops | Single-node cold backup/restore drill | Implemented as script | `scripts/restore_integrity.py` tars the volume on the same host and restores to a throwaway copy; passed once on a dev machine. Not off-host, not incremental |
| Ops | Disaster recovery (off-host backup, RPO/RTO targets) | Planned | Must prove before cutover |
