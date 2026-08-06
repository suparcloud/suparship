#!/usr/bin/env bash
# hack/dev/vault-bootstrap.sh — the non-Helm parts of the local HashiCorp Vault
# setup, run by the Tiltfile after the `vault` helm_resource is up (vault mode
# only):
#
#   0. initialise Vault (first run only) and unseal it (every run)
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
# ── Why step 0 exists ──────────────────────────────────────────────────────
# The dev Vault runs standalone with file storage on a PVC so its data survives a
# pod restart. That persistence costs auto-unseal: Vault comes up uninitialised
# once and SEALED after every restart. So this script initialises on first run
# (1 key share, 1 threshold — dev convenience, never do this for real), stashes
# the root token and unseal key in a Secret, and unseals from that stash on every
# later run.
#
# RE-RUN THIS AFTER A VAULT POD RESTART. Nothing in the cluster unseals Vault for
# you — that would mean handing the unseal key to a controller, which is not
# worth building for a dev loop. In Tilt it is one click on `vault-bootstrap`.
#
# DEV ONLY, and not only because of the 1-of-1 unseal key: the root token is
# reused as both suparship's write token and ESO's read token, so nothing here
# is least-privilege. A real install mints scoped per-scope policies (see
# `secrets.VaultReadPolicies` and docs/secrets.md "Least privilege").
#
# Idempotent. Used by: Tiltfile (local_resource 'vault-bootstrap', vault mode).
set -euo pipefail

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_dev_context

VAULT_NS="${SUPARSHIP_VAULT_NS:-vault}"
VAULT_POD="vault-0"
VAULT_MOUNT="${SUPARSHIP_VAULT_MOUNT:-suparship}"
SYSTEM_NS="${SUPARSHIP_SYSTEM_NAMESPACE:-suparship-system}"
ESO_NS="${SUPARSHIP_ESO_NAMESPACE:-external-secrets}"
MULTI="${SUPARSHIP_MULTI:-0}"
# Where the generated root token + unseal key are stashed between runs. In the
# vault namespace so `kind delete cluster` (or deleting the namespace) takes the
# keys with the data they unseal — leaving a stale key next to a wiped PVC would
# just produce a confusing failure on the next run.
KEYS_SECRET="vault-dev-keys"

info() { printf "  \033[0;36m%s\033[0m\n" "$*"; }
ok()   { printf "  \033[0;32m✓\033[0m  %s\n" "$*"; }
warn() { printf "  \033[0;33m–\033[0m  %s\n" "$*"; }
die()  { printf "  \033[0;31mERROR:\033[0m %s\n" "$*" >&2; exit 1; }

# vault runs a vault CLI command inside the pod. VAULT_ADDR is set in the pod's
# env by the chart; VAULT_TOKEN is passed per-call once we have one.
vault_exec() { kubectl -n "$VAULT_NS" exec "$VAULT_POD" -- "$@"; }

# ── 0. Init (first run) + unseal (every run) ───────────────────────────────
# Wait for Running, NOT Ready: a sealed Vault fails its readiness probe by
# design, so waiting for Ready here would deadlock — we are the thing that makes
# it ready. (The Tiltfile disables the probe for the same reason.)
info "Waiting for the Vault pod to be Running..."
for _ in $(seq 1 60); do
  phase="$(kubectl -n "$VAULT_NS" get pod "$VAULT_POD" -o jsonpath='{.status.phase}' 2>/dev/null || true)"
  [ "$phase" = "Running" ] && break
  sleep 2
done
[ "${phase:-}" = "Running" ] \
  || die "$VAULT_POD is not Running in namespace $VAULT_NS (phase: ${phase:-<none>}) — is the vault helm_resource up?"

