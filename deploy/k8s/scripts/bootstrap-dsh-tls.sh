#!/usr/bin/env bash
set -euo pipefail
umask 077
[[ $# == 3 ]] || { echo "Usage: $0 CONTEXT NAMESPACE PRIVATE_DIRECTORY" >&2; exit 2; }
k=(kubectl --context "$1" --namespace "$2")
directory=$3
service="omnirelay-dsh-runtime.$2.svc"
owner="omnirelay-dsh-runtime-0.$service"
existing=$("${k[@]}" get secret omnirelay-dsh-tls --ignore-not-found -o name)
[[ -z "$existing" ]] || { echo "Runtime TLS Secret already present"; exit 0; }
install -d -m 700 "$directory"
if [[ ! -d "$directory/pki" ]]; then
    stage=$(mktemp -d "$directory/.pki.XXXXXX")
    openssl ecparam -name prime256v1 -genkey -noout -out "$stage/ca.key"
    openssl req -new -x509 -key "$stage/ca.key" -days 1825 -subj '/CN=OmniRelay Agent CA' \
      -addext 'basicConstraints=critical,CA:TRUE,pathlen:0' -addext 'keyUsage=critical,keyCertSign,cRLSign' -out "$stage/ca.crt"
    openssl ecparam -name prime256v1 -genkey -noout -out "$stage/tls.key"
    openssl req -new -key "$stage/tls.key" -subj "/CN=$owner" -out "$stage/tls.csr"
    openssl x509 -req -in "$stage/tls.csr" -CA "$stage/ca.crt" -CAkey "$stage/ca.key" -CAcreateserial -days 365 \
      -extfile <(printf '%s\n' 'basicConstraints=critical,CA:FALSE' 'keyUsage=critical,digitalSignature' \
        'extendedKeyUsage=serverAuth,clientAuth' "subjectAltName=DNS:$owner,DNS:$service") -out "$stage/tls.crt"
    mv -T "$stage" "$directory/pki"
fi
openssl verify -CAfile "$directory/pki/ca.crt" -verify_hostname "$owner" "$directory/pki/tls.crt" >/dev/null
"${k[@]}" create secret generic omnirelay-dsh-tls --from-file="$directory/pki/ca.crt" \
    --from-file="$directory/pki/tls.crt" --from-file="$directory/pki/tls.key"
