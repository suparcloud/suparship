#!/usr/bin/env bash
# hack/preflight.sh — verify required tools are installed before starting.
#
# Usage:
#   hack/preflight.sh local    # checks: go, npm
#   hack/preflight.sh cluster  # checks: go, npm, kind, kubectl, helm
#   hack/preflight.sh tilt     # checks: docker, ctlptl, tilt, kubectl, helm, npm, htpasswd
#
# Exits 0 when all required tools are present.
# Exits 1 and prints an install-hint table when any are missing.
# Never auto-installs anything.
set -euo pipefail

MODE="${1:-local}"

missing_cmds=()
missing_hints=()

need() {
  local cmd="$1" hint="$2"
  if ! command -v "$cmd" >/dev/null 2>&1; then
    missing_cmds+=("$cmd")
    missing_hints+=("$hint")
  fi
}

# ── Always required ───────────────────────────────────────────────────────
need npm "https://nodejs.org/en/download/  (npm is bundled with Node.js)"

# tilt mode does NOT need go on the host — suparship compiles inside the dev
# container; the other modes build the binary locally.
if [ "$MODE" != "tilt" ]; then
  need go "https://go.dev/dl/"
fi

# ── Cluster mode additionally requires ───────────────────────────────────
if [ "$MODE" = "cluster" ]; then
  need kind    "https://kind.sigs.k8s.io/docs/user/quick-start/#installation"
  need kubectl "https://kubernetes.io/docs/tasks/tools/"
  need helm    "https://helm.sh/docs/intro/install/"
fi

# ── Tilt mode (task up) additionally requires ────────────────────────────
if [ "$MODE" = "tilt" ]; then
  need docker   "https://docs.docker.com/get-docker/  (macOS: OrbStack/Docker Desktop · Linux: rootful docker engine — the ingress binds host port 80)"
  need kind     "https://kind.sigs.k8s.io/docs/user/quick-start/#installation  (macOS: brew install kind · Linux: release binary)"
  need ctlptl   "https://github.com/tilt-dev/ctlptl#how-do-i-install-it  (macOS: brew install tilt-dev/tap/ctlptl · Linux: release binary)"
  need tilt     "https://docs.tilt.dev/install.html  (macOS: brew install tilt-dev/tap/tilt · Linux: curl installer)"
  need kubectl  "https://kubernetes.io/docs/tasks/tools/"
  need helm     "https://helm.sh/docs/intro/install/"
  need htpasswd "macOS: preinstalled · Ubuntu/Debian: apache2-utils · RHEL/Fedora: httpd-tools"
  need git      "https://git-scm.com/downloads"
fi

# ── All present — nothing to do ──────────────────────────────────────────
if [ ${#missing_cmds[@]} -eq 0 ]; then
  exit 0
fi

# ── Print missing tools with actionable install links ────────────────────
echo ""
printf "  \033[0;31mPreflight failed:\033[0m the following required tools are not installed.\n"
echo "  ──────────────────────────────────────────────────────────────────"
for i in "${!missing_cmds[@]}"; do
  printf "  \033[0;31m✗\033[0m  %-10s  %s\n" "${missing_cmds[$i]}" "${missing_hints[$i]}"
done
echo ""
printf "  Install the tools listed above, then re-run.\n"
echo ""
exit 1
