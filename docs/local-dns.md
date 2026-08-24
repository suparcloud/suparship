# Local DNS for suparship Development

> **`task dev:dns` automates all of this** — it's a no-op where `*.localhost`
> already resolves (systemd-resolved distros: Ubuntu 18.04+, Fedora 33+
> synthesize it natively), configures Homebrew dnsmasq on macOS, and dnsmasq
> via apt/dnf on other Linux setups. This page is the reference for what it
> does and the manual fallbacks. The ingress is part of the default `task up`,
> so app URLs (`http://<app>.<env>.localhost`) depend on this resolving.

Without wildcard DNS you need a manual `/etc/hosts` entry for every ingress
hostname — which is unworkable for preview environments like
`pr-<n>.<app>.preview.localhost`.

The solution is a local dnsmasq rule that resolves all `*.localhost` addresses
to `127.0.0.1` (unless your resolver already does it natively).

## macOS (automated)

```bash
task dev:dns
```

This script (idempotent, safe to re-run):

1. Installs `dnsmasq` via Homebrew.
2. Adds `address=/.localhost/127.0.0.1` to the dnsmasq config.
3. Starts the dnsmasq service as root (needs `sudo` once to bind port 53).
4. Creates `/etc/resolver/localhost` so macOS routes `*.localhost` queries to
   the local dnsmasq.
5. Flushes the mDNS cache.

### Verify

```bash
# Query dnsmasq directly — always works regardless of VPN
dig @127.0.0.1 argocd.localhost        # should print 127.0.0.1

# Verify the macOS resolver framework (same path browsers/curl use)
dscacheutil -q host -a name argocd.localhost   # should show ip_address: 127.0.0.1
ping -c1 argocd.localhost                      # should resolve to 127.0.0.1
```

> **Tailscale / VPN users:** `dig argocd.localhost` (without `@127.0.0.1`)
> will return NXDOMAIN even after correct setup. `dig` bypasses the macOS
> resolver framework and queries the VPN's nameserver directly
> (`100.100.100.100` for Tailscale). This is misleading — browsers, `curl`,
> and `ping` all use `getaddrinfo()` which **does** honour
> `/etc/resolver/localhost`, so everything works. Use `dscacheutil` or
> `ping` as the definitive test.

### scutil check

```bash
scutil --dns | grep -A4 localhost
# Expected output:
#   domain   : localhost
#   nameserver[0] : 127.0.0.1
```

### Undo

```bash
sudo brew services stop dnsmasq
sudo rm /etc/resolver/localhost
# Remove the suparship block from $(brew --prefix)/etc/dnsmasq.conf
```

---

## Linux

### Option A — standalone dnsmasq

```bash
# Ubuntu / Debian
sudo apt install dnsmasq

# Add to /etc/dnsmasq.d/suparship.conf
echo "address=/.localhost/127.0.0.1" | sudo tee /etc/dnsmasq.d/suparship.conf
sudo systemctl restart dnsmasq

# Tell NetworkManager (if used) to pass *.localhost to dnsmasq
# In /etc/NetworkManager/dnsmasq.d/suparship.conf:
echo "server=/localhost/127.0.0.1" | sudo tee /etc/NetworkManager/dnsmasq.d/suparship.conf
sudo systemctl restart NetworkManager
```

### Option B — systemd-resolved per-domain stub

```bash
sudo mkdir -p /etc/systemd/resolved.conf.d/
cat <<EOF | sudo tee /etc/systemd/resolved.conf.d/suparship.conf
[Resolve]
DNS=127.0.0.1
Domains=~localhost
EOF

# Also run a dnsmasq instance for the actual address→IP mapping:
echo "address=/.localhost/127.0.0.1" | sudo tee /etc/dnsmasq.d/suparship.conf
sudo systemctl restart dnsmasq
sudo systemctl restart systemd-resolved
```

---

## Why `*.localhost` and not a custom TLD?

suparship ingresses already use `*.localhost` hostnames (e.g. `argocd.localhost`,
`gitea.localhost`, `shipnotes-frontend.staging.localhost`). Keeping that convention means:

- No extra configuration when deploying to a real cluster (hostnames change
  to real domains).
- Browsers treat `.localhost` as a secure context, so cookies work without
  HTTPS.
- The pattern scales to preview environments:
  `pr-<n>.<app>.preview.localhost`

## Port note

The kind cluster maps ingress port 80 to **host port 80** (Docker's helper
binds it unprivileged on macOS; on Linux the rootful docker daemon does), so
URLs need no port suffix:

```
http://argocd.localhost
http://gitea.localhost
http://pr-<n>.<app>.preview.localhost
```

The legacy `:8880` mapping is kept alongside for muscle memory; both reach
the same ingress.
