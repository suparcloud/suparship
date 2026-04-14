#!/usr/bin/env bash
# hack/install-gitea-tls-proxy.sh — deploy a TLS-terminating reverse proxy
# in front of Gitea so in-cluster tools (Kargo) can reach it over HTTPS.
#
# Kargo v0.9 refuses to pass credentials over HTTP. This proxy solves that
# by providing an HTTPS endpoint at gitea-https.gitea.svc.cluster.local:3000.
#
# Idempotent: safe to run multiple times.
#
# Usage:
#   ./hack/install-gitea-tls-proxy.sh
#   task dev:cluster:gitea-tls-proxy
set -euo pipefail

GITEA_NAMESPACE="gitea"
PROXY_NAME="gitea-https"
CERT_SECRET="gitea-tls-cert"

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"
CERTS_DIR="${REPO_ROOT}/config/kind/gitea-certs"

info()  { printf "  \033[0;36m%s\033[0m\n" "$*"; }
ok()    { printf "  \033[0;32m✓\033[0m  %s\n" "$*"; }
skip()  { printf "  \033[0;33m–\033[0m  %s\n" "$*"; }

echo ""
echo "  suparShip — Gitea TLS proxy"
echo "  ──────────────────────────────────────────────────────────────────"
echo ""

# ── 1. Generate self-signed cert ──────────────────────────────────────────
info "TLS certificate for '${PROXY_NAME}'..."
if [ -f "${CERTS_DIR}/tls.crt" ] && [ -f "${CERTS_DIR}/tls.key" ]; then
  skip "already exists"
else
  mkdir -p "${CERTS_DIR}"
  openssl req -newkey rsa:2048 -nodes -sha256 \
    -keyout "${CERTS_DIR}/tls.key" \
    -x509 -days 3650 \
    -out "${CERTS_DIR}/tls.crt" \
    -subj "/CN=${PROXY_NAME}.${GITEA_NAMESPACE}.svc.cluster.local" \
    -addext "subjectAltName=DNS:${PROXY_NAME},DNS:${PROXY_NAME}.${GITEA_NAMESPACE},DNS:${PROXY_NAME}.${GITEA_NAMESPACE}.svc,DNS:${PROXY_NAME}.${GITEA_NAMESPACE}.svc.cluster.local" 2>/dev/null
  ok "generated self-signed cert"
fi
echo ""

# ── 2. Create/update TLS secret ──────────────────────────────────────────
info "Kubernetes TLS secret '${CERT_SECRET}'..."
kubectl create secret tls "${CERT_SECRET}" \
  --cert="${CERTS_DIR}/tls.crt" \
  --key="${CERTS_DIR}/tls.key" \
  --namespace="${GITEA_NAMESPACE}" \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null
ok "secret created/updated"
echo ""

# ── 3. Deploy nginx TLS proxy ────────────────────────────────────────────
# ── 3a. Build TLS proxy image if needed ───────────────────────────────────
PROXY_IMAGE="localhost:5001/suparship/tls-proxy:latest"
info "TLS proxy image..."
if docker manifest inspect "${PROXY_IMAGE}" >/dev/null 2>&1; then
  skip "already pushed"
else
  PROXY_SRC="${REPO_ROOT}/hack/tls-proxy"
  CGO_ENABLED=0 GOOS=linux GOARCH="$(uname -m | sed 's/x86_64/amd64/')" \
    go build -o "${PROXY_SRC}/tls-proxy" "${PROXY_SRC}/main.go"
  docker build -t "${PROXY_IMAGE}" "${PROXY_SRC}"
  docker push "${PROXY_IMAGE}"
  kind load docker-image "${PROXY_IMAGE}" --name "${KIND_CLUSTER_NAME:-suparship-dev}" 2>/dev/null || true
  ok "built and pushed"
fi
echo ""

info "Deploying TLS proxy pod + service..."
kubectl apply -f - <<'MANIFEST'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: gitea-https
  namespace: gitea
  labels:
    app: gitea-https
spec:
  replicas: 1
  selector:
    matchLabels:
      app: gitea-https
  template:
    metadata:
      labels:
        app: gitea-https
    spec:
      containers:
        - name: proxy
          image: kind-registry:5000/suparship/tls-proxy:latest
          imagePullPolicy: IfNotPresent
          ports:
            - containerPort: 3000
          env:
            - name: UPSTREAM_URL
              value: "http://gitea-http.gitea.svc.cluster.local:3000"
          volumeMounts:
            - name: tls
              mountPath: /etc/tls
              readOnly: true
      volumes:
        - name: tls
          secret:
            secretName: gitea-tls-cert
---
apiVersion: v1
kind: Service
metadata:
  name: gitea-https
  namespace: gitea
spec:
  selector:
    app: gitea-https
  ports:
    - port: 3000
      targetPort: 3000
      protocol: TCP
MANIFEST
ok "proxy deployed"
echo ""

# ── 4. Wait for proxy to be ready ────────────────────────────────────────
info "Waiting for proxy pod..."
kubectl rollout status deployment/gitea-https -n "${GITEA_NAMESPACE}" --timeout=60s
ok "proxy is ready"
echo ""

cat <<EOF
  ──────────────────────────────────────────────────────────────────
  Gitea TLS proxy ready.

  HTTPS endpoint (in-cluster):
    https://gitea-https.gitea.svc.cluster.local:3000

  Use insecureSkipTLSVerify=true for self-signed cert.
  Kargo credential secrets will work over this HTTPS endpoint.
EOF
