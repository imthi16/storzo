#!/usr/bin/env bash
set -euo pipefail
# Run after a successful smoke test and after manually uploading a persistent test file via UI.
# Never use docker compose down -v: that command DELETES the persistent volumes.
docker compose restart storage backend frontend
printf 'Containers restarted. Verify your test image is still present at http://localhost:8088\n'
printf 'Then run: python3 scripts/smoke_test.py\n'
