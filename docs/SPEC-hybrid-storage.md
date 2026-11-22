# SPEC (DRAFT): Hybrid storage — gateway layer, branches, credential vending, provenance

**Status: DRAFT — for decision, not an implementation order.** Drafted 2026-11-22 by OpenCode
(qwen4-max-preview) in worktree `docs/hybrid-spec-hardening`. Nothing in this document is
implemented except where it explicitly cites the already-shipped agent gateway.

Sources: `docs/R&D/HANDOFF.md` and `docs/R&D/PLAN-storage-direction.md` (untracked in the main
checkout), `docs/SPEC-agent-gateway.md` (implemented), `docs/research/dedupe-numbers-verified.md`
(this branch), and the shared four-branch API contract (revoke / issued-list / internal-revoked /
cloud-connect endpoints).

**Boundary that stays:** README/CLAUDE.md's "not a custom storage engine" line is unchanged.
The own-engine spike is **not approved in writing**; this draft contains only a Phase 0
conformance/fault-harness outline for it (§7), no engine design or implementation plan.

## 1. Decisions already made (do not re-litigate)

From HANDOFF and SPEC-agent-gateway (D1–D6): hybrid sequencing (agent features in a Storzo-owned
layer first, SeaweedFS keeps the bytes); S3 is the contract between gateway and storage; Go gateway
in front, SeaweedFS internal-only; single admin, owner's own agents first; scoped credentials + MCP
shipped as the first agent feature; Rust reserved for the (unapproved) engine spike; rclone for
cloud copy, `remote.mount` for connect mode — no custom transfer engine.

## 2. Shape of the hybrid

```
agents / SDKs / MCP ──SigV4──> Storzo gateway (Go)          [shipped: verify, scope, proxy, audit]
                                   │  + branch resolution, credential revocation, provenance hooks  [draft]
                                   ▼
                          storage interface (S3 subset, §3)
                                   ▼
                          SeaweedFS (pinned 4.53_full)      [bytes today; swappable later via §7 harness]
console ──cookie+CSRF──> FastAPI management plane            [shipped; extended per shared contract]
```

The console already speaks only boto3/S3 to storage — no SeaweedFS-specific calls leak into it
(verified by inspection of `backend/app/s3.py` and routers). The hybrid keeps that property and
makes it a tested contract (§3) so a future engine swap is a storage-interface change, not a
console rewrite.

## 3. Storage interface (the swap boundary)

The interface is the S3 operation subset the gateway and backend actually use, nothing more:

- Bucket: CreateBucket, DeleteBucket (empty-only enforced by Storzo), ListBuckets, HeadBucket.
- Object: PutObject, GetObject (range reads for thumbnails/presigned), HeadObject, DeleteObject,
  ListObjectsV2 (prefix/delimiter/pagination), CopyObject (501 in gateway v1; backend uses it for
  server-side copies — must be in the conformance set), multipart create/upload/complete/abort.
- Metadata: `x-amz-meta-*` round-trip (the console's `sha256` depends on it; note the gateway
  canonicalizes header case — see RUNBOOK §2), user metadata on HEAD/GET.
- Auth: SigV4 header and presigned, path-style; master pass-through plus scoped tokens.

**Conformance, not parity:** the contract is "the operations Storzo uses behave S3-compatibly",
checked by a test suite (§7 Phase 0), not full S3 parity. Versioning, object lock, ACLs and
lifecycle stay delegated/unvalidated as today (FEATURE_MATRIX).

## 4. Branch model for agents (design sketch — not implemented)

Goal: an agent can stage writes on a branch and the owner can merge or discard them atomically,
without copying object bytes (copy-on-write).

Sketch, consistent with the reports' agreement that "branch = metadata pointer":

- A **commit** is a Storzo-owned manifest: `{commit_id, parent, branch, entries: key → (content_hash,
  size, metadata), created_at, kid}`. Content hashes are SHA-256 (already recorded for console uploads).
- A **branch** is a mutable pointer to a commit plus a staging area. Staged writes land as ordinary
  objects under a reserved prefix (e.g. `.storzo/staging/<branch>/…`); a commit records their hashes
  and the branch pointer moves atomically (single SQLite writer — the backend already pins one replica).
- **Reads** on a branch resolve through the manifest; reads on `main` are today's path unchanged.
- **Merge** = apply a commit's entries to the target branch's manifest; conflicts (same key changed
  on both sides since the common parent) are surfaced, never auto-resolved.
