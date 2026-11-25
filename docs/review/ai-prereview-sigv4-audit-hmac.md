# Storzo gateway security pre-review

Source reviewed: `826c94aef819c8e13685469e563a35a10e425654` (`main`). This is a static, AI-assisted review of the verifier, proxy, and audit HMAC paths for human follow-up. Scenarios describe the trust boundary and likely consequence. Runtime behavior and deployment exposure were not tested.

## Medium — Unsigned S3 metadata is promoted to the upstream master signature

**Source:** `gateway/internal/verify/sigv4.go:166–198`, `gateway/internal/proxy/proxy.go:255–269`, `gateway/internal/proxy/proxy.go:195–202`.

Inbound verification rebuilds the signature from the declared signed headers. It keeps the original header map for forwarding. The scoped allowlist admits object metadata and checksum headers based on name alone, then the outbound signer signs the resulting request with the storage master credential. AWS S3 requires planned `x-amz-*` request headers in the canonical headers, with a specific exception for the payload hash already bound by the canonical request. [AWS S3 header authentication](https://docs.aws.amazon.com/AmazonS3/latest/developerguide/sig-v4-header-based-auth.html)

**Scenario.** A party with access to a still-valid signed upload can cause attacker-controlled custom metadata to reach storage even when the signature binds the file bytes. This affects the signed object's metadata integrity and any downstream process that trusts those fields. Method, path, and scoped prefix still constrain the request.

**Fix.** Require client signature coverage for each forwarded S3 control and user metadata field. Handle the verified payload hash and regenerated date as explicit gateway-derived values.

## Medium — Implicit hashing allocates request bodies before SigV4 verification

**Source:** `gateway/internal/verify/sigv4.go:120–140`, `gateway/internal/verify/sigv4.go:159–165`, `gateway/internal/verify/sigv4.go:236–238`, `gateway/internal/verify/sigv4.go:269–280`, `gateway/internal/proxy/proxy.go:227–251`, `compose.yaml:60`.

The implicit-hash path reads the full bounded body before validating SigV4. An active scoped token is enough to reach that read without proving possession of the corresponding SigV4 secret. The configured body cap is 64 MiB per request and the per-credential limiter runs after verification.

**Scenario.** A caller with a current scoped token and without its signing secret can tie up gateway memory with concurrent invalid uploads. A shared gateway under memory pressure can slow down or lose service for unrelated clients. The actual concurrency needed depends on infrastructure limits.

**Fix.** Require an explicit payload digest for body-bearing requests. Bound aggregate buffering and concurrent pre-authentication work independently of the per-request cap.

## Medium — Audit replay can duplicate authentic records

**Source:** `gateway/internal/audit/emitter.go:294–315`, `gateway/internal/audit/emitter.go:325–331`, `gateway/internal/audit/emitter.go:370–375`, `backend/app/routers/internal.py:45–58`, `backend/app/routers/internal.py:85–100`, `compose.yaml:59`, `frontend/nginx.conf:14`.

The MAC authenticates the raw body and exact timestamp text. The receiver accepts timestamps within ±300 seconds and appends every accepted event. The shipped Compose audit hop uses HTTP. Public nginx blocks the internal path, so attacker replay requires both access to a signed batch and backend reachability.

**Scenario.** An observer of the internal audit hop with backend access can cause one genuine operation to appear multiple times during an investigation. Retries can also duplicate a batch when the receiver writes the rows but the emitter loses the response.

**Fix.** Authenticate the internal transport. Include a stable batch identity in the signed body and enforce it transactionally with the event writes so retries are idempotent.

## Low — Client cookies can cross the gateway-to-storage boundary

**Source:** `gateway/internal/proxy/proxy.go:151–166`, `gateway/internal/proxy/proxy.go:195–202`, `gateway/internal/proxy/proxy.go:255–263`.

The rewrite removes the signing credentials and selected S3 headers but leaves ordinary request headers such as `Cookie` intact. The scoped filter only covers `x-amz-*`.

**Scenario.** In a same-host deployment, a browser making a signed S3 request can carry a cookie intended for another service on that host. The storage service receives the cookie and storage-side logging or instrumentation could retain it. Impact depends on cookie scope and which clients share a hostname.

**Fix.** Strip cookies and unrelated sensitive request headers before re-signing. Preserve the HTTP headers needed for supported S3 operations.

## Human validation priorities

- **Path and query interpretation.** Scope uses the decoded path and Go-parsed query. The proxy retains escaped paths and the raw spelling of surviving query components across AWS SDK re-signing. Confirm that the deployed SeaweedFS version resolves the same bucket, key, and operation for encoded separators, literal and encoded plus signs, empty query values, and multipart parameters. A mismatch could cause rejection or change the target that storage uses. Source alone did not establish a scope escape. `gateway/internal/verify/query.go:8–18`, `gateway/internal/verify/scope.go:31–71`, `gateway/internal/proxy/proxy.go:167–177`, `gateway/internal/proxy/proxy.go:195–202`. [AWS S3 query authentication](https://docs.aws.amazon.com/AmazonS3/latest/developerguide/sigv4-query-string-auth.html)
- **Revocation transport.** Compose configures HTTP and the poller installs any structurally valid successful snapshot. Where an intermediary can alter responses, an omitted revoked identity could continue operating within its original scope. Validate actual transport isolation and authentication. The feed's request MAC also omits method, path, and query, so bind those fields when revising the internal HMAC protocol. `compose.yaml:59`, `gateway/internal/audit/revocations.go:71–108`, `backend/app/routers/internal.py:103–110`.
- **Audit redirects and trailers.** The emitter's default HTTP client follows redirects, which can expose audit data or authentication headers to a redirected destination. Confirm destination controls and disable cross-origin redirects. The proxy allowlist covers the initial header map. Confirm the wire-level treatment of ordinary HTTP trailers and whether SeaweedFS uses them as operation controls. `gateway/internal/audit/emitter.go:112–115`, `gateway/internal/audit/emitter.go:325–331`, `gateway/internal/verify/sigv4.go:150–155`, `gateway/internal/proxy/proxy.go:255–263`.

## Other reviewed controls

- Token, SigV4, payload, signed chunk, and audit MAC checks use `hmac.Equal` or `hmac.compare_digest`. The ordinary string comparison in the SigV4 path checks the public scope and signed-header list. `gateway/internal/verify/token.go:61–78`, `gateway/internal/verify/sigv4.go:226–246`, `gateway/internal/verify/sigv4.go:286–289`, `gateway/internal/verify/chunked.go:87–88`, `backend/app/routers/internal.py:85–87`.
- `Claims.Master` is excluded from token JSON. Master selection requires a tokenless matching access key, and the flag is assigned after successful SigV4 verification and any required body hash check. The proxy returns on verifier errors before forwarding. The scoped matrix checks bucket and prefix and limits writes and multipart operations to `rw`. `gateway/internal/verify/token.go:20–29`, `gateway/internal/verify/sigv4.go:120–133`, `gateway/internal/verify/sigv4.go:236–255`, `gateway/internal/proxy/proxy.go:230–242`, `gateway/internal/verify/scope.go:25–101`.
- Header signatures are reusable for 15 minutes plus 60 seconds after signing. Presigns allow a declared lifetime up to seven days plus 60 seconds, subject to scoped token expiry plus its own 60-second grace and to the gateway's revocation snapshot. Treat still-valid presigned URLs as bearer capabilities. With `UNSIGNED-PAYLOAD`, possession authorizes the operation without binding the body bytes. This is a supported SigV4 mode. `gateway/internal/verify/sigv4.go:24–25`, `gateway/internal/verify/sigv4.go:60–64`, `gateway/internal/verify/sigv4.go:135–159`, `gateway/internal/verify/token.go:15`, `gateway/internal/verify/token.go:78–84`, `gateway/internal/proxy/proxy.go:243–246`. [AWS S3 payload signing policy](https://docs.aws.amazon.com/AmazonS3/latest/developerguide/bucket-policy-s3-sigv4-conditions.html)
