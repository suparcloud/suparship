#!/usr/bin/env bash
# hack/install-gitea-init.sh — initialise the gitops monorepo in an
# already-running Gitea and wire it into ArgoCD.
#
# Extracted from hack/install-gitea.sh (steps 6-9) so it works against either
# the Tilt port-forward (http://localhost:3000, default) or the optional
# ingress (http://gitea.localhost:8880). Gitea itself is installed by the
# Tiltfile's `gitea` helm_resource; this script runs after it is ready.
#
#   1. push the gitops repo skeleton           (hack/init-gitops-repo.sh)
#   2. register the repo with ArgoCD           (Secret gitea-gitops-repo)
#   3. apply the root App-of-Apps              (Application suparship-apps)
#
# Idempotent. Used by: Tiltfile (local_resource 'init-gitops').
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# Host-reachable Gitea URL: port-forward by default, ingress when set.
export GITEA_HOST_URL="${GITEA_HOST_URL:-http://localhost:3000}"
export GITEA_ADMIN_USER="${GITEA_ADMIN_USER:-gitops}"
export GITEA_ADMIN_PASS="${GITEA_ADMIN_PASS:-gitops-dev-only}"
export GITOPS_REPO_ORG="${GITOPS_REPO_ORG:-gitops}"
export GITOPS_REPO_NAME="${GITOPS_REPO_NAME:-gitops}"
# In-cluster URL ArgoCD (also in-cluster) uses to reach Gitea.
export GITEA_CLUSTER_URL="${GITEA_CLUSTER_URL:-http://gitea-http.gitea.svc.cluster.local:3000}"
export REPO_ROOT
ARGOCD_NAMESPACE="${ARGOCD_NAMESPACE:-argocd}"

info() { printf "  \033[0;36m%s\033[0m\n" "$*"; }
ok()   { printf "  \033[0;32m✓\033[0m  %s\n" "$*"; }
skip() { printf "  \033[0;33m–\033[0m  %s\n" "$*"; }

# ── 1. Repo skeleton (also creates the repo + waits for Gitea) ─────────────
hack/init-gitops-repo.sh

# ── 2. Register the gitops repo with ArgoCD ───────────────────────────────
info "Registering gitops repo with ArgoCD..."
if kubectl get secret gitea-gitops-repo -n "${ARGOCD_NAMESPACE}" >/dev/null 2>&1; then
  skip "ArgoCD repo secret already exists"
else
  kubectl apply -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: gitea-gitops-repo
  namespace: ${ARGOCD_NAMESPACE}
  labels:
    argocd.argoproj.io/secret-type: repository
stringData:
  type: git
  url: ${GITEA_CLUSTER_URL}/${GITOPS_REPO_ORG}/${GITOPS_REPO_NAME}.git
  username: ${GITEA_ADMIN_USER}
  password: ${GITEA_ADMIN_PASS}
EOF
  ok "ArgoCD repo secret created"
fi

# ── 3. Root App-of-Apps watching gitops-output/_infra ─────────────────────
info "Applying root ArgoCD App of Apps (suparship-apps)..."
if kubectl get application suparship-apps -n "${ARGOCD_NAMESPACE}" >/dev/null 2>&1; then
  skip "suparship-apps Application already exists"
else
  kubectl apply -f - <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: suparship-apps
  namespace: ${ARGOCD_NAMESPACE}
  labels:
    suparship.io/managed-by: suparship
    suparship.io/role: root-app
  finalizers:
    - resources-finalizer.argocd.argoproj.io
spec:
  project: default
  source:
    repoURL: ${GITEA_CLUSTER_URL}/${GITOPS_REPO_ORG}/${GITOPS_REPO_NAME}.git
    targetRevision: main
    path: gitops-output
    directory:
      recurse: true
      include: "{_infra/*.yaml,_infra/kargo/*.yaml}"
  destination:
    server: https://kubernetes.default.svc
    namespace: ${ARGOCD_NAMESPACE}
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
EOF
  ok "suparship-apps Application created"
fi
