# Storzo MVP release status (2026-11-09)

**State:** demo source candidate, not production approved.

- Management API: previous session reported 3/3 mocked integration tests passing. Re-run independently before demo.
- Frontend: source created; dependency installation/build was not verified in the preparation environment.
- Docker/Compose/Helm: configurations exist; actual build and Kubernetes startup not verified.
- Storage backend is **SeaweedFS**, not a new independently validated S3 storage engine.
- No GCS/AWS cloud storage dependency required.
- Do not publish private credentials or company data to public GitHub.
- Do not switch production MinIO traffic before backups, S3 compatibility, rollback, TLS, and recovery tests.

## Demo checklist

1. `python3 scripts/setup_env.py`
2. `docker compose up -d --build`
3. `docker compose ps`
4. Login to `http://localhost:8088` and upload test image.
5. Verify download bytes/checksum; test restart/persistence with `bash scripts/demo_verify.sh`.
6. Record test evidence and gaps for the TL; keep production storage unchanged.
