#!/usr/bin/env bash
# hack/dev/workload-cluster.sh — prepare ONE kind workload cluster and register
# it with the suparship running on the tooling cluster.
#
#   hack/dev/workload-cluster.sh <name> [displayName]
#
#   1. install External Secrets + sealed-secrets INTO the workload cluster
#   2. register it with suparship via POST /api/v1/clusters
#
# Step 2 goes through the API rather than the `suparship cluster add` CLI so the
# host needs no Go toolchain (the contributor guide promises `go` is optional —
# suparship is compiled inside the dev container). The API path writes the same
# three objects: the cluster ConfigMap, the kubeconfig Secret, and the ArgoCD
# cluster Secret.
#
# The kubeconfig registered is the `kind ... --internal` form, whose server is a
# container-name address on the shared `kind` Docker network. That is what
# ArgoCD needs, because ArgoCD dials the workload API server from INSIDE the
# tooling cluster — the host-facing 127.0.0.1:PORT address would not resolve.
#
# Idempotent. Used by: Tiltfile (local_resource 'workload-<name>').
set -euo pipefail

NAME="${1:?usage: workload-cluster.sh <name> [displayName]}"
DISPLAY="${2:-$NAME}"
CTX="kind-${NAME}"

# Pin the TOOLING-cluster kubectl to the dev context — see hack/dev/lib.sh.
# Calls against the workload cluster pass --context "$CTX" explicitly.
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_dev_context

# NOTE: `command helm`, not the shadowed helm() from lib.sh. That function injects
# --kube-context for the TOOLING cluster; adding our own would pass the flag twice
# and leave which cluster we hit up to the flag parser. These calls target the
# WORKLOAD cluster, so bypass the shadow and be explicit.
# Versions: ESO matches the tooling cluster's pin in the Tiltfile.
ESO_VERSION="2.2.0"
SEALED_SECRETS_VERSION="2.18.6"

API="${SUPARSHIP_API:-http://localhost:8080}"
USER="${SUPARSHIP_DEV_USER:-admin}"
PASS="${SUPARSHIP_DEV_PASSWORD:-devpass}"

info() { printf "  \033[0;36m%s\033[0m\n" "$*"; }
ok()   { printf "  \033[0;32m✓\033[0m  %s\n" "$*"; }
die()  { printf "  \033[0;31mERROR:\033[0m %s\n" "$*" >&2; exit 1; }

# Capture before matching — see the note in lib.sh: `| grep -q` under pipefail
# intermittently reports a match as a failure via SIGPIPE.
CONTEXTS="$(command kubectl config get-contexts -o name 2>/dev/null || true)"
grep -qx "$CTX" <<<"$CONTEXTS" \
  || die "context $CTX not found. Run: task up:multi"

# ── 1. Workload-cluster prerequisites ─────────────────────────────────────
info "[$NAME] installing External Secrets + sealed-secrets"
command helm --kube-context "$CTX" repo add eso https://charts.external-secrets.io >/dev/null 2>&1 || true
command helm --kube-context "$CTX" repo add sealed-secrets https://bitnami-labs.github.io/sealed-secrets >/dev/null 2>&1 || true
command helm --kube-context "$CTX" repo update eso sealed-secrets >/dev/null 2>&1 || true

command helm --kube-context "$CTX" upgrade --install external-secrets eso/external-secrets \
  --namespace external-secrets --create-namespace \
  --version "$ESO_VERSION" --set installCRDs=true --wait --timeout 5m >/dev/null
ok "[$NAME] external-secrets $ESO_VERSION"

command helm --kube-context "$CTX" upgrade --install sealed-secrets sealed-secrets/sealed-secrets \
  --namespace kube-system \
  --version "$SEALED_SECRETS_VERSION" \
  --set fullnameOverride=sealed-secrets-controller \
  --wait --timeout 5m >/dev/null
# fullnameOverride matches seal.DefaultControllerName, which suparship looks up
# when fetching this cluster's sealing certificate.
ok "[$NAME] sealed-secrets $SEALED_SECRETS_VERSION"

# ── 2. Register with suparship ────────────────────────────────────────────
# --internal: server address reachable from inside the tooling cluster.
KUBECONFIG_B64="$(kind get kubeconfig --name "$NAME" --internal 2>/dev/null | base64 | tr -d '\n')"
[ -n "$KUBECONFIG_B64" ] || die "could not read kubeconfig for kind cluster $NAME"
API_SERVER="$(kind get kubeconfig --name "$NAME" --internal 2>/dev/null \
  | sed -n 's/^ *server: *//p' | head -1)"
[ -n "$API_SERVER" ] || die "could not determine API server for $NAME"

COOKIE="$(curl -s -c - -X POST "$API/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$USER\",\"password\":\"$PASS\"}" 2>/dev/null \
  | awk '/suparship_session/ {print $NF}')"
[ -n "$COOKIE" ] || die "could not log in to $API as $USER (is the suparship resource green?)"

EXISTING="$(curl -s -o /dev/null -w '%{http_code}' -b "suparship_session=$COOKIE" \
  "$API/api/v1/clusters/$NAME")"
if [ "$EXISTING" = "200" ]; then
  ok "[$NAME] already registered ($API_SERVER)"
  exit 0
fi

BODY="$(printf '{"name":"%s","displayName":"%s","apiServer":"%s","kubeconfig":"%s"}' \
  "$NAME" "$DISPLAY" "$API_SERVER" "$KUBECONFIG_B64")"
CODE="$(curl -s -o /tmp/suparship-cluster-add.out -w '%{http_code}' \
  -X POST "$API/api/v1/clusters" -b "suparship_session=$COOKIE" \
  -H 'Content-Type: application/json' -d "$BODY")"
case "$CODE" in
  2*) ok "[$NAME] registered ($API_SERVER)" ;;
  *)  die "[$NAME] registration failed (HTTP $CODE): $(head -c 300 /tmp/suparship-cluster-add.out)" ;;
esac
