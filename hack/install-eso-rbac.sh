#!/usr/bin/env bash
# hack/install-eso-rbac.sh — the non-Helm parts of ESO setup, extracted from
# the (now removed) monolithic ESO installer, so the Tiltfile can run them after the
# external-secrets helm_resource is up:
#
#   1. suparship-eso-reader ServiceAccount + cluster-wide read ClusterRole/Binding
#   2. wait for the ClusterSecretStore CRD to be Established (avoids a race)
#   3. apply the demo global ClusterSecretStore (k8s backend)
#
# Idempotent. Used by: Tiltfile (local_resource 'eso-reader').
set -euo pipefail

# Pin kubectl to the dev cluster — see hack/dev/lib.sh.
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/dev/lib.sh"
require_dev_context

SYSTEM_NAMESPACE="${SUPARSHIP_SYSTEM_NAMESPACE:-suparship-system}"
ESO_SA_NAME="suparship-eso-reader"
PROFILE="${SUPARSHIP_PROFILE:-demo}"

info() { printf "  \033[0;36m%s\033[0m\n" "$*"; }
ok()   { printf "  \033[0;32m✓\033[0m  %s\n" "$*"; }
die()  { printf "  \033[0;31mERROR:\033[0m %s\n" "$*" >&2; exit 1; }

kubectl get namespace "${SYSTEM_NAMESPACE}" >/dev/null 2>&1 \
  || kubectl create namespace "${SYSTEM_NAMESPACE}"

# ── 1. eso-reader SA + cluster-wide read (vault namespaces are dynamic) ────
info "ServiceAccount '${ESO_SA_NAME}' (+ cluster-wide secret read)..."
kubectl apply -f - <<EOF
apiVersion: v1
kind: ServiceAccount
metadata:
  name: ${ESO_SA_NAME}
  namespace: ${SYSTEM_NAMESPACE}
  labels:
    suparship.io/managed-by: suparship
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: ${ESO_SA_NAME}
  labels:
    suparship.io/managed-by: suparship
rules:
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: ${ESO_SA_NAME}
  labels:
    suparship.io/managed-by: suparship
subjects:
  - kind: ServiceAccount
    name: ${ESO_SA_NAME}
    namespace: ${SYSTEM_NAMESPACE}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: ${ESO_SA_NAME}
EOF
ok "ServiceAccount, ClusterRole, ClusterRoleBinding ready"

# ── 2. Wait for the CRD before applying a ClusterSecretStore ──────────────
info "Waiting for ClusterSecretStore CRD to be established..."
kubectl wait crd/clustersecretstores.external-secrets.io \
  --for=condition=Established --timeout=60s \
  && ok "ClusterSecretStore CRD established" \
  || die "Timed out waiting for ClusterSecretStore CRD"

# ── 3. Demo global ClusterSecretStore (k8s backend) ───────────────────────
if [ "${PROFILE}" = "demo" ]; then
  info "Applying global ClusterSecretStore (k8s backend — demo profile)..."
  kubectl apply -f - <<EOF
apiVersion: external-secrets.io/v1
kind: ClusterSecretStore
metadata:
  name: suparship-store-global
  labels:
    app.kubernetes.io/managed-by: suparship
spec:
  provider:
    kubernetes:
      remoteNamespace: suparship-secrets-global
      auth:
        serviceAccount:
          name: ${ESO_SA_NAME}
          namespace: ${SYSTEM_NAMESPACE}
EOF
  ok "suparship-store-global applied (demo)"
fi
