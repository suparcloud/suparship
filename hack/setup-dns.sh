#!/usr/bin/env bash
# hack/setup-dns.sh — configure wildcard DNS for suparship local development.
#
# Without this, every new ingress hostname (argocd.localhost, gitea.localhost,
# pr-<n>.<app>.preview.localhost, …) must be added manually to /etc/hosts.
# That quickly becomes unmanageable for preview environments.
#
# Makes ALL *.localhost addresses resolve to 127.0.0.1 automatically — zero
# /etc/hosts entries. Cross-platform, detect-first:
#
#   any OS   → if *.localhost already resolves, exit 0 (systemd-resolved
#              distros — Ubuntu 18.04+, Fedora 33+ — synthesize it natively).
#   Linux    → dnsmasq via apt/dnf/yum + a wildcard rule in /etc/dnsmasq.d;
#              with systemd-resolved active, a ~localhost routing drop-in;
#              without it, dnsmasq on 127.0.0.1 (plus a resolv.conf hint for
#              NetworkManager setups). Verifies at the end.
#   macOS    → Homebrew dnsmasq + /etc/resolver/localhost (steps below).
#
# Idempotent: safe to re-run. Each step is skipped if already applied.
# The dev ingress binds host port 80, so after this these all work as-is:
#   http://argocd.localhost   http://<app>.<env>.localhost
#   http://pr-<n>.<app>-<component>.preview.localhost
#
# Usage:
#   ./hack/setup-dns.sh     # run directly
#   task dev:dns            # preferred: via Taskfile
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

# ── Already working? (any OS) ─────────────────────────────────────────────
# systemd-resolved distros (Ubuntu 18.04+, Fedora 33+) synthesize
# `*.localhost → 127.0.0.1` natively (RFC 6761), so many Linux machines need
# nothing at all. Check through the same path applications use.
resolves() {
  if command -v getent >/dev/null 2>&1; then
    getent hosts test.localhost >/dev/null 2>&1
  else
    # macOS has no getent; curl exit 6 = could-not-resolve.
    local rc=0
    curl -s -o /dev/null --max-time 2 http://test.localhost/ 2>/dev/null || rc=$?
    [ "$rc" -ne 6 ]
  fi
}
if resolves; then
  ok "*.localhost already resolves — nothing to do."
  exit 0
fi

# ── Linux: dnsmasq via the standard per-distro path ───────────────────────
OS="$(uname -s)"
if [ "${OS}" = "Linux" ]; then
  echo ""
  echo "  suparship — local DNS setup (wildcard *.localhost, Linux)"
  echo "  ──────────────────────────────────────────────────────────────────"
  echo ""
  [ "$(id -u)" -eq 0 ] || command -v sudo >/dev/null 2>&1 \
    || die "needs root (or sudo) to configure DNS"
  SUDO=""; [ "$(id -u)" -eq 0 ] || SUDO="sudo"

  # Install dnsmasq (Ubuntu/Debian: apt; RHEL/CentOS/Fedora: dnf/yum).
  if ! command -v dnsmasq >/dev/null 2>&1; then
    info "installing dnsmasq..."
    if command -v apt-get >/dev/null 2>&1; then $SUDO apt-get install -y dnsmasq
    elif command -v dnf >/dev/null 2>&1; then $SUDO dnf install -y dnsmasq
    elif command -v yum >/dev/null 2>&1; then $SUDO yum install -y dnsmasq
    else die "no apt-get/dnf/yum found — install dnsmasq manually (docs/local-dns.md)"
    fi
  fi
  ok "dnsmasq present"

  # Wildcard rule. Bind only loopback so we never serve the network.
  $SUDO mkdir -p /etc/dnsmasq.d
  printf '%s\naddress=/.localhost/127.0.0.1\nlisten-address=127.0.0.1\nbind-interfaces\n' \
    "${DNS_MARK}" | $SUDO tee /etc/dnsmasq.d/suparship-localhost.conf >/dev/null
  ok "wildcard rule in /etc/dnsmasq.d/suparship-localhost.conf"

  if systemctl is-active --quiet systemd-resolved 2>/dev/null; then
    # resolved owns :53 on 127.0.0.53 — run dnsmasq beside it on 127.0.0.1
    # and route only the localhost domain there via a drop-in.
    $SUDO mkdir -p /etc/systemd/resolved.conf.d
    printf '[Resolve]\nDNS=127.0.0.1\nDomains=~localhost\n' \
      | $SUDO tee /etc/systemd/resolved.conf.d/suparship-localhost.conf >/dev/null
    $SUDO systemctl enable --now dnsmasq >/dev/null 2>&1 || $SUDO systemctl restart dnsmasq
    $SUDO systemctl restart systemd-resolved
    ok "systemd-resolved routes ~localhost → dnsmasq (drop-in applied)"
  else
    # No resolved (Debian default, RHEL with plain NetworkManager): dnsmasq
    # on loopback + make sure the resolver actually asks 127.0.0.1 first.
    $SUDO systemctl enable --now dnsmasq >/dev/null 2>&1 || $SUDO systemctl restart dnsmasq
    if ! grep -q "^nameserver 127.0.0.1" /etc/resolv.conf 2>/dev/null; then
      warn "add 'nameserver 127.0.0.1' as the FIRST entry in /etc/resolv.conf"
      warn "(with NetworkManager: set [main] dns=dnsmasq in NetworkManager.conf"
      warn " and move the rule to /etc/NetworkManager/dnsmasq.d/ — docs/local-dns.md)"
    fi
    ok "dnsmasq running on 127.0.0.1"
  fi

  if resolves; then
    ok "*.localhost → 127.0.0.1 verified"
  else
    warn "*.localhost still does not resolve — see docs/local-dns.md for your distro's resolver specifics"
    exit 1
  fi
  exit 0
fi

[ "${OS}" = "Darwin" ] || die "unsupported OS: ${OS} (see docs/local-dns.md)"

# ── Banner ────────────────────────────────────────────────────────────────
echo ""
echo "  suparship — local DNS setup (wildcard *.localhost)"
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
# suparship: route *.localhost DNS queries to local dnsmasq
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

  Example URLs:
    http://argocd.localhost             ArgoCD UI
    http://gitea.localhost               Gitea UI
    http://shipnotes-frontend.staging.localhost   Demo app (staging)
    http://pr-<n>.<app>.preview.localhost   Preview environments

  Next step:
    task up                   provision the full dev cluster

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
