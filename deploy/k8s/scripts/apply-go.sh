#!/usr/bin/env bash
set -euo pipefail
umask 077
[[ $# == 2 ]] || { printf 'Usage: %s <kubectl-context> <go-image>\n' "$0" >&2; exit 2; }
root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
kubectl=(kubectl --context "$1" -n omni)
image="$2"
[[ "$image" =~ ^[a-zA-Z0-9./:_@-]+$ ]] || exit 2

source "$root/private/route.env"
[[ "$OMNI_K8S_WEB_NODEPORT" =~ ^[0-9]{1,5}$ ]] || exit 2
setup=true
if [[ -s "$root/private/feishu.env" ]]; then
  "${kubectl[@]}" create secret generic omnirelay-feishu --from-env-file="$root/private/feishu.env" \
    --dry-run=client -o yaml | "${kubectl[@]}" apply --server-side --field-manager=omnirelay-deploy -f -
  setup=false
fi
"${kubectl[@]}" create configmap omnirelay-runtime \
  --from-literal="OMNIRELAY_PUBLIC_ORIGIN=$OMNI_PUBLIC_ORIGIN" \
  --from-literal="OMNIRELAY_SETUP_MODE=$setup" \
  --dry-run=client -o yaml | "${kubectl[@]}" apply --server-side --field-manager=omnirelay-deploy -f -

job=$(sed "s|OMNIRELAY_GO_IMAGE|$image|g" "$root/base/go/migrate.yaml" | "${kubectl[@]}" create -f - -o name)
if ! "${kubectl[@]}" wait --for=condition=complete "$job" --timeout=130s; then
  "${kubectl[@]}" logs "$job" >&2 || true
  exit 1
fi
"${kubectl[@]}" logs "$job"
"${kubectl[@]}" exec postgres-0 -- psql -q -U postgres -d omnirelay -v ON_ERROR_STOP=1 \
  -c 'REVOKE INSERT, UPDATE, DELETE ON TABLE omnirelay_schema_migrations FROM omni_runtime'
sed -e "s|OMNIRELAY_GO_IMAGE|$image|g" -e "s|WEB_NODEPORT|$OMNI_K8S_WEB_NODEPORT|g" "$root/base/go/omnirelay.yaml" \
  | "${kubectl[@]}" apply --server-side --field-manager=omnirelay-deploy -f -
"${kubectl[@]}" rollout status deployment/omnirelay --timeout=120s