- **GC is the biggest risk** (HANDOFF): a staged/committed content hash must be reference-counted
  across all branches and staging areas before any delete; GC only removes hashes with zero
  references, and never runs concurrently with a commit without a lock. This is the part most likely
  to lose data if rushed; it gets its own spec and fault tests before implementation.

Open design questions (need decisions before implementation): branch visibility to scoped
credentials (a `rw` credential scoped to `run-42/` — does it write to a branch or to main?),
manifest storage location (SQLite table vs filer objects), and whether branches are per-bucket or
global. Recommended default: per-bucket branches, credentials keep writing to main unless issued
with an explicit branch claim, manifests in the backend SQLite (single writer already enforced).

## 5. Credential vending (extends SPEC-agent-gateway, shared contract)

Issuing, scope matrix, MCP and audit are shipped. The agreed extensions (shared API contract;
implementation lives on the sibling revoke branch, this spec adopts the interface):

- **Revoke:** `POST /api/credentials/{kid}/revoke` (`require_write`) → `200 {"kid": "...",
  "revoked": true}`; `404` for unknown/malformed kid; audit action `credential.revoke`.
- **Issued list:** no new endpoint — the UI reads `GET /api/audit` rows with action
  `credential.issue` (detail `kid=… mode=… exp=…`, optional `label=…`).
- **Gateway revocation sync:** `GET /api/internal/revoked?since=<unix>`, HMAC-authenticated exactly
  like the audit emitter (`CRED_SIGNING_KEY` over `<timestamp>.<body>`), blocked at nginx like
  `/api/internal/audit`; returns `{"kids": [...], "now": <unix>}`. Gateway caches the revoked set
  and checks it after token verification. Until sync lands, the kill switch remains
  `CRED_SIGNING_KEY` rotation (documented in README).
