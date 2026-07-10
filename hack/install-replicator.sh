#!/usr/bin/env bash
# hack/install-replicator.sh — install Stakater Replicator (kubernetes-replicator)
# into the suparship dev cluster.
#
# Replicator watches ConfigMaps and Secrets annotated for replication and
# automatically copies them into target namespaces. suparship uses it to
# propagate upper-level (Org, Environment, Project) env-var ConfigMaps from
# suparship-system into each app namespace without duplicating manifests.
#
# Idempotent: uses `helm upgrade --install` so re-running is safe.
#
# Chart:   mittwald/kubernetes-replicator
# Repo:    https://helm.mittwald.de
# Source:  https://github.com/mittwald/kubernetes-replicator
#
# Usage:
#   ./hack/install-replicator.sh      # run directly
#   task dev:cluster:replicator       # preferred: via Taskfile
set -euo pipefail

# ── Pinned versions ────────────────────────────────────────────────────────
# https://github.com/mittwald/kubernetes-replicator/releases
REPLICATOR_CHART_VERSION="2.12.3"
REPLICATOR_APP_VERSION="v2.12.3"
REPLICATOR_CHART_REPO="https://helm.mittwald.de"
REPLICATOR_CHART_NAME="kubernetes-replicator"
REPLICATOR_REPO_ALIAS="mittwald"

# ── Config ─────────────────────────────────────────────────────────────────
REPLICATOR_NAMESPACE="stakater-replicator"
REPLICATOR_RELEASE="kubernetes-replicator"
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
echo "  suparship — Stakater Replicator install  (${REPLICATOR_APP_VERSION})"
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
info "Helm repo '${REPLICATOR_REPO_ALIAS}'..."
if helm repo list 2>/dev/null | grep -q "^${REPLICATOR_REPO_ALIAS}"; then
  skip "already added — updating"
  helm repo update "${REPLICATOR_REPO_ALIAS}" >/dev/null
else
  helm repo add "${REPLICATOR_REPO_ALIAS}" "${REPLICATOR_CHART_REPO}"
  helm repo update "${REPLICATOR_REPO_ALIAS}" >/dev/null
  ok "added"
fi
echo ""

# ── 3. Create namespace ────────────────────────────────────────────────────
info "Namespace '${REPLICATOR_NAMESPACE}'..."
if kubectl get namespace "${REPLICATOR_NAMESPACE}" >/dev/null 2>&1; then
  skip "already exists"
else
  kubectl create namespace "${REPLICATOR_NAMESPACE}"
  ok "created"
fi
echo ""

# ── 4. Install or upgrade ──────────────────────────────────────────────────
info "Replicator ${REPLICATOR_APP_VERSION} (helm upgrade --install)..."
echo "  Chart: ${REPLICATOR_REPO_ALIAS}/${REPLICATOR_CHART_NAME}:${REPLICATOR_CHART_VERSION}"
echo ""

helm upgrade --install "${REPLICATOR_RELEASE}" \
  "${REPLICATOR_REPO_ALIAS}/${REPLICATOR_CHART_NAME}" \
  --namespace  "${REPLICATOR_NAMESPACE}" \
  --version    "${REPLICATOR_CHART_VERSION}" \
  --wait \
  --timeout    "${HELM_TIMEOUT}" \
  --atomic

ok "Replicator installed / up-to-date"
echo ""

# ── 5. Confirm rollout ─────────────────────────────────────────────────────
info "Waiting for kubernetes-replicator deployment..."
kubectl rollout status deployment/kubernetes-replicator \
  -n "${REPLICATOR_NAMESPACE}" \
  --timeout="${HELM_TIMEOUT}" >/dev/null 2>&1 \
  && ok "kubernetes-replicator is running" \
  || skip "rollout check skipped — verify: kubectl get pods -n ${REPLICATOR_NAMESPACE}"
echo ""

# ── Done ───────────────────────────────────────────────────────────────────
cat <<EOF
  ──────────────────────────────────────────────────────────────────
  Stakater Replicator is ready.

  Namespace    ${REPLICATOR_NAMESPACE}
  App version  ${REPLICATOR_APP_VERSION}
  Chart        ${REPLICATOR_REPO_ALIAS}/${REPLICATOR_CHART_NAME}:${REPLICATOR_CHART_VERSION}

  Usage: annotate a ConfigMap or Secret in suparship-system with:
    replicator.v1.mittwald.de/replicate-to: ".*"

  Verify pods:
    kubectl get pods -n ${REPLICATOR_NAMESPACE}

  Docs:
    https://github.com/mittwald/kubernetes-replicator

EOF
