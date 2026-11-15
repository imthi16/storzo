# Security and production blocking checklist

## Blocking before any production company data

- [ ] Pin and scan SeaweedFS, Python, npm and nginx images by tested, signed release digests. `latest` is for demo only.
- [ ] Install TLS certs and set `COOKIE_SECURE=true`. Keep private S3 and admin endpoints behind appropriate network access control.
- [ ] Protect credentials in a secrets manager/External Secrets operator. Avoid long-lived static admin keys where STS/role credentials suffice.
- [ ] Rate-limit admin login and set proxy request limits. Review remote address trust and audit real client IPs.
- [ ] Enforce per-team roles, least-privilege policies, identity lifecycle and MFA/SSO requirements.
- [ ] Run S3 compatibility suite for all app features including multipart, versioning, range, conditional operations and presigned URL semantics.
- [ ] Independently assess container, Kubernetes RBAC, secrets, encryption at rest/in transit, storage policy defaults and upstream CVEs.
- [ ] Perform restore drills from off-host backups of objects **and metadata**. Show RPO/RTO and data-checksum validation.
- [ ] Benchmark on expected image sizes, concurrency and equal durability semantics versus existing MinIO.
- [ ] Test power-loss/crash failure with actual disk sync guarantees, full disks, network partitions, node failover, upgrades/rollbacks.
- [ ] Confirm legal/compliance requirements: retention, legal hold, deletion, encryption, privacy and audit retention.
- [ ] Expose the web UI only through trusted TLS ingress; configure WAF/rate limits as required.
- [ ] Independent human or third-party security review of the gateway (`gateway/internal/verify`, `proxy`) and the audit ingest. So far: unit and mutation tests plus cross-review by other AI agents, which found 10 issues, all fixed, and raised one Kubernetes concern that is still open. No human review has happened.
- [ ] Kubernetes: add NetworkPolicies so only the gateway and backend can reach SeaweedFS (S3 and filer). The demo installs none, so another pod may read objects directly and bypass scope and audit. Unverified; prove it on a pinned chart first.
- [ ] Set `CRED_SIGNING_KEY` (32+ characters) from a secrets manager and plan its rotation. It signs agent tokens and audit ingest; there is no revoke-before-expiry, so rotation (restart backend and gateway) is the kill switch.
- [ ] Test your real SDKs and tools against the gateway over HTTPS (AWS CLI, aws-sdk-go-v2, `aws-chunked` streaming uploads return 501, range requests, concurrency) and measure throughput under load. Only boto3 over HTTP was tested.
- [ ] Audit durability: events are queued (10,000), retried 4 times and then counted as failed; there is no disk spool, so a long backend outage loses audit rows. Keep the backend unreachable except through nginx; replay inside the 300 s window is accepted.

## Already present in the MVP source

- [x] Random local credential generator creates untracked `.env` with mode 0600.
- [x] Signed HttpOnly session cookie, SameSite Strict, and CSRF checks for mutations.
- [x] Object key/bucket validation in management API; image preview restricted to raster MIME types.
- [x] SeaweedFS S3 local binding defaults to 127.0.0.1 for Compose.
- [x] Container state is volume-mounted, and admin action audit is persisted separately.
- [x] Agent credentials are signed, bound to one bucket and prefix (trailing `/` required), expire within 1 hour, and issuing is audited. The Go gateway enforces them; in Compose SeaweedFS publishes no host port (verified with `scripts/gateway_e2e.py` and a port check). Scoped requests forward only an `x-amz-*` allowlist, so they cannot set object-lock, ACL or encryption headers with the master key.
- [x] Audit ingest authenticates before reading the body: key length, timestamp window, and a 64-hex signature over `<timestamp>.<body>`, then a 4 MiB streamed cap.

## Known limitations

- Single admin account only; no password rotation UI or OIDC/MFA.
- Gateway requests (allowed and denied) are written to the SQLite audit trail by best-effort batches; denied requests have an empty `kid`. Access that bypasses the gateway (Kubernetes without NetworkPolicy, or direct access to storage) is not logged.
- Master-key applications pass through the gateway without scope checks; ACL and grant headers are stripped for them too.
- `aws-chunked` streaming uploads and CopyObject return 501 through the gateway.
- Backend upload hashes data before pushing it, but full integrity and durability are not proven until read-back and restore testing.
- The SeaweedFS Helm demo chart is single-node by choice; an existing cluster alone does not provide storage HA.
- Helm release history can contain rendered secret values with this demo script, so this script is unsuitable for strict production secret handling.
- The React build pulls npm dependencies, so lockfile + dependency audit should be checked in and scanned before release.
