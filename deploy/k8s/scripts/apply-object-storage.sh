#!/usr/bin/env bash
set -euo pipefail
umask 077

if [[ $# != 1 ]]; then
  printf 'Usage: %s <kubectl-context>\n' "$0" >&2
  exit 2
fi

deployment="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
private="$deployment/private/minio"
kubectl=(kubectl --context "$1")
owner=omnirelay-deploy
namespace=omni
if [[ -z ${MINIO_IMAGE:-} && -r "$deployment/private/minio-image.env" ]]; then
  source "$deployment/private/minio-image.env"
fi
image=${MINIO_IMAGE:-}
if [[ -n "$image" && ! "$image" =~ ^[a-zA-Z0-9./:_@-]+$ ]]; then
  printf 'MINIO_IMAGE must be a container image reference.\n' >&2
  exit 2
fi

"${kubectl[@]}" apply --server-side --field-manager="$owner" -f "$deployment/base/namespace/namespace.yaml"
provisioner=$("${kubectl[@]}" get storageclass local-path -o jsonpath='{.provisioner}')
if [[ "$provisioner" != rancher.io/local-path ]]; then
  printf 'Expected rancher.io/local-path. Configure a storage overlay for this cluster.\n' >&2
  exit 1
fi
"${kubectl[@]}" apply --server-side --field-manager="$owner" -f "$deployment/base/minio/storageclass-local-retain.yaml"
install -d -m 700 "$private"

root_event=$("${kubectl[@]}" -n "$namespace" get secret minio-root --ignore-not-found -o name)
if [[ ! -s "$private/root.env" ]]; then
  if [[ -n "$root_event" ]]; then
    printf 'The cluster already has minio-root. Restore the local credentials before continuing.\n' >&2
    exit 1
  fi
  root_user="omni-$(openssl rand -hex 8)"
  root_password=$(openssl rand -hex 32)
  printf 'MINIO_ROOT_USER=%s\nMINIO_ROOT_PASSWORD=%s\n' "$root_user" "$root_password" > "$private/root.env"
  unset root_user root_password
fi

if [[ ! -s "$private/ca.crt" ]]; then
  existing_tls=$("${kubectl[@]}" -n "$namespace" get secret minio-tls --ignore-not-found -o name)
  if [[ -n "$existing_tls" || -e "$private/ca.key" ]]; then
    printf 'MinIO CA state is incomplete. Restore the target host CA before continuing.\n' >&2
    exit 1
  fi
  openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$private/ca.key"
  openssl req -new -x509 -key "$private/ca.key" -days 1825 -subj '/CN=OmniRelay S3 CA' \
    -addext 'basicConstraints=critical,CA:TRUE,pathlen:0' \
    -addext 'keyUsage=critical,keyCertSign,cRLSign' -out "$private/ca.crt"
fi

if [[ ! -s "$private/public.crt" || ! -s "$private/private.key" ]]; then
  if [[ -e "$private/public.crt" || -e "$private/private.key" ]]; then
    printf 'MinIO certificate state is incomplete. Restore the certificate and key together.\n' >&2
    exit 1
  fi
  openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$private/private.key"
  openssl req -new -key "$private/private.key" -subj '/CN=minio.omni.svc' -out "$private/server.csr"
  cat > "$private/server.ext" <<'EXT'
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=serverAuth
subjectAltName=DNS:minio,DNS:minio.omni,DNS:minio.omni.svc,DNS:minio.omni.svc.cluster.local
EXT
  openssl x509 -req -in "$private/server.csr" -CA "$private/ca.crt" -CAkey "$private/ca.key" \
    -CAcreateserial -days 365 -extfile "$private/server.ext" -out "$private/public.crt"
fi

openssl verify -CAfile "$private/ca.crt" "$private/public.crt"
openssl x509 -checkend 0 -noout -in "$private/public.crt"
"${kubectl[@]}" -n "$namespace" create secret generic minio-root --from-env-file="$private/root.env" \
  --dry-run=client -o yaml | "${kubectl[@]}" apply --server-side --field-manager="$owner" -f -
"${kubectl[@]}" -n "$namespace" create secret generic minio-tls \
  --from-file=public.crt="$private/public.crt" --from-file=private.key="$private/private.key" \
  --dry-run=client -o yaml | "${kubectl[@]}" apply --server-side --field-manager="$owner" -f -
"${kubectl[@]}" -n "$namespace" create configmap minio-ca --from-file=ca.crt="$private/ca.crt" \
  --dry-run=client -o yaml | "${kubectl[@]}" apply --server-side --field-manager="$owner" -f -
if [[ -n "$image" ]]; then
  sed -E "s|^([[:space:]]+image:).*|\\1 $image|" "$deployment/base/minio/minio.yaml" \
    | "${kubectl[@]}" apply --server-side --field-manager="$owner" -f -
else
  "${kubectl[@]}" apply --server-side --field-manager="$owner" -f "$deployment/base/minio/minio.yaml"
fi
"${kubectl[@]}" -n "$namespace" rollout status statefulset/minio --timeout=90s
"${kubectl[@]}" -n "$namespace" get statefulset,pvc,svc -l app.kubernetes.io/part-of=omnirelay