- Residual risks unchanged from SPEC-agent-gateway: shared signing key forges any scope until
  rotation; audit is best-effort (bounded queue, no disk spool); revocation sync adds a polling
  window — a revoked kid may be accepted until the next poll (bounded staleness must be stated in
  the revoke branch's docs, not hidden).

## 6. Provenance chain (design sketch — not implemented)

Goal: tamper-evident history of "which credential wrote which content, on which branch, when".

- Every gateway write already produces an audit row (kid, method, bucket, key, status). Add the
  content SHA-256 to write events (the gateway already hashes bodies it verifies) so audit rows can
  be joined to content, not just keys.
- Commits (§4) carry `parent`, forming a hash-linked chain per branch; each commit id is
  `SHA-256(parent || manifest bytes)`. Altering any historical commit breaks every descendant id.
- **Honest limit:** hash-linking detects tampering only if some anchor is trusted. SQLite on the
  same host is not a trust anchor — an attacker with host access can rewrite the whole chain.
  Optional later: periodically publish chain heads somewhere append-only (off-host log, or a
  public transparency log). Until then, provenance is "tamper-evident against casual edits",
  not a security guarantee. Do not market it as the latter.

## 7. Own-engine spike — Phase 0 outline ONLY (spike not approved)

The user has not approved the own-engine spike in writing. This section defines **only** the
harness that would make a future spike decision evidence-based, runnable against SeaweedFS today
as the reference implementation. No engine language, layout or implementation is specified here
beyond what D5 already records (Rust reserved).

- **Conformance suite:** the §3 operation subset as executable tests (boto3 + raw SigV4 cases),
  run against the pinned SeaweedFS through the gateway and direct. Includes metadata round-trip
  (both header cases), multipart commit/abort, presigned GET/PUT, error codes (403/404/409/501).
- **Fault harness:** deterministic kill-9 during PUT/multipart-commit; torn-write simulation
  (truncate/partial flush of the data path where injectable); restart-and-verify (the existing
  `scripts/restart_integrity.py` is the seed); power-cut semantics explicitly listed as *not
  provable in containers* without block-device fault injection.
- **Durability checks:** SHA-256 readback after every fault case; the scrub (§RUNBOOK) as the
  steady-state checker; fsync-behaviour probes recorded as observations, not claims (HANDOFF:
  nothing published on SeaweedFS fsync/power-loss behaviour — measure, don't assume).
- **Exit criterion for even considering Phase 1:** the harness runs green against SeaweedFS and
  documents where SeaweedFS itself fails it. Those failures are the spike's real requirements list.
  Starting Phase 1 needs a separate, written user approval.

## 8. Cloud connect / migration (Phase 1, owned by another branch)

Per the shared contract, Muse defines the exact JSON in `docs/CLOUD_CONNECT.md`:
`POST /api/migrations/estimate`, `POST /api/migrations`, `GET /api/migrations`,
`GET /api/migrations/{id}`, `POST /api/migrations/{id}/cancel`. HANDOFF decisions stand: rclone
copy behind a Storzo job UI, `remote.mount` for connect mode, `rclone check --one-way` verification,
dry-run egress estimate. This draft adds no parallel design; the storage interface (§3) is the
only touch point (migration writes through the same S3 contract).

## 9. Open questions — recommended defaults (the user decides)

Each item: **Recommended default** with reasoning, then **Needs your decision**.

1. **Rust or Go for the engine spike?**
   Recommended default: **Rust**, keeping SPEC-agent-gateway D5 as recorded (user already chose
   Rust for the spike, Go for the gateway). Reasoning: the gateway decision is shipped and stable;
   reopening the language choice now buys nothing until the spike is approved, and the Phase 0
   harness (§7) is language-agnostic anyway.
   **Needs your decision:** confirm D5 stands, or change it before any spike spec is written.

2. **Single node or cluster first?**
   Recommended default: **single node**. Reasoning: both reports agree single-node-first; the
   provable claim is crash safety on one node; distribution multiplies the fault surface before
   the durability basics are evidenced; the current deployment is single-node anyway.
   **Needs your decision:** confirm single-node as the first target (cluster only after §7-style
   fault evidence exists).

3. **First agent feature: branches, scoped credentials + MCP, or dedupe?**
   Recommended default: **branches next** — scoped credentials + MCP are already shipped (D4),
   and dedupe is the weakest bet: verified numbers show chunk dedupe alone saves ~14.8% on the
   ZipLLM sample while the 54.1% result needs tensor-aware dedup **plus** delta compression
   (docs/research/dedupe-numbers-verified.md), a much larger build. Branches are metadata-only
   (§4) and directly serve agent workflows.
   **Needs your decision:** branches next, or a different first feature; and the §4 sub-questions
   (branch visibility, manifest location, per-bucket vs global).

4. **Self-host product for others, or learning/portfolio?**
   Recommended default: **treat the engine as a time-boxed learning spike until product demand is
   validated**; keep shipping the hybrid product meanwhile. Reasoning: the Gemini report's
   9–12-month disk-layer estimate is unverified but directionally consistent with the fault-test
   surface in ARCHITECTURE.md's expansion note; a from-scratch engine is only worth that cost if
   people will actually self-host it. The hybrid (gateway + branches + provenance) is valuable
   either way and does not depend on the answer.
   **Needs your decision:** product vs portfolio — this decides how much the own-engine spike is
   worth, and whether §7 ever proceeds past the harness.

5. **Data size, versioned buckets, which cloud first?**
   Recommended default: **AWS S3 first** for migration (largest rclone ecosystem, most
   S3-compatible targets derive from it), versioning **out of scope** until the storage interface
   conformance suite covers delete-marker semantics (README already warns about them).
   **Needs your decision:** your actual data size (drives whether the §3 subset and the cold-backup
   scheme are adequate at all), whether versioned buckets are required, and which cloud you
   actually use — the default is a guess without that.

## 10. Risks and what this draft does not claim

- GC under branches is the top data-loss risk (§4); no branch code should land without reference
  counting tests and a fault harness.
- Revocation polling leaves a bounded staleness window; rotation remains the kill switch.
- Provenance hash-chains are not a security boundary without an external anchor (§6).
- Dedupe savings for model files are workload-dependent; the verified numbers are one paper's
  sample, not a promise for Storzo workloads (images today, not LLM weights).
- This draft specifies no engine implementation, changes no README/CLAUDE.md boundary line, and
  claims nothing is built beyond the cited shipped gateway and this branch's hardening scripts.

## 11. Out of scope

Own-engine Phase 1+ (needs written approval); tensor-aware dedupe implementation; MCP over HTTP;
multi-tenant identity; rate limits; Kubernetes NetworkPolicy; the cloud-connect JSON itself
(Muse's `docs/CLOUD_CONNECT.md`); the revoke implementation (sibling branch — this spec only
adopts its contract).
