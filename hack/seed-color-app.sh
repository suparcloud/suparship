#!/usr/bin/env bash
# hack/seed-color-app.sh — create the color-app demo via the suparship API.
#
# Prerequisites:
#   1. Dev cluster running:           task dev:cluster
#   2. color-app image pushed:        task demo:color-app:push VERSION=0.1.0
#   3. suparship server running:      task dev:cluster:serve   (in another terminal)
#
# Admin credentials are auto-bootstrapped if not already present. The
# generated password is persisted to .env.cluster for subsequent runs.
#
# The script authenticates as admin, creates the color-app in the demo project,
# and prints the expected URLs. The publisher commits Kargo CRs + ArgoCD
# manifests to the gitops repo automatically.
#
# Usage:
#   ./hack/seed-color-app.sh         # run directly
#   task dev:cluster:seed-color-app   # preferred: via Taskfile
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# ── Load credentials ──────────────────────────────────────────────────────
if [ -f .env ]; then
  set -a; source .env; set +a
fi
if [ -f .env.cluster ]; then
  set -a; source .env.cluster; set +a
fi

# shellcheck source=hack/write-dev-credentials.sh
source hack/write-dev-credentials.sh

API_BASE="${SUPARSHIP_API_BASE:-http://localhost:8080}"
ADMIN_USER="${SUPARSHIP_ADMIN_USER:-admin}"
ADMIN_PASS="${SUPARSHIP_ADMIN_PASS:-}"

# Registry: in-cluster name used by pods when pulling images.
REGISTRY_HOST="${REGISTRY_HOST:-kind-registry:5000}"

# ── Helpers ───────────────────────────────────────────────────────────────
info()  { printf "  \033[0;36m%s\033[0m\n" "$*"; }
ok()    { printf "  \033[0;32m✓\033[0m  %s\n" "$*"; }
skip()  { printf "  \033[0;33m–\033[0m  %s\n" "$*"; }
die()   { printf "  \033[0;31mERROR:\033[0m %s\n" "$*" >&2; exit 1; }

echo ""
echo "  suparship — seed color-app demo"
echo "  ──────────────────────────────────────────────────────────────────"
echo ""

# ── 1. Wait for API ──────────────────────────────────────────────────────
info "Waiting for suparship API at ${API_BASE}..."
for i in $(seq 1 30); do
  if curl -sf "${API_BASE}/healthz" >/dev/null 2>&1; then
    ok "API is ready"
    break
  fi
  if [ "$i" -eq 30 ]; then
    die "API not reachable at ${API_BASE}/healthz after 30 attempts. Is the server running?"
  fi
  sleep 1
done
echo ""

# ── 1b. Ensure admin credentials exist ──────────────────────────────────
# If SUPARSHIP_ADMIN_PASS is empty, bootstrap (or reset) the admin user
# and persist the password to .env.cluster for this run and future runs.
if [ -z "$ADMIN_PASS" ]; then
  info "SUPARSHIP_ADMIN_PASS not set — bootstrapping admin credentials..."

  # Build the binary if it doesn't exist yet.
  if [ ! -f bin/suparship ]; then
    info "Building suparship binary..."
    go build -o bin/suparship ./cmd/suparship
    ok "binary built"
  fi

  # Try bootstrap; if credentials already exist, reset the password instead.
  BOOTSTRAP_OUT=$(./bin/suparship admin bootstrap --force 2>&1) || true
  ADMIN_PASS=$(echo "$BOOTSTRAP_OUT" | grep 'Password:' | sed 's/.*Password: //' | tr -d '[:space:]')

  if [ -z "$ADMIN_PASS" ]; then
    die "Could not extract admin password from bootstrap output."
  fi

  write_credential SUPARSHIP_ADMIN_USER "${ADMIN_USER}"
  write_credential SUPARSHIP_ADMIN_PASS "${ADMIN_PASS}"
  ok "admin credentials bootstrapped and saved to .env.cluster"
  echo ""
