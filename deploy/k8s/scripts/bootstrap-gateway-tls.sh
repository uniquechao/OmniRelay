#!/usr/bin/env bash
set -euo pipefail
umask 077
[[ $# == 3 ]] || { echo "Usage: $0 CONTEXT NAMESPACE DSH_PRIVATE_DIRECTORY" >&2; exit 2; }
k=(kubectl --context "$1" --namespace "$2")
service="omnirelay-agent-gateway.$2.svc"
pki=$3/pki
existing=$("${k[@]}" get secret omnirelay-agent-gateway-tls --ignore-not-found -o name)
[[ -z "$existing" ]] || { echo "Gateway TLS Secret present"; exit 0; }
if [[ ! -d "$pki/gateway" ]]; then
    stage=$(mktemp -d "$pki/.gateway.XXXXXX")
    openssl ecparam -name prime256v1 -genkey -noout -out "$stage/tls.key"
    openssl req -new -key "$stage/tls.key" -subj "/CN=$service" -out "$stage/tls.csr"
    openssl x509 -req -in "$stage/tls.csr" -CA "$pki/ca.crt" -CAkey "$pki/ca.key" -days 365 \
        -extfile <(printf '%s\n' 'basicConstraints=critical,CA:FALSE' 'keyUsage=critical,digitalSignature' \
        'extendedKeyUsage=serverAuth,clientAuth' "subjectAltName=DNS:$service") -out "$stage/tls.crt"
    mv -T "$stage" "$pki/gateway"
fi
openssl verify -CAfile "$pki/ca.crt" -verify_hostname "$service" "$pki/gateway/tls.crt" >/dev/null
"${k[@]}" create secret generic omnirelay-agent-gateway-tls --from-file="$pki/ca.crt" \
    --from-file="$pki/gateway/tls.crt" --from-file="$pki/gateway/tls.key"
