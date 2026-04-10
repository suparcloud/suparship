#!/usr/bin/env bash
# hack/serve-cluster.sh — build + start backend (cluster mode) + Vite frontend.
#
# Assumes the suparship-dev kind cluster is already running and configured.
# Run `task dev:cluster` first if the cluster does not exist yet.
#
# This script is meant to be restarted freely during development — it only
# builds the Go binary and launches the two dev servers. The cluster is
# never touched here.
#
# Usage:
#   ./hack/serve-cluster.sh        # run directly
#   task dev:cluster:serve         # preferred: via Taskfile
#
# Ctrl+C stops backend + frontend. The kind cluster keeps running.
set -euo pipefail

CLUSTER_NAME="suparship-dev"
KUBE_CONTEXT="kind-${CLUSTER_NAME}"

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# ── Load .env + .env.cluster ─────────────────────────────────────────────
if [ -f .env ]; then
  # shellcheck disable=SC1091
  set -a; source .env; set +a
fi
if [ -f .env.cluster ]; then
  # shellcheck disable=SC1091
  set -a; source .env.cluster; set +a
fi

# Cluster mode must NOT be activated with fake-mode vars.
unset SUPARSHIP_DEV_MODE SUPARSHIP_CLUSTER_MODE 2>/dev/null || true

# ── Config ────────────────────────────────────────────────────────────────
ADDR="${SUPARSHIP_ADDR:-:8080}"
BACKEND_PORT="${ADDR#:}"
FRONTEND_PORT="${SUPARSHIP_FRONTEND_PORT:-5173}"

# ── Verify cluster is reachable ───────────────────────────────────────────
if ! kubectl --context "${KUBE_CONTEXT}" cluster-info >/dev/null 2>&1; then
  printf "\n  \033[0;31mERROR:\033[0m Cluster '%s' is not reachable.\n" "${CLUSTER_NAME}"
  printf "  Run the cluster setup first:\n"
  printf "    task dev:cluster\n\n"
  exit 1
fi

# ── npm install (first run only) ──────────────────────────────────────────
if [ ! -d ui/node_modules ]; then
  echo "  [ui]  installing npm packages (first time, may take a moment)..."
  (cd ui && npm install --silent)
fi

# ── Banner ────────────────────────────────────────────────────────────────
cat <<EOF

  suparShip — cluster dev  (backend → ${KUBE_CONTEXT})
  ──────────────────────────────────────────────────────────────────
  Backend   →  http://localhost:${BACKEND_PORT}
               local process, talks to kind cluster via KUBECONFIG
  Frontend  →  http://localhost:${FRONTEND_PORT}
  Templates →  ${REPO_ROOT}/templates  (local — no cluster push needed)
  ArgoCD    →  http://argocd.localhost:8880  (gitops / admin)
  Gitea     →  http://gitea.localhost:8880  (gitops / gitops-dev-only)
               gitops repo: http://gitea.localhost:8880/gitops/gitops

  Ctrl+C stops backend + frontend. Cluster keeps running.
  To delete the cluster: task dev:cluster:delete

EOF

# ── Build Go binary ───────────────────────────────────────────────────────
printf "  [api] building... "
go build -o bin/suparship ./cmd/suparship
echo "ok"

# ── Start backend in cluster mode ────────────────────────────────────────
# SUPARSHIP_DEV_MODE is unset → internal/config.Load() returns ModeKubernetes.
# SUPARSHIP_TEMPLATES_DIR points at the local checkout so template edits are
# picked up on server restart without pushing ConfigMaps to the cluster.
SUPARSHIP_CORS_ORIGINS="http://localhost:${FRONTEND_PORT}" \
  SUPARSHIP_TEMPLATES_DIR="${REPO_ROOT}/templates" \
  ./bin/suparship server &
API_PID=$!

# ── Clean shutdown on Ctrl+C ─────────────────────────────────────────────
cleanup() {
  printf "\n  Stopping backend...\n"
  kill "$API_PID" 2>/dev/null || true
  wait "$API_PID" 2>/dev/null || true
  printf "  Cluster '%s' is still running.\n" "${CLUSTER_NAME}"
  printf "  Remove it with: task dev:cluster:delete\n\n"
}
trap cleanup EXIT INT TERM

# ── Start frontend in foreground (blocks until Ctrl+C) ───────────────────
(cd ui && npm run dev)
