#!/usr/bin/env bash
# hack/install-reloader.sh — install Stakater Reloader into the suparShip
# dev cluster.
#
# Reloader watches ConfigMaps and Secrets and performs rolling restarts on
# Deployments (and StatefulSets, DaemonSets) that reference them when a change
# is detected. suparShip uses it to ensure pods automatically restart after
# ESO pulls updated secret values — preventing stale credentials without
# manual rollouts.
#
# To opt in, annotate a Deployment with:
#   reloader.stakater.com/auto: "true"
#
# Idempotent: uses `helm upgrade --install` so re-running is safe.
#
# Chart:   stakater/reloader
# Repo:    https://stakater.github.io/stakater-charts
# Source:  https://github.com/stakater/Reloader
#
# Usage:
#   ./hack/install-reloader.sh      # run directly
#   task dev:cluster:reloader       # preferred: via Taskfile
set -euo pipefail

# ── Pinned versions ────────────────────────────────────────────────────────
# https://github.com/stakater/Reloader/releases
RELOADER_CHART_VERSION="2.2.9"      # chart-v2.2.9 → app v1.0.26
RELOADER_APP_VERSION="v1.0.26"
RELOADER_CHART_REPO="https://stakater.github.io/stakater-charts"
RELOADER_CHART_NAME="reloader"
RELOADER_REPO_ALIAS="stakater"

# ── Config ─────────────────────────────────────────────────────────────────
RELOADER_NAMESPACE="stakater-reloader"
RELOADER_RELEASE="reloader"
HELM_TIMEOUT="120s"

# ── Repo root ──────────────────────────────────────────────────────────────
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# ── Helpers ────────────────────────────────────────────────────────────────
info()  { printf "  \033[0;36m%s\033[0m\n" "$*"; }
ok()    { printf "  \033[0;32m✓\033[0m  %s\n" "$*"; }
skip()  { printf "  \033[0;33m–\033[0m  %s\n" "$*"; }
die()   { printf "  \033[0;31mERROR:\033[0m %s\n" "$*" >&2; exit 1; }

# ── Banner ─────────────────────────────────────────────────────────────────
echo ""
echo "  suparShip — Stakater Reloader install  (${RELOADER_APP_VERSION})"
echo "  ──────────────────────────────────────────────────────────────────"
echo ""

# ── 1. Prereq checks ───────────────────────────────────────────────────────
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

# ── 2. Add / update Helm repo ──────────────────────────────────────────────
info "Helm repo '${RELOADER_REPO_ALIAS}'..."
if helm repo list 2>/dev/null | grep -q "^${RELOADER_REPO_ALIAS}"; then
  skip "already added — updating"
  helm repo update "${RELOADER_REPO_ALIAS}" >/dev/null
else
  helm repo add "${RELOADER_REPO_ALIAS}" "${RELOADER_CHART_REPO}"
  helm repo update "${RELOADER_REPO_ALIAS}" >/dev/null
  ok "added"
fi
echo ""

# ── 3. Create namespace ────────────────────────────────────────────────────
info "Namespace '${RELOADER_NAMESPACE}'..."
if kubectl get namespace "${RELOADER_NAMESPACE}" >/dev/null 2>&1; then
  skip "already exists"
else
  kubectl create namespace "${RELOADER_NAMESPACE}"
  ok "created"
fi
echo ""

# ── 4. Install or upgrade ──────────────────────────────────────────────────
info "Reloader ${RELOADER_APP_VERSION} (helm upgrade --install)..."
echo "  Chart: ${RELOADER_REPO_ALIAS}/${RELOADER_CHART_NAME}:${RELOADER_CHART_VERSION}"
echo ""

helm upgrade --install "${RELOADER_RELEASE}" \
  "${RELOADER_REPO_ALIAS}/${RELOADER_CHART_NAME}" \
  --namespace  "${RELOADER_NAMESPACE}" \
  --version    "${RELOADER_CHART_VERSION}" \
  --wait \
  --timeout    "${HELM_TIMEOUT}" \
  --atomic

ok "Reloader installed / up-to-date"
echo ""

# ── 5. Confirm rollout ─────────────────────────────────────────────────────
info "Waiting for reloader deployment..."
kubectl rollout status deployment/reloader-reloader \
  -n "${RELOADER_NAMESPACE}" \
  --timeout="${HELM_TIMEOUT}" >/dev/null 2>&1 \
  && ok "reloader is running" \
  || skip "rollout check skipped — verify: kubectl get pods -n ${RELOADER_NAMESPACE}"
echo ""

# ── Done ───────────────────────────────────────────────────────────────────
cat <<EOF
  ──────────────────────────────────────────────────────────────────
  Stakater Reloader is ready.

  Namespace    ${RELOADER_NAMESPACE}
  App version  ${RELOADER_APP_VERSION}
  Chart        ${RELOADER_REPO_ALIAS}/${RELOADER_CHART_NAME}:${RELOADER_CHART_VERSION}

  Usage: add this annotation to any Deployment to enable auto-restart
  on ConfigMap or Secret change:
    reloader.stakater.com/auto: "true"

  Verify pods:
    kubectl get pods -n ${RELOADER_NAMESPACE}

  Docs:
    https://github.com/stakater/Reloader

EOF
