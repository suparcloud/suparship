#!/usr/bin/env bash
# hack/seed.sh — apply deterministic demo data to the suparShip cluster.
#
# Creates (or updates) three ConfigMaps in the suparship-system namespace:
#   suparship-org-config      — default org (admins team, org_admin binding)
#   suparship-project-demo    — demo project (hello service, staging + prod)
#   suparship-preview-pr-42   — demo preview environment
#
# Idempotent: uses kubectl apply, safe to run multiple times.
# The seeded data mirrors internal/fake/seed.go so the UI looks consistent
# across fake mode (task dev) and cluster mode (task dev:cluster).
#
# Prerequisites:
#   task dev:cluster:bootstrap   # cluster and suparship-system namespace must exist
#
# Admin credentials are NOT created here — they require a bcrypt hash.
# Run once per cluster:
#   ./bin/suparship admin bootstrap
#
# Usage:
#   ./hack/seed.sh       # run directly
#   task seed            # preferred: via Taskfile
set -euo pipefail

NAMESPACE="suparship-system"
SEED_DIR="config/seed"

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# ── Helpers ───────────────────────────────────────────────────────────────
ok()  { printf "  \033[0;32m✓\033[0m  %s\n" "$*"; }
die() { printf "  \033[0;31mERROR:\033[0m %s\n" "$*" >&2; exit 1; }

# ── Prereqs ───────────────────────────────────────────────────────────────
command -v kubectl >/dev/null 2>&1 || die "'kubectl' not found."
kubectl get namespace "$NAMESPACE" >/dev/null 2>&1 \
  || die "Namespace '$NAMESPACE' not found. Run: task dev:cluster:bootstrap"

echo ""
echo "  suparShip — seed demo data  (kubectl apply -f ${SEED_DIR}/)"
echo "  ──────────────────────────────────────────────────────────────────"
echo ""

# ── Apply all seed manifests ──────────────────────────────────────────────
kubectl apply -f "${SEED_DIR}/" --namespace="${NAMESPACE}" >/dev/null

ok "suparship-org-config          (default org, admins team)"
ok "suparship-project-demo        (hello service, staging + prod)"
ok "suparship-preview-pr-42       (demo preview for demo/hello)"

# ── Admin credentials reminder ────────────────────────────────────────────
echo ""
if kubectl get secret suparship-admin-auth -n "$NAMESPACE" >/dev/null 2>&1; then
  ok "suparship-admin-auth          (already exists)"
else
  printf "  \033[0;33m!\033[0m  No admin credentials found.\n"
  printf "      Build and run once per cluster:\n"
  printf "        go build -o bin/suparship ./cmd/suparship\n"
  printf "        ./bin/suparship admin bootstrap\n"
fi

cat <<EOF

  ──────────────────────────────────────────────────────────────────
  Seed complete.

  Verify:
    kubectl get configmaps -n ${NAMESPACE} -l suparship.io/type=project
    kubectl get configmaps -n ${NAMESPACE} -l suparship.io/type=preview

EOF
