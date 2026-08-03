# Local DNS for suparship Development

> **Optional — only needed for the ingress path.** The default Tilt dev loop
> (`task up`) reaches every service via localhost port-forwards and needs **no**
> DNS setup. You only need the wildcard DNS below if you opt into ingress with
> `task up:ingress` (or the legacy `*.localhost:8880` routing). See
> [contributor-guide/hacking-on-suparship.md](contributor-guide/hacking-on-suparship.md).

Without wildcard DNS you need a manual `/etc/hosts` entry for every ingress
hostname — which is unworkable for preview environments like
`pr-123.hello.preview.localhost`.

The solution is a local dnsmasq rule that resolves all `*.localhost` addresses
to `127.0.0.1`.

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
`gitea.localhost`, `hello-staging.localhost`). Keeping that convention means:

- No extra configuration when deploying to a real cluster (hostnames change
  to real domains).
- Browsers treat `.localhost` as a secure context, so cookies work without
  HTTPS.
- The pattern scales to preview environments:
  `pr-123.hello.preview.localhost:8880`

## Port note

The kind cluster maps `container:80 → host:8880` to avoid requiring root
privileges. URLs therefore include `:8880` even after DNS is configured.

```
http://argocd.localhost:8880
http://gitea.localhost:8880
http://pr-123.hello.preview.localhost:8880
```

If you want clean port-80 URLs, you can add a `pf` rule on macOS to forward
`localhost:80 → localhost:8880` — but this is optional and requires sudo at
boot.