# `vault status` exits 0 unsealed, 2 sealed, 1 on error (including "not yet
# initialised"), so read the JSON rather than branching on the exit code.
read_status() {
  vault_exec vault status -format=json 2>/dev/null || true
}
status="$(read_status)"
# An empty/unparseable status means the server is still booting — retry briefly.
if [ -z "$status" ]; then
  for _ in $(seq 1 30); do
    sleep 2
    status="$(read_status)"
    [ -n "$status" ] && break
  done
fi
[ -n "$status" ] || die "could not read 'vault status' from $VAULT_POD"

initialized="$(printf '%s' "$status" | grep -o '"initialized"[[:space:]]*:[[:space:]]*[a-z]*' | grep -o '[a-z]*$' || echo false)"

if [ "$initialized" != "true" ]; then
  info "Vault is uninitialised — initialising (1 key share, dev only)..."
  init_json="$(vault_exec vault operator init -key-shares=1 -key-threshold=1 -format=json)" \
    || die "vault operator init failed"
  # Parse without jq: the dev loop's preflight does not require it. `vault
  # -format=json` PRETTY-PRINTS, so unseal_keys_b64 spans lines and grep -o (which
  # is line-based) cannot match the array — flatten first. root_token sits on one
  # line, so it needs no flattening.
  UNSEAL_KEY="$(printf '%s' "$init_json" | tr '\n' ' ' \
    | grep -o '"unseal_keys_b64"[^]]*]' | grep -o '"[^"]*"' | tail -1 | tr -d '"')"
  ROOT_TOKEN="$(printf '%s' "$init_json" \
    | grep -o '"root_token"[[:space:]]*:[[:space:]]*"[^"]*"' | sed 's/.*"\([^"]*\)"$/\1/')"
  [ -n "$UNSEAL_KEY" ] && [ -n "$ROOT_TOKEN" ] || die "could not parse init output (keys/token)"

  kubectl -n "$VAULT_NS" create secret generic "$KEYS_SECRET" \
    --from-literal=unseal-key="$UNSEAL_KEY" \
    --from-literal=root-token="$ROOT_TOKEN" \
    --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  ok "initialised; root token + unseal key stashed in $VAULT_NS/$KEYS_SECRET"
else
  # Already initialised: the keys must be in the stash, or this Vault's data is
  # unreachable and the honest move is to say so rather than fail obscurely later.
  kubectl -n "$VAULT_NS" get secret "$KEYS_SECRET" >/dev/null 2>&1 || die \
    "Vault is initialised but $VAULT_NS/$KEYS_SECRET is missing, so it cannot be unsealed.
     Its data is unrecoverable without the key. Wipe and start over:
       kubectl -n $VAULT_NS delete pvc data-$VAULT_POD && kubectl -n $VAULT_NS delete pod $VAULT_POD
     then re-run this script."
  UNSEAL_KEY="$(kubectl -n "$VAULT_NS" get secret "$KEYS_SECRET" -o jsonpath='{.data.unseal-key}' | base64 -d)"
  ROOT_TOKEN="$(kubectl -n "$VAULT_NS" get secret "$KEYS_SECRET" -o jsonpath='{.data.root-token}' | base64 -d)"
  ok "already initialised; keys read from $VAULT_NS/$KEYS_SECRET"
fi

sealed="$(printf '%s' "$(read_status)" | grep -o '"sealed"[[:space:]]*:[[:space:]]*[a-z]*' | grep -o '[a-z]*$' || echo true)"
if [ "$sealed" = "true" ]; then
  info "Vault is sealed — unsealing..."
  vault_exec vault operator unseal "$UNSEAL_KEY" >/dev/null || die "vault operator unseal failed"
  ok "unsealed"
else
  ok "already unsealed"
fi

# Every later step authenticates with the generated root token.
VAULT_DEV_TOKEN="${SUPARSHIP_VAULT_TOKEN:-$ROOT_TOKEN}"

# ── 1. KV v2 mount ─────────────────────────────────────────────────────────

