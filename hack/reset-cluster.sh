#!/usr/bin/env bash
# hack/reset-cluster.sh — remove suparShip seed data from the cluster.
#
# Deletes only the three ConfigMaps applied by hack/seed.sh:
#   suparship-org-config
#   suparship-project-demo
#   suparship-preview-pr-42
#
# What this does NOT touch:
#   - the kind cluster itself        (use: task dev:cluster:delete)
#   - ArgoCD installation
#   - admin auth Secret              (suparship-admin-auth)
#   - any namespaces
#   - any ConfigMaps you created yourself
#
# After running this, restore demo data with:
#   task seed              (or task dev:cluster which seeds automatically)
#
# Usage:
#   ./hack/reset-cluster.sh      # run directly
#   task dev:cluster:reset       # preferred: via Taskfile
set -euo pipefail

NAMESPACE="suparship-system"

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# ── Helpers ───────────────────────────────────────────────────────────────
ok()   { printf "  \033[0;32m✓\033[0m  deleted    %s\n" "$*"; }
skip() { printf "  \033[0;33m–\033[0m  not found  %s  (already clean)\n" "$*"; }
die()  { printf "  \033[0;31mERROR:\033[0m %s\n" "$*" >&2; exit 1; }

command -v kubectl >/dev/null 2>&1 || die "'kubectl' not found."

echo ""
echo "  suparShip — reset cluster seed data"
echo "  ──────────────────────────────────────────────────────────────────"
echo ""

# If the namespace does not exist there is nothing to clean.
if ! kubectl get namespace "$NAMESPACE" >/dev/null 2>&1; then
  echo "  Namespace '$NAMESPACE' not found — nothing to clean."
  echo ""
  exit 0
fi

# ── Delete seed ConfigMaps ────────────────────────────────────────────────
delete_cm() {
  local name="$1"
  if kubectl get configmap "$name" -n "$NAMESPACE" >/dev/null 2>&1; then
    kubectl delete configmap "$name" -n "$NAMESPACE" >/dev/null
    ok "${NAMESPACE}/${name}"
  else
    skip "${NAMESPACE}/${name}"
  fi
}

delete_cm suparship-org-config
delete_cm suparship-project-demo
delete_cm suparship-preview-pr-42

cat <<EOF

  ──────────────────────────────────────────────────────────────────
  Seed data removed.

  Restore:  task seed
  Full dev: task dev:cluster   (seeds automatically on startup)

  To wipe the whole cluster:
    task dev:cluster:delete

EOF
