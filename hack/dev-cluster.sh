#!/usr/bin/env bash
# hack/dev-cluster.sh — start backend (cluster mode) + Vite frontend.
#
# The backend connects to the suparship-dev kind cluster instead of using
# the fake/in-memory data provider. Use this when you need a real Kubernetes
# runtime to test features that cannot be exercised in fake mode:
#   - preview environment creation and deletion
#   - service promotion flows (staging → prod)
#   - runtime status and logs against real workloads
#   - ArgoCD sync and health integration
#
# This script is idempotent. It:
#   1. Runs cluster bootstrap (creates kind cluster + namespaces if needed)
#   2. Installs ArgoCD if the argocd-server Deployment is absent
#   3. Builds the Go binary
#   4. Starts the backend in Kubernetes mode (SUPARSHIP_DEV_MODE is unset)
#   5. Starts the Vite frontend dev server
#
# Ctrl+C stops backend + frontend. The kind cluster keeps running.
#
# Usage:
#   ./hack/dev-cluster.sh        # run directly
#   task dev:cluster             # preferred: via Taskfile
set -euo pipefail

CLUSTER_NAME="suparship-dev"
KUBE_CONTEXT="kind-${CLUSTER_NAME}"

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# ── Load .env (inherit SUPARSHIP_ADDR, SUPARSHIP_FRONTEND_PORT, etc.) ────
if [ -f .env ]; then
  # shellcheck disable=SC1091
  set -a; source .env; set +a
fi

# Cluster mode must NOT be activated with fake-mode vars.
# Unset them so the server falls through to config.ModeKubernetes.
unset SUPARSHIP_DEV_MODE SUPARSHIP_CLUSTER_MODE 2>/dev/null || true

# ── Config ────────────────────────────────────────────────────────────────
ADDR="${SUPARSHIP_ADDR:-:8080}"
BACKEND_PORT="${ADDR#:}"              # strip leading colon → "8080"
FRONTEND_PORT="${SUPARSHIP_FRONTEND_PORT:-5173}"

# ── 1. Bootstrap cluster (idempotent) ────────────────────────────────────
echo ""
echo "  suparShip — cluster dev  (pre-flight)"
echo "  ──────────────────────────────────────────────────────────────────"
echo ""
hack/bootstrap-cluster.sh

# ── 2. Ensure ArgoCD is installed ────────────────────────────────────────
# Check for the argocd-server Deployment as a quick proxy for "installed".
# When absent, run the full install script (helm upgrade --install, ~3-5 min
# on first run; near-instant on repeat runs once images are cached).
if kubectl get deployment argocd-server -n argocd >/dev/null 2>&1; then
  echo "  –  ArgoCD already installed — skipping"
  echo ""
else
  hack/install-argocd.sh
fi

# ── 3. Banner ─────────────────────────────────────────────────────────────
cat <<EOF
  ──────────────────────────────────────────────────────────────────
  suparShip — cluster dev  (backend → ${KUBE_CONTEXT})
  ──────────────────────────────────────────────────────────────────
  Backend   →  http://localhost:${BACKEND_PORT}
               local process, talks to kind cluster via KUBECONFIG
  Frontend  →  http://localhost:${FRONTEND_PORT}
  ArgoCD    →  kubectl port-forward svc/argocd-server -n argocd 8180:80
               then open http://localhost:8180

  Good for:
    previews · promotions · runtime status · real pod logs · ArgoCD sync

  Handy checks:
    kubectl get nodes
    kubectl get ns
    kubectl get pods -n argocd

  Ctrl+C stops backend + frontend. Cluster keeps running.
  To delete: task dev:cluster:delete

EOF

# ── 4. Build Go binary ────────────────────────────────────────────────────
printf "  [api] building... "
go build -o bin/suparship ./cmd/suparship
echo "ok"

# ── 5. npm install (first run only) ──────────────────────────────────────
if [ ! -d ui/node_modules ]; then
  echo "  [ui]  installing npm packages (first time, may take a moment)..."
  (cd ui && npm install --silent)
fi

# ── 6. Start backend in cluster mode ─────────────────────────────────────
# SUPARSHIP_DEV_MODE is unset → internal/config.Load() returns ModeKubernetes.
# The server reads KUBECONFIG (defaulting to ~/.kube/config), which was set
# to the kind cluster context in step 1.
SUPARSHIP_CORS_ORIGINS="http://localhost:${FRONTEND_PORT}" \
  ./bin/suparship server &
API_PID=$!

# ── 7. Clean shutdown on Ctrl+C ──────────────────────────────────────────
cleanup() {
  printf "\n  Stopping backend...\n"
  kill "$API_PID" 2>/dev/null || true
  wait "$API_PID" 2>/dev/null || true
  printf "  Cluster '%s' is still running.\n" "${CLUSTER_NAME}"
  printf "  Remove it with: task dev:cluster:delete\n\n"
}
trap cleanup EXIT INT TERM

# ── 8. Start frontend in foreground (blocks until Ctrl+C) ─────────────────
(cd ui && npm run dev)
