#!/usr/bin/env bash
# hack/install-eso.sh — install External Secrets Operator (ESO) and
# bootstrap the demo ClusterSecretStores into the suparShip dev cluster.
#
# ESO translates external secret backend references into native Kubernetes
# Secrets. suparShip uses it to pull secrets referenced by SecretRefs in
# the five-level env config hierarchy into Kubernetes Secrets that containers
# load via envFrom.
#
# This script does three things:
#   1. Install ESO via Helm (external-secrets/external-secrets)
#   2. Create the suparship-eso-reader ServiceAccount in suparship-system
#      (used by the k8s ClusterSecretStore to read Secrets)
#   3. Apply the demo ClusterSecretStores (k8s backend configured; vault
#      and aws-sm left as stubs for users to fill in)
#
# Idempotent: uses `helm upgrade --install` and `kubectl apply`.
#
# Chart:   external-secrets/external-secrets
# Repo:    https://charts.external-secrets.io
# Source:  https://github.com/external-secrets/external-secrets
#
# Usage:
#   ./hack/install-eso.sh      # run directly
#   task dev:cluster:eso       # preferred: via Taskfile
set -euo pipefail

# ── Pinned versions ────────────────────────────────────────────────────────
# https://github.com/external-secrets/external-secrets/releases
ESO_CHART_VERSION="2.2.0"       # chart 2.2.0 → app v0.17.0
ESO_APP_VERSION="v0.17.0"
ESO_CHART_REPO="https://charts.external-secrets.io"
ESO_CHART_NAME="external-secrets"
ESO_REPO_ALIAS="external-secrets"

# ── Config ─────────────────────────────────────────────────────────────────
ESO_NAMESPACE="external-secrets"
ESO_RELEASE="external-secrets"
SYSTEM_NAMESPACE="suparship-system"
ESO_SA_NAME="suparship-eso-reader"
HELM_TIMEOUT="180s"
PROFILE="${SUPARSHIP_PROFILE:-demo}"

# ── Repo root ──────────────────────────────────────────────────────────────
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# ── Helpers ────────────────────────────────────────────────────────────────
info()  { printf "  \033[0;36m%s\033[0m\n" "$*"; }
ok()    { printf "  \033[0;32m✓\033[0m  %s\n" "$*"; }
skip()  { printf "  \033[0;33m–\033[0m  %s\n" "$*"; }
die()   { printf "  \033[0;31mERROR:\033[0m %s\n" "$*" >&2; exit 1; }

# ── Banner ─────────────────────────────────────────────────────────────────
echo ""
echo "  suparShip — External Secrets Operator install  (${ESO_APP_VERSION})"
echo "  ──────────────────────────────────────────────────────────────────"
echo ""

# ── 1. Prereq checks ───────────────────────────────────────────────────────
info "Checking prerequisites..."

for cmd in kubectl helm; do
  if command -v "$cmd" >/dev/null 2>&1; then
    ok "$cmd"
  else
    die "'$cmd' not found. Run: task dev:cluster:bootstrap"
  fi
done

kubectl cluster-info >/dev/null 2>&1 \
  || die "No cluster reachable. Run: task dev:cluster:bootstrap"

# suparship-system must exist (created by bootstrap-cluster.sh).
kubectl get namespace "${SYSTEM_NAMESPACE}" >/dev/null 2>&1 \
  || die "Namespace '${SYSTEM_NAMESPACE}' not found. Run: task dev:cluster:bootstrap"

echo ""

# ── 2. Add / update Helm repo ──────────────────────────────────────────────
info "Helm repo '${ESO_REPO_ALIAS}'..."
if helm repo list 2>/dev/null | grep -q "^${ESO_REPO_ALIAS}"; then
  skip "already added — updating"
  helm repo update "${ESO_REPO_ALIAS}" >/dev/null
else
  helm repo add "${ESO_REPO_ALIAS}" "${ESO_CHART_REPO}"
  helm repo update "${ESO_REPO_ALIAS}" >/dev/null
  ok "added"
fi
echo ""

# ── 3. Create ESO namespace ────────────────────────────────────────────────
info "Namespace '${ESO_NAMESPACE}'..."
if kubectl get namespace "${ESO_NAMESPACE}" >/dev/null 2>&1; then
  skip "already exists"
else
  kubectl create namespace "${ESO_NAMESPACE}"
  ok "created"
fi
echo ""

# ── 4. Install or upgrade ESO ──────────────────────────────────────────────
info "ESO ${ESO_APP_VERSION} (helm upgrade --install)..."
echo "  Chart: ${ESO_REPO_ALIAS}/${ESO_CHART_NAME}:${ESO_CHART_VERSION}"
echo ""

helm upgrade --install "${ESO_RELEASE}" \
  "${ESO_REPO_ALIAS}/${ESO_CHART_NAME}" \
  --namespace  "${ESO_NAMESPACE}" \
  --version    "${ESO_CHART_VERSION}" \
  --set        "installCRDs=true" \
  --wait \
  --timeout    "${HELM_TIMEOUT}" \
  --atomic

