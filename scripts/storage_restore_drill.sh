#!/usr/bin/env bash
# Restore drill: filer metadata AND object data, onto a throwaway compose
# project with a unique per-run name (hardening-drill-<timestamp>-<pid>),
# then a read-only SHA-256 scrub of the copy.
#
# Usage: scripts/storage_restore_drill.sh [source-compose-project]
#   source project defaults to "storzo". Requires: running source stack,
#   docker, host python3 with boto3, and the repo .env (source master keys).
#   The source project must not be in the reserved "hardening-drill*"
#   namespace; the drill refuses to reuse an existing project/volume name.
#
# What it does, in order:
#   1. Seeds a scratch bucket on the SOURCE with known random objects
#      (4 with sha256 metadata, 1 without).
#   2. Backs up filer metadata via scripts/storage_backup_meta.sh (live export).
#   3. Cold-backs up the storage volume: stop source storage, tar, start.
#   4. Starts a throwaway storage-only stack (unique drill project, port 28333).
#   5. Proves the metadata backup loads: fs.meta.load into the empty drill
#      filer, then checks the scratch bucket is listed (metadata only).
#   6. Wipes the DRILL volume, extracts the full cold tar, restarts, and runs
#      scripts/storage_scrub.py plus an independent byte-hash check of EVERY
#      seeded object (size + SHA-256, including the no-metadata fixture).
#   7. Cleans up: scratch bucket, drill project volumes, temp dir.
#
# Only the drill project's own volumes are ever deleted, and only after
# asserting the drill volume differs from the source volume and carries the
# drill project prefix. The source volume is mounted read-only for the tar
# and never removed. RPO/RTO numbers printed at the end are measured on this
# host only; see docs/RUNBOOK-storage.md.
set -euo pipefail

# Reserved drill namespace; per-run project names are DRILL_BASE-<ts>-<pid>.
DRILL_BASE=hardening-drill

# --- Guards and helpers (scripts/tests sources this file to call them) ---

# The source project must not sit in the drill namespace: a source named
# hardening-drill* would share compose volume names with the throwaway stack.
check_drill_project() {
  local project="$1"
  if [[ "$project" == "$DRILL_BASE"* ]]; then
    echo "ERROR: source project '$project' collides with the drill namespace '$DRILL_BASE'" >&2
    return 1
  fi
}

# The drill volume must be the drill project's own, never the source's.
# Called before every destructive step (wipe and teardown).
check_drill_volume() {
  local volume="$1" drill_project="$2" drill_volume="$3"
  if [[ -z "$drill_volume" || "$drill_volume" == "$volume" ]]; then
    echo "ERROR: drill volume '$drill_volume' is empty or equals source volume '$volume'; refusing" >&2
    return 1
  fi
  if [[ "$drill_volume" != "${drill_project}_"* ]]; then
    echo "ERROR: drill volume '$drill_volume' lacks drill project prefix '${drill_project}_'; refusing" >&2
    return 1
  fi
}

# Refuse to reuse an existing compose project or volume under the drill name.
drill_name_free() {
  local drill_project="$1"
  if [[ -n "$(docker ps -aq --filter "label=com.docker.compose.project=$drill_project")" ]]; then
    echo "ERROR: compose project '$drill_project' already exists" >&2
    return 1
  fi
  if docker volume ls --format '{{.Name}}' | grep -q "^${drill_project}_"; then
    echo "ERROR: a volume for drill project '$drill_project' already exists" >&2
    return 1
  fi
}

# Arm recovery BEFORE the stop request: a partially successful stop must
# still trigger the EXIT-trap restart (ordering per scripts/restore_integrity.py).
stop_source_storage() {
  SOURCE_STOPPED=1
  docker compose -p "$PROJECT" stop storage >/dev/null
}

cleanup() {
  local rc=$?
  if [[ "$SOURCE_STOPPED" == 1 ]]; then
    # Idempotent: compose start is a no-op if storage was never actually stopped.
    docker compose -p "$PROJECT" start storage >/dev/null 2>&1 || true
  fi
  if [[ "$DRILL_UP" == 1 && -f "$WORK/drill-compose.yaml" ]] \
    && check_drill_volume "$VOLUME" "$DRILL_PROJECT" "$DRILL_VOLUME"; then
    # Throwaway drill project only; never the source project's volumes.
    docker compose -p "$DRILL_PROJECT" -f "$WORK/drill-compose.yaml" down -v \
      >/dev/null 2>&1 || true
  fi
  if [[ "$SEEDED" == 1 && -f "$WORK/expected.json" ]]; then
    python3 - "$WORK/expected.json" <<'PY' >/dev/null 2>&1 || true
import json, sys
from pathlib import Path
sys.path.insert(0, str(Path('scripts').resolve()))
from storage_scrub import load_env, make_client
load_env('.env')
import os
exp = json.loads(Path(sys.argv[1]).read_text())
s = make_client('http://localhost:8333', os.environ['S3_ACCESS_KEY'], os.environ['S3_SECRET_KEY'])
for key in exp['objects']:
    s.delete_object(Bucket=exp['bucket'], Key=key)
s.delete_bucket(Bucket=exp['bucket'])
PY
  fi
  rm -rf "$WORK"
  exit "$rc"
}

