#!/usr/bin/env bash
# hack/install-gitea.sh — install Gitea (local git service) into the suparShip
# dev cluster, initialise the gitops monorepo, and bootstrap the root ArgoCD
# "App of Apps" that watches it.
#
# Idempotent: uses `helm upgrade --install` and skips steps already completed.
#
# Prerequisites:
#   - kind cluster running       (task dev:cluster:bootstrap)
#   - NGINX ingress controller   (task dev:cluster:ingress)
#   - ArgoCD installed           (task dev:cluster:argocd)
#
# After this script:
#   Gitea UI:          http://gitea.localhost:8880  (gitops / gitops-dev-only)
#   GitOps repo:       http://gitea.localhost:8880/gitops/gitops
#   Root ArgoCD App:   kubectl get app suparship-apps -n argocd
#   Credentials:       .env.cluster  (git-ignored, sourced locally)
#
# Repo layout pushed to Gitea:
#   charts/web-service/        Helm chart for the web-service template
#   gitops-output/             suparship writes ArgoCD Application CRDs here
#     <project>/<app>/<env>/
#       argocd-app.yaml        picked up by the root "suparship-apps" App
#
# Used by: hack/dev-cluster.sh
#
# Usage:
#   ./hack/install-gitea.sh       # run directly
#   task dev:cluster:gitea        # preferred: via Taskfile
set -euo pipefail

# ── Pinned versions ───────────────────────────────────────────────────────
GITEA_CHART_VERSION="10.6.0"   # gitea Helm chart → Gitea v1.22.x
# Changelog: https://gitea.com/gitea/helm-chart/releases

# ── Config ────────────────────────────────────────────────────────────────
GITEA_NAMESPACE="gitea"
GITEA_RELEASE="gitea"
GITEA_HELM_REPO_NAME="gitea-charts"
GITEA_HELM_REPO_URL="https://dl.gitea.com/charts/"
GITEA_VALUES="config/gitea/values-dev.yaml"

GITEA_ADMIN_USER="gitops"
GITEA_ADMIN_PASS="gitops-dev-only"

# Host-accessible URL (via kind port mapping 80→8880).
GITEA_HOST_URL="http://gitea.localhost:8880"
# In-cluster URL that ArgoCD (also in-cluster) uses.
GITEA_CLUSTER_URL="http://gitea-http.gitea.svc.cluster.local:3000"

GITOPS_REPO_NAME="gitops"
GITOPS_REPO_ORG="${GITEA_ADMIN_USER}"   # repo is owned by the admin user

HELM_TIMEOUT="300s"
ARGOCD_NAMESPACE="argocd"

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# Export vars needed by child scripts (init-gitops-repo.sh).
export GITEA_HOST_URL GITEA_ADMIN_USER GITEA_ADMIN_PASS
export GITOPS_REPO_ORG GITOPS_REPO_NAME GITEA_CLUSTER_URL REPO_ROOT

# ── Credential writer ─────────────────────────────────────────────────────
# shellcheck source=hack/write-dev-credentials.sh
source hack/write-dev-credentials.sh

# ── Helpers ───────────────────────────────────────────────────────────────
info()  { printf "  \033[0;36m%s\033[0m\n" "$*"; }
ok()    { printf "  \033[0;32m✓\033[0m  %s\n" "$*"; }
skip()  { printf "  \033[0;33m–\033[0m  %s\n" "$*"; }
die()   { printf "  \033[0;31mERROR:\033[0m %s\n" "$*" >&2; exit 1; }

# ── Banner ────────────────────────────────────────────────────────────────
echo ""
echo "  suparShip — Gitea install  (dev profile, chart ${GITEA_CHART_VERSION})"
echo "  ──────────────────────────────────────────────────────────────────"
echo ""

# ── 1. Prereq checks ──────────────────────────────────────────────────────
info "Checking prerequisites..."

for cmd in kubectl helm curl git; do
  if command -v "$cmd" >/dev/null 2>&1; then
    ok "$cmd"
  else
    die "'$cmd' not found."
  fi
done

kubectl get namespace "ingress-nginx" >/dev/null 2>&1 \
  || die "NGINX ingress controller not found. Run: task dev:cluster:ingress"

kubectl get namespace "${ARGOCD_NAMESPACE}" >/dev/null 2>&1 \
  || die "ArgoCD namespace not found. Run: task dev:cluster:argocd"

echo ""

# ── 2. Namespace ──────────────────────────────────────────────────────────
if kubectl get namespace "${GITEA_NAMESPACE}" >/dev/null 2>&1; then
  skip "namespace '${GITEA_NAMESPACE}' already exists"
else
  kubectl create namespace "${GITEA_NAMESPACE}"
  ok "namespace '${GITEA_NAMESPACE}' created"
fi
echo ""

# ── 3. Add / refresh Helm repo ───────────────────────────────────────────
info "Helm repo '${GITEA_HELM_REPO_NAME}'..."
if helm repo list 2>/dev/null | awk '{print $1}' | grep -qx "${GITEA_HELM_REPO_NAME}"; then
  skip "already added"
else
  helm repo add "${GITEA_HELM_REPO_NAME}" "${GITEA_HELM_REPO_URL}" >/dev/null
  ok "added (${GITEA_HELM_REPO_URL})"
fi

printf "  Updating repo index... "
helm repo update "${GITEA_HELM_REPO_NAME}" >/dev/null
echo "ok"
echo ""

# ── 4. Install or upgrade Gitea ──────────────────────────────────────────
if helm status "${GITEA_RELEASE}" -n "${GITEA_NAMESPACE}" >/dev/null 2>&1; then
  skip "Gitea already installed — skipping helm upgrade"
  echo ""
