#!/usr/bin/env bash
set -euo pipefail
[[ $# == 1 ]] || { echo '用法：rollback-go.sh <kubectl-context>' >&2; exit 2; }
k=(kubectl --context "$1" -n omni)
exec 9>"${OMNI_UI_ARCHIVE:-/var/lib/omnirelay/ui}/.release.lock"
flock -x 9
patch=$("${k[@]}" get deployment omnirelay -o json | python3 -c '
import json,re,sys
d=json.load(sys.stdin)
ref=d["metadata"].get("annotations",{}).get("omnirelay.io/previous-image","")
if not re.fullmatch(r"[\w./:-]+@sha256:[a-f0-9]{64}",ref):
    sys.exit("缺少上一镜像摘要")
containers=d["spec"]["template"]["spec"]["containers"]
i=next(i for i,c in enumerate(containers) if c["name"]=="omnirelay")
print(json.dumps([{"op":"test","path":"/metadata/resourceVersion","value":d["metadata"]["resourceVersion"]},
                  {"op":"replace","path":f"/spec/template/spec/containers/{i}/image","value":ref}]))')
"${k[@]}" patch deployment omnirelay --field-manager=omnirelay-deploy --type=json -p "$patch"
"${k[@]}" rollout status deployment/omnirelay --timeout=120s