# Sourced by tests: expose the functions above without running the drill.
if [[ "${BASH_SOURCE[0]}" != "$0" ]]; then
  return 0
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
PROJECT="${1:-storzo}"
DRILL_PROJECT="$DRILL_BASE-$(date -u +%Y%m%d%H%M%S)-$$"
DRILL_PORT=28333
check_drill_project "$PROJECT"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/storzo-drill-XXXXXX")"
SOURCE_STOPPED=0
DRILL_UP=0
SEEDED=0
VOLUME=""
DRILL_VOLUME=""
trap cleanup EXIT

command -v docker >/dev/null || { echo "ERROR: docker not found" >&2; exit 3; }
python3 -c 'import boto3' 2>/dev/null || { echo "ERROR: host python3 needs boto3" >&2; exit 3; }
[[ -f .env ]] || { echo "ERROR: .env missing; run python3 scripts/setup_env.py" >&2; exit 3; }
drill_name_free "$DRILL_PROJECT"
CID="$(docker compose -p "$PROJECT" ps -q storage)"
[[ -n "$CID" ]] || { echo "ERROR: source stack not running (project '$PROJECT')" >&2; exit 3; }
IMAGE="$(docker inspect -f '{{.Config.Image}}' "$CID")"
VOLUME="$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}' "$CID")"
[[ -n "$VOLUME" ]] || { echo "ERROR: cannot resolve source storage volume" >&2; exit 3; }

echo "== 1. Seed scratch data on source =="
python3 - "$WORK/expected.json" <<'PY'
import hashlib, json, os, secrets, sys
from pathlib import Path
sys.path.insert(0, str(Path('scripts').resolve()))
from storage_scrub import load_env, make_client
load_env('.env')
s = make_client('http://localhost:8333', os.environ['S3_ACCESS_KEY'], os.environ['S3_SECRET_KEY'])
bucket = 'storzo-drill-' + secrets.token_hex(5)
s.create_bucket(Bucket=bucket)
objects = {}
for i in range(4):
    data = secrets.token_bytes(1024 * 1024)
    digest = hashlib.sha256(data).hexdigest()
    key = f'drill/obj-{i}.bin'
    s.put_object(Bucket=bucket, Key=key, Body=data, Metadata={'sha256': digest})
    objects[key] = {'sha256': digest, 'size': len(data), 'bytes_sha256': digest}
data = secrets.token_bytes(64 * 1024)
s.put_object(Bucket=bucket, Key='drill/no-meta.bin', Body=data)
objects['drill/no-meta.bin'] = {'sha256': None, 'size': len(data),
                                'bytes_sha256': hashlib.sha256(data).hexdigest()}
Path(sys.argv[1]).write_text(json.dumps({'bucket': bucket, 'objects': objects}))
print(f'seeded {len(objects)} objects in {bucket}')
PY
SEEDED=1
BUCKET="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["bucket"])' "$WORK/expected.json")"

echo "== 2. Filer metadata backup (live export) =="
t=$SECONDS
bash scripts/storage_backup_meta.sh "$WORK/meta" "$PROJECT"
META_BACKUP_S=$((SECONDS - t))
META_DIR="$(find "$WORK/meta" -mindepth 1 -maxdepth 1 -type d | head -1)"

echo "== 3. Cold volume backup (source storage stopped during tar) =="
t=$SECONDS
stop_source_storage
docker run --rm -v "$VOLUME:/data:ro" -v "$WORK:/backup" alpine \
  tar cf /backup/storage.tar -C /data .
docker compose -p "$PROJECT" start storage >/dev/null
SOURCE_STOPPED=0
COLD_BACKUP_S=$((SECONDS - t))

echo "== 4. Throwaway drill stack (-p $DRILL_PROJECT) =="
DRILL_AK="$(python3 -c 'import secrets;print(secrets.token_hex(12))')"
DRILL_SK="$(python3 -c 'import secrets;print(secrets.token_hex(24))')"
DRILL_ADMIN="$(python3 -c 'import secrets;print(secrets.token_hex(12))')"
cat > "$WORK/drill-compose.yaml" <<YAML
services:
  storage:
    image: $IMAGE
    environment:
      AWS_ACCESS_KEY_ID: $DRILL_AK
      AWS_SECRET_ACCESS_KEY: $DRILL_SK
      WEED_ADMIN_PASSWORD: $DRILL_ADMIN
    volumes:
      - storage-data:/data
    ports:
      - "127.0.0.1:$DRILL_PORT:8333"
