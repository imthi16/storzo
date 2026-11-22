# Cloud Connect — Phase 1: one-time copy into Storzo

One-time copy from AWS S3 or any S3-compatible storage into an existing Storzo
bucket. This is **not** sync, not continuous replication, and does not delete
anything on the source. GCS and Azure Blob are **not supported yet**.

## API

All POST routes require login + CSRF (`require_write`); GET routes require login.

### `POST /api/migrations/estimate`

```json
{
  "source": {"endpoint": null, "region": "us-east-1", "bucket": "my-aws-bucket",
             "prefix": "photos/", "access_key_id": "AKIA...", "secret_access_key": "..."},
  "destination": {"bucket": "existing-storzo-bucket", "prefix": "imported/"}
}
```

`endpoint` is optional for AWS (omit/`null`); set it for S3-compatible stores.
Source and destination prefixes must be empty or end with `/` (for example
`photos/` or `logs/2026/`); both routes return `400` otherwise. rclone treats
a prefix as a directory path, so a partial name such as `logs/2026-` would
copy nothing, while a literal key-prefix estimate would count matching keys.
Returns (bounded scan, `MIGRATION_SCAN_LIMIT`, default 3000 objects, like the
dashboard scan):

```json
{"object_count": 120, "total_bytes": 5368709120, "partial": false, "scan_limit": 3000,
 "egress_usd": 0.45, "egress_price_per_gb_usd": 0.09,
 "price_note": "Estimate only: ... Prices change; check your provider. ..."}
```

No data is copied. The egress figure is a **dry-run estimate** at the
configurable rate `MIGRATION_EGRESS_USD_PER_GB` (default **$0.09/GB**, the AWS
internet-egress list price at time of writing). It is an estimate only; prices
change and same-region/same-provider transfers may cost less or nothing.

### `POST /api/migrations` → `201` job object

Starts the copy. `409` if a migration is already running (one at a time —
documented limit; the check and the queued-row insert are one atomic
reservation, so concurrent requests get exactly one `201`), `404`/`503` from
S3 checks if the destination bucket does not exist, and `503` with a clear
message if `rclone` is not available at runtime. Destination must be an
existing Storzo bucket. Cancelling a queued job prevents its copy from ever
launching. On backend startup, any job left in `queued`, `running` or
`verifying` by a restart is marked `failed` with the error
`interrupted by a backend restart` (and audited), so it no longer blocks
new migrations forever.

### `GET /api/migrations` · `GET /api/migrations/{id}`

Job shape:

```json
{"id": "…", "state": "queued|running|verifying|done|failed|cancelled",
 "verify_status": "verified|mismatched|failed|null",
 "source_bucket": "…", "source_prefix": "…", "source_endpoint": null, "source_region": "us-east-1",
 "dest_bucket": "…", "dest_prefix": "…",
 "bytes_done": 0, "objects_done": 0, "total_bytes": 0, "total_objects": 0,
 "errors": ["… (capped at 20)"], "created_at": "…", "updated_at": "…"}
```

Job state is stored in a new `migrations` table in the existing SQLite
database. **No secrets are stored** there, in the audit detail, or in logs.

### `POST /api/migrations/{id}/cancel`

Terminates the running `rclone` process; the job becomes `cancelled`.

Audit actions: `migration.start`, `migration.finish`, `migration.cancel`
(non-secret detail only: ids, buckets, prefixes, counts, verify status).

## How the copy runs

- Tool: **rclone**, subprocess, never a shell. Exact flags:
  - Copy: `rclone copy src:<bucket>[/<prefix>] dst:<bucket>[/<prefix>] --use-json-log --stats=1s --stats-one-line --stats-log-level NOTICE`
    (`--use-json-log` makes progress parseable; `--stats=1s` emits stats every
    second; `--stats-one-line` keeps each stat on one line; `--stats-log-level
    NOTICE` is required because rclone logs stats at `INFO` by default while
    its default log level is `NOTICE`, so stats would otherwise never be
    emitted — see the `--stats-log-level` section of the rclone docs).
    Progress
    (`bytes`/`transfers`/`totalBytes`/`totalTransfers`) is parsed from the
    JSON log lines on stderr and written to the job row.
  - Verify: `rclone check src:… dst:… --one-way --use-json-log` — exit 0 →
    `verified`, non-zero → `mismatched`; a failed copy or runner error →
    `failed`.
- Credentials are passed **only via environment**
  (`RCLONE_CONFIG_SRC_*` / `RCLONE_CONFIG_DST_*`, e.g.
  `RCLONE_CONFIG_SRC_ACCESS_KEY_ID`, `RCLONE_CONFIG_SRC_SECRET_ACCESS_KEY`,
  `RCLONE_CONFIG_DST_ENDPOINT`), never on the command line, never logged,
  never persisted. Source credentials live in memory for the job duration only.
- The destination remote points at Storzo's internal S3 endpoint with the
  backend's own S3 keys.

## SSRF guard

Source endpoints that resolve to private, loopback, link-local, reserved or
multicast addresses are refused (`400`). Set
`MIGRATION_ALLOW_PRIVATE_ENDPOINTS=true` only on trusted networks when copying
from a LAN S3-compatible store — and understand that this re-opens SSRF risk.

## rclone version pin

`backend/Dockerfile` pins **rclone v1.76.0** (`linux-amd64` zip) and verifies
SHA-256 `b00f00b3266c3764413afe599d3ec316f6f43b3e0ea8a0bc1d186d2133e46df7`,
taken from the official `SHA256SUMS` at
`https://downloads.rclone.org/v1.76.0/SHA256SUMS` (fetched and checked when
2026-11-23; no `curl | sh`). Bump version + checksum together. v1.68.2 was replaced because its binary was built with Go 1.23.3 and CI's Trivy scan reported 58 stdlib CVEs (2 critical, 56 high) in it; v1.76.0 is built with Go 1.27.3. The SHA256SUMS file and the zip come from the same host, so the pin proves integrity against a changed download, not against a compromised release.

## Environment

| Var | Default | Meaning |
|---|---|---|
| `MIGRATION_EGRESS_USD_PER_GB` | `0.09` | Estimate price per GB |
| `MIGRATION_SCAN_LIMIT` | `3000` | Estimate scan cap |
| `MIGRATION_ALLOW_PRIVATE_ENDPOINTS` | `false` | Allow private source endpoints |

## Limits / out of scope

One migration at a time; one-time copy only (no sync, no continuous mode, no
delete-on-source); GCS and Azure not supported yet; estimate is capped and may
be `partial`.
