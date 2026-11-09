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

## Already present in the MVP source

- [x] Random local credential generator creates untracked `.env` with mode 0600.
- [x] Signed HttpOnly session cookie, SameSite Strict, and CSRF checks for mutations.
- [x] Object key/bucket validation in management API; image preview restricted to raster MIME types.
- [x] SeaweedFS S3 local binding defaults to 127.0.0.1 for Compose.
- [x] Container state is volume-mounted, and admin action audit is persisted separately.

## Known limitations

- Single admin account only; no password rotation UI or OIDC/MFA.
- S3 access through the **native gateway** is not logged to the UI's SQLite audit. Native gateway logs must be centralized separately for enterprise audit.
- Backend upload hashes data before pushing it, but full integrity and durability are not proven until read-back and restore testing.
- The SeaweedFS Helm demo chart is single-node by choice; an existing cluster alone does not provide storage HA.
- Helm release history can contain rendered secret values with this demo script, so this script is unsuitable for strict production secret handling.
- The React build pulls npm dependencies, so lockfile + dependency audit should be checked in and scanned before release.
