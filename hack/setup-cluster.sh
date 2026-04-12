#!/usr/bin/env bash
# hack/setup-cluster.sh — idempotent cluster infrastructure setup.
#
# Brings up the suparship-dev kind cluster with all required components:
#   0. Wildcard *.localhost DNS via dnsmasq (machine-level, one-time)
#   1. kind cluster + namespaces
#   2. NGINX ingress controller
#   3. ArgoCD
#   4. Gitea + gitops repo registered with ArgoCD
#   5. Gitops repo initialisation (charts + gitops-output skeleton)
#   6. Demo data seed
#   7. cert-manager (Kargo dependency — TLS certificate management)
#   8. Argo Rollouts (Kargo dependency — progressive delivery primitives)
#   9. Kargo (GitOps-native promotion engine)
#
# Credentials (ArgoCD, Gitea) are written to .env.cluster (git-ignored)
# and printed as a summary at the end.
#
# Safe to re-run at any time. Each step checks whether it is already
# present and skips if so. Exits 0 when the cluster is ready.
#
# Usage:
#   ./hack/setup-cluster.sh        # run directly
#   task dev:cluster               # preferred: via Taskfile
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# ── Load .env ─────────────────────────────────────────────────────────────
if [ -f .env ]; then
  # shellcheck disable=SC1091
  set -a; source .env; set +a
fi

# ── Preflight checks ─────────────────────────────────────────────────────
hack/preflight.sh cluster

# ── Header ────────────────────────────────────────────────────────────────
echo ""
echo "  suparShip — cluster setup  (idempotent)"
echo "  ──────────────────────────────────────────────────────────────────"
echo ""

# ── 0. DNS setup (machine-level, one-time — idempotent, safe to re-run) ──
# On Linux, setup-dns.sh prints manual instructions and exits 0.
# On macOS, it installs/configures dnsmasq and may prompt for sudo.
hack/setup-dns.sh

# ── 1. Bootstrap cluster (creates kind cluster + namespaces if absent) ────
hack/bootstrap-cluster.sh

# ── 1b. Local container registry (kind-registry on localhost:5001) ─────────
hack/install-registry.sh

# ── 2. NGINX ingress controller ───────────────────────────────────────────
if kubectl get deployment ingress-nginx-controller \
     -n ingress-nginx >/dev/null 2>&1; then
  echo "  –  NGINX ingress controller already installed — skipping"
  echo ""
else
  hack/install-ingress.sh
fi

# ── 3. ArgoCD ─────────────────────────────────────────────────────────────
if kubectl get deployment argocd-server -n argocd >/dev/null 2>&1; then
  echo "  –  ArgoCD already installed — skipping"
  echo ""
else
  hack/install-argocd.sh
fi

# ── 4. Gitea + gitops repo ────────────────────────────────────────────────
if helm status gitea -n gitea >/dev/null 2>&1; then
  echo "  –  Gitea already installed — skipping"
  echo ""
else
  hack/install-gitea.sh
fi

# ── 5. Gitops repo initialisation ─────────────────────────────────────────
# init-gitops-repo.sh is idempotent: it pushes the skeleton but will not
# overwrite commits already present in the remote repo.
export GITEA_HOST_URL="${GITEA_HOST_URL:-http://gitea.localhost:8880}"
export GITEA_ADMIN_USER="${GITEA_ADMIN_USER:-gitops}"
export GITEA_ADMIN_PASS="${GITEA_ADMIN_PASS:-gitops-dev-only}"
export GITOPS_REPO_ORG="${GITOPS_REPO_ORG:-gitops}"
export GITOPS_REPO_NAME="${GITOPS_REPO_NAME:-gitops}"
export GITEA_CLUSTER_URL="${GITEA_CLUSTER_URL:-http://gitea-http.gitea.svc.cluster.local:3000}"
export REPO_ROOT
hack/init-gitops-repo.sh

# ── 6. Seed demo data (idempotent) ───────────────────────────────────────
hack/seed.sh

# ── 7. cert-manager (required by Kargo for TLS) ───────────────────────────
if kubectl get deployment cert-manager \
     -n cert-manager >/dev/null 2>&1; then
  echo "  –  cert-manager already installed — skipping"
  echo ""
else
  hack/install-cert-manager.sh
fi

# ── 8. Argo Rollouts (required by Kargo) ─────────────────────────────────
if kubectl get deployment argo-rollouts \
     -n argo-rollouts >/dev/null 2>&1; then
  echo "  –  Argo Rollouts already installed — skipping"
  echo ""
else
  hack/install-argo-rollouts.sh
fi

# ── 9. Kargo (GitOps-native promotion engine) ─────────────────────────────
if helm status kargo -n kargo >/dev/null 2>&1; then
  echo "  –  Kargo already installed — skipping"
  echo ""
else
  hack/install-kargo.sh
fi

# ── 10. Color-app source repo in Gitea (optional, for demo completeness) ──
hack/init-color-app-repo.sh

# ── 11. Admin credentials check ──────────────────────────────────────────
if ! kubectl get secret suparship-admin-auth -n suparship-system >/dev/null 2>&1; then
  echo ""
  printf "  \033[0;33mWARNING:\033[0m No admin credentials found in the cluster.\n"
  printf "  Run once to enable login:\n"
  printf "    go build -o bin/suparship ./cmd/suparship\n"
  printf "    ./bin/suparship admin bootstrap\n"
  echo ""
fi

# ── Done — print credential summary ──────────────────────────────────────
echo ""
echo "  ──────────────────────────────────────────────────────────────────"
echo "  Cluster is ready."
echo ""
if [ -f .env.cluster ]; then
  echo "  Credentials written to .env.cluster (git-ignored):"
  echo ""
  grep -v '^#' .env.cluster | grep -v '^$' | while IFS='=' read -r key val; do
    printf "    %-40s %s\n" "${key}" "${val}"
  done
  echo ""
else
  printf "  \033[0;33mWARN:\033[0m .env.cluster not found — credentials may not have been written.\n"
  echo ""
fi
echo "  Start the dev servers with:"
echo "    task dev:cluster:serve"
echo "  ──────────────────────────────────────────────────────────────────"
echo ""
