#!/usr/bin/env bash
# hack/setup-dns.sh — configure wildcard DNS for suparShip local development.
#
# Without this, every new ingress hostname (argocd.localhost, gitea.localhost,
# pr-123.hello.preview.localhost, …) must be added manually to /etc/hosts.
# That quickly becomes unmanageable for preview environments.
#
# This script installs and configures dnsmasq so that ALL *.localhost addresses
# resolve to 127.0.0.1 automatically — zero /etc/hosts entries needed.
#
# ─────────────────────────────────────────────────────────────────────────────
# What it does (macOS only):
#   1. Installs dnsmasq via Homebrew if absent.
#   2. Appends `address=/.localhost/127.0.0.1` to $(brew --prefix)/etc/dnsmasq.conf
#      if not already present.
#   3. Starts (or restarts) the dnsmasq service as root so it can bind port 53.
#   4. Creates /etc/resolver/localhost so macOS routes *.localhost queries to
#      the local dnsmasq (127.0.0.1:53) instead of the upstream resolver.
#   5. Flushes the mDNS cache so the new resolver is picked up immediately.
#
# Idempotent: safe to re-run. Each step is skipped if already applied.
#
# After running this script, these URLs all work without /etc/hosts:
#   http://argocd.localhost:8880
#   http://gitea.localhost:8880
#   http://hello-staging.localhost:8880
#   http://pr-123.hello.preview.localhost:8880
#
# Note: port :8880 is still required (kind maps container:80 → host:8880).
# Run `task dev:dns:port-forward` to get clean :80/:443 URLs (optional, macOS).
#
# Linux users:
#   Use systemd-resolved or NetworkManager dnsmasq plugin instead.
#   See: docs/local-dns.md
#
# Usage:
#   ./hack/setup-dns.sh             # run directly
#   task dev:dns:setup              # preferred: via Taskfile
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

RESOLVER_DIR="/etc/resolver"
RESOLVER_FILE="${RESOLVER_DIR}/localhost"
DNS_MARK="# suparship: wildcard *.localhost"

# ── Helpers ───────────────────────────────────────────────────────────────
info()  { printf "  \033[0;36m%s\033[0m\n" "$*"; }
ok()    { printf "  \033[0;32m✓\033[0m  %s\n" "$*"; }
skip()  { printf "  \033[0;33m–\033[0m  %s\n" "$*"; }
warn()  { printf "  \033[0;33mWARN:\033[0m %s\n" "$*"; }
die()   { printf "  \033[0;31mERROR:\033[0m %s\n" "$*" >&2; exit 1; }

# ── OS gate ───────────────────────────────────────────────────────────────
OS="$(uname -s)"
if [ "${OS}" != "Darwin" ]; then
  warn "This script only automates setup on macOS."
  echo ""
  echo "  On Linux, configure dnsmasq via your distro's package manager:"
  echo "    Ubuntu/Debian:  sudo apt install dnsmasq"
  echo "    Fedora/RHEL:    sudo dnf install dnsmasq"
  echo ""
  echo "  Then add to /etc/dnsmasq.conf (or /etc/dnsmasq.d/suparship.conf):"
  echo "    address=/.localhost/127.0.0.1"
  echo ""
  echo "  Or, with systemd-resolved:"
  echo "    sudo mkdir -p /etc/systemd/resolved.conf.d/"
  echo "    echo -e '[Resolve]\nDNS=127.0.0.1\nDomains=~localhost' | \\"
  echo "      sudo tee /etc/systemd/resolved.conf.d/suparship.conf"
  echo "    sudo systemctl restart systemd-resolved"
  echo ""
  echo "  For more details: docs/local-dns.md"
  echo ""
  exit 0
fi

# ── Banner ────────────────────────────────────────────────────────────────
echo ""
echo "  suparShip — local DNS setup (wildcard *.localhost)"
echo "  ──────────────────────────────────────────────────────────────────"
echo ""

# ── 1. Homebrew check ─────────────────────────────────────────────────────
info "Checking Homebrew..."
if ! command -v brew >/dev/null 2>&1; then
  die "Homebrew not found. Install it from https://brew.sh and re-run."
fi
ok "brew  ($(brew --prefix))"
echo ""

BREW_PREFIX="$(brew --prefix)"
DNSMASQ_CONF="${BREW_PREFIX}/etc/dnsmasq.conf"

# ── 2. Install dnsmasq ────────────────────────────────────────────────────
info "dnsmasq..."
if brew list dnsmasq >/dev/null 2>&1; then
  skip "already installed ($(brew list --versions dnsmasq))"
else
  echo "  Installing dnsmasq..."
  brew install dnsmasq
  ok "dnsmasq installed"
fi
echo ""

# ── 3. Configure dnsmasq ─────────────────────────────────────────────────
info "dnsmasq config (${DNSMASQ_CONF})..."
if grep -qF "${DNS_MARK}" "${DNSMASQ_CONF}" 2>/dev/null; then
  skip "wildcard rule already present"
else
  cat >>"${DNSMASQ_CONF}" <<EOF

${DNS_MARK}
address=/.localhost/127.0.0.1
# Forward all other queries to upstream DNS (Google)
server=8.8.8.8
server=8.8.4.4
EOF
  ok "added: address=/.localhost/127.0.0.1"
fi
echo ""

