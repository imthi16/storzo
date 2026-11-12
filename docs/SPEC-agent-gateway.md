# SPEC: Agent gateway with scoped short-lived credentials and MCP (v1)

Status: proposed, in review via PR. Redaction scan clean; the Codex quality-score gate was not run.
Sources: `docs/R&D/HANDOFF.md`, `docs/R&D/PLAN-storage-direction.md` (both untracked in the main checkout, not on `main`).
Verified against `origin/main` @ 5fd15b9 on 2026-11-12.

## Decisions already made (do not re-litigate)

| # | Decision | Source |
|---|----------|--------|
| D1 | A Storzo-owned Go gateway sits in front of SeaweedFS. SeaweedFS S3 becomes internal-only. The gateway is the one place scope and expiry are enforced. | user, 2026-11-12 |
| D2 | v1 serves the owner's own agents. Single admin, no tenants. | user |
| D3 | v1 done bar: scoped, expiring (<= 60 min), 403 outside scope or after expiry, every issue and use audited. No revoke-before-expiry, no rate limits. | user |
| D4 | First agent feature is scoped credentials + MCP. Branches and dedupe come later. | user |
| D5 | Go for gateway, credential verification and MCP. Rust for the own-engine spike. S3 is the contract between gateway and storage. | user |
| D6 | The own-engine spike is NOT in this spec. It gets its own spec (see Out of Scope). | derived, needs your OK |

## Context

Today every application shares one master S3 key pair and talks to SeaweedFS on `:8333` directly (`README.md:26`, `docs/ARCHITECTURE.md:19`). Nothing can hand an agent "this bucket and prefix, read-only, for 15 minutes", because the only enforcement point is SeaweedFS itself and Storzo owns no code in the data path. The gateway creates that point. It is also the layer that later hosts branches, provenance and the engine swap, so the S3 contract must stay clean.

## Current state (verified)

- `backend/app/s3.py:10-21` builds boto3 clients from one `S3_ACCESS_KEY` / `S3_SECRET_KEY`. Console traffic only.
- `backend/app/security.py:32-40`: single admin, signed cookie, `require_write` checks CSRF. No API tokens exist.
- `backend/app/db.py:20-33`: SQLite audit table, WAL, one writer. Helm pins backend to 1 replica.
- `compose.yaml`: `storage` publishes `127.0.0.1:8333`; backend and frontend are internal. No gateway service.
- `backend/tests/test_api.py`: 20 tests, `FakeS3` swapped in for `s3.s3_client`.
- Live probe, 2026-11-12, `chrislusf/seaweedfs:4.53_full` `mini` on a throwaway container, boto3 1.44.1 over plain HTTP, path-style (evidence: live run, script not committed). Passed: signed PUT/GET; `x-amz-content-sha256: UNSIGNED-PAYLOAD` PUT/GET; 3-part (20 MB) multipart with UNSIGNED-PAYLOAD, SHA-256 of the readback equals the source; ListObjectsV2 with prefix; CopyObject (SeaweedFS supports it, the gateway still returns 501 in v1); default SDK checksum PUT (returned `x-amz-checksum-crc32`); wrong secret returns `SignatureDoesNotMatch`. NOT shown: that the SDK default PUT actually used `aws-chunked` framing on the wire, behaviour over HTTPS, or throughput through a proxy.

## Proposed change

```
agent / SDK / MCP client ──SigV4 (agent key + session token)──> gateway (Go, :8333)
                                                                   │ verify, check scope+exp, audit
                                                                   ▼ re-sign with master key
                                                          SeaweedFS S3 (internal only)

admin console ──cookie+CSRF──> FastAPI POST /api/credentials ──> returns key triple
```

### Credential format (stateless, no revocation state)

Claims, HMAC-SHA256 signed with `CRED_SIGNING_KEY` (>= 32 chars, startup fails closed like `SESSION_SECRET`):

```json
{"kid":"STZ<16 base32>","bucket":"agent-data","prefix":"run-42/","mode":"ro|rw","iat":"<unix seconds>","exp":"<iat + ttl_seconds>","jti":"<uuid>"}
```

- `access_key_id` = `kid`
- `secret_access_key` = hex(HMAC(CRED_SIGNING_KEY, "secret|" + kid))
- `session_token` = base64url(claims) + "." + base64url(HMAC(CRED_SIGNING_KEY, claims))
- Gateway recomputes the secret from `kid`, verifies SigV4, verifies the token signature and `exp` (60 s clock skew), then enforces scope.
- TTL: default 900 s, max 3600 s. Larger requests get 422.

### Scope rules (gateway)

| Request | `ro` | `rw` |
|---------|------|------|
| GET/HEAD object, key under prefix | allow | allow |
| ListObjectsV2 on bucket, `prefix` param starts with scope prefix | allow | allow |
| ListObjectsV2 with missing or out-of-scope `prefix` | 403 | 403 |
| PUT object, multipart create/upload/complete/abort, DELETE object (key under prefix) | 403 | allow |
| CopyObject (`x-amz-copy-source`) | 501 | 501 |
| Any bucket-level op (create/delete bucket, ACL, policy, versioning) | 403 | 403 |
| Other bucket than `claims.bucket` | 403 | 403 |
| Key fails `safe_key` rules (`..`, leading `/`, backslash, NUL, > 1024 bytes) | 400 | 400 |

