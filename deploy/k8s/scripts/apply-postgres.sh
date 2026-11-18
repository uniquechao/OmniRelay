#!/usr/bin/env bash
set -euo pipefail
umask 077
[[ $# == 1 ]] || { printf 'Usage: %s <kubectl-context>\n' "$0" >&2; exit 2; }
root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
private="$root/private/postgres"
kubectl=(kubectl --context "$1" -n omni)
owner=omnirelay-deploy
install -d -m 700 "$private"
if [[ ! -s "$private/bootstrap.env" ]]; then
  if [[ -n $("${kubectl[@]}" get secret postgres-bootstrap --ignore-not-found -o name) ]]; then
    exit 1
  fi
  for key in POSTGRES_PASSWORD OMNI_PG_MIGRATE_PASSWORD OMNI_PG_RUNTIME_PASSWORD; do
    printf '%s=%s\n' "$key" "$(openssl rand -hex 32)"
  done > "$private/bootstrap.env"
fi
source "$private/bootstrap.env"
printf 'OMNIRELAY_DATABASE_URL=postgresql://omni_migrate:%s@postgres.omni.svc:5432/omnirelay?sslmode=verify-full&sslrootcert=/run/pg-ca/ca.crt\n' \
  "$OMNI_PG_MIGRATE_PASSWORD" > "$private/migrate.env"
printf 'OMNIRELAY_DATABASE_URL=postgresql://omni_runtime:%s@postgres.omni.svc:5432/omnirelay?sslmode=verify-full&sslrootcert=/run/pg-ca/ca.crt\n' \
  "$OMNI_PG_RUNTIME_PASSWORD" > "$private/runtime.env"

if [[ ! -s "$private/ca.crt" ]]; then
  [[ ! -e "$private/ca.key" && -z $("${kubectl[@]}" get secret postgres-tls --ignore-not-found -o name) ]] ||
    { printf 'Restore the PostgreSQL CA files before retrying.\n' >&2; exit 1; }
  openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$private/ca.key"
  openssl req -new -x509 -key "$private/ca.key" -days 1825 -subj '/CN=OmniRelay PostgreSQL CA' \
    -addext 'basicConstraints=critical,CA:TRUE,pathlen:0' \
    -addext 'keyUsage=critical,keyCertSign,cRLSign' -out "$private/ca.crt"
fi
if [[ ! -s "$private/tls.crt" && ! -e "$private/tls.key" ]]; then
  openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$private/tls.key"
  openssl req -new -key "$private/tls.key" -subj '/CN=postgres.omni.svc' -out "$private/server.csr"
  cat > "$private/server.ext" <<'EXT'
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=serverAuth
subjectAltName=DNS:postgres,DNS:postgres.omni,DNS:postgres.omni.svc,DNS:postgres.omni.svc.cluster.local
EXT
  openssl x509 -req -in "$private/server.csr" -CA "$private/ca.crt" -CAkey "$private/ca.key" \
    -CAcreateserial -days 365 -extfile "$private/server.ext" -out "$private/tls.crt"
fi

openssl verify -verify_hostname postgres.omni.svc -CAfile "$private/ca.crt" "$private/tls.crt"
apply() { "${kubectl[@]}" apply --server-side --field-manager="$owner" -f -; }
for name in bootstrap migrate runtime; do
  "${kubectl[@]}" create secret generic "postgres-$name" --from-env-file="$private/$name.env" --dry-run=client -o yaml | apply
done
"${kubectl[@]}" create secret tls postgres-tls --cert="$private/tls.crt" --key="$private/tls.key" --dry-run=client -o yaml | apply
"${kubectl[@]}" create configmap postgres-ca --from-file=ca.crt="$private/ca.crt" --dry-run=client -o yaml | apply
"${kubectl[@]}" create configmap postgres-init --from-file=10-roles.sh="$root/base/postgres/init-roles.sh" --dry-run=client -o yaml | apply
"${kubectl[@]}" apply --server-side --field-manager="$owner" -f "$root/base/postgres/postgres.yaml"
"${kubectl[@]}" rollout status statefulset/postgres --timeout=120s