# `vault secrets enable` is not idempotent (errors on an existing mount), so
# check first. The pod's env carries VAULT_ADDR but NOT a token — dev-mode Vault
# used to preset one, standalone does not — so each call passes VAULT_TOKEN.
vault_auth() { vault_exec env VAULT_TOKEN="$VAULT_DEV_TOKEN" "$@"; }

if vault_auth vault secrets list -format=json 2>/dev/null \
    | grep -q "\"${VAULT_MOUNT}/\""; then
  ok "KV v2 mount '${VAULT_MOUNT}' already enabled"
else
  vault_auth vault secrets enable -path="$VAULT_MOUNT" -version=2 kv >/dev/null \
    || die "could not enable the KV v2 mount '${VAULT_MOUNT}'"
  ok "KV v2 mount '${VAULT_MOUNT}' enabled"
fi

# ── 1b. The address every consumer dials ───────────────────────────────────
# There is ONE org-level Vault address, and it is rendered into every cluster's
# ClusterSecretStore — so it must be reachable from the tooling cluster AND
# every workload cluster. Single-cluster: the Service DNS name is fine.
# Multi-cluster: workload clusters cannot resolve the tooling cluster's
# Service DNS, but all kind clusters share the "kind" docker network — so
# expose Vault on a NodePort and address it via the tooling node's
# docker-network IP (reachable from every cluster's pods; NOT from the macOS
# host, which is why the port-forward at localhost:8200 still exists for you).
if [ "$MULTI" = "1" ]; then
  info "multi mode: exposing Vault on a NodePort for cross-cluster access..."
  kubectl -n "$VAULT_NS" patch svc vault -p '{"spec":{"type":"NodePort"}}' >/dev/null
  NODE_PORT="$(kubectl -n "$VAULT_NS" get svc vault -o jsonpath='{.spec.ports[?(@.name=="http")].nodePort}')"
  NODE_IP="$(docker inspect suparship-dev-control-plane \
    --format '{{with index .NetworkSettings.Networks "kind"}}{{.IPAddress}}{{end}}' 2>/dev/null || true)"
  [ -n "$NODE_PORT" ] || die "could not read Vault NodePort"
  [ -n "$NODE_IP" ] || die "could not determine the tooling node's docker-network IP"
  VAULT_ADDR="http://${NODE_IP}:${NODE_PORT}"
  ok "Vault reachable cross-cluster at $VAULT_ADDR"
else
  VAULT_ADDR="http://vault.${VAULT_NS}.svc.cluster.local:8200"
fi

# ── 2. suparship's write token ─────────────────────────────────────────────
# The name/key pair matches secrets.VaultTokenSecretName/VaultTokenSecretKey.
kubectl -n "$SYSTEM_NS" create secret generic suparship-vault-token \
  --from-literal=token="$VAULT_DEV_TOKEN" \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null
ok "write-token Secret suparship-vault-token in $SYSTEM_NS (Vault's root token)"

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

printf "\n  Vault (DEV ONLY — persistent file storage, 1-of-1 unseal key, root token\n"
printf "  reused as both write and read token; none of this is least-privilege):\n"
printf "    UI/API : http://localhost:8200\n"
printf "    token  : %s\n" "$VAULT_DEV_TOKEN"
printf "    keys   : kubectl -n %s get secret %s -o yaml\n" "$VAULT_NS" "$KEYS_SECRET"
printf "    CLI    : kubectl -n %s exec -it %s -- env VAULT_TOKEN=%s vault kv list %s/\n" \
  "$VAULT_NS" "$VAULT_POD" "$VAULT_DEV_TOKEN" "$VAULT_MOUNT"
printf "\n  Data persists across pod restarts. After a restart Vault comes back SEALED —\n"
printf "  re-run this script (or re-trigger 'vault-bootstrap' in Tilt) to unseal it.\n"
printf "  Reset: kubectl -n %s delete pvc data-%s secret %s && kubectl -n %s delete pod %s\n\n" \
  "$VAULT_NS" "$VAULT_POD" "$KEYS_SECRET" "$VAULT_NS" "$VAULT_POD"
