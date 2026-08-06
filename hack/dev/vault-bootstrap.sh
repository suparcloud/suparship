#!/usr/bin/env bash
# hack/dev/vault-bootstrap.sh — the non-Helm parts of the local HashiCorp Vault
# setup, run by the Tiltfile after the `vault` helm_resource is up (vault mode
# only):
#
#   1. enable the KV v2 mount suparship writes items to (default: suparship)
#   2. create the write-token Secret the server reads (suparship-vault-token)
#   3. create the ESO read-token Secret (vault-token in external-secrets) — a
#      dev shortcut standing in for the per-cluster sealed-token flow, which on
#      a real install publishes this Secret through gitops
#   4. switch the org's secret backend to vault via the API
#
# Step 4 goes through PUT /org/secret-backend deliberately, NOT by editing the
# suparship-org-config ConfigMap: the handler MERGES onto the stored config
# (pinned by TestPutSecretsBackend_PreservesConfigAcrossTypeSwitch), so this is
# safe to run after hack/seed.sh or seed-multi.sh have rewritten the org — and
# it exercises the same code path an operator's UI click does. Chart values
# (secrets.backend) are irrelevant in the dev loop: the Tiltfile strips both the
# org ConfigMap and the Helm hooks from the chart render.
#
# DEV ONLY. The Vault runs `server.dev.enabled=true`: in-memory storage, auto
# unsealed, root token "root". Everything it stores dies with the pod.
#
# Idempotent. Used by: Tiltfile (local_resource 'vault-bootstrap', vault mode).
set -euo pipefail

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_dev_context

VAULT_NS="${SUPARSHIP_VAULT_NS:-vault}"
VAULT_POD="vault-0"
VAULT_MOUNT="${SUPARSHIP_VAULT_MOUNT:-suparship}"
# The fixed dev root token (set via server.dev.devRootToken in the Tiltfile).
VAULT_DEV_TOKEN="${SUPARSHIP_VAULT_TOKEN:-root}"
# In-cluster address: suparship and ESO both dial the Service, not the
# host port-forward. Dev mode serves plain HTTP.
VAULT_ADDR="http://vault.${VAULT_NS}.svc.cluster.local:8200"
SYSTEM_NS="${SUPARSHIP_SYSTEM_NAMESPACE:-suparship-system}"
ESO_NS="${SUPARSHIP_ESO_NAMESPACE:-external-secrets}"

info() { printf "  \033[0;36m%s\033[0m\n" "$*"; }
ok()   { printf "  \033[0;32m✓\033[0m  %s\n" "$*"; }
warn() { printf "  \033[0;33m–\033[0m  %s\n" "$*"; }
die()  { printf "  \033[0;31mERROR:\033[0m %s\n" "$*" >&2; exit 1; }

# ── 1. KV v2 mount ─────────────────────────────────────────────────────────
info "Waiting for Vault pod..."
kubectl -n "$VAULT_NS" wait pod/"$VAULT_POD" --for=condition=Ready --timeout=120s >/dev/null \
  || die "vault-0 not Ready in namespace $VAULT_NS — is the vault helm_resource up?"

# `vault secrets enable` is not idempotent (errors on an existing mount), so
# check first. The dev pod has VAULT_ADDR/VAULT_TOKEN preconfigured for root.
if kubectl -n "$VAULT_NS" exec "$VAULT_POD" -- vault secrets list -format=json 2>/dev/null \
    | grep -q "\"${VAULT_MOUNT}/\""; then
  ok "KV v2 mount '${VAULT_MOUNT}' already enabled"
else
  kubectl -n "$VAULT_NS" exec "$VAULT_POD" -- \
    vault secrets enable -path="$VAULT_MOUNT" -version=2 kv >/dev/null
  ok "KV v2 mount '${VAULT_MOUNT}' enabled"
fi

# ── 2. suparship's write token ─────────────────────────────────────────────
# The name/key pair matches secrets.VaultTokenSecretName/VaultTokenSecretKey.
kubectl -n "$SYSTEM_NS" create secret generic suparship-vault-token \
  --from-literal=token="$VAULT_DEV_TOKEN" \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null
ok "write-token Secret suparship-vault-token in $SYSTEM_NS (dev root token)"

# ── 3. ESO's read token ────────────────────────────────────────────────────
# secrets.VaultTokenClusterSecretName in the namespace the generated
# ClusterSecretStore's tokenSecretRef points at. On a real cluster this arrives
# as a SealedSecret through gitops; creating it directly is the dev shortcut,
# same as the tooling cluster never sealing its own credentials.
kubectl -n "$ESO_NS" create secret generic vault-token \
  --from-literal=token="$VAULT_DEV_TOKEN" \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null
ok "read-token Secret vault-token in $ESO_NS"

# ── 4. Point the org at the vault backend (API merge, not a CM rewrite) ────
API="${SUPARSHIP_API:-http://localhost:8080}"
USER="${SUPARSHIP_DEV_USER:-admin}"
PASS="${SUPARSHIP_DEV_PASSWORD:-devpass}"

COOKIE="$(curl -s -c - -X POST "$API/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$USER\",\"password\":\"$PASS\"}" 2>/dev/null \
  | awk '/suparship_session/ {print $NF}')"

if [ -z "$COOKIE" ]; then
  warn "could not reach $API — org backend NOT switched to vault."
  warn "Re-run this script once suparship is up, or switch it in"
  warn "Settings → Secrets Backend (address: $VAULT_ADDR, mount: $VAULT_MOUNT)."
else
  code="$(curl -s -o /tmp/vault-bootstrap-resp.$$ -w '%{http_code}' \
    -X PUT -b "suparship_session=$COOKIE" \
    -H 'Content-Type: application/json' \
    "$API/api/v1/org/secret-backend" \
    -d "{\"type\":\"vault\",\"vault\":{\"address\":\"$VAULT_ADDR\",\"mount\":\"$VAULT_MOUNT\"}}")"
  if [ "${code:0:1}" = "2" ]; then
    ok "org secret backend set to vault ($VAULT_ADDR, mount '$VAULT_MOUNT')"
  else
    warn "PUT /org/secret-backend returned HTTP $code:"
    sed 's/^/     /' /tmp/vault-bootstrap-resp.$$ >&2 || true
  fi
  rm -f /tmp/vault-bootstrap-resp.$$
fi

printf "\n  Vault (dev mode, DEV ONLY — in-memory, root token %q):\n" "$VAULT_DEV_TOKEN"
printf "    UI/API : http://localhost:8200      (token: %s)\n" "$VAULT_DEV_TOKEN"
printf "    CLI    : kubectl -n %s exec -it %s -- vault kv list %s/\n\n" "$VAULT_NS" "$VAULT_POD" "$VAULT_MOUNT"
