#!/usr/bin/env bash
# hack/init-color-app-repo.sh — push color-app source to a Gitea repo.
#
# Creates a "demo/color-app" repository in Gitea containing the color-app
# Go source code. This makes the demo story more authentic: source code lives
# in Git, CI builds an image, pushes to the local registry, and Kargo detects
# the new tag.
#
# Idempotent: skips if the repo already has content.
#
# Environment variables (defaults match dev cluster):
#   GITEA_HOST_URL       e.g. http://gitea.localhost:8880
#   GITEA_ADMIN_USER     e.g. gitops
#   GITEA_ADMIN_PASS     e.g. gitops-dev-only
#   REPO_ROOT            absolute path to the suparship repo root
#
# Usage:
#   ./hack/init-color-app-repo.sh
#   task dev:cluster:init-color-app-repo
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

GITEA_HOST_URL="${GITEA_HOST_URL:-http://gitea.localhost:8880}"
GITEA_ADMIN_USER="${GITEA_ADMIN_USER:-gitops}"
GITEA_ADMIN_PASS="${GITEA_ADMIN_PASS:-gitops-dev-only}"

COLOR_APP_ORG="demo"
COLOR_APP_REPO="color-app"

# ── Helpers ───────────────────────────────────────────────────────────────
info()  { printf "  \033[0;36m%s\033[0m\n" "$*"; }
ok()    { printf "  \033[0;32m✓\033[0m  %s\n" "$*"; }
skip()  { printf "  \033[0;33m–\033[0m  %s\n" "$*"; }
die()   { printf "  \033[0;31mERROR:\033[0m %s\n" "$*" >&2; exit 1; }

echo ""
echo "  suparship — init color-app source repo in Gitea"
echo "  ──────────────────────────────────────────────────────────────────"
echo ""

# ── 1. Wait for Gitea ────────────────────────────────────────────────────
info "Waiting for Gitea at ${GITEA_HOST_URL}..."
for i in $(seq 1 20); do
  if curl -sf --max-time 3 "${GITEA_HOST_URL}/api/v1/version" >/dev/null 2>&1; then
    ok "Gitea is reachable"
    break
  fi
  if [ "$i" -eq 20 ]; then
    die "Gitea not reachable at ${GITEA_HOST_URL} within 60s"
  fi
  sleep 3
done
echo ""

# ── 2. Create org "demo" if it doesn't exist ────────────────────────────
info "Ensuring org '${COLOR_APP_ORG}' exists..."
ORG_STATUS=$(curl -sf -o /dev/null -w "%{http_code}" \
  "${GITEA_HOST_URL}/api/v1/orgs/${COLOR_APP_ORG}" \
  -u "${GITEA_ADMIN_USER}:${GITEA_ADMIN_PASS}" 2>/dev/null || echo "000")

if [ "$ORG_STATUS" = "200" ]; then
  skip "org already exists"
else
  CREATE_ORG_STATUS=$(curl -sf -X POST \
    "${GITEA_HOST_URL}/api/v1/orgs" \
    -H "Content-Type: application/json" \
    -u "${GITEA_ADMIN_USER}:${GITEA_ADMIN_PASS}" \
    -d "{
      \"username\": \"${COLOR_APP_ORG}\",
      \"full_name\": \"Demo\",
      \"description\": \"Demo apps for suparship\",
      \"visibility\": \"public\"
    }" \
    -o /dev/null -w "%{http_code}" 2>/dev/null || echo "000")
  if [ "$CREATE_ORG_STATUS" = "201" ]; then
    ok "org created"
  else
    die "Failed to create org (HTTP ${CREATE_ORG_STATUS})"
  fi
fi
echo ""

# ── 3. Create repo "demo/color-app" ─────────────────────────────────────
info "Ensuring repo '${COLOR_APP_ORG}/${COLOR_APP_REPO}' exists..."
REPO_STATUS=$(curl -sf -o /dev/null -w "%{http_code}" \
  "${GITEA_HOST_URL}/api/v1/repos/${COLOR_APP_ORG}/${COLOR_APP_REPO}" \
  -u "${GITEA_ADMIN_USER}:${GITEA_ADMIN_PASS}" 2>/dev/null || echo "000")

