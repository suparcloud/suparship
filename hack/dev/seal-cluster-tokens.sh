#!/usr/bin/env bash
# seal-cluster-tokens.sh — run the REAL per-cluster seal pipeline in dev.
#
# Production flow: an operator pastes the backend read token per workload
# cluster (Settings → Secrets Backend); suparship stashes it, seals it with
# that cluster's sealed-secrets certificate, and publishes the unified
# ClusterSecretStore through gitops. The dev loop used to skip this entirely
# (vault-bootstrap's shortcut store), which left credential health warning
# "no sealed read token" for every seeded cluster and the Platform page
# "Not ready".
#
# This script drives the same API an operator would: it pastes the dev Vault
# write token (dev-only simplification — production would mint a scoped read
# token) for every registered cluster. Idempotent: re-pasting re-seals.
set -euo pipefail

API="${SUPARSHIP_API:-http://localhost:8080/api/v1}"
USER="${SUPARSHIP_DEV_USER:-admin@local}"
PASS="${SUPARSHIP_DEV_PASSWORD:-admin123}"
SYSTEM_NS="suparship-system"

say() { printf '   %s\n' "$*"; }
ok()  { printf '✅ %s\n' "$*"; }
die() { printf '❌ %s\n' "$*" >&2; exit 1; }

token="$(kubectl -n "$SYSTEM_NS" get secret suparship-vault-token \
  -o jsonpath='{.data.token}' 2>/dev/null | base64 -d)" \
  || die "vault write token secret not found — did vault-bootstrap run?"
[ -n "$token" ] || die "suparship-vault-token exists but its token key is empty"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
cookies="$tmp/cookies.txt"

curl -sS -o /dev/null --max-time 10 -c "$cookies" \
  -X POST "$API/auth/login" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$USER\",\"password\":\"$PASS\"}" \
  || die "suparship API unreachable at $API"

clusters="$(curl -sS -b "$cookies" "$API/clusters" \
  | python3 -c 'import json,sys; [print(c["name"]) for c in json.load(sys.stdin).get("clusters", [])]')" \
  || die "could not list clusters"
[ -n "$clusters" ] || die "no clusters registered yet — is the seed done?"

failed=0
for cluster in $clusters; do
  resp="$(curl -sS -b "$cookies" --max-time 120 \
    -X POST "$API/org/secret-backend/clusters/$cluster/connect-token" \
    -H 'Content-Type: application/json' \
    -d "{\"token\":\"$token\"}")"
  if printf '%s' "$resp" | grep -q '"error"'; then
    printf '⚠️  %s: %s\n' "$cluster" "$resp"
    failed=1
  else
    ok "sealed read token published for cluster $cluster"
  fi
done

[ "$failed" -eq 0 ] || die "some clusters failed to seal — see above"
ok "per-cluster seal pipeline complete"
