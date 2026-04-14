#!/usr/bin/env bash
# hack/install-cert-manager.sh — install cert-manager into the suparShip dev cluster.
#
# cert-manager is required by Kargo to provision TLS certificates for the
# Kargo API server and webhooks server (via a self-signed Issuer).
#
# Idempotent: uses `helm upgrade --install` so re-running is safe.
# Requires the cluster to exist:
#   task dev:cluster:bootstrap
#
# Usage:
#   ./hack/install-cert-manager.sh        # run directly
#   task dev:cluster:cert-manager         # preferred: via Taskfile
#   task dev:cluster:kargo                # installs cert-manager → Argo Rollouts → Kargo
set -euo pipefail

# ── Pinned versions ───────────────────────────────────────────────────────
# Update these together when upgrading cert-manager.
# Chart changelog: https://github.com/cert-manager/cert-manager/releases
CERTMANAGER_CHART_VERSION="v1.17.1"   # cert-manager Helm chart + app version

# ── Config ────────────────────────────────────────────────────────────────
CERTMANAGER_NAMESPACE="cert-manager"
CERTMANAGER_RELEASE="cert-manager"
CERTMANAGER_HELM_REPO_NAME="jetstack"
CERTMANAGER_HELM_REPO_URL="https://charts.jetstack.io"
HELM_TIMEOUT="300s"

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
echo "  suparShip — cert-manager install  (dev profile, ${CERTMANAGER_CHART_VERSION})"
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

kubectl cluster-info >/dev/null 2>&1 \
  || die "No cluster reachable. Run: task dev:cluster:bootstrap"

echo ""

# ── 2. Create namespace ───────────────────────────────────────────────────
info "Namespace '${CERTMANAGER_NAMESPACE}'..."
if kubectl get namespace "${CERTMANAGER_NAMESPACE}" >/dev/null 2>&1; then
  skip "already exists"
else
  kubectl create namespace "${CERTMANAGER_NAMESPACE}"
  ok "created"
fi
echo ""

# ── 3. Add / refresh Helm repo ────────────────────────────────────────────
info "Helm repo '${CERTMANAGER_HELM_REPO_NAME}'..."
if helm repo list 2>/dev/null | awk '{print $1}' | grep -qx "${CERTMANAGER_HELM_REPO_NAME}"; then
  skip "already added"
else
  helm repo add "${CERTMANAGER_HELM_REPO_NAME}" "${CERTMANAGER_HELM_REPO_URL}" >/dev/null
  ok "added (${CERTMANAGER_HELM_REPO_URL})"
fi

printf "  Updating repo index... "
helm repo update "${CERTMANAGER_HELM_REPO_NAME}" >/dev/null
echo "ok"
echo ""

# ── 4. Install or upgrade cert-manager ────────────────────────────────────
# crds.enabled=true installs the CRDs as part of the Helm release so they
# are upgraded together with the chart. This is the recommended approach for
# dev clusters.
info "cert-manager ${CERTMANAGER_CHART_VERSION} (helm upgrade --install)..."
echo "  This may take a few minutes on first run (image pull)."
echo ""

helm upgrade --install "${CERTMANAGER_RELEASE}" \
  "${CERTMANAGER_HELM_REPO_NAME}/cert-manager" \
  --namespace  "${CERTMANAGER_NAMESPACE}" \
  --version    "${CERTMANAGER_CHART_VERSION}" \
  --set        "crds.enabled=true" \
  --wait \
  --timeout    "${HELM_TIMEOUT}" \
  --atomic

ok "cert-manager installed / up-to-date"
echo ""

# ── 5. Confirm webhook rollout ────────────────────────────────────────────
info "Waiting for cert-manager webhook..."
kubectl rollout status deployment/cert-manager-webhook \
  -n "${CERTMANAGER_NAMESPACE}" \
  --timeout="${HELM_TIMEOUT}" >/dev/null
ok "cert-manager webhook is running"
echo ""

# ── Done ──────────────────────────────────────────────────────────────────
cat <<EOF
  ──────────────────────────────────────────────────────────────────
  cert-manager is ready.

  Namespace   ${CERTMANAGER_NAMESPACE}
  Version     ${CERTMANAGER_CHART_VERSION}

  Verify pods:
    kubectl get pods -n ${CERTMANAGER_NAMESPACE}

  Verify CRDs:
    kubectl get crds | grep cert-manager

  cert-manager docs:
    https://cert-manager.io/docs/

  Next step — install Kargo:
    task dev:cluster:kargo

EOF
