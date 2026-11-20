#!/usr/bin/env bash
set -euo pipefail
umask 077
[[ $# == 2 ]] || { printf 'Usage: %s <kubectl-context> <go-image>\n' "$0" >&2; exit 2; }
root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
kubectl=(kubectl --context "$1" -n omni)
image="$2"
[[ "$image" =~ ^[a-zA-Z0-9./:_-]+@sha256:[a-f0-9]{64}$ ]] || exit 2

source "$root/private/route.env"
[[ "$OMNI_K8S_WEB_NODEPORT" =~ ^[0-9]{1,5}$ ]] || exit 2
bot_send_enabled=${OMNIRELAY_FEISHU_BOT_SEND_ENABLED-true}
if [[ "$bot_send_enabled" != true && "$bot_send_enabled" != false ]]; then
  printf '%s\n' 'OMNIRELAY_FEISHU_BOT_SEND_ENABLED 必须是 true 或 false。' >&2
  exit 2
fi
# 先准备同源资源，最后随 Deployment 一起写入此次切换的恢复摘要。
export OMNI_UI_ARCHIVE=${OMNI_UI_ARCHIVE:-/var/lib/omnirelay/ui}
install -d -m 0755 "$OMNI_UI_ARCHIVE"
exec 9>"$OMNI_UI_ARCHIVE/.release.lock"
flock -x 9
existing=$("${kubectl[@]}" get deployment omnirelay --ignore-not-found -o name)
previous=""
if [[ -n "$existing" ]]; then
  current=$("${kubectl[@]}" get "$existing" -o jsonpath='{.spec.template.spec.containers[?(@.name=="omnirelay")].image}')
  if [[ "$current" == "$image" ]]; then
    previous=$("${kubectl[@]}" get "$existing" -o jsonpath='{.metadata.annotations.omnirelay\.io/previous-image}')
  else
    "${kubectl[@]}" rollout status "$existing" --timeout=120s
    if [[ "$current" == *@sha256:* ]]; then
      previous="$current"
    else
      previous=$("${kubectl[@]}" get pods -l app.kubernetes.io/name=omnirelay -o json |
        python3 -c 'import json,sys
pods=[p for p in json.load(sys.stdin)["items"] if not p["metadata"].get("deletionTimestamp")]
refs={s["imageID"].removeprefix("docker-pullable://") for p in pods for s in p["status"]["containerStatuses"] if s["name"]=="omnirelay" and s["ready"]}
if len(refs)!=1: sys.exit("无法确定上一运行镜像的唯一摘要")
print(refs.pop())')
    fi
    [[ "$previous" =~ ^[a-zA-Z0-9./:_-]+@sha256:[a-f0-9]{64}$ ]] || { echo '无法确认上一镜像的仓库摘要' >&2; exit 1; }
  fi
fi
if [[ -n "$previous" ]]; then
  python3 "$root/scripts/publish-ui.py" image "$OMNI_PUBLIC_ORIGIN" "$previous"
fi
if [[ -n "$previous" ]] && [[ "$(docker image inspect --format '{{index .Config.Labels "io.omnirelay.ui.entry"}}' "$previous")" != react ]]; then
  python3 "$root/scripts/publish-ui.py" verify-legacy "$OMNI_PUBLIC_ORIGIN"
fi
python3 "$root/scripts/publish-ui.py" image "$OMNI_PUBLIC_ORIGIN" "$image"
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
sed -e "s|OMNIRELAY_PREVIOUS_IMAGE|$previous|g" -e "s|OMNIRELAY_GO_IMAGE|$image|g" \
  -e "s|WEB_NODEPORT|$OMNI_K8S_WEB_NODEPORT|g" -e "s|BOT_SEND_ENABLED_VALUE|$bot_send_enabled|g" "$root/base/go/omnirelay.yaml" \
  | "${kubectl[@]}" apply --server-side --field-manager=omnirelay-deploy -f -
"${kubectl[@]}" rollout status deployment/omnirelay --timeout=120s
