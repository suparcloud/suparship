#!/usr/bin/env bash
# hack/bootstrap-cluster.sh — create and configure the suparShip dev cluster.
#
# Idempotent: safe to run multiple times. Skips any step that is already done.
#
# What this does:
#   1. Verify required tools are installed (kind, kubectl, helm)
#   2. Create a kind cluster named "suparship-dev" if it does not exist
#   3. Set kubectl context to the new cluster
#   4. Create foundational Kubernetes namespaces
#
# What this does NOT do:
#   - Install ArgoCD or Kargo (separate step)
#   - Install an ingress controller (separate step)
#   - Deploy any application workloads
#
# Usage:
#   ./hack/bootstrap-cluster.sh      # run directly
#   task dev:cluster:bootstrap        # preferred: via Taskfile
set -euo pipefail

CLUSTER_NAME="suparship-dev"
KUBE_CONTEXT="kind-${CLUSTER_NAME}"
KIND_CONFIG="config/kind/cluster.yaml"

# Foundational namespaces — created now so subsequent install steps can
# target them without needing to create them again.
NAMESPACES=(
  suparship-system   # suparShip control-plane components
  argocd             # pre-create so ArgoCD install can target it later
)

# ── Repo root ────────────────────────────────────────────────────────────
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# ── Helpers ───────────────────────────────────────────────────────────────
info()  { printf "  \033[0;36m%s\033[0m\n" "$*"; }
ok()    { printf "  \033[0;32m✓\033[0m  %s\n" "$*"; }
skip()  { printf "  \033[0;33m–\033[0m  %s\n" "$*"; }
err()   { printf "  \033[0;31mERROR:\033[0m %s\n" "$*" >&2; }
die()   { err "$*"; exit 1; }

# ── Banner ────────────────────────────────────────────────────────────────
echo ""
echo "  suparShip — cluster bootstrap"
echo "  ──────────────────────────────────────────────────────────────────"
echo ""

# ── 1. Check prerequisites ────────────────────────────────────────────────
info "Checking prerequisites..."
missing=0

check_cmd() {
  local cmd="$1" install_url="$2"
  if command -v "$cmd" >/dev/null 2>&1; then
    ok "$cmd  ($(command -v "$cmd"))"
  else
    err "'$cmd' not found — install it and retry."
    printf "         Install: %s\n" "$install_url" >&2
    missing=$((missing + 1))
  fi
}

check_cmd kind    "https://kind.sigs.k8s.io/docs/user/quick-start/#installation"
check_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
check_cmd helm    "https://helm.sh/docs/intro/install/"

if [ "$missing" -gt 0 ]; then
  echo ""
  die "Missing $missing required tool(s). Install them and re-run this script."
fi
echo ""

# ── 2. Create kind cluster ────────────────────────────────────────────────
info "Cluster '${CLUSTER_NAME}'..."
if kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"; then
  skip "already exists — skipping creation"
else
  echo "  Creating (this takes ~30 seconds)..."
  kind create cluster --name "${CLUSTER_NAME}" --config "${KIND_CONFIG}"
  ok "cluster created"
fi
echo ""

# ── 3. Set kubectl context ────────────────────────────────────────────────
info "Setting kubectl context..."
kubectl config use-context "${KUBE_CONTEXT}" >/dev/null
ok "context set to '${KUBE_CONTEXT}'"
echo ""

# ── 4. Create namespaces ──────────────────────────────────────────────────
info "Creating namespaces..."
for ns in "${NAMESPACES[@]}"; do
  if kubectl get namespace "${ns}" >/dev/null 2>&1; then
    skip "${ns}  (already exists)"
  else
    kubectl create namespace "${ns}" >/dev/null
    ok "${ns}"
  fi
done
echo ""

# ── Done ──────────────────────────────────────────────────────────────────
cat <<EOF
  ──────────────────────────────────────────────────────────────────
  Bootstrap complete.

  Cluster     ${CLUSTER_NAME}
  Context     ${KUBE_CONTEXT}
  Namespaces  $(IFS=, ; echo "${NAMESPACES[*]}")

  Next steps:
    task dev:dns:setup     (once per machine) wildcard *.localhost DNS via dnsmasq
    task dev               start backend + frontend (still uses fake mode)
    kubectl get ns         verify namespaces

  To remove the cluster:
    task dev:cluster:delete

EOF
