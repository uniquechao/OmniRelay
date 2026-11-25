#!/usr/bin/env bash
# Prompt on the terminal, wait for the mounted Secret, then make one model call.
set +x
set -euo pipefail
[[ $# == 2 ]] || { echo "Usage: $0 CONTEXT NAMESPACE" >&2; exit 2; }
k=(kubectl --context "$1" --namespace "$2")
service=omnirelay-dsh-runtime
workload=$("${k[@]}" get service "$service" -o 'go-template={{index .spec.selector "app.kubernetes.io/name"}}')
[[ -n "$workload" && $("${k[@]}" get statefulset "$workload" -o 'jsonpath={.spec.serviceName}') == "$service" ]] || {
    echo "Cannot identify the Runtime StatefulSet for $service" >&2
    exit 1
}
pod="$workload-0"
IFS= read -rsp 'DSH model API key: ' key </dev/tty
echo >&2
[[ -n "$key" ]] || exit 2
fingerprint=$(printf '%s' "$key" | sha256sum | cut -d' ' -f1)
printf '%s' "$key" | "${k[@]}" create secret generic omnirelay-dsh-model --from-file=DEEPSEEK_API_KEY=/dev/stdin --dry-run=client -o json |
    "${k[@]}" apply --server-side --field-manager=omnirelay-dsh-config -f -
unset key
# Secret projection is asynchronous, including when replacing an existing credential.
"${k[@]}" exec -i "$pod" -c runtime -- python -c '
import hashlib, os, pathlib, sys, time
expected = sys.stdin.readline().strip()
path = pathlib.Path(os.environ["DEEPSEEK_API_KEY_FILE"])
for attempt in range(90):
    try:
        if hashlib.sha256(path.read_bytes()).hexdigest() == expected:
            sys.exit(0)
    except FileNotFoundError:
        pass
    time.sleep(2)
sys.exit("key projection pending")
' <<<"$fingerprint"
"${k[@]}" wait --for=condition=Ready "pod/$pod" --timeout=90s
"${k[@]}" exec "$pod" -c runtime -- python probe.py model
