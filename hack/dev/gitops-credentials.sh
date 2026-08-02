#!/usr/bin/env bash
# hack/dev/gitops-credentials.sh — the Secret suparship uses to PUSH to the dev
# Gitea, referenced by gitops.existingSecret in hack/dev/values-dev.yaml.
#
# hack/install-gitea-init.sh already creates the ArgoCD-side repository Secret
# (argocd/gitea-gitops-repo) so ArgoCD can READ the repo. This is the other half:
# suparship's own git client needs credentials to WRITE. Without it the publisher
# is disabled and app creates no-op — they log "published" and produce nothing.
#
# generic provider -> username + password (basic auth), matching Gitea.
#
# Idempotent. Used by: Tiltfile (local_resource 'gitops-credentials').
set -euo pipefail

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_dev_context

NS="${SUPARSHIP_SYSTEM_NAMESPACE:-suparship-system}"
SECRET="suparship-gitops-credentials"
USER="${GITEA_ADMIN_USER:-gitops}"
PASS="${GITEA_ADMIN_PASS:-gitops-dev-only}"

kubectl get namespace "$NS" >/dev/null 2>&1 || kubectl create namespace "$NS" >/dev/null

kubectl create secret generic "$SECRET" -n "$NS" \
  --from-literal=username="$USER" \
  --from-literal=password="$PASS" \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null

printf "  \033[0;32m✓\033[0m  %s ready in %s (user: %s)\n" "$SECRET" "$NS" "$USER"
