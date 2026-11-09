# Storzo

**Self-hosted, S3-compatible object and image storage platform for Docker and Kubernetes.**

**Status: MVP candidate — not production ready.**

Storzo is designed for teams that currently run MinIO on local Kubernetes storage and want a self-hosted, cloud-independent alternative. The current prototype integrates **SeaweedFS** for the S3-compatible object data plane, a **FastAPI** management backend, and a **React** administration console. It is not yet an independent Rust storage engine, nor a verified drop-in replacement for every MinIO feature.

## MVP scope
- Local storage on Docker volumes or Kubernetes PVCs; no required GCS or AWS S3 service.
- Bucket and object browsing, image upload/download and previews.
- Administration API, authentication and basic audit history.
- Docker Compose and starter Helm chart.
- Optional demo integrity and restart verification scripts.

## Verification status
Backend mock-S3 tests: **3 passed** in the preparation environment on November 9, 2026. Actual Docker build, SeaweedFS integration, frontend build, Kubernetes deployment, security audit, restore testing, and production S3 compatibility still require independent verification.

## Contributing
Use separate feature branches for Claude Code (backend), Meta Muse Code (frontend) and Codex (independent testing/review); merge to `main` only when tests pass. Do not commit secrets, `.env`, office data, or customer files.

**License:** Pending a deliberate selection and third-party license review. Do not assume this repository has an open-source license.

**Tagline:** Your Storage. Your Control.
