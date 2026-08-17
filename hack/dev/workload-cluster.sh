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
# Pin kubectl/helm to the TOOLING cluster — see hack/dev/lib.sh. Calls against
# the WORKLOAD cluster go through its own kubeconfig instead (built below).
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_dev_context

# `command helm` bypasses the helm() shadow from lib.sh, which would inject the
# tooling cluster's --kube-context alongside our --kubeconfig.
# Versions: ESO matches the tooling cluster's pin in the Tiltfile.
ESO_VERSION="2.2.0"
SEALED_SECRETS_VERSION="2.18.6"

API="${SUPARSHIP_API:-http://localhost:8080}"
USER="${SUPARSHIP_DEV_USER:-admin@local}"
PASS="${SUPARSHIP_DEV_PASSWORD:-admin123}"

info() { printf "  \033[0;36m%s\033[0m\n" "$*"; }
ok()   { printf "  \033[0;32m✓\033[0m  %s\n" "$*"; }
die()  { printf "  \033[0;31mERROR:\033[0m %s\n" "$*" >&2; exit 1; }

# Talk to the workload cluster through a STANDALONE kubeconfig from kind, not
# through a context in the ambient one.
#
# Tilt hands each local_resource a minimal kubeconfig containing only its own
# context (kind-suparship-dev) — a sensible guardrail, but it means the workload
# contexts are invisible here even though they exist in your shell. Looking them
# up by context name works when you run this script by hand and fails every time
# under Tilt, which is exactly the sort of gap a from-scratch run exposes.
#
# kind is the source of truth for a kind cluster's credentials, so ask it.
kind get clusters 2>/dev/null | grep -qx "$NAME" \
  || die "kind cluster $NAME not found. Run: task up:multi"

WORKLOAD_KUBECONFIG="$(mktemp -t suparship-wl-XXXXXX)"
trap 'rm -f "$WORKLOAD_KUBECONFIG"' EXIT
kind get kubeconfig --name "$NAME" > "$WORKLOAD_KUBECONFIG" 2>/dev/null
[ -s "$WORKLOAD_KUBECONFIG" ] || die "could not export kubeconfig for kind cluster $NAME"

# ── 1. Workload-cluster prerequisites ─────────────────────────────────────
info "[$NAME] installing External Secrets + sealed-secrets"
command helm --kubeconfig "$WORKLOAD_KUBECONFIG" repo add eso https://charts.external-secrets.io >/dev/null 2>&1 || true
command helm --kubeconfig "$WORKLOAD_KUBECONFIG" repo add sealed-secrets https://bitnami-labs.github.io/sealed-secrets >/dev/null 2>&1 || true
command helm --kubeconfig "$WORKLOAD_KUBECONFIG" repo update eso sealed-secrets >/dev/null 2>&1 || true

command helm --kubeconfig "$WORKLOAD_KUBECONFIG" upgrade --install external-secrets eso/external-secrets \
  --namespace external-secrets --create-namespace \
  --version "$ESO_VERSION" --set installCRDs=true --wait --timeout 5m >/dev/null
ok "[$NAME] external-secrets $ESO_VERSION"

command helm --kubeconfig "$WORKLOAD_KUBECONFIG" upgrade --install sealed-secrets sealed-secrets/sealed-secrets \
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
