#!/usr/bin/env bash
set -euo pipefail
umask 077

if [[ $# != 1 ]]; then
  printf 'Usage: %s <kubectl-context>\n' "$0" >&2
  exit 2
fi

deployment="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
private="$deployment/private"
client=${MINIO_ADMIN_CLIENT:-"$private/assets/silo/mcli"}
credentials="$private/minio"
kubectl=(kubectl --context "$1" -n omni)
if [[ ! -x "$client" ]]; then
  printf 'Set MINIO_ADMIN_CLIENT to the executable path of a trusted mcli.\n' >&2
  exit 1
fi

existing=$("${kubectl[@]}" get secret omnirelay-s3 --ignore-not-found -o name)
if [[ ! -s "$credentials/app.env" ]]; then
  if [[ -n "$existing" ]]; then
    printf 'The cluster has omnirelay-s3. Restore the target host credentials.\n' >&2
    exit 1
  fi
  printf 'OMNIRELAY_S3_ACCESS_KEY_ID=relay-%s\nOMNIRELAY_S3_SECRET_ACCESS_KEY=%s\n' \
    "$(openssl rand -hex 8)" "$(openssl rand -hex 32)" > "$credentials/app.env"
fi
source "$credentials/app.env"
source "$credentials/root.env"

port=${MINIO_VERIFY_PORT:-19000}
MC_CONFIG_DIR=$(mktemp -d "$credentials/mcli.XXXXXX")
forward_log=$(mktemp "$credentials/port-forward.XXXXXX")
"${kubectl[@]}" port-forward --address 127.0.0.1 service/minio "$port:9000" > "$forward_log" 2>&1 &
forward_pid=$!
cleanup() {
  kill "$forward_pid" 2>/dev/null || true
  wait "$forward_pid" 2>/dev/null || true
  unset MC_HOST_omni MC_HOST_app
  python3 -c 'import shutil,sys; shutil.rmtree(sys.argv[1])' "$MC_CONFIG_DIR"
  unlink "$forward_log"
}
trap cleanup EXIT

install -d -m 700 "$MC_CONFIG_DIR/certs/CAs"
install -m 600 "$credentials/ca.crt" "$MC_CONFIG_DIR/certs/CAs/omni-s3.crt"
export MC_CONFIG_DIR
export MC_RESOLVE="minio.omni.svc:$port=127.0.0.1"
export MC_HOST_omni="https://$MINIO_ROOT_USER:$MINIO_ROOT_PASSWORD@minio.omni.svc:$port"
export MC_HOST_app="https://$OMNIRELAY_S3_ACCESS_KEY_ID:$OMNIRELAY_S3_SECRET_ACCESS_KEY@minio.omni.svc:$port"
unset MINIO_ROOT_USER MINIO_ROOT_PASSWORD
ready=false
for ((try=0; try<25; try++)); do
  if curl -fsS --max-time 4 --noproxy '*' --cacert "$credentials/ca.crt" \
    --resolve "minio.omni.svc:$port:127.0.0.1" "https://minio.omni.svc:$port/minio/health/ready" >/dev/null 2>&1; then
    ready=true
    break
  fi
  if ! kill -0 "$forward_pid" 2>/dev/null; then
    cat "$forward_log" >&2
    exit 1
  fi
  sleep 1
done
if [[ "$ready" != true ]]; then
  printf 'The S3 readiness endpoint did not respond.\n' >&2
  exit 1
fi

"$client" mb --ignore-existing omni/omnirelay-files >/dev/null
"$client" admin policy create omni omnirelay-files-rw "$deployment/base/minio/app-policy.json" >/dev/null
if ! "$client" admin user info omni "$OMNIRELAY_S3_ACCESS_KEY_ID" >/dev/null 2>&1; then
  printf '%s\n%s\n' "$OMNIRELAY_S3_ACCESS_KEY_ID" "$OMNIRELAY_S3_SECRET_ACCESS_KEY" |
    "$client" admin user add omni >/dev/null
fi
"$client" admin policy attach omni omnirelay-files-rw --user "$OMNIRELAY_S3_ACCESS_KEY_ID" >/dev/null

key="deployment-check/$(openssl rand -hex 8)"
payload=$(openssl rand -hex 16)
printf '%s' "$payload" | "$client" pipe "app/omnirelay-files/$key" >/dev/null
test "$("$client" cat "app/omnirelay-files/$key")" = "$payload"
if "$client" admin info app >/dev/null 2>&1; then
  printf 'Application credentials unexpectedly have administrative access.\n' >&2
  exit 1
fi
"$client" rm "omni/omnirelay-files/$key" >/dev/null

"${kubectl[@]}" create secret generic omnirelay-s3 --from-env-file="$credentials/app.env" \
  --dry-run=client -o yaml | "${kubectl[@]}" apply --server-side --field-manager=omnirelay-deploy -f -
printf 'Bucket-scoped application credentials passed the write/read and admin-denial checks.\n'
