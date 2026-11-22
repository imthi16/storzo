# Storzo feature matrix — honest MVP status

Status meanings: **Implemented** = source code exists, subject to end-to-end validation; **Delegated** = provided by SeaweedFS S3 engine, not authored by Storzo; **Planned** = not implemented and must not be demonstrated as existing.

| Component | Feature | Status | Evidence / notes |
|---|---|---|---|
| React UI | Admin sign-in / sign-out | Implemented | Cookie session via FastAPI, CSRF on writes |
| React UI | Overview and bucket stats | Implemented | Capped 3000-object scan. The 14-day upload chart counts console uploads plus successful gateway `PUT` requests (a multipart upload counts once per part, and the number includes applications and scripts, not only agents); the file-type colors pass the project palette checks for adjacent categories, not for every pair, so the legend text carries the identity |
| React UI | Bucket create/list/delete (empty) | Implemented | Backend S3 calls |
| React UI | Object and folder browsing | Implemented | Prefix/Delimiter, first-page pagination |
| React UI | Drag-and-drop image upload | Implemented | 50 MB default backend limit |
| React UI | Image thumbnail and full preview | Implemented | Original unchanged, on-demand WebP thumbnail |
| React UI | Download / delete | Implemented | Requires authenticated session; CSRF on delete |
| React UI | Presigned download link | Implemented | Requires public S3 endpoint / local port-forward |
| React UI | Activity log | Implemented | Console actions and gateway requests; gateway rows show method, allow/deny and status; filter by source and decision (server-side, `GET /api/audit?source=&decision=`); 24-hour timestamps; skip link. Not tested with a screen reader |
| React UI | Settings / Health | Implemented | Single admin only. The "Before production" list shows open items as unchecked; nothing on that page asserts production readiness |
| React UI | Agent credentials | Implemented | `#/credentials` issues via `POST /api/credentials` (bucket, prefix ending in `/`, ro/rw, TTL presets up to the 1 hour server max) and shows the secret and session token once, plus storzo-mcp env and MCP JSON. Recent rows are `credential.issue` audit events. Revoke calls `POST /api/credentials/{kid}/revoke` and, on 404 or 405, says revoke is not available on this server yet. Playwright spec is written; not run against a live stack in this change |
| Backend | Audit logging | Implemented | Local SQLite database with persistent volume |
| Backend | SHA-256 of uploaded object | Implemented | Added to S3 object metadata for UI uploads |
| Backend | S3 SigV4/S3 SDK support | Delegated | The SDK talks to the Storzo gateway, which verifies and forwards to SeaweedFS; SeaweedFS does the S3 work |
| Backend | Multipart upload protocol | Delegated | SeaweedFS implementation; verify SDK workload |
| Backend | Versioning and object lock | Delegated/Needs validation | Do not assume MinIO parity without test |
| Backend | IAM and bucket policies | Delegated/Needs validation | Current UI only has one administrative identity |
| Backend | Replication and erasure coding | Delegated/Needs validation | Demo is SINGLE-NODE without replication |
| Backend | Scoped agent credentials API | Implemented | `POST /api/credentials`: bucket + prefix (ends in `/`) + `ro`/`rw`, TTL 1-3600 s. Enforced by the gateway |
| Backend | Gateway audit ingest | Implemented | `POST /api/internal/audit`: HMAC over `<timestamp>.<body>`, 300 s window (a replay inside it is accepted), 4 MiB body cap, field limits; blocked at nginx |
| Gateway | Credential verification (token, SigV4, scope matrix) | Implemented | Go. Cross-language vectors from the Python issuer; mutation-checked; live-tested through the gateway with boto3 over HTTP. Not tested with AWS CLI or HTTPS |
| Gateway | MCP stdio server (`storzo-mcp`) | Implemented | Unit tests use a fake S3; `gateway/cmd/storzo-mcp/gateway_integration_test.go` runs it through the real proxy handler (`ro` and `rw`). Also checked by hand against the live Compose stack from Claude Code, Codex, OpenCode, Cursor and Muse (2026-11-22) |
| Gateway | S3 proxy (`storzo-gateway`) | Implemented | Re-signs with the master key; scoped requests forward only an `x-amz-*` allowlist; master clients pass through (ACL headers stripped); Signed trailer-free `aws-chunked` PUTs are decoded and verified within the hashed-body cap (fake-upstream tests). CopyObject and other streaming variants return 501. Load not measured beyond single 50 MB objects, a few runs: direct put 62-95 / get 213-227 MB/s, via gateway put 51-62 / get 230-244 MB/s |
| Gateway | Scoped credential revocation | Implemented | Admin POST with login and CSRF. Gateway polls an HMAC-authenticated internal feed every 10 s by default, returns 403 AccessDenied for cached revoked keys, and retains its last successful snapshot during backend outages |
| Gateway | Audit emitter | Implemented | Batched, bounded queue, 4 attempts, `Failed()`/`Dropped()` counters, drains on shutdown. No disk spool |
| Ops | SeaweedFS internal-only | Implemented in Compose | `storage` publishes no port; on Kubernetes, another pod may reach SeaweedFS directly unless the optional NetworkPolicy is enabled (default off, never applied to a cluster; see the NetworkPolicy row) |
| Backend | Per-tenant quota | Planned | Requires policy/identity model |
| Backend | Content-addressable dedupe | Planned | Metadata/references and race safety needed |
| Backend | Automatic original-preserving variants | Planned | Needs async queue and cache invalidation |
| Backend | Intelligent tiering | Planned | Outside scope; no paid cloud required |
| Backend | Full-text / visual search | Planned | Optional future feature |
| Ops | Docker Compose | Implemented as config | Not run by authoring environment |
| Ops | Frontend/backend Dockerfiles | Implemented as source | Local image build still required |
| Ops | Helm chart and SeaweedFS chart values | Implemented as config | Unvalidated live deployment; adjust cluster needs |
| Ops | Kubernetes NetworkPolicy | Implemented as config | `deploy/helm/storzo/templates/networkpolicy.yaml`, `networkPolicy.enabled` default false. When on: default-deny ingress for backend and gateway; frontend and gateway may reach backend:8000; `networkPolicy.clients` may reach gateway:8333; backend and gateway may egress to the storage selector and cluster DNS. `/api/internal` has no extra ingress; the frontend nginx still returns 404 for it, and the backend Service must stay unpublished. Not applied to a live cluster in this change |
| Ops | Secrets generator | Implemented | Random generated secret; local .env only |
| Ops | Backend unit tests | Implemented and passed | Fake-S3 suite; CI also runs ruff, mypy and an 85% coverage floor |
| Ops | Gateway Go tests | Implemented | CI runs gofmt, vet and race tests; gateway image is built and Trivy-scanned in CI |
| Ops | Gateway e2e acceptance script | Implemented | `scripts/gateway_e2e.py` against a live stack: 35 checks including a ledger that requires one audit row per request. Runs in CI (`e2e` job) before the Playwright suite |
| Ops | Real S3 integrity smoke script | Implemented as script | Run only against live stack |
| Ops | Verified restart persistence | Manual validation required | Do not mark complete based on PVC configuration alone |
| Ops | TLS / enterprise auth / MFA | Planned | Must be implemented before internet exposure |
| Ops | Single-node cold backup/restore drill | Implemented as script | `scripts/restore_integrity.py` tars the volume on the same host and restores to a throwaway copy; passed once on a dev machine. Not off-host, not incremental |
| Ops | Disaster recovery (off-host backup, RPO/RTO targets) | Planned | Must prove before cutover |