Path-style addressing only. Expired token: 403 `ExpiredToken`. Bad signature: 403 `SignatureDoesNotMatch`. Error bodies are S3 XML so SDKs behave.

### API (FastAPI, admin only)

```
POST /api/credentials        require_write
  req : {"bucket":str,"prefix":str,"mode":"ro"|"rw","ttl_seconds":int=900,"label":str|null}
  res : 201 {"access_key_id","secret_access_key","session_token","expires_at","endpoint"}
  err : 400 invalid bucket/prefix (safe_bucket + safe_key rules), 404 bucket missing, 422 ttl out of range
  audit: action "credential.issue", bucket, key=prefix, detail="kid=... mode=... exp=..."
```

The secret is returned once and never stored. No list or revoke endpoint in v1.

### MCP server (Go, stdio transport only in v1)

Config: `STORZO_ENDPOINT`, `STORZO_ACCESS_KEY_ID`, `STORZO_SECRET_ACCESS_KEY`, `STORZO_SESSION_TOKEN`, `STORZO_BUCKET`, `STORZO_PREFIX`. It calls the gateway as an ordinary S3 client, so scope is enforced server-side, never in the MCP process.

| Tool | Maps to | Notes |
|------|---------|-------|
| `list_objects(prefix?, max_keys<=1000)` | ListObjectsV2 | defaults to scope prefix |
| `get_object(key, max_bytes=1048576)` | GetObject | text returned inline; larger or binary returns metadata + error `too_large` |
| `put_object(key, text)` | PutObject | only registered when mode is `rw` |
| `delete_object(key)` | DeleteObject | only registered when mode is `rw` |

### Audit

Gateway emits one event per request: `kid`, method, bucket, key, status, bytes, decision (`allow`/`deny:<reason>`). Issue events come from FastAPI via `db.audit()`.

OPEN (needs your call): SQLite is single-writer, so the gateway must not open `audit.db` directly. Recommendation: gateway POSTs batched events to `POST /api/internal/audit` on the backend, authenticated by `HMAC(CRED_SIGNING_KEY, body)`, with `frontend/nginx.conf` returning 404 for `/api/internal/`. Alternative: gateway-owned DB plus a read endpoint, which splits the console's activity view in two.

### Deploy

- `compose.yaml`: new `gateway` service published on `${S3_BIND_IP:-127.0.0.1}:8333`; `storage` stops publishing 8333 and is reachable only on the `internal` network. `S3_ENDPOINT` for the backend stays `http://storage:8333`.
- `deploy/helm/storzo`: new `gateway.yaml` template; `scripts/k8s_demo_install.sh` points the public S3 service at the gateway.
- Update `README.md` lines 26, 39, 101, `docs/ARCHITECTURE.md:19`, `CLAUDE.md` ("Apps talk to SeaweedFS directly"). Keep the "not a custom storage engine" line unchanged (HANDOFF rule).
- Master-key apps keep working by pointing at the gateway with the master credentials, which the gateway passes through with full access. (Decision needed: see Open questions 2.)

## Files reference

| File | Change |
|------|--------|
| `gateway/` (new) | Go module: SigV4 verify, token verify, scope, proxy, audit emit |
| `gateway/cmd/storzo-mcp/` (new) | MCP stdio server |
| `gateway/Dockerfile` (new) | distroless static binary |
| `backend/app/routers/credentials.py` (new) | `POST /api/credentials` |
| `backend/app/routers/internal.py` (new) | audit ingest, HMAC-authed |
| `backend/app/credentials.py` (new) | claims + token sign, shared test vectors |
| `backend/app/config.py` | `CRED_SIGNING_KEY` |
| `backend/app/main.py:15-26` | register routers, startup check for the key |
| `backend/tests/test_api.py` | +credential tests, `FakeS3` unchanged |
| `compose.yaml`, `frontend/nginx.conf`, `deploy/helm/storzo/templates/gateway.yaml`, `scripts/setup_env.py`, `.github/workflows/ci.yml` | wire gateway, generate key, add Go job |
| `README.md`, `docs/ARCHITECTURE.md`, `docs/SECURITY_CHECKLIST.md`, `CLAUDE.md` | doc updates |

## Acceptance criteria