volumes:
  storage-data:
YAML
docker compose -p "$DRILL_PROJECT" -f "$WORK/drill-compose.yaml" up -d >/dev/null
DRILL_UP=1
DRILL_CID="$(docker compose -p "$DRILL_PROJECT" -f "$WORK/drill-compose.yaml" ps -q storage)"
DRILL_VOLUME="$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}' "$DRILL_CID")"
check_drill_volume "$VOLUME" "$DRILL_PROJECT" "$DRILL_VOLUME"

echo "== 5. Metadata-only restore check (fs.meta.load into empty filer) =="
docker compose -p "$DRILL_PROJECT" -f "$WORK/drill-compose.yaml" cp \
  "$META_DIR/filer-meta.save" storage:/tmp/restore.save
printf 'fs.meta.load /tmp/restore.save\n' \
  | docker compose -p "$DRILL_PROJECT" -f "$WORK/drill-compose.yaml" exec -T storage weed shell
S3_ACCESS_KEY="$DRILL_AK" S3_SECRET_KEY="$DRILL_SK" python3 - "$BUCKET" <<'PY'
import os, sys, time
sys.path.insert(0, 'scripts')
from storage_scrub import make_client
s = make_client('http://127.0.0.1:28333',
                os.environ['S3_ACCESS_KEY'], os.environ['S3_SECRET_KEY'])
for attempt in range(30):
    try:
        names = [b['Name'] for b in s.list_buckets().get('Buckets', [])]
        break
    except Exception:
        if attempt == 29:
            raise
        time.sleep(2)
assert sys.argv[1] in names, f'metadata restore: bucket {sys.argv[1]} not listed after fs.meta.load'
print('PASS: fs.meta.load restored filer metadata (bucket listed; data not yet restored)')
PY

echo "== 6. Full data restore + scrub =="
t=$SECONDS
docker compose -p "$DRILL_PROJECT" -f "$WORK/drill-compose.yaml" stop storage >/dev/null
# Wipe only the drill's own throwaway volume, then extract the cold backup.
check_drill_volume "$VOLUME" "$DRILL_PROJECT" "$DRILL_VOLUME"
docker run --rm -v "$DRILL_VOLUME:/data" -v "$WORK:/backup" alpine \
  sh -c 'find /data -mindepth 1 -delete && tar xf /backup/storage.tar -C /data'
docker compose -p "$DRILL_PROJECT" -f "$WORK/drill-compose.yaml" start storage >/dev/null
S3_ACCESS_KEY="$DRILL_AK" S3_SECRET_KEY="$DRILL_SK" python3 - <<'PY'
import os, sys, time
sys.path.insert(0, 'scripts')
from storage_scrub import make_client
s = make_client('http://127.0.0.1:28333', os.environ['S3_ACCESS_KEY'], os.environ['S3_SECRET_KEY'])
for attempt in range(30):
    try:
        s.list_buckets()
        break
    except Exception:
        if attempt == 29:
            raise RuntimeError('drill S3 did not recover within ~60 s')
        time.sleep(2)
print('drill S3 ready')
PY
RESTORE_S=$((SECONDS - t))

t=$SECONDS
S3_ACCESS_KEY="$DRILL_AK" S3_SECRET_KEY="$DRILL_SK" \
  python3 scripts/storage_scrub.py --endpoint "http://127.0.0.1:$DRILL_PORT" \
  --env-file /dev/null --state "$WORK/scrub-state.json"
SCRUB_S=$((SECONDS - t))

S3_ACCESS_KEY="$DRILL_AK" S3_SECRET_KEY="$DRILL_SK" python3 - "$WORK/expected.json" <<'PY'
import json, os, sys
sys.path.insert(0, 'scripts')
from storage_scrub import make_client, verify_expected
exp = json.loads(open(sys.argv[1]).read())
s = make_client('http://127.0.0.1:28333', os.environ['S3_ACCESS_KEY'], os.environ['S3_SECRET_KEY'])
verify_expected(s, exp)
print(f"PASS: independent size + byte-hash check on all {len(exp['objects'])} restored objects")
PY

echo "== 7. Measurements (this host, this dataset only) =="
echo "meta_backup_seconds=$META_BACKUP_S"
echo "cold_backup_seconds=$COLD_BACKUP_S (includes source storage stop/start)"
echo "restore_seconds=$RESTORE_S (wipe + extract + start + S3 ready)"
echo "scrub_seconds=$SCRUB_S"
echo "dataset: 5 objects, ~4.1 MiB, single node, same host/disk"
echo "DRILL PASS"
