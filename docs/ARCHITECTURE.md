# Architecture and scope

## Data plane and control plane

```text
 Web browser                                Application / AI agent (S3 SDK)
      |                                                |
 HTTP(S) 8088                                      HTTP 8333
      |                                                |
 Storzo React/nginx                           Storzo gateway (Go)
      |                                  verify token + SigV4 + scope,
 /api proxy to FastAPI                   re-sign with master key, forward
      |        ^                                |             |
      |        +--- POST /api/internal/audit ---+             |
      |             (HMAC, batched)                           |
 SQLite audit                                                 v
      |                                     SeaweedFS S3 (internal network only)
 boto3 S3 SDK (server-side) ------------------------>         |
                                                  SeaweedFS filer + volume store
                                                              |
                                                  Local disk / Kubernetes PVC
```

**Important:** Application and agent S3 traffic goes through the gateway, not through the FastAPI console, so the management API is not on the data path. Gateway requests, allowed and denied, are recorded in the same SQLite audit trail as console actions. Audit delivery is best effort (a 10,000-event queue, 4 attempts per batch, no disk spool), so a long backend outage can lose rows. Cloud storage is not required.

## Runtime trust boundaries

1. The UI and backend share one origin via Nginx `/api/` reverse proxy. The backend has no externally exposed port by default.
2. Browser admin session cookie is HttpOnly, SameSite Strict; mutating API actions require a CSRF token. Session signing secret lives in `.env` or Kubernetes Secret. TLS with secure cookies must be configured before network exposure.
3. In Compose, SeaweedFS publishes no host port and the gateway publishes `127.0.0.1:8333`. In Kubernetes the gateway and SeaweedFS Services are ClusterIP and the demo installs no NetworkPolicy, so another workload in the cluster may reach SeaweedFS (and its filer) directly and bypass the gateway; this is unverified. The master S3 keys are used server-side by the backend and the gateway; the UI does not receive the secret key.
4. Presigned URLs are intentionally bearer links. Whoever receives one can retrieve the object until expiry. For Kubernetes, signed links require a reachable S3 hostname (local port-forward in demo).
5. SeaweedFS serves S3 protocol behavior. Storzo's dashboard does not implement all S3 requests; storage API feature parity is a separate upstream/version validation concern.

## Durability

- The Compose `storage-data` volume is persisted between restarts. The Kubernetes example uses PersistentVolumeClaims.
- **A PVC alone is not a backup.** Single-node image data can still be lost to server/volume failure. Use independent off-host or separate-site backups and test restores.
- For production-sized multi-node storage, design SeaweedFS master/volume/filer replicas and placement based on physical failure domains, disk performance, and existing StorageClass semantics. Do not confuse replication across pods sharing one failed node/disk with real redundancy.
- SHA-256 on UI uploads provides comparison metadata but does not itself prove underlying crash safety. Run controlled failure/restart tests and hash verification on reads.

## Scaling limits

- Backend includes one local SQLite audit writer; Helm uses one replica and `Recreate` rollout.
- Dashboard scans at most 3,000 objects per request; for production metrics use background indexing/Prometheus and never scan every object on dashboard load.
- Thumbnail generation is synchronous and limited to image files <= 12 MiB; production requires bounded worker queues, caching, decompression safeguards, MIME verification, limits, and safe SVG handling.
- UI upload limit defaults to 50 MiB, separate from the S3 data plane's multipart behavior (the gateway buffers a hashed body up to `GATEWAY_MAX_HASHED_BODY_MB`, default 64; `UNSIGNED-PAYLOAD` streams).

`GET /api/dashboard` retains its existing totals and adds `buckets` (name, object_count, total_bytes; descending bytes), `by_type` (type, count, bytes; descending bytes), `largest` (up to five objects by descending size), and `recent` (up to eight by descending modification time). Object entries contain bucket, key, size and an ISO-8601 modified timestamp. Types are image, video, audio, document, archive or other, classified by lowercase file extension; only nonempty types appear. All fields reuse the same capped 3,000-object listing without additional S3 calls. Empty buckets and buckets not reached before the cap appear with zero counts/bytes; `sampled` and `message` retain their existing meaning, so these lists describe the scanned sample rather than full-storage rankings.

## Agent gateway

Scoped, short-lived credentials for the owner's own AI agents. Design, decisions and acceptance criteria: [`SPEC-agent-gateway.md`](SPEC-agent-gateway.md).

- **Issuing:** `POST /api/credentials` (admin session and CSRF) signs a token `{kid, bucket, prefix, mode, iat, exp, jti}` with `CRED_SIGNING_KEY` and derives the secret from the `kid`. The token is the S3 session token; nothing is stored, so there is no revoke before expiry.
- **Request path:** `storzo-gateway` parses the SigV4 request (header or presigned), verifies the token MAC and expiry, recomputes the signature with the derived secret, applies the scope matrix, and only then forwards to SeaweedFS signed with the master key (UNSIGNED-PAYLOAD stays unsigned; a verified body hash is forwarded). Denied requests make no upstream call. Master-key clients are recognised by their access key and no session token, verified against the master secret, and passed through without scope checks.
- **What reaches storage:** for scoped credentials only `x-amz-meta-*`, checksum headers and the verified payload hash. For everyone, ACL and grant headers are removed.
- **Audit:** the gateway sends one event per request to `POST /api/internal/audit` over HTTP inside the compose network, signed with HMAC-SHA256 over `<unix timestamp>.<body>` and accepted within 300 s. The endpoint is blocked at nginx, authenticates before reading the body, and caps it at 4 MiB. The gateway never opens `audit.db` (SQLite has one writer). The audit queue is in memory. The console reads the rows back with `GET /api/audit?source=all|gateway|console&decision=allow|deny` (validated, parameterized SQL) and charts uploads with `GET /api/audit/uploads-by-day`, whose items carry `count`, `console` and `gateway`. A gateway write is a `gateway.request` row with `method=PUT` and a 2xx status. The `kid` is taken only from claims the gateway fully authenticated (token MAC, expiry and SigV4 signature), so it is recorded for allowed requests and for authenticated denials such as an out-of-scope request; a request that fails authentication (wrong secret, forged or expired token) is stored with an empty `kid`.
- **Trust:** `CRED_SIGNING_KEY` forges any scope until rotated. The backend must not be reachable from outside the compose network.
- **Not covered:** aws-chunked streaming (501), CopyObject (501), revoke, rate limits, multi-tenant users, a console credentials form.

## Deployment components

- **`storage`:** upstream `chrislusf/seaweedfs` container running single-node `weed mini` and local data volume.
- **`gateway`:** Go proxy (`gateway/`), static binary in a distroless non-root image (UID 65532), listens on 8333; published on `127.0.0.1:8333` in Compose, ClusterIP Service in Helm.
- **`backend`:** custom FastAPI Docker image, Python non-root UID 10001, persisted `/app/state/audit.db`.
- **`frontend`:** Vite/React compiled assets served by nginx on port 8080 as non-root UID 101, proxy to backend.
- **Kubernetes:** upstream SeaweedFS chart plus `deploy/helm/storzo` for backend/frontend/PVC/services and optional ingress.

## Next architectural expansion

If the team insists on an independent Rust engine later, do not start it before measuring this working baseline. A Rust engine requires a durable journal/atomic writes, fsync semantics, concurrent operations, metadata consistency, SigV4 canonicalization, ETags, multipart commit/abort, permissions, checksums, lifecycle and recovery tests. These are independent engineering projects, not just a `cargo build` exercise.
