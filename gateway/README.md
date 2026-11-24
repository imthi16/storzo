# Storzo gateway

This module is the Storzo S3 gateway for scoped agent credentials.

- `cmd/storzo-gateway`: the proxy in front of SeaweedFS. It verifies a request, re-signs it with the master key and forwards it, and emits one audit event per request.
- `internal/verify`: token, SigV4 and scope verification (details below).
- `internal/proxy`: request rebuild and forwarding, the `x-amz-*` allowlist for scoped credentials, master pass-through.
- `internal/audit`: batched, HMAC-signed delivery of audit events to the backend.
- `cmd/storzo-mcp`: a stdio MCP server, a thin S3 client (scope is enforced by the gateway, not there).

Required environment for the gateway: `STORAGE_ENDPOINT`, `STORAGE_ACCESS_KEY`, `STORAGE_SECRET_KEY`, `CRED_SIGNING_KEY` (32+ characters) and `AUDIT_URL`; optional `GATEWAY_LISTEN` (default `:8333`) and `GATEWAY_MAX_HASHED_BODY_MB` (default 64). Set `GATEWAY_REVOCATION_POLL_INTERVAL` to a positive Go duration to override the 10 s polling interval. The gateway fetches a revocation snapshot at startup from the `revoked` sibling of `AUDIT_URL`, and retains its last successful snapshot when polling fails. Scoped keys in the snapshot get 403 `AccessDenied`. Set `GATEWAY_REVOCATION_MAX_STALE` to a positive Go duration to override the 60 s staleness bound: once the last successful snapshot is older than the bound (or none exists, e.g. a cold start with an unreachable backend), scoped requests fail closed with 503 `ServiceUnavailable` and an audit event until a poll succeeds again; master-key requests are unaffected. The gateway refuses to start unless the bound exceeds the poll interval plus the 3 s poll timeout. It fails at startup if any required value is missing. It runs as a Compose service and a Helm Deployment; see the repository README ("Agent access").

The rest of this file describes the verification library (`internal/verify`).

The Python credential issuer signs the **base64url claims segment**, not the
raw JSON bytes. Token verification follows that implementation and accepts
expiry through `exp + 60` seconds inclusive. Secrets are derived as lowercase
hex HMAC-SHA256 of `secret|kid`.

`SafeKey` matches the Python validator on decoded UTF-8 keys. Requests must
retain the original path-style URL: do not clean paths or decode them twice.
`Scope` operates on the decoded `net/url.URL.Path` and rejects unsupported
object and bucket subresources. Call scope enforcement only after authentication.

Regenerate the public test fixtures from the repository's real issuer and
botocore signer:

```sh
cd backend
python3 tests/gateway_vectors.py
cd ../gateway
go vet ./...
go test ./...
```

The fixture signing key is public synthetic test data. Fixture generation uses
random access keys/UUIDs and a fixed signing timestamp, so the JSON
is intentionally regenerated rather than a byte-for-byte reproducible snapshot.

`Verifier.Verify(request, now)` verifies token integrity/expiry, binds the token's
`kid` to SigV4 credentials, verifies the signature, and enforces scope. Errors are
`*verify.Error`; its `Write` method emits an S3 XML response. No request is sent
upstream. `Scope` is also exposed for isolated policy tests, and must not be used
as a substitute for `Verify` at an HTTP trust boundary.

SigV4 canonicalization and signing come from AWS SDK for Go v2's maintained
signer. Its public API does not expose verification: this module parses the
authentication envelope, re-signs a clone containing the declared signed headers,
and compares the resulting signature in constant time. It does not implement
canonicalization. S3 path escaping is configured explicitly, and presigning
header hoisting is disabled because the incoming signed-header selection is
already fixed. Unsupported signed-header combinations fail closed.

Both header auth and presigned URLs require a scoped session token. Header
signatures have a 15-minute validity window; presigns use their signed
`X-Amz-Expires` value (1–604800 seconds), always bounded by credential expiry.
The clock skew is 60 seconds. Expired credentials return `ExpiredToken` even
when a request signature is also stale.

`UNSIGNED-PAYLOAD` and complete SHA-256 payload hashes are supported. Scoped header-signed PUTs also support trailer-free `STREAMING-AWS4-HMAC-SHA256-PAYLOAD`
with `aws-chunked` content encoding. Hashed and signed chunked bodies are
verified in memory within the proxy's configured cap. The proxy re-signs
decoded uploads with the master key and the decoded payload hash and length.
Unsigned payloads intentionally provide no body integrity guarantee. Other
streaming variants and CopyObject return 501. The verifier requires the original
escaped path.

Tests cover the scope matrix, Python-issued credentials, botocore header and presigned signatures for GET/list/PUT, Go SDK signer requests, tampering (including MAC flips and claim rewrites under the original MAC), payload hashing, expiry boundaries and XML errors. The proxy and audit packages have their own tests against fake upstreams. The whole chain was also run against a live SeaweedFS with boto3 over plain HTTP and by `scripts/gateway_e2e.py` on Docker Compose; AWS CLI, HTTPS and load were not tested.
