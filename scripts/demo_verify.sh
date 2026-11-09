#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
command -v docker >/dev/null || { echo 'Docker not installed'; exit 1; }
command -v python3 >/dev/null || { echo 'Python3 not installed'; exit 1; }
python3 scripts/setup_env.py

echo '=== Building and launching the local stack ==='
docker compose up -d --build

echo '=== Waiting for storage readiness ==='
ready=0
for i in $(seq 1 45); do
  if curl -fsS --max-time 2 http://localhost:8088/api/health/ready > /dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 3
done
if [[ "$ready" -ne 1 ]]; then
  echo 'FAIL: stack did not become ready; inspect docker compose logs --tail=150'
  docker compose ps
  exit 1
fi
curl -fsS http://localhost:8088/api/health/ready

echo '=== Checking Python boto3 availability ==='
python3 -c 'import boto3' || { echo 'Install boto3: python3 -m pip install boto3'; exit 1; }

echo '=== Native S3 SDK smoke tests ==='
python3 scripts/smoke_test.py

echo '=== Actual storage restart and checksum verification ==='
python3 scripts/restart_integrity.py

echo '=== Cold backup and restore-to-fresh-volume verification ==='
python3 scripts/restore_integrity.py

echo '=== All demo checks passed ==='
echo 'Open http://localhost:8088 and sign in with the admin password from .env'