# ── 4. Start / restart dnsmasq as root ───────────────────────────────────
info "dnsmasq service (needs sudo to bind port 53)..."
echo "  You may be prompted for your password."
echo ""
if sudo brew services list 2>/dev/null | grep -q "^dnsmasq.*started"; then
  sudo brew services restart dnsmasq >/dev/null
  ok "dnsmasq restarted"
else
  sudo brew services start dnsmasq >/dev/null
  ok "dnsmasq started"
fi
echo ""

# ── 5. /etc/resolver/localhost ────────────────────────────────────────────
info "/etc/resolver/localhost (needs sudo)..."
if [ -f "${RESOLVER_FILE}" ] && grep -qF "nameserver 127.0.0.1" "${RESOLVER_FILE}" 2>/dev/null; then
  skip "resolver already configured"
else
  sudo mkdir -p "${RESOLVER_DIR}"
  cat <<EOF | sudo tee "${RESOLVER_FILE}" >/dev/null
# suparShip: route *.localhost DNS queries to local dnsmasq
domain localhost
search localhost
nameserver 127.0.0.1
EOF
  ok "created ${RESOLVER_FILE}"
fi
echo ""

# ── 6. Flush DNS cache ────────────────────────────────────────────────────
info "Flushing DNS cache (mDNSResponder)..."
sudo killall -HUP mDNSResponder 2>/dev/null || true
ok "cache flushed"
echo ""

# ── 7. Verify resolution ─────────────────────────────────────────────────
# IMPORTANT: `dig` and `nslookup` bypass the macOS resolver framework and
# query the system nameserver directly. If Tailscale (or another VPN) is
# active, that catches the query before dnsmasq — making dig return NXDOMAIN
# even though everything is configured correctly.
#
# The right test is dscacheutil, which uses getaddrinfo() just like browsers,
# curl, and ping do, and therefore honours /etc/resolver/* correctly.
info "Verifying DNS resolution (via getaddrinfo / dscacheutil)..."
sleep 1  # give mDNSResponder a moment to reload

# Verify dnsmasq itself is answering correctly first.
if command -v dig >/dev/null 2>&1; then
  DNSMASQ_RESULT="$(dig +short @127.0.0.1 argocd.localhost 2>/dev/null | head -1 || true)"
  if [ "${DNSMASQ_RESULT}" = "127.0.0.1" ]; then
    ok "dnsmasq directly:  argocd.localhost → 127.0.0.1  ✓"
  else
    warn "dnsmasq returned '${DNSMASQ_RESULT}' — check: brew services list | grep dnsmasq"
  fi
fi

# Verify the macOS resolver framework picks it up (the path browsers/curl use).
SYSCACHE_RESULT="$(dscacheutil -q host -a name argocd.localhost 2>/dev/null \
  | awk '/^ip_address:/{print $2}' | head -1 || true)"
if [ "${SYSCACHE_RESULT}" = "127.0.0.1" ]; then
  ok "macOS resolver:    argocd.localhost → 127.0.0.1  ✓"
else
  # dscacheutil caches aggressively; flush and retry once.
  dscacheutil -flushcache 2>/dev/null || true
  sudo killall -HUP mDNSResponder 2>/dev/null || true
  sleep 1
  SYSCACHE_RESULT="$(dscacheutil -q host -a name argocd.localhost 2>/dev/null \
    | awk '/^ip_address:/{print $2}' | head -1 || true)"
  if [ "${SYSCACHE_RESULT}" = "127.0.0.1" ]; then
    ok "macOS resolver:    argocd.localhost → 127.0.0.1  ✓  (after cache flush)"
  else
    warn "macOS resolver check inconclusive (got '${SYSCACHE_RESULT}')."
    warn "If you have Tailscale / a VPN running, 'dig argocd.localhost' will"
    warn "show NXDOMAIN — that is expected and misleading. Test with:"
    warn "  ping -c1 argocd.localhost   (uses getaddrinfo, not raw DNS)"
  fi
fi
echo ""

# ── Done ──────────────────────────────────────────────────────────────────
cat <<EOF
  ──────────────────────────────────────────────────────────────────
  DNS setup complete.

  All *.localhost addresses now resolve to 127.0.0.1 automatically.
  No /etc/hosts entries needed — including preview environments.

  Example URLs (port :8880 required — kind maps container:80 → host:8880):
    http://argocd.localhost:8880       ArgoCD UI
    http://gitea.localhost:8880        Gitea UI
    http://hello-staging.localhost:8880       Demo app (staging)
    http://pr-123.hello.preview.localhost:8880   Preview environments

  Next step:
    task dev:cluster                   start the full cluster dev stack

  To verify DNS manually:
    dig @127.0.0.1 argocd.localhost    query dnsmasq directly → 127.0.0.1
    dscacheutil -q host -a name argocd.localhost   via macOS resolver → 127.0.0.1
    ping -c1 argocd.localhost          uses getaddrinfo (same path as browsers)

    NOTE: plain 'dig argocd.localhost' (no @127.0.0.1) will appear to fail
    if Tailscale or another VPN is active — it bypasses /etc/resolver and
    queries the VPN nameserver directly. That is misleading; browsers and
    curl are unaffected.

  To undo:
    sudo brew services stop dnsmasq
    sudo rm ${RESOLVER_FILE}
    # Remove the suparship block from: ${DNSMASQ_CONF}

EOF
