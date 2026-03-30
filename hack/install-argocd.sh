#!/usr/bin/env bash
# hack/install-argocd.sh — install a minimal ArgoCD into the suparShip dev cluster.
#
# Idempotent: uses `helm upgrade --install` so re-running is safe.
# Requires the cluster and the `argocd` namespace to exist first:
#   task dev:cluster:bootstrap
#
# When to use this:
#   Only needed when working on features that interact with real ArgoCD
#   resources (Application syncs, health checks, etc.).
#   Most contributors do NOT need this — `task dev` (fake mode) is sufficient.
#
# Usage:
#   ./hack/install-argocd.sh      # run directly
#   task dev:cluster:argocd       # preferred: via Taskfile
set -euo pipefail

# ── Pinned versions ───────────────────────────────────────────────────────
# Update these together when upgrading ArgoCD.
# Chart changelog: https://github.com/argoproj/argo-helm/releases
ARGOCD_CHART_VERSION="7.7.0"   # argo-cd Helm chart → ArgoCD v2.13.x

# ── Config ────────────────────────────────────────────────────────────────
ARGOCD_NAMESPACE="argocd"
ARGOCD_RELEASE="argocd"
ARGOCD_HELM_REPO_NAME="argo"
ARGOCD_HELM_REPO_URL="https://argoproj.github.io/argo-helm"
ARGOCD_VALUES="config/argocd/values-dev.yaml"
HELM_TIMEOUT="300s"   # 5 min — image pulls can be slow on first run

# ── Repo root ─────────────────────────────────────────────────────────────
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# ── Helpers ───────────────────────────────────────────────────────────────
info()  { printf "  \033[0;36m%s\033[0m\n" "$*"; }
ok()    { printf "  \033[0;32m✓\033[0m  %s\n" "$*"; }
skip()  { printf "  \033[0;33m–\033[0m  %s\n" "$*"; }
die()   { printf "  \033[0;31mERROR:\033[0m %s\n" "$*" >&2; exit 1; }

# ── Banner ────────────────────────────────────────────────────────────────
echo ""
echo "  suparShip — ArgoCD install  (dev profile, chart ${ARGOCD_CHART_VERSION})"
echo "  ──────────────────────────────────────────────────────────────────"
echo ""

# ── 1. Prereq checks ──────────────────────────────────────────────────────
info "Checking prerequisites..."

for cmd in kubectl helm; do
  if command -v "$cmd" >/dev/null 2>&1; then
    ok "$cmd"
  else
    die "'$cmd' not found. Run: task dev:cluster:bootstrap"
  fi
done

# Verify the cluster bootstrap has been run.
kubectl get namespace "$ARGOCD_NAMESPACE" >/dev/null 2>&1 \
  || die "Namespace '${ARGOCD_NAMESPACE}' not found. Run: task dev:cluster:bootstrap"

echo ""

# ── 2. Add / refresh Helm repo ────────────────────────────────────────────
info "Helm repo '${ARGOCD_HELM_REPO_NAME}'..."
if helm repo list 2>/dev/null | awk '{print $1}' | grep -qx "${ARGOCD_HELM_REPO_NAME}"; then
  skip "already added"
else
  helm repo add "${ARGOCD_HELM_REPO_NAME}" "${ARGOCD_HELM_REPO_URL}" >/dev/null
  ok "added (${ARGOCD_HELM_REPO_URL})"
fi

printf "  Updating repo index... "
helm repo update "${ARGOCD_HELM_REPO_NAME}" >/dev/null
echo "ok"
echo ""

# ── 3. Install or upgrade ArgoCD ──────────────────────────────────────────
info "ArgoCD (helm upgrade --install)..."
echo "  This may take a few minutes on first run (image pull)."
echo ""

helm upgrade --install "${ARGOCD_RELEASE}" \
  "${ARGOCD_HELM_REPO_NAME}/argo-cd" \
  --namespace  "${ARGOCD_NAMESPACE}" \
  --version    "${ARGOCD_CHART_VERSION}" \
  --values     "${ARGOCD_VALUES}" \
  --wait \
  --timeout    "${HELM_TIMEOUT}" \
  --atomic

ok "ArgoCD installed / up-to-date"
echo ""

# ── 4. Confirm argocd-server rollout ─────────────────────────────────────
info "Waiting for argocd-server..."
kubectl rollout status deployment/argocd-server \
  -n "${ARGOCD_NAMESPACE}" \
  --timeout="${HELM_TIMEOUT}" >/dev/null
ok "argocd-server is running"
echo ""

# ── 5. Retrieve initial admin password ───────────────────────────────────
ADMIN_PASSWORD=""
if kubectl get secret argocd-initial-admin-secret \
     -n "${ARGOCD_NAMESPACE}" >/dev/null 2>&1; then
  ADMIN_PASSWORD="$(
    kubectl get secret argocd-initial-admin-secret \
      -n "${ARGOCD_NAMESPACE}" \
      -o jsonpath='{.data.password}' \
    | base64 -d
  )"
else
  ADMIN_PASSWORD="(secret deleted — may have been rotated after first login)"
fi

# ── Done ──────────────────────────────────────────────────────────────────
cat <<EOF
  ──────────────────────────────────────────────────────────────────
  ArgoCD is ready.

  Namespace   ${ARGOCD_NAMESPACE}
  Chart       argo-cd ${ARGOCD_CHART_VERSION}
  Username    admin
  Password    ${ADMIN_PASSWORD}

  Open the UI:
    kubectl port-forward svc/argocd-server -n ${ARGOCD_NAMESPACE} 8180:80
    open http://localhost:8180

  Or use the argocd CLI:
    argocd login localhost:8180 \\
      --username admin --password '${ADMIN_PASSWORD}' --plaintext

  Verify pods:
    kubectl get pods -n ${ARGOCD_NAMESPACE}

  Re-retrieve the password later:
    kubectl get secret argocd-initial-admin-secret \\
      -n ${ARGOCD_NAMESPACE} -o jsonpath='{.data.password}' | base64 -d

EOF
