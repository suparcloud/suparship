#!/usr/bin/env bash
# hack/dev.sh — start backend (fake/in-memory) + Vite frontend in one command.
#
# Usage:
#   ./hack/dev.sh          # loads .env automatically if present
#   task dev               # preferred: calls this script via Taskfile
#
# Ctrl+C stops both servers cleanly.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# ── Load .env ─────────────────────────────────────────────────────────────
if [ -f .env ]; then
  # shellcheck disable=SC1091
  set -a; source .env; set +a
fi

# ── Config ────────────────────────────────────────────────────────────────
ADDR="${SUPARSHIP_ADDR:-:8080}"
BACKEND_PORT="${ADDR#:}"         # strip leading colon → "8080"
FRONTEND_PORT="${SUPARSHIP_FRONTEND_PORT:-5173}"
LOGIN="${SUPARSHIP_ADMIN_EMAIL:-admin@local}"
PASS="${SUPARSHIP_ADMIN_PASSWORD:-admin123}"

# ── Preflight checks ─────────────────────────────────────────────────────
hack/preflight.sh local

# ── Banner ────────────────────────────────────────────────────────────────
cat <<EOF

  suparship — local dev  (fake / in-memory mode, no cluster required)
  ────────────────────────────────────────────────────────────────────
  Backend   →  http://localhost:${BACKEND_PORT}
  Frontend  →  http://localhost:${FRONTEND_PORT}
  Login     →  ${LOGIN}  /  ${PASS}

  Ctrl+C to stop both servers.

EOF

# ── Build Go binary ───────────────────────────────────────────────────────
printf "  [api] building... "
go build -o bin/suparship ./cmd/suparship
echo "ok"

# ── npm install (first run only) ──────────────────────────────────────────
if [ ! -d ui/node_modules ]; then
  echo "  [ui]  installing npm packages (first time, may take a moment)..."
  (cd ui && npm install --silent)
fi

# ── Start backend in background ───────────────────────────────────────────
SUPARSHIP_CORS_ORIGINS="http://localhost:${FRONTEND_PORT}" \
  ./bin/suparship server &
API_PID=$!

# ── Stop both servers on exit ─────────────────────────────────────────────
cleanup() {
  printf "\n  Stopping...\n"
  kill "$API_PID" 2>/dev/null || true
  wait "$API_PID" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

# ── Start frontend in foreground (blocks until Ctrl+C) ───────────────────
(cd ui && npm run dev)
