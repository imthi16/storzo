#!/usr/bin/env bash
# One-shot SeaweedFS filer metadata backup with a SHA-256 manifest.
#
# Uses `weed shell` -> `fs.meta.save`, verified against the pinned image
# chrislusf/seaweedfs:4.53_full (see docs/RUNBOOK-storage.md for how).
# `weed filer.meta.backup` is NOT used here: it is a continuous replication
# command into a second filer store, not a one-shot file export.
#
# Usage: scripts/storage_backup_meta.sh <backup-dir> [compose-project]
#   compose-project defaults to "storzo" (the name in compose.yaml).
#
# The export is taken from the LIVE filer (it must be running). It is an
# approximate point-in-time snapshot: writes during the export may or may
# not be included. For a consistent cold copy, stop writes first; the
# restore drill pairs this export with a stopped-storage volume tar.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if [[ $# -lt 1 || $# -gt 2 ]]; then
  echo "Usage: $0 <backup-dir> [compose-project]" >&2
  exit 3
fi
BACKUP_DIR="$1"
PROJECT="${2:-storzo}"

CID="$(docker compose -p "$PROJECT" ps -q storage)"
if [[ -z "$CID" ]]; then
  echo "ERROR: no running 'storage' container in compose project '$PROJECT'" >&2
  exit 3
fi

DEST="$BACKUP_DIR/filer-meta-$(date -u +%Y%m%d-%H%M%S)"
mkdir -p "$DEST"

REMOTE_TMP=/tmp/storzo-filer-meta.save
OUT="$(printf 'fs.meta.save -o %s\n' "$REMOTE_TMP" \
  | docker compose -p "$PROJECT" exec -T storage weed shell 2>&1)"
if ! grep -q 'is saved to' <<<"$OUT"; then
  echo "ERROR: fs.meta.save did not confirm a save:" >&2
  echo "$OUT" >&2
  exit 1
fi
docker compose -p "$PROJECT" cp "storage:$REMOTE_TMP" "$DEST/filer-meta.save"
docker compose -p "$PROJECT" exec -T storage rm -f "$REMOTE_TMP"

{
  echo "utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "compose_project=$PROJECT"
  echo "image=$(docker inspect -f '{{.Config.Image}}' "$CID")"
  echo "weed_version=$(docker compose -p "$PROJECT" exec -T storage weed version 2>/dev/null | head -1)"
  echo "command=weed shell fs.meta.save -o $REMOTE_TMP (live filer export)"
} > "$DEST/info.txt"

(cd "$DEST" && sha256sum filer-meta.save info.txt > SHA256SUMS && sha256sum -c SHA256SUMS)
echo "OK: filer metadata backup at $DEST (manifest verified)"
