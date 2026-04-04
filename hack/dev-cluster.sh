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
#   - GitOps commits to Gitea, ArgoCD sync and deploy
#
# This script is idempotent. It:
#   1. Runs cluster bootstrap (creates kind cluster + namespaces if needed)
#   2. Installs NGINX ingress controller if absent
#   3. Installs ArgoCD if the argocd-server Deployment is absent
#   4. Installs Gitea + creates gitops repo + registers with ArgoCD if absent
#   5. Seeds demo data
#   6. Builds the Go binary
#   7. Starts the backend in Kubernetes mode (SUPARSHIP_DEV_MODE is unset)
#   8. Starts the Vite frontend dev server
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

# ── Preflight checks ─────────────────────────────────────────────────────
hack/preflight.sh cluster

# ── 1. Bootstrap cluster (idempotent) ────────────────────────────────────
echo ""
echo "  suparShip — cluster dev  (pre-flight)"
echo "  ──────────────────────────────────────────────────────────────────"
echo ""
hack/bootstrap-cluster.sh

# ── 2. Ensure NGINX ingress controller is installed ──────────────────────
if kubectl get deployment ingress-nginx-controller \
     -n ingress-nginx >/dev/null 2>&1; then
  echo "  –  NGINX ingress controller already installed — skipping"
  echo ""
else
  hack/install-ingress.sh
fi

# ── 3. Ensure ArgoCD is installed ────────────────────────────────────────
# Check for the argocd-server Deployment as a quick proxy for "installed".
# When absent, run the full install script (helm upgrade --install, ~3-5 min
# on first run; near-instant on repeat runs once images are cached).
if kubectl get deployment argocd-server -n argocd >/dev/null 2>&1; then
  echo "  –  ArgoCD already installed — skipping"
  echo ""
else
  hack/install-argocd.sh
fi

# ── 4. Ensure Gitea is installed + gitops repo exists ────────────────────
if helm status gitea -n gitea >/dev/null 2>&1; then
  echo "  –  Gitea already installed — skipping"
  echo ""
else
  hack/install-gitea.sh
fi

# ── 5. Seed demo data (idempotent) ───────────────────────────────────────
hack/seed.sh

# ── 6. Admin credentials check ───────────────────────────────────────────
# Seed does not create auth credentials (they require a bcrypt hash).
# Warn once if bootstrap has not been run; the server will start but login
# will fail until the secret exists.
if ! kubectl get secret suparship-admin-auth -n suparship-system >/dev/null 2>&1; then
  echo ""
  printf "  \033[0;33mWARNING:\033[0m No admin credentials found in the cluster.\n"
  printf "  Run once to enable login:\n"
  printf "    go build -o bin/suparship ./cmd/suparship\n"
  printf "    ./bin/suparship admin bootstrap\n"
  echo ""
fi

# ── 7. Banner ─────────────────────────────────────────────────────────────
cat <<EOF
  ──────────────────────────────────────────────────────────────────
  suparShip — cluster dev  (backend → ${KUBE_CONTEXT})
  ──────────────────────────────────────────────────────────────────
  Backend   →  http://localhost:${BACKEND_PORT}
               local process, talks to kind cluster via KUBECONFIG
  Frontend  →  http://localhost:${FRONTEND_PORT}
  ArgoCD    →  http://argocd.localhost:8880  (gitops / admin)
  Gitea     →  http://gitea.localhost:8880  (gitops / gitops-dev-only)
               gitops repo: http://gitea.localhost:8880/gitops/gitops

  Good for:
    previews · promotions · runtime status · real pod logs · ArgoCD sync

  DNS (run once per machine — no /etc/hosts needed after this):
    task dev:dns:setup             wildcard *.localhost → 127.0.0.1
    Without it: manually add to /etc/hosts →
      127.0.0.1 gitea.localhost argocd.localhost

  Handy checks:
    kubectl get nodes
    kubectl get ns
    kubectl get pods -n argocd
    kubectl get pods -n gitea

  Ctrl+C stops backend + frontend. Cluster keeps running.
  To delete: task dev:cluster:delete

EOF

# ── 8. Build Go binary ────────────────────────────────────────────────────
printf "  [api] building... "
go build -o bin/suparship ./cmd/suparship
echo "ok"

# ── 9. npm install (first run only) ──────────────────────────────────────
if [ ! -d ui/node_modules ]; then
  echo "  [ui]  installing npm packages (first time, may take a moment)..."
  (cd ui && npm install --silent)
fi

# ── 10. Start backend in cluster mode ────────────────────────────────────
# SUPARSHIP_DEV_MODE is unset → internal/config.Load() returns ModeKubernetes.
# The server reads KUBECONFIG (defaulting to ~/.kube/config), which was set
# to the kind cluster context in step 1.
SUPARSHIP_CORS_ORIGINS="http://localhost:${FRONTEND_PORT}" \
  ./bin/suparship server &
API_PID=$!

# ── 11. Clean shutdown on Ctrl+C ─────────────────────────────────────────
cleanup() {
  printf "\n  Stopping backend...\n"
  kill "$API_PID" 2>/dev/null || true
  wait "$API_PID" 2>/dev/null || true
  printf "  Cluster '%s' is still running.\n" "${CLUSTER_NAME}"
  printf "  Remove it with: task dev:cluster:delete\n\n"
}
trap cleanup EXIT INT TERM

# ── 12. Start frontend in foreground (blocks until Ctrl+C) ────────────────
(cd ui && npm run dev)