if [ "$REPO_STATUS" = "200" ]; then
  skip "repo already exists"
else
  CREATE_STATUS=$(curl -sf -X POST \
    "${GITEA_HOST_URL}/api/v1/orgs/${COLOR_APP_ORG}/repos" \
    -H "Content-Type: application/json" \
    -u "${GITEA_ADMIN_USER}:${GITEA_ADMIN_PASS}" \
    -d "{
      \"name\": \"${COLOR_APP_REPO}\",
      \"description\": \"Color app demo — displays a color for Kargo promotion demos\",
      \"private\": false,
      \"auto_init\": true,
      \"default_branch\": \"main\"
    }" \
    -o /dev/null -w "%{http_code}" 2>/dev/null || echo "000")
  if [ "$CREATE_STATUS" = "201" ]; then
    ok "repo created"
  else
    die "Failed to create repo (HTTP ${CREATE_STATUS})"
  fi
fi
echo ""

# ── 4. Check if already has content beyond auto-init ─────────────────────
info "Checking existing content..."
TREE_COUNT=$(curl -sf \
  "${GITEA_HOST_URL}/api/v1/repos/${COLOR_APP_ORG}/${COLOR_APP_REPO}/git/trees/main?recursive=false" \
  -u "${GITEA_ADMIN_USER}:${GITEA_ADMIN_PASS}" 2>/dev/null \
  | grep -o '"path"' | wc -l | tr -d ' ' || echo "0")

if [ "${TREE_COUNT}" -ge 3 ] 2>/dev/null; then
  skip "repo already has content (${TREE_COUNT} paths) — skipping push"
  echo ""
  exit 0
fi
echo ""

# ── 5. Clone and push source ────────────────────────────────────────────
CLONE_URL="http://${GITEA_ADMIN_USER}:${GITEA_ADMIN_PASS}@${GITEA_HOST_URL#http://}/${COLOR_APP_ORG}/${COLOR_APP_REPO}.git"

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

info "Cloning repo..."
git -c advice.detachedHead=false clone --quiet "${CLONE_URL}" "${TMPDIR}/color-app"
cd "${TMPDIR}/color-app"

git config user.email "suparship-dev@local"
git config user.name  "suparship Dev Bot"

# Copy source files.
cp "${REPO_ROOT}/demo/color-app/main.go" .
cp "${REPO_ROOT}/demo/color-app/go.mod" .
cp "${REPO_ROOT}/demo/color-app/Dockerfile" .
cp "${REPO_ROOT}/demo/color-app/.dockerignore" .

cat > README.md <<'EOF'
# color-app

A tiny Go HTTP server that displays its configured color. Used by **suparship**
to demonstrate Kargo promotion pipelines.

## Endpoints

| Path          | Response                                       |
|---------------|------------------------------------------------|
| `GET /`       | HTML page with the color as background         |
| `GET /api/color` | `{"color":"blue","version":"0.1.0"}`       |
| `GET /healthz`   | `{"ok":true}`                              |

## Build

```bash
docker build --build-arg VERSION=0.1.0 --build-arg COLOR=blue -t color-app .
```

## Environment Variables

| Variable | Default | Description         |
|----------|---------|---------------------|
| `COLOR`  | `blue`  | Background color    |
| `PORT`   | `8080`  | Listen port         |
EOF
ok "source files copied"

git add .
git commit --quiet -m "feat: initial color-app source

Go HTTP server that returns a configurable color.
Used for suparship Kargo promotion demos."

git push --quiet origin main
ok "pushed to ${GITEA_HOST_URL}/${COLOR_APP_ORG}/${COLOR_APP_REPO}"
echo ""

cat <<EOF
  ──────────────────────────────────────────────────────────────────
  color-app source repo is ready.

  Gitea UI:  ${GITEA_HOST_URL}/${COLOR_APP_ORG}/${COLOR_APP_REPO}

EOF
