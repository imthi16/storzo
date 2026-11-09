#!/usr/bin/env bash
set -euo pipefail
# Demo/bootstrap only. Do NOT use on existing production storage releases.
cd "$(dirname "$0")/.."
if [[ ! -f .env ]]; then echo 'Run python3 scripts/setup_env.py first'; exit 1; fi
if [[ -z "${BACKEND_IMAGE:-}" || -z "${FRONTEND_IMAGE:-}" ]]; then
  echo 'Set BACKEND_IMAGE and FRONTEND_IMAGE to images reachable by your cluster'; exit 1
fi
set -a
source .env
set +a
NAMESPACE="${NAMESPACE:-storzo-demo}"
TMP_VALUES="$(mktemp)"
TMP_SECRET="$(mktemp)"
trap 'rm -f "$TMP_VALUES" "$TMP_SECRET"' EXIT
chmod 600 "$TMP_VALUES" "$TMP_SECRET"
python3 - "$TMP_VALUES" <<'PY'
import os, pathlib, sys
text=pathlib.Path('deploy/seaweedfs-values-demo.yaml').read_text()
text=text.replace('__ACCESS_KEY__', os.environ['S3_ACCESS_KEY']).replace('__SECRET_KEY__', os.environ['S3_SECRET_KEY'])
pathlib.Path(sys.argv[1]).write_text(text)
PY
printf 'ADMIN_PASSWORD=%s\nSESSION_SECRET=%s\nS3_ACCESS_KEY=%s\nS3_SECRET_KEY=%s\n' \
  "$ADMIN_PASSWORD" "$SESSION_SECRET" "$S3_ACCESS_KEY" "$S3_SECRET_KEY" > "$TMP_SECRET"
kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -
kubectl -n "$NAMESPACE" create secret generic storzo-credentials --from-env-file="$TMP_SECRET" --dry-run=client -o yaml | kubectl apply -f -
helm repo add seaweedfs https://seaweedfs.github.io/seaweedfs/helm --force-update
helm repo update seaweedfs
helm upgrade --install seaweedfs seaweedfs/seaweedfs -n "$NAMESPACE" -f "$TMP_VALUES" --wait --timeout 10m
helm upgrade --install storzo deploy/helm/storzo -n "$NAMESPACE" \
  --set backend.image="$BACKEND_IMAGE" --set frontend.image="$FRONTEND_IMAGE" \
  --set backend.s3Endpoint=http://seaweedfs-s3:8333 \
  --wait --timeout 5m
printf '\nDEMO only. Helm release history contains SeaweedFS S3 credentials; protect namespace RBAC.\n'
printf 'Open console with: kubectl -n %s port-forward svc/storzo-storzo-frontend 8088:80\n' "$NAMESPACE"
printf 'S3 signed URLs require an externally reachable S3 endpoint, configured separately.\n'