1. `POST /api/credentials` without login returns 401; without `x-csrf-token` returns 403.
2. `ttl_seconds` 0, negative or > 3600 returns 422; omitted gives `expires_at` = now + 900 s (+/- 2 s).
3. boto3 using an `ro` key for `bucket/run-42/` reads `run-42/a.txt` with 200 and gets 403 on PUT of the same key.
4. The same key gets 403 for `run-43/a.txt`, for another bucket, for ListObjectsV2 without `prefix`, and for `prefix=run-4`.
5. `rw` key can PUT, multipart upload (>= 20 MB, 3 parts) and DELETE inside the prefix; CopyObject returns 501.
6. A token 1 s past `exp` + skew returns 403 `ExpiredToken`; a token with one byte changed returns 403.
7. Key `run-42/../run-43/a.txt` and `%2e%2e` variants return 400 and never reach SeaweedFS (proxy test asserts zero upstream calls).
8. Bucket-level calls (CreateBucket, DeleteBucket) return 403 for every agent key.
9. SeaweedFS is not reachable from the host on 8333 except through the gateway (`docker compose` port check).
10. Every issue and every gateway request (allowed or denied) creates one audit row visible in the console Activity page, including `kid`, decision and status.
11. MCP `put_object` and `delete_object` are absent from `tools/list` for an `ro` credential; calling the underlying S3 op with that credential still returns 403.
12. Each test in the Testing Plan states its card: `protects=<observable>; fails_when=<wrong result>; no_claim=<what green does not prove>`.
13. Existing 20 backend tests, frontend build and Playwright specs pass unchanged.

## Testing plan

| Layer | What | Count |
|-------|------|-------|
| Unit (Go) | token sign/verify, scope matrix (table above), key normalization | +25 |
| Unit (Python) | claims signing vectors identical to Go vectors, ttl bounds, authz | +6 |
| Integration | gateway against live SeaweedFS: boto3 read/write/list/multipart/403 cases | +8 |
| Integration | MCP stdio: tools/list per mode, get/put/list against gateway | +4 |
| E2E | compose up, issue credential via API, agent writes, console shows audit rows | +1 |
| Spike | live probe: SeaweedFS 4.53 accepts re-signed UNSIGNED-PAYLOAD and aws-chunked decode path | 1 |

Cross-language vectors are the key check: a token issued by Python must verify in Go.

## Risks and failure modes

- **SigV4 reimplementation.** A verification bug is a security bug. Mitigation: use a maintained Go SigV4 library for signing/verifying, test against boto3, aws-sdk-go-v2 and `aws-cli` presigned URLs; no hand-rolled canonicalization. Streaming `aws-chunked` bodies are the hard part: v1 requires chunk-signature verification, or v1 rejects `STREAMING-*` payloads with 501 and documents `UNSIGNED-PAYLOAD` / full-hash only. Pick one before build (Open question 3).
- **Latency and throughput.** Gateway proxies every byte. Not measured. Acceptance adds a baseline: record direct vs gateway throughput for a 100 MB PUT/GET; no pass/fail target in v1.
- **Compatibility gaps.** Unsupported ops return 501, never silently pass through.
- **Shared signing key leak** forges any scope until rotation; v1 has no revocation, so rotation (restart with a new key) is the kill switch. Document it in `docs/SECURITY_CHECKLIST.md`.
- **Audit volume.** One row per request can balloon `audit.db`. v1 keeps it; retention is a follow-up.

## Rollback

Gateway is additive. Revert the PR and restore the `storage` port mapping in `compose.yaml`; no data or schema migrates (the audit table is unchanged). Master-key apps repoint to SeaweedFS directly.

## Effort (per component)

| Component | Estimate |
|-----------|----------|
| Live SeaweedFS probe + spike | 0.5 d |
| Go gateway: verify, scope, proxy | 3 d |
| Python credentials + internal audit + nginx | 1 d |
| MCP server | 1 d |
| Tests (unit, integration, cross-language vectors) | 2 d |
| Compose, Helm, CI Go job, docs | 1 d |
| Total | ~8.5 d |

## Out of scope

- Own-engine spike (Rust): separate spec after this lands. It needs the gateway's S3 contract and the crash/fault harness first.
- Copy-on-write branches, tensor-aware dedupe, provenance chain.
- Revoke before expiry, per-key rate limits, multi-tenant users, credential list UI.
- MCP over HTTP, MCP resources/prompts.
- Cloud connect and migration (rclone job UI).
- SeaweedFS hardening (filer metadata backup, scrub, fsck).

## Open questions

Resolved 2026-11-12 by taking the recommended default because the user asked me to decide (change any of these before filing):

1. Audit transport: backend ingest endpoint `POST /api/internal/audit`, HMAC-authed, blocked at nginx. (SQLite stays single-writer.)
2. Master-key apps go through the gateway with pass-through. SeaweedFS stays internal-only.
3. `aws-chunked` streaming uploads: reject with 501 in v1 and document `UNSIGNED-PAYLOAD` / full-hash signing. Revisit once a real agent SDK needs it. The probe did not show how the default SDK frames the body, so this is the conservative choice.
4. No console Credentials form in v1. API only.

## Rules for the implementing agent

1. Acceptance criteria are the `protects / fails_when / no_claim` profile of each test. Never "make the tests pass".
2. Real code and real tests land in the same change; no stubs or deferred tests.
3. Any change to a test, validator, CI step, timeout, tolerance, threshold, snapshot or suppression is reported on its own line with justification, before other results.
4. A reported command names the command and its observed result; a command you did not run is not reported; stderr is never silenced in a command you cite.
5. Your report is a claim until the parent re-executes it: say what you verified, what you reused, and what you could not check.
