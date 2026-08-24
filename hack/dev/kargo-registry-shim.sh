#!/usr/bin/env bash
# hack/dev/kargo-registry-shim.sh — let Kargo discover images from the
# plain-HTTP kind registry.
#
# Kargo's Warehouse image discovery always speaks HTTPS to a non-localhost
# registry (insecureSkipTLSVerify only skips certificate VERIFICATION — see
# pkg/image/repository_client.go upstream: name.ParseReference is never given
# name.Insecure), so against the ctlptl kind registry it fails with
# "server gave HTTP response to HTTPS client" and freight is never created.
#
# The shim: an in-cluster nginx that terminates self-signed TLS on :5000 and
# proxies to the real registry (http://kind-registry:5000, resolved normally
# from ITS pod), plus a hostAliases entry on the kargo-controller Deployment
# pointing "kind-registry" at the shim's ClusterIP. Only Kargo's resolution
# changes: nodes keep pulling straight from the registry over HTTP, ArgoCD and
# CI are untouched, and Kargo's insecureSkipTLSVerify accepts the self-signed
# cert.
#
# Idempotent. Used by: Tiltfile (local_resource 'kargo-registry-shim').
set -euo pipefail

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_dev_context

NS=registry

info() { printf "  \033[0;36m%s\033[0m\n" "$*"; }
ok()   { printf "  \033[0;32m✓\033[0m  %s\n" "$*"; }
die()  { printf "  \033[0;31mERROR:\033[0m %s\n" "$*" >&2; exit 1; }

kubectl get ns "$NS" >/dev/null 2>&1 || kubectl create ns "$NS" >/dev/null

# ── 1. Self-signed cert (CN/SAN kind-registry; Kargo skips verification) ───
if ! kubectl -n "$NS" get secret kind-registry-tls-cert >/dev/null 2>&1; then
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  openssl req -x509 -newkey rsa:2048 -sha256 -days 3650 -nodes \
    -keyout "$tmp/key.pem" -out "$tmp/cert.pem" \
    -subj "/CN=kind-registry" -addext "subjectAltName=DNS:kind-registry" \
    >/dev/null 2>&1
  kubectl -n "$NS" create secret tls kind-registry-tls-cert \
    --cert="$tmp/cert.pem" --key="$tmp/key.pem" >/dev/null
  ok "self-signed cert secret created"
else
  ok "cert secret already present"
fi

# ── 2. TLS-terminating proxy in front of the registry ──────────────────────
kubectl apply -f - >/dev/null <<'YAML'
apiVersion: v1
kind: ConfigMap
metadata:
  name: kind-registry-tls-nginx
  namespace: registry
data:
  default.conf: |
    server {
      listen 5000 ssl;
      ssl_certificate     /tls/tls.crt;
      ssl_certificate_key /tls/tls.key;
      client_max_body_size 0;
      location / {
        proxy_pass http://kind-registry:5000;
        proxy_http_version 1.1;
        proxy_set_header Host $http_host;
      }
    }
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kind-registry-tls
  namespace: registry
  labels: {app: kind-registry-tls}
spec:
  replicas: 1
  selector:
    matchLabels: {app: kind-registry-tls}
  template:
    metadata:
      labels: {app: kind-registry-tls}
    spec:
      containers:
        - name: nginx
          image: nginx:1.27-alpine
          ports: [{containerPort: 5000}]
          volumeMounts:
            - {name: conf, mountPath: /etc/nginx/conf.d}
            - {name: tls, mountPath: /tls}
      volumes:
        - name: conf
          configMap: {name: kind-registry-tls-nginx}
        - name: tls
          secret: {secretName: kind-registry-tls-cert}
---
apiVersion: v1
kind: Service
metadata:
  name: kind-registry-tls
  namespace: registry
spec:
  selector: {app: kind-registry-tls}
  ports: [{port: 5000, targetPort: 5000}]
YAML
ok "TLS proxy applied"
kubectl -n "$NS" rollout status deployment kind-registry-tls --timeout=180s >/dev/null

# ── 3. Point ONLY kargo-controller's "kind-registry" at the proxy ──────────
ip="$(kubectl -n "$NS" get svc kind-registry-tls -o jsonpath='{.spec.clusterIP}')"
[ -n "$ip" ] || die "no ClusterIP for kind-registry-tls"
kubectl -n kargo patch deployment kargo-controller --type strategic \
  -p "{\"spec\":{\"template\":{\"spec\":{\"hostAliases\":[{\"ip\":\"$ip\",\"hostnames\":[\"kind-registry\"]}]}}}}" >/dev/null
kubectl -n kargo rollout status deployment kargo-controller --timeout=180s >/dev/null
ok "kargo-controller resolves kind-registry → $ip (TLS shim)"
