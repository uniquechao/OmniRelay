#!/usr/bin/env bash
set -eu -o pipefail
cd "$(dirname "$0")/../.."
umask 077
mkdir -p .runtime
suite=$(mktemp -d "$PWD/.runtime/group-gate.XXXXXX")
export OMNI_GATE_CERTS="$suite" OMNI_GATE_CONTAINER="omni-group-gate-${suite##*.}"
image=${OMNI_GATE_IMAGE:-omnirelay-dsh-runtime:group-request}
volume="$OMNI_GATE_CONTAINER-data"
read -r OMNI_GATE_GO_PORT OMNI_GATE_RUNTIME_PORT model_port < <(python3 -c 'import socket; ss=[socket.socket() for _ in range(3)]; [s.bind(("127.0.0.1",0)) for s in ss]; print(*(s.getsockname()[1] for s in ss))')
export OMNI_GATE_GO_PORT OMNI_GATE_RUNTIME_PORT
cleanup() {
    docker logs "$OMNI_GATE_CONTAINER" > "$suite/runtime.log" 2>&1 || true
    docker rm -f "$OMNI_GATE_CONTAINER" >/dev/null 2>&1 || true
    docker volume rm "$volume" >/dev/null 2>&1 || true
    if [[ -n "${gate_pg:-}" ]]; then docker rm -fv "$gate_pg" >/dev/null 2>&1 || true; fi
    if [[ -n "${model_pid:-}" ]]; then kill "$model_pid" 2>/dev/null || true; fi
    find "$suite" -maxdepth 1 -type f \( -name '*.key' -o -name '*.csr' \) -delete
}
trap cleanup EXIT
if [[ -z "${OMNIRELAY_TEST_DATABASE_URL:-}" ]]; then
    gate_pg="$OMNI_GATE_CONTAINER-pg"
    pg_password=$(openssl rand -hex 20)
    docker run -d --name "$gate_pg" -e POSTGRES_PASSWORD="$pg_password" -p 127.0.0.1::5432 postgres:16-bookworm >/dev/null
    pg_binding=$(docker port "$gate_pg" 5432/tcp)
    export OMNIRELAY_TEST_DATABASE_URL="postgres://postgres:${pg_password}@${pg_binding}/postgres?sslmode=disable"
    for _ in {1..40}; do
        if docker exec "$gate_pg" pg_isready -h 127.0.0.1 -U postgres >/dev/null; then break; fi
        sleep .25
    done
fi
docker build --provenance=false -t "$image" runtime/dsh > "$suite/build.log" 2>&1 || { cat "$suite/build.log" >&2; exit 1; }
openssl req -x509 -newkey rsa:2048 -nodes -keyout "$suite/ca.key" -out "$suite/ca.crt" -subj /CN=internal-gate -days 1 2>/dev/null
for subject in localhost gateway intruder; do
    openssl req -new -newkey rsa:2048 -nodes -keyout "$suite/$subject.key" -out "$suite/$subject.csr" -subj "/CN=$subject" 2>/dev/null
    openssl x509 -req -in "$suite/$subject.csr" -CA "$suite/ca.crt" -CAkey "$suite/ca.key" -CAcreateserial -out "$suite/$subject.crt" -days 1 \
        -extfile <(printf 'subjectAltName=DNS:%s,IP:127.0.0.1\n' "$subject") 2>/dev/null
done
# Only the per-run runtime key is mounted. The outer directory is owner-only.
chmod 444 "$suite/localhost.key" "$suite/localhost.crt" "$suite/ca.crt"
python3 tests/integration/group-agent-model.py "$model_port" > "$suite/model.log" 2>&1 &
model_pid=$!
docker volume create "$volume" >/dev/null
mkdir -m 755 "$suite/model"
docker run -d --name "$OMNI_GATE_CONTAINER" --network host --read-only --tmpfs /tmp:rw,exec,nosuid,nodev \
    -v "$volume:/var/lib/omnirelay-agent" -v "$suite/ca.crt:/certs/ca.crt:ro" \
    -v "$suite/model:/model:ro" \
    -v "$suite/localhost.crt:/certs/tls.crt:ro" -v "$suite/localhost.key:/certs/tls.key:ro" \
    -e AGENT_CA=/certs/ca.crt -e AGENT_CERT=/certs/tls.crt -e AGENT_KEY=/certs/tls.key \
    -e AGENT_OWNER=localhost -e AGENT_GATEWAY_NAME=gateway -e AGENT_GATEWAY_URL="https://127.0.0.1:$OMNI_GATE_GO_PORT" \
    -e AGENT_LISTEN_HOST=127.0.0.1 -e AGENT_PORT="$OMNI_GATE_RUNTIME_PORT" -e DSH_MODEL=deepseek-v4-flash \
    -e DEEPSEEK_BASE_URL="http://127.0.0.1:$model_port/anthropic" -e DEEPSEEK_API_KEY_FILE=/model/DEEPSEEK_API_KEY "$image" >/dev/null
# A client signed by the same CA with the wrong service identity must be denied.
code=$(curl --retry 15 --retry-connrefused --retry-delay 1 --max-time 4 -so /dev/null -w '%{http_code}' --cacert "$suite/ca.crt" \
    --cert "$suite/intruder.crt" --key "$suite/intruder.key" "https://localhost:$OMNI_GATE_RUNTIME_PORT/works/00000000-0000-4000-8000-000000000000")
test "$code" = 403
docker exec "$OMNI_GATE_CONTAINER" python probe.py livez
if docker exec "$OMNI_GATE_CONTAINER" python probe.py readyz; then
    echo "Unexpected readiness without model Secret" >&2
    exit 1
fi
printf '%s' internal-fixture > "$suite/model/DEEPSEEK_API_KEY"
chmod 444 "$suite/model/DEEPSEEK_API_KEY"
docker exec "$OMNI_GATE_CONTAINER" python probe.py readyz
docker exec "$OMNI_GATE_CONTAINER" python probe.py model
grep -q '^DSH model probe path /anthropic/v1/messages$' "$suite/model.log"
go test -count=1 -v -run '^(TestGatewayPermissionGate|TestRuntimeImageGate|TestPrivateReplyGate|TestDirectedNoticeGate|TestNoticePermissionGate)$' ./internal/orchestrator/group | tee "$suite/gates.log"
for gate in TestGatewayPermissionGate TestRuntimeImageGate TestPrivateReplyGate TestDirectedNoticeGate TestNoticePermissionGate; do
    grep -q "^--- PASS: $gate " "$suite/gates.log"
done
printf 'Internal Agent messaging gates passed. Evidence: %s\n' "$suite"
