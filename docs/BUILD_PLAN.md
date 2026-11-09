# Development handoff for Claude Code, OpenAI Codex and Meta Muse Code

The archive contains a runnable-source MVP, **not** a completed or validated company deployment. Use three agents in separate Git branches/worktrees; avoid simultaneous edits to the same files. Merge only after tests pass. Do not touch the company's existing production MinIO deployment.

## Responsibilities

| Agent | Branch | Files and mission |
|---|---|---|
| Claude Code | `feat/storage-hardening` | Audit backend S3 API, fail-closed auth, exception mapping, lifecycle/multipart compatibility, SeaweedFS config and PVC health |
| Meta Muse Code | `feat/console-polish` | Frontend build, UX, accessibility, upload progress, responsive bug fixes, API integration |
| OpenAI Codex | `test/end-to-end` | Docker/Compose smoke testing, security adversarial tests, S3 client compatibility, restart integrity, docs and CI repair |

Do a coordinated merge to `integration` and tag only after the readiness list passes. Production approval is a separate step.

## Claude Code paste prompt

```
Inspect this Storzo repository. Today we must demonstrate an honest, working local S3 image-storage product using existing Docker/Kubernetes; no GCS or AWS required. SeaweedFS is the delegated S3 core, Storzo is a custom control plane. Do NOT claim full S3 feature parity or durable HA until tested. You own only backend/, scripts/ where necessary, and backend tests. First run tests, review the FastAPI API for incorrect S3 error handling, file upload boundary enforcement, concurrency, session security, browser presign behavior, server restart, and storage credential config. Fix real bugs, add tests, document any unproven issues, and produce a PR. Never modify production storage or commit .env.
```

## Muse Code paste prompt

```
Open this Storzo repository. Own only frontend/ and frontend build test/workflow additions. Run npm install and npm run build, fix TypeScript/build issues, make the React admin UI fully usable: authentication, dashboard, bucket list, prefix navigation, upload (including failure feedback), image previews, presigned link copy, download/delete, audit and responsive layout. Keep the backend API contract unchanged unless discussing with the backend agent. Test with a running local Docker Compose stack and a browser. Provide screenshots and a PR; do not modify or expose S3 credentials.
```

## Codex paste prompt

```
Act as an independent QA/security reviewer for Storzo. Work on your own test/CI branch. Run backend pytest, React npm build, docker compose config, real docker compose up -d --build, and scripts/smoke_test.py. Test upload/download integrity and persistence across container restart with the same object. Review login CSRF, signed URL behavior, path/key validation, oversized files, file metadata, bucket deletion, S3 credential exposure, data loss from `docker compose down -v`, and K8s chart template logic (helm lint/template). Compare the S3 operations used by the company's actual app against SeaweedFS; do not assume parity. Add failing tests for defects, fix carefully, and deliver a pass/fail checklist. Never touch existing production MinIO.
```

## Priority order

1. **P0** Run backend tests and React build, repair build errors.
2. **P0** Docker Compose launch: all services running, backend readiness and S3 direct SDK smoke test pass.
3. **P0** UI upload JPG/PNG -> browse -> preview -> download -> compare SHA-256; restart storage -> same object persists.
4. **P0** Verify all secrets remain private; no public demo exposure; stage isolated from office production.
5. **P1** Helm lint/template and staging cluster deployment; verify mounted PVC and port-forwards.
6. **P1** Add an automated browser test and signed URL validation for both Docker and K8s.
7. **P2** Production requirements: HA, off-host backup/restore, enterprise auth, quotas and audited SDK parity.

## Git workflow

```bash
git init
git add .
git commit -m 'Initial Storzo self-hosted storage MVP'
# Use separate worktrees/branches for agents. Each agent opens a PR.
```

The `.gitignore` excludes `.env`, DB files and generated build artifacts. Do not copy `.env` into AI model prompts, screenshots, logs or pull requests.
