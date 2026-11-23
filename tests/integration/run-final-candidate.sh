#!/usr/bin/env bash
# 在仓库根目录运行。产品从指定提交导出，复验脚本取自当前目录。
set -euo pipefail
umask 077
sha=$(git rev-parse --verify "${1:-HEAD}^{commit}")
mkdir -p .runtime
run=$(mktemp -d .runtime/final-candidate.XXXXXX)
mkdir -p "$run/src" "$run/tmp"
git archive "$sha" | tar -x -C "$run/src"
echo "$sha" > "$run/revision"
cmp package-lock.json "$run/src/package-lock.json"
ln -s "$PWD/node_modules" "$run/src/node_modules"
(
  cd "$run/src"
  npm run build
  go build -o ../omnirelay ./cmd/omnirelay
) > "$run/build.log" 2>&1
sha256sum "$run/omnirelay" "$run/src/internal/webui/dist/"{index,setup}.html > "$run/build.sha256"

db=
server=
proxy=
cleanup() {
  if [[ -n "$server" ]]; then kill "$server" 2>/dev/null || true; wait "$server" || true; fi
  if [[ -n "$proxy" ]]; then kill "$proxy" 2>/dev/null || true; wait "$proxy" || true; fi
  if [[ -n "$db" ]]; then docker stop "$db" >/dev/null; fi
  rm -f "$run"/{db-password,web.key}
}
trap cleanup EXIT
openssl rand -hex 24 > "$run/db-password"
db=$(docker run --rm -d --publish 127.0.0.1::5432 --tmpfs /var/lib/postgresql/data:rw,size=384m \
  --mount "type=bind,src=$PWD/$run/db-password,dst=/run/secrets/test-password,readonly" \
  -e POSTGRES_PASSWORD_FILE=/run/secrets/test-password -e POSTGRES_DB=acceptance postgres:16-bookworm)
pg=(docker exec -i -u postgres "$db")
for i in {1..100}; do
  if "${pg[@]}" pg_isready -h 127.0.0.1 -d acceptance >/dev/null; then break; fi
  sleep 0.2
done
"${pg[@]}" openssl req -x509 -nodes -newkey rsa:2048 -days 1 -subj /CN=localhost \
  -addext subjectAltName=IP:127.0.0.1 -keyout /var/lib/postgresql/data/server.key \
  -out /var/lib/postgresql/data/server.crt 2> "$run/cert.log"
"${pg[@]}" psql -v ON_ERROR_STOP=1 -d acceptance -c 'ALTER SYSTEM SET ssl TO on' -c 'SELECT pg_reload_conf()' >/dev/null
"${pg[@]}" cat /var/lib/postgresql/data/server.crt > "$run/pg.crt"
openssl req -x509 -nodes -newkey rsa:2048 -days 1 -subj /CN=localhost -addext subjectAltName=IP:127.0.0.1 \
  -keyout "$run/web.key" -out "$run/web.crt" 2>> "$run/cert.log"

python3 -u - "$run/proxy-port" > "$run/outbound.log" <<'PY' &
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
import sys
class Reject(BaseHTTPRequestHandler):
    def deny(self):
        print("出站请求", self.command)
        self.send_error(502)
    do_CONNECT = do_GET = do_POST = deny
    def log_message(self, *args):
        pass
server = HTTPServer(("127.0.0.1", 0), Reject)
Path(sys.argv[1]).write_text(str(server.server_port))
server.serve_forever()
PY
proxy=$!
for i in {1..50}; do [[ -s "$run/proxy-port" ]] && break; sleep 0.1; done
port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')
export H3_ORIGIN="https://127.0.0.1:$port"
db_addr=$(docker port "$db" 5432)
app=(env -i PATH="$PATH" HOME="$HOME" OMNIRELAY_LISTEN_ADDR="127.0.0.1:$port" OMNIRELAY_PUBLIC_ORIGIN="$H3_ORIGIN"
  OMNIRELAY_DATABASE_URL="postgres://postgres:$(cat "$run/db-password")@$db_addr/acceptance?sslmode=verify-full&sslrootcert=$PWD/$run/pg.crt"
  OMNIRELAY_TLS_KEY_FILE="$PWD/$run/web.key" OMNIRELAY_TLS_CERT_FILE="$PWD/$run/web.crt"
  OMNIRELAY_FEISHU_APP_ID=cli_fixture OMNIRELAY_FEISHU_TENANT_KEY=tenant OMNIRELAY_BOOTSTRAP_OPEN_ID=manager
  HTTPS_PROXY="http://127.0.0.1:$(cat "$run/proxy-port")" HTTP_PROXY="http://127.0.0.1:$(cat "$run/proxy-port")"
  NO_PROXY=127.0.0.1,localhost "$run/omnirelay")
"${app[@]}" migrate
"${pg[@]}" psql -v ON_ERROR_STOP=1 -d acceptance < tests/fixtures/final-candidate.sql
H3_PASSWORD=$(openssl rand -hex 18)
export H3_PASSWORD
for account in manager second; do printf %s "$H3_PASSWORD" | "${app[@]}" password "$account" "$account"; done
"${app[@]}" serve > "$run/server.log" 2>&1 &
server=$!
for i in {1..50}; do
  if curl -fsS --cacert "$run/web.crt" "$H3_ORIGIN/readyz" -o /dev/null 2>/dev/null; then break; fi
  sleep 0.2
done
curl -fsS --cacert "$run/web.crt" "$H3_ORIGIN/readyz" -o /dev/null
export OMNIRELAY_PLAYWRIGHT_MODULE="$PWD/node_modules/playwright-core"
export TMPDIR="$run/tmp"
status=0
node tests/integration/final_candidate.mjs > "$run/panels-bot.log" 2>&1 || status=1
if node tests/integration/task_two_accounts.mjs > "$run/tasks.log" 2>&1; then
  echo '双账号用例通过' >> "$run/tasks.log"
else
  status=1
fi
if [[ -s "$run/outbound.log" ]]; then status=1; fi
echo "候选 $sha 的验收记录位于 $run"
exit "$status"
