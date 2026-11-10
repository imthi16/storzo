# Architecture and scope

## Data plane and control plane

```text
                 Web browser
                     |
                HTTP(S) 8088
                     |
           Storzo React/Nginx
                     |
           /api proxy to FastAPI
                     |
          +----------+-----------+
          |                      |
    SQLite audit              boto3 S3 SDK
    (persistent PVC)             |
                                  v
Existing application ----------> SeaweedFS S3 gateway :8333
  (boto3/AWS SDK)                 |
                                 v
                  SeaweedFS filer + volume store
                                 |
                   Local disk / Kubernetes PVC
```

**Important:** The existing application's object reads/writes bypass our FastAPI console entirely. This avoids turning the management API into the availability bottleneck for existing application traffic. It also means our SQLite audit log captures *console management actions only*, not every direct S3 object access. Cloud storage is not required.

## Runtime trust boundaries

1. The UI and backend share one origin via Nginx `/api/` reverse proxy. The backend has no externally exposed port by default.
2. Browser admin session cookie is HttpOnly, SameSite Strict; mutating API actions require a CSRF token. Session signing secret lives in `.env` or Kubernetes Secret. TLS with secure cookies must be configured before network exposure.
3. SeaweedFS S3 port is localhost-bound for Docker demo. Kubernetes S3 service is ClusterIP. S3 keys are used server-side by the admin backend; the UI does not receive the secret key.
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
- UI upload limit defaults to 50 MiB, separate from the native S3 gateway's multipart behavior.

`GET /api/dashboard` retains its existing totals and adds `buckets` (name, object_count, total_bytes; descending bytes), `by_type` (type, count, bytes; descending bytes), `largest` (up to five objects by descending size), and `recent` (up to eight by descending modification time). Object entries contain bucket, key, size and an ISO-8601 modified timestamp. Types are image, video, audio, document, archive or other, classified by lowercase file extension; only nonempty types appear. All fields reuse the same capped 3,000-object listing without additional S3 calls. Empty buckets and buckets not reached before the cap appear with zero counts/bytes; `sampled` and `message` retain their existing meaning, so these lists describe the scanned sample rather than full-storage rankings.

## Deployment components

- **`storage`:** upstream `chrislusf/seaweedfs` container running single-node `weed mini` and local data volume.
- **`backend`:** custom FastAPI Docker image, Python non-root UID 10001, persisted `/app/state/audit.db`.
- **`frontend`:** Vite/React compiled assets served by nginx on port 8080 as non-root UID 101, proxy to backend.
- **Kubernetes:** upstream SeaweedFS chart plus `deploy/helm/storzo` for backend/frontend/PVC/services and optional ingress.

## Next architectural expansion

If the team insists on an independent Rust engine later, do not start it before measuring this working baseline. A Rust engine requires a durable journal/atomic writes, fsync semantics, concurrent operations, metadata consistency, SigV4 canonicalization, ETags, multipart commit/abort, permissions, checksums, lifecycle and recovery tests. These are independent engineering projects, not just a `cargo build` exercise.
