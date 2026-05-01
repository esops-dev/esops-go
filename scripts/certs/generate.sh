#!/usr/bin/env bash
#
# Generate a throw-away CA + server cert + client cert for the local
# mTLS dev cluster (docker-compose service `es-mtls`).
#
# Idempotent: if every expected file already exists under out/, exits 0
# without touching them. Pass --force to wipe and regenerate.
#
# Not for any production use. Keys are written mode 0644 so the ES
# container (uid 1000) can read them from a bind-mount on macOS/Linux.
#
# Layout:
#   out/ca.crt    out/ca.key
#   out/server.crt  out/server.key    (SAN: es-mtls, localhost, 127.0.0.1)
#   out/client.crt  out/client.key    (CN=esops-client)

set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
out="$here/out"

force=1

expected=(ca.crt ca.key server.crt server.key client.crt client.key)
if [[ $force -eq 0 ]]; then
  have_all=1
  for f in "${expected[@]}"; do
    [[ -f "$out/$f" ]] || { have_all=0; break; }
  done
  if [[ $have_all -eq 1 ]]; then
    echo "certs: already present at $out (use --force to regenerate)"
    exit 0
  fi
fi

rm -rf "$out"
mkdir -p "$out"
cd "$out"

# 1. Root CA.
openssl genrsa -out ca.key 2048 2>/dev/null
openssl req -x509 -new -nodes -key ca.key -sha256 -days 825 \
  -subj "/CN=esops-dev-ca" \
  -out ca.crt 2>/dev/null

# 2. Server cert for the es-mtls container.
cat > server.ext <<'EOF'
authorityKeyIdentifier=keyid,issuer
basicConstraints=CA:FALSE
keyUsage = digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = @alt_names

[alt_names]
DNS.1 = es-mtls
DNS.2 = localhost
IP.1  = 127.0.0.1
EOF
openssl genrsa -out server.key 2048 2>/dev/null
openssl req -new -key server.key -subj "/CN=es-mtls" -out server.csr 2>/dev/null
openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -out server.crt -days 825 -sha256 -extfile server.ext 2>/dev/null

# 3. Client cert for esops. The CN is the identity that the ES PKI
#    realm sees — role_mapping in docker-compose keys off it.
cat > client.ext <<'EOF'
authorityKeyIdentifier=keyid,issuer
basicConstraints=CA:FALSE
keyUsage = digitalSignature, keyEncipherment
extendedKeyUsage = clientAuth
EOF
openssl genrsa -out client.key 2048 2>/dev/null
openssl req -new -key client.key -subj "/CN=esops-client" -out client.csr 2>/dev/null
openssl x509 -req -in client.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -out client.crt -days 825 -sha256 -extfile client.ext 2>/dev/null

rm -f server.csr server.ext client.csr client.ext ca.srl

chmod 644 ./*.key ./*.crt

echo "certs: generated at $out"
