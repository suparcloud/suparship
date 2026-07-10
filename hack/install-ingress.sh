#!/usr/bin/env bash
# hack/install-ingress.sh — install the NGINX ingress controller for kind.
#
# Idempotent: skips installation when the ingress-nginx controller Deployment
# already exists.
#
# The kind cluster is configured with extraPortMappings so that containerPort
# 80 is reachable at http://localhost:8880 and containerPort 443 at
# https://localhost:8443. The NGINX ingress controller uses hostPort mode on
# those ports, so Ingress resources work without NodePort hacks.
#
# Used by: hack/dev-cluster.sh
# Requires: kubectl, cluster bootstrapped (task dev:cluster:bootstrap)
#
# Usage:
#   ./hack/install-ingress.sh       # run directly
#   task dev:cluster:ingress        # preferred: via Taskfile
set -euo pipefail

# ── Pinned version ────────────────────────────────────────────────────────
# Keep in sync with the kind-specific deploy manifest below.
# Changelog: https://github.com/kubernetes/ingress-nginx/releases
INGRESS_NGINX_VERSION="v1.10.1"

INGRESS_NAMESPACE="ingress-nginx"
INGRESS_DEPLOY="ingress-nginx-controller"
HELM_TIMEOUT="300s"

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# ── Helpers ───────────────────────────────────────────────────────────────
info()  { printf "  \033[0;36m%s\033[0m\n" "$*"; }
ok()    { printf "  \033[0;32m✓\033[0m  %s\n" "$*"; }
skip()  { printf "  \033[0;33m–\033[0m  %s\n" "$*"; }
die()   { printf "  \033[0;31mERROR:\033[0m %s\n" "$*" >&2; exit 1; }

# ── Banner ────────────────────────────────────────────────────────────────
echo ""
echo "  suparship — NGINX ingress controller  (kind profile, ${INGRESS_NGINX_VERSION})"
echo "  ──────────────────────────────────────────────────────────────────"
echo ""

# ── 1. Prereq checks ──────────────────────────────────────────────────────
info "Checking prerequisites..."
command -v kubectl >/dev/null 2>&1 || die "'kubectl' not found."
ok "kubectl"

kubectl get namespace suparship-system >/dev/null 2>&1 \
  || die "Namespace 'suparship-system' not found. Run: task dev:cluster:bootstrap"
echo ""

# ── 2. Skip if already installed ─────────────────────────────────────────
if kubectl get deployment "${INGRESS_DEPLOY}" \
     -n "${INGRESS_NAMESPACE}" >/dev/null 2>&1; then
  skip "NGINX ingress controller already installed — skipping"
  echo ""
  echo "  ──────────────────────────────────────────────────────────────────"
  echo "  Ingress is ready.  HTTP →  http://localhost:8880"
  echo ""
  exit 0
fi

# ── 3. Apply the kind-specific deploy manifest ───────────────────────────
# This manifest is the canonical kind-flavoured NGINX ingress controller that
# configures hostNetwork / hostPort to match the kind extraPortMappings.
# Source: https://kind.sigs.k8s.io/docs/user/ingress/#ingress-nginx
MANIFEST_URL="https://raw.githubusercontent.com/kubernetes/ingress-nginx/controller-${INGRESS_NGINX_VERSION}/deploy/static/provider/kind/deploy.yaml"

info "Applying NGINX ingress controller manifest (${INGRESS_NGINX_VERSION})..."
echo "  URL: ${MANIFEST_URL}"
echo ""
kubectl apply -f "${MANIFEST_URL}"
echo ""

# ── 4. Wait for the controller to be ready ───────────────────────────────
info "Waiting for ingress-nginx-controller rollout (may take ~60s)..."
kubectl rollout status deployment/"${INGRESS_DEPLOY}" \
  -n "${INGRESS_NAMESPACE}" \
  --timeout="${HELM_TIMEOUT}"
echo ""

ok "NGINX ingress controller is running"
echo ""

# ── Done ──────────────────────────────────────────────────────────────────
cat <<EOF
  ──────────────────────────────────────────────────────────────────
  NGINX ingress controller is ready.

  Namespace   ${INGRESS_NAMESPACE}
  Version     ${INGRESS_NGINX_VERSION}

  Port mappings (kind → host):
    HTTP   containerPort 80  →  http://localhost:8880
    HTTPS  containerPort 443 →  https://localhost:8443

  Any Ingress with className=nginx will be routable via these ports.
  Example: add to /etc/hosts →  127.0.0.1 gitea.localhost argocd.localhost

  Verify pods:
    kubectl get pods -n ${INGRESS_NAMESPACE}

EOF