ok "ESO installed / up-to-date"
echo ""

# ── 5. Confirm controller rollout ─────────────────────────────────────────
info "Waiting for external-secrets controller..."
kubectl rollout status deployment/external-secrets \
  -n "${ESO_NAMESPACE}" \
  --timeout="${HELM_TIMEOUT}" >/dev/null 2>&1 \
  && ok "external-secrets controller is running" \
  || skip "rollout check skipped — verify: kubectl get pods -n ${ESO_NAMESPACE}"
echo ""

# ── 6. Create suparship-eso-reader ServiceAccount ─────────────────────────
# The k8s ClusterSecretStore uses this SA to read Secrets from suparship-system.
info "ServiceAccount '${ESO_SA_NAME}' in '${SYSTEM_NAMESPACE}'..."
kubectl apply -f - <<EOF
apiVersion: v1
kind: ServiceAccount
metadata:
  name: ${ESO_SA_NAME}
  namespace: ${SYSTEM_NAMESPACE}
  labels:
    suparship.io/managed-by: suparship
  annotations:
    suparship.io/description: "ServiceAccount used by the suparship-k8s-store ClusterSecretStore to read Secrets from ${SYSTEM_NAMESPACE}."
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: ${ESO_SA_NAME}
  namespace: ${SYSTEM_NAMESPACE}
  labels:
    suparship.io/managed-by: suparship
rules:
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: ${ESO_SA_NAME}
  namespace: ${SYSTEM_NAMESPACE}
  labels:
    suparship.io/managed-by: suparship
subjects:
  - kind: ServiceAccount
    name: ${ESO_SA_NAME}
    namespace: ${SYSTEM_NAMESPACE}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: ${ESO_SA_NAME}
EOF
ok "ServiceAccount, Role, RoleBinding created/updated"
echo ""

# ── 6b. Wait for ClusterSecretStore CRD to be fully established ───────────
# Helm --wait ensures the controller pods are running but the CRDs may still
# be in the process of being registered with the API server.  Without this
# wait the subsequent kubectl apply fails with "no matches for kind
# ClusterSecretStore in version external-secrets.io/v1".
info "Waiting for ClusterSecretStore CRD to be established..."
kubectl wait crd/clustersecretstores.external-secrets.io \
  --for=condition=Established \
  --timeout=60s \
  && ok "ClusterSecretStore CRD is established" \
  || die "Timed out waiting for ClusterSecretStore CRD to be established"
echo ""

# ── 7. Apply demo ClusterSecretStores ─────────────────────────────────────
# For the demo profile, apply the k8s ClusterSecretStore. For core/full
# profiles, the publisher generates stores from VaultBindings — users
# configure them via `suparship secrets backend set`.
if [ "${PROFILE}" = "demo" ]; then
  info "Applying ClusterSecretStore (k8s backend — demo profile)..."
  kubectl apply -f - <<EOF
apiVersion: external-secrets.io/v1
kind: ClusterSecretStore
metadata:
  name: suparship-k8s-store
  labels:
    app.kubernetes.io/managed-by: suparship
  annotations:
    suparship.io/description: "Kubernetes Secrets backend for demo/default use. Reads Secrets from suparship-system namespace."
spec:
  provider:
    kubernetes:
      remoteNamespace: ${SYSTEM_NAMESPACE}
      auth:
        serviceAccount:
          name: ${ESO_SA_NAME}
          namespace: ${SYSTEM_NAMESPACE}
EOF
  ok "suparship-k8s-store applied (demo)"
else
  info "Non-demo profile (${PROFILE}) — skipping ClusterSecretStore creation."
  info "Configure your secret backend with:"
  info "  suparship secrets backend set --type=1password --mode=connect --connect-host=<url> --isolation=hard"
  info "  suparship secrets backend binding add --env=prod --vault=<uuid> --auth-secret=op-prod-token"
  info "  suparship secrets token import --env=prod --from-file=token.txt"
  ok "ESO installed — configure backend via CLI or UI"
fi
echo ""

# ── Done ───────────────────────────────────────────────────────────────────
cat <<EOF
  ──────────────────────────────────────────────────────────────────
  External Secrets Operator is ready.

  Profile      ${PROFILE}
  Namespace    ${ESO_NAMESPACE}
  App version  ${ESO_APP_VERSION}
  Chart        ${ESO_REPO_ALIAS}/${ESO_CHART_NAME}:${ESO_CHART_VERSION}

  Verify pods:
    kubectl get pods -n ${ESO_NAMESPACE}

  Verify stores:
    kubectl get clustersecretstores

  Configure your secret backend:
    suparship secrets backend set --type=1password ...
    suparship secrets backend binding add --env=prod ...
    suparship secrets token import --env=prod --from-file=token.txt

  Docs:
    https://external-secrets.io

EOF