else
  info "Installing Gitea (helm upgrade --install)..."
  echo "  This may take a few minutes on first run (image pull)."
  echo ""

  helm upgrade --install "${GITEA_RELEASE}" \
    "${GITEA_HELM_REPO_NAME}/gitea" \
    --namespace  "${GITEA_NAMESPACE}" \
    --version    "${GITEA_CHART_VERSION}" \
    --values     "${GITEA_VALUES}" \
    --wait \
    --timeout    "${HELM_TIMEOUT}" \
    --atomic

  ok "Gitea installed"
  echo ""
fi

# ── 5. Wait for Gitea HTTP to be reachable via ingress ───────────────────
info "Waiting for Gitea to respond at ${GITEA_HOST_URL} ..."
READY=false
for i in $(seq 1 30); do
  if curl -sf --max-time 3 "${GITEA_HOST_URL}/api/v1/version" >/dev/null 2>&1; then
    READY=true
    break
  fi
  printf "."
  sleep 3
done
echo ""

if [ "${READY}" = "false" ]; then
  die "Gitea did not become reachable at ${GITEA_HOST_URL} within 90s.
  Check: kubectl get pods -n ${GITEA_NAMESPACE}
  Ensure 127.0.0.1 gitea.localhost is in /etc/hosts"
fi
ok "Gitea is reachable"
echo ""

# ── 6. Create the gitops repository (idempotent) ─────────────────────────
info "Ensuring gitops repository exists..."

REPO_EXISTS=$(curl -sf \
  "${GITEA_HOST_URL}/api/v1/repos/${GITOPS_REPO_ORG}/${GITOPS_REPO_NAME}" \
  -u "${GITEA_ADMIN_USER}:${GITEA_ADMIN_PASS}" \
  -o /dev/null -w "%{http_code}" 2>/dev/null || echo "000")

if [ "${REPO_EXISTS}" = "200" ]; then
  skip "repo '${GITOPS_REPO_ORG}/${GITOPS_REPO_NAME}' already exists"
else
  curl -sf -X POST \
    "${GITEA_HOST_URL}/api/v1/user/repos" \
    -H "Content-Type: application/json" \
    -u "${GITEA_ADMIN_USER}:${GITEA_ADMIN_PASS}" \
    -d "{
      \"name\": \"${GITOPS_REPO_NAME}\",
      \"description\": \"suparShip GitOps manifests — generated by suparship\",
      \"private\": false,
      \"auto_init\": true,
      \"default_branch\": \"main\"
    }" >/dev/null
  ok "repo '${GITOPS_REPO_ORG}/${GITOPS_REPO_NAME}' created"
fi
echo ""

# ── 7. Initialise gitops repo content ────────────────────────────────────
# Pushes charts/ skeleton and gitops-output/ placeholder if not yet present.
info "Initialising gitops repo content..."
echo ""
hack/init-gitops-repo.sh

# ── 8. Register repo with ArgoCD ─────────────────────────────────────────
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
echo ""

# ── 9. Apply root App of Apps ─────────────────────────────────────────────
# The root Application watches gitops-output/ recursively for argocd-app.yaml
# files and applies them. Each file committed by suparship becomes a child
# Application that syncs a Helm chart from charts/.
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
echo ""

# ── 10. Persist credentials to .env.cluster ───────────────────────────────
info "Writing Gitea + GitOps credentials to .env.cluster..."
GITOPS_REPO_HOST_URL="${GITEA_HOST_URL}/${GITOPS_REPO_ORG}/${GITOPS_REPO_NAME}.git"
GITOPS_REPO_CLUSTER_URL="${GITEA_CLUSTER_URL}/${GITOPS_REPO_ORG}/${GITOPS_REPO_NAME}.git"

write_credential GITEA_URL                     "${GITEA_HOST_URL}"
write_credential GITEA_USERNAME                "${GITEA_ADMIN_USER}"
write_credential GITEA_PASSWORD                "${GITEA_ADMIN_PASS}"
write_credential SUPARSHIP_GITOPS_REPO_URL     "${GITOPS_REPO_HOST_URL}"
write_credential SUPARSHIP_GITOPS_REPO_USER    "${GITEA_ADMIN_USER}"
write_credential SUPARSHIP_GITOPS_REPO_PASSWORD "${GITEA_ADMIN_PASS}"
write_credential SUPARSHIP_ARGOCD_REPO_URL     "${GITOPS_REPO_CLUSTER_URL}"
ok ".env.cluster updated"
echo ""

# ── Done ──────────────────────────────────────────────────────────────────
cat <<EOF
  ──────────────────────────────────────────────────────────────────
  Gitea + GitOps bootstrapped.

  Gitea UI          ${GITEA_HOST_URL}
  Username          ${GITEA_ADMIN_USER}
  Password          ${GITEA_ADMIN_PASS}

  GitOps repo       ${GITOPS_REPO_HOST_URL}
  Repo layout:
    charts/web-service/     Helm chart for the web-service template
    gitops-output/          suparship writes Application CRDs here

  Root ArgoCD App   suparship-apps  (watches gitops-output/_infra/**/*.yaml for ApplicationSets + AppProjects)
    kubectl get application suparship-apps -n ${ARGOCD_NAMESPACE}

  Credentials saved to .env.cluster  (git-ignored)
  Source in shell:  source .env.cluster

  /etc/hosts (must have):
    127.0.0.1 gitea.localhost

  Verify:
    kubectl get pods -n ${GITEA_NAMESPACE}
    kubectl get application suparship-apps -n ${ARGOCD_NAMESPACE}
    kubectl get secret gitea-gitops-repo -n ${ARGOCD_NAMESPACE}

EOF
