#!/usr/bin/env bash
# hack/dev/endpoints.sh — the ONE place the dev-loop endpoint table lives.
#
# Printed in two places so it's visible wherever you're looking:
#   - the terminal: `task up*` echoes it before Tilt takes over (the default
#     `tilt up` terminal never shows Tiltfile print output);
#   - the Tilt UI: the `endpoints` local_resource runs this after the
#     user-facing services are ready, so its log is the consolidated list and
#     its links are clickable.
#
# Variant knobs (mirroring the Tiltfile flags):
#   SUPARSHIP_VAULT=0    hide the Vault row     (Vault is ON by default)
#   SUPARSHIP_INGRESS=0  hide the ingress rows  (ingress is ON by default)
set -euo pipefail

row() { printf "  %-22s %-30s %s\n" "$1" "$2" "${3:-}"; }

# One dev password everywhere a human logs in: admin123. (Gitea's `gitops`
# account is machinery — its password is threaded through init scripts and
# stored cluster credentials, so it keeps its own.)
row "suparShip UI"    "http://localhost:5173"      "admin@local / admin123  (Vite HMR)"
row "suparShip API"   "http://localhost:8080"      "admin@local / admin123"
row "Tilt UI"         "http://localhost:10350"
row "ArgoCD"          "http://localhost:8081"      "admin / admin123"
row "Gitea"           "http://localhost:3000"      "gitops / gitops-dev-only"
row "Kargo UI"        "http://localhost:8083"      "password: admin123  (admin login is password-only)"
row "Registry (kind)" "localhost:5001"             "no auth — Tilt builds + CD demo pushes"
row "Registry (priv)" "localhost:5010"             "admin / admin123  (docker login localhost:5010)"
if [ "${SUPARSHIP_VAULT:-1}" != "0" ]; then
  row "Vault"         "http://localhost:8200"      "token: admin123"
fi
if [ "${SUPARSHIP_INGRESS:-1}" != "0" ]; then
  row "App URLs"      "http://<app>.<env>.localhost"  "e.g. http://shipnotes-frontend.staging.localhost after \`task demo:shipnotes\`"
  # *.localhost must resolve to loopback for app URLs to work in a browser.
  # curl exit 6 = could-not-resolve (anything else, incl. connection refused,
  # means DNS is fine). macOS: one-time dnsmasq setup; Linux: docs/local-dns.md.
  rc=0; curl -s -o /dev/null --max-time 2 http://test.localhost/ 2>/dev/null || rc=$?
  if [ "$rc" -eq 6 ]; then
    printf "  %s\n" "⚠ *.localhost does not resolve — run \`task dev:dns\` once (macOS) or see docs/local-dns.md"
  fi
fi
