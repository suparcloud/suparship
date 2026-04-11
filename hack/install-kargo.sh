#!/usr/bin/env bash
# hack/install-kargo.sh — install Kargo into the suparShip dev cluster.
#
# Idempotent: uses `helm upgrade --install` so re-running is safe.
#
# Kargo is distributed as an OCI Helm chart via GitHub Container Registry —
# there is no separate Helm repo to add.
# Chart: oci://ghcr.io/akuity/kargo-charts/kargo
#
# Prerequisites (must be installed first):
#   - kind cluster:       task dev:cluster:bootstrap
#   - ArgoCD:             task dev:cluster:argocd
#   - cert-manager:       task dev:cluster:cert-manager   ← REQUIRED by Kargo (TLS certs)
#   - Argo Rollouts:      task dev:cluster:argo-rollouts  ← REQUIRED by Kargo (progressive delivery)
#
# When to use this:
#   Only needed when working on features that use real Kargo promotion
#   pipelines (Warehouse, Stage, Promotion CRs). Most contributors do
#   NOT need this — `task dev` (fake mode) is sufficient.
#
# Usage:
#   ./hack/install-kargo.sh      # run directly (ensure Argo Rollouts is installed first)
#   task dev:cluster:kargo       # preferred: installs Argo Rollouts then Kargo automatically
set -euo pipefail

# ── Pinned versions ───────────────────────────────────────────────────────
# Update these together when upgrading Kargo.
# Releases: https://github.com/akuity/kargo/releases
# OCI chart: oci://ghcr.io/akuity/kargo-charts/kargo
KARGO_CHART_VERSION="0.9.0"     # kargo OCI chart version → Kargo v0.9.0
KARGO_APP_VERSION="v0.9.0"
KARGO_OCI_CHART="oci://ghcr.io/akuity/kargo-charts/kargo"

# ── Config ────────────────────────────────────────────────────────────────
KARGO_NAMESPACE="kargo"
KARGO_RELEASE="kargo"
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
echo "  suparShip — Kargo install  (dev profile, ${KARGO_APP_VERSION})"
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

# Verify cert-manager CRDs are present (required by Kargo for TLS certificates).
if ! kubectl get crd certificates.cert-manager.io >/dev/null 2>&1; then
  die "cert-manager CRDs not found. Run: task dev:cluster:cert-manager"
fi
ok "cert-manager CRDs present"

# Verify Argo Rollouts CRDs are present (required by Kargo).
if ! kubectl get crd rollouts.argoproj.io >/dev/null 2>&1; then
  die "Argo Rollouts CRDs not found. Run: task dev:cluster:argo-rollouts"
fi
ok "Argo Rollouts CRDs present"

echo ""

# ── 2. Create namespace ───────────────────────────────────────────────────
info "Namespace '${KARGO_NAMESPACE}'..."
if kubectl get namespace "${KARGO_NAMESPACE}" >/dev/null 2>&1; then
  skip "already exists"
else
  kubectl create namespace "${KARGO_NAMESPACE}"
  ok "created"
fi
echo ""

# ── 3. Install or upgrade Kargo via OCI chart ─────────────────────────────
# Kargo is published as an OCI chart — no `helm repo add` needed.
info "Kargo ${KARGO_APP_VERSION} (OCI chart, helm upgrade --install)..."
echo "  Chart: ${KARGO_OCI_CHART}:${KARGO_CHART_VERSION}"
echo "  This may take a few minutes on first run (image pull)."
echo ""

helm upgrade --install "${KARGO_RELEASE}" \
  "${KARGO_OCI_CHART}" \
  --namespace  "${KARGO_NAMESPACE}" \
  --version    "${KARGO_CHART_VERSION}" \
  --set        "api.adminAccount.enabled=false" \
  --wait \
  --timeout    "${HELM_TIMEOUT}" \
  --atomic

ok "Kargo installed / up-to-date"
echo ""

# ── 4. Confirm controller rollout ─────────────────────────────────────────
info "Waiting for kargo-controller..."
kubectl rollout status deployment/kargo-controller \
  -n "${KARGO_NAMESPACE}" \
  --timeout="${HELM_TIMEOUT}" >/dev/null 2>&1 \
  && ok "kargo-controller is running" \
  || skip "kargo-controller rollout check skipped (may use different name — check: kubectl get pods -n ${KARGO_NAMESPACE})"
echo ""

# ── Done ──────────────────────────────────────────────────────────────────
cat <<EOF
  ──────────────────────────────────────────────────────────────────
  Kargo is ready.

  Namespace   ${KARGO_NAMESPACE}
  App version ${KARGO_APP_VERSION}
  Chart       ${KARGO_OCI_CHART}:${KARGO_CHART_VERSION}

  Verify pods:
    kubectl get pods -n ${KARGO_NAMESPACE}

  Verify CRDs:
    kubectl get crds | grep kargo

  Kargo docs:
    https://docs.kargo.io

EOF