fi

info "Authenticating as '${ADMIN_USER}'..."
COOKIE_JAR=$(mktemp)
trap 'rm -f "$COOKIE_JAR"' EXIT

LOGIN_RESP=$(curl -sf -X POST "${API_BASE}/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -c "$COOKIE_JAR" \
  -d "{\"username\":\"${ADMIN_USER}\",\"password\":\"${ADMIN_PASS}\"}" \
  -w "\n%{http_code}" 2>&1) || true

LOGIN_CODE=$(echo "$LOGIN_RESP" | tail -1)
if [ "$LOGIN_CODE" != "200" ]; then
  die "Login failed (HTTP ${LOGIN_CODE}). Check SUPARSHIP_ADMIN_PASS."
fi
ok "authenticated"
echo ""

# ── 3. Check if color-app already exists ──────────────────────────────────
info "Checking if color-app already exists in project 'demo'..."
CHECK_CODE=$(curl -sf -o /dev/null -w "%{http_code}" \
  -b "$COOKIE_JAR" \
  "${API_BASE}/api/v1/projects/demo/apps/color-app" 2>/dev/null) || CHECK_CODE="000"

if [ "$CHECK_CODE" = "200" ]; then
  skip "color-app already exists — skipping creation"
  echo ""
  cat <<EOF
  ──────────────────────────────────────────────────────────────────
  color-app already seeded.

  Staging:  http://color-app.staging.localhost:8880
  Prod:     http://color-app.prod.localhost:8880
  API:      http://color-app.staging.localhost:8880/api/color

EOF
  exit 0
fi
echo ""

# ── 4. Create the color-app ──────────────────────────────────────────────
info "Creating color-app in project 'demo'..."
CREATE_RESP=$(curl -s -X POST "${API_BASE}/api/v1/projects/demo/apps" \
  -H "Content-Type: application/json" \
  -b "$COOKIE_JAR" \
  -w "\n%{http_code}" \
  -d "{
    \"name\": \"color-app\",
    \"displayName\": \"Color App\",
    \"description\": \"Demo app that displays a color — for Kargo promotion demos.\",
    \"template\": \"color-app\",
    \"values\": {
      \"service_name\": \"color-app\",
      \"image_repository\": \"${REGISTRY_HOST}/demo/color-app\",
      \"image_tag\": \"0.1.0\",
      \"color\": \"blue\",
      \"port\": 8080
    }
  }") || true

CREATE_CODE=$(echo "$CREATE_RESP" | tail -1)
CREATE_BODY=$(echo "$CREATE_RESP" | sed '$d')

if [ "$CREATE_CODE" = "201" ]; then
  ok "color-app created"
elif [ "$CREATE_CODE" = "409" ]; then
  skip "color-app already exists (409)"
else
  echo "  Response: ${CREATE_BODY}"
  die "Failed to create color-app (HTTP ${CREATE_CODE})"
fi
echo ""

# ── Done ──────────────────────────────────────────────────────────────────
cat <<EOF
  ──────────────────────────────────────────────────────────────────
  color-app demo is seeded.

  The publisher has committed Kargo CRs and ArgoCD manifests to
  the gitops repo. ArgoCD will sync them to the cluster.

  Staging:  http://color-app.staging.localhost:8880
  Prod:     http://color-app.prod.localhost:8880
  JSON API: http://color-app.staging.localhost:8880/api/color

  Test the promotion pipeline:

    # Push a new version (green)
    task demo:color-app:push VERSION=0.2.0 COLOR=green

    # Kargo detects the new tag → creates Freight → auto-promotes to staging
    curl http://color-app.staging.localhost:8880/api/color

    # Promote to prod via the API:
    curl -X POST http://localhost:8080/api/v1/projects/demo/apps/color-app/promote \\
      -H "Content-Type: application/json" \\
      -b <(cat $COOKIE_JAR) \\
      -d '{"toEnvironment":"prod"}'

EOF
