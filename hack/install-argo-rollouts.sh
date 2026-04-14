#!/usr/bin/env bash
# hack/install-argo-rollouts.sh — install Argo Rollouts into the suparShip dev cluster.
#
# Argo Rollouts is a required dependency for Kargo: it provides the
# progressive-delivery primitives (canary, blue/green) that Kargo's
# `rollout-update` promotion step orchestrates.
#
# Idempotent: uses `helm upgrade --install` so re-running is safe.
# Requires the cluster to exist:
#   task dev:cluster:bootstrap
#
# Usage:
#   ./hack/install-argo-rollouts.sh       # run directly
#   task dev:cluster:argo-rollouts        # preferred: via Taskfile
#   task dev:cluster:kargo                # installs rollouts first, then kargo
set -euo pipefail

# ── Pinned versions ───────────────────────────────────────────────────────
# Update these together when upgrading Argo Rollouts.
# Chart changelog: https://github.com/argoproj/argo-helm/releases
ROLLOUTS_CHART_VERSION="2.37.6"   # argo-rollouts Helm chart → Rollouts v1.7.6

# ── Config ────────────────────────────────────────────────────────────────
ROLLOUTS_NAMESPACE="argo-rollouts"
ROLLOUTS_RELEASE="argo-rollouts"
ARGO_HELM_REPO_NAME="argo"
ARGO_HELM_REPO_URL="https://argoproj.github.io/argo-helm"
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
echo "  suparShip — Argo Rollouts install  (dev profile, chart ${ROLLOUTS_CHART_VERSION})"
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
info "Namespace '${ROLLOUTS_NAMESPACE}'..."
if kubectl get namespace "${ROLLOUTS_NAMESPACE}" >/dev/null 2>&1; then
  skip "already exists"
else
  kubectl create namespace "${ROLLOUTS_NAMESPACE}"
  ok "created"
fi
echo ""

# ── 3. Add / refresh Helm repo ────────────────────────────────────────────
info "Helm repo '${ARGO_HELM_REPO_NAME}'..."
if helm repo list 2>/dev/null | awk '{print $1}' | grep -qx "${ARGO_HELM_REPO_NAME}"; then
  skip "already added"
else
  helm repo add "${ARGO_HELM_REPO_NAME}" "${ARGO_HELM_REPO_URL}" >/dev/null
  ok "added (${ARGO_HELM_REPO_URL})"
fi

printf "  Updating repo index... "
helm repo update "${ARGO_HELM_REPO_NAME}" >/dev/null
echo "ok"
echo ""

# ── 4. Install or upgrade Argo Rollouts ───────────────────────────────────
info "Argo Rollouts (helm upgrade --install, chart ${ROLLOUTS_CHART_VERSION})..."
echo "  This may take a few minutes on first run (image pull)."
echo ""

helm upgrade --install "${ROLLOUTS_RELEASE}" \
  "${ARGO_HELM_REPO_NAME}/argo-rollouts" \
  --namespace  "${ROLLOUTS_NAMESPACE}" \
  --version    "${ROLLOUTS_CHART_VERSION}" \
  --set        "dashboard.enabled=true" \
  --wait \
  --timeout    "${HELM_TIMEOUT}" \
  --atomic

ok "Argo Rollouts installed / up-to-date"
echo ""

# ── 5. Confirm controller rollout ─────────────────────────────────────────
info "Waiting for argo-rollouts controller..."
kubectl rollout status deployment/argo-rollouts \
  -n "${ROLLOUTS_NAMESPACE}" \
  --timeout="${HELM_TIMEOUT}" >/dev/null
ok "argo-rollouts controller is running"
echo ""

# ── Done ──────────────────────────────────────────────────────────────────
cat <<EOF
  ──────────────────────────────────────────────────────────────────
  Argo Rollouts is ready.

  Namespace   ${ROLLOUTS_NAMESPACE}
  Chart       argo-rollouts ${ROLLOUTS_CHART_VERSION}

  Verify pods:
    kubectl get pods -n ${ROLLOUTS_NAMESPACE}

  Verify CRDs:
    kubectl get crds | grep rollout

  Argo Rollouts docs:
    https://argo-rollouts.readthedocs.io

  Next step — install Kargo:
    task dev:cluster:kargo

EOF
