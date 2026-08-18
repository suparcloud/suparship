#!/usr/bin/env bash
# hack/dev/seed-multi.sh — rebind the seeded environments onto the REAL workload
# clusters registered by hack/dev/workload-cluster.sh.
#
# The default seed (config/seed/) binds staging and prod to two cluster records
# that both point at https://kubernetes.default.svc — the tooling cluster wearing
# two hats. That is fine for a single-cluster loop but it is fiction, and two
# records sharing one API server actively collide: the ArgoCD cluster Secret name
# is derived solely from the server URL (internal/kube/cluster_store.go), so the
# second registration overwrites the first.
#
# This rewrites the org config to:
#   staging -> cluster "staging"   (kind-staging)
#   prod    -> cluster "prod"      (kind-prod)
# and switches namespacePattern from {app}-{env} to {app}, because the cluster
# boundary now provides the isolation the -{env} suffix was compensating for.
#
# It also drops the two placeholder cluster records, so `suparship cluster list`
# reflects reality.
#
# Idempotent. Used by: Tiltfile (local_resource 'seed-multi', multi mode only).
set -euo pipefail

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_dev_context

NS="suparship-system"
ok()   { printf "  \033[0;32m✓\033[0m  %s\n" "$*"; }
info() { printf "  \033[0;36m%s\033[0m\n" "$*"; }

info "rebinding environments onto the workload clusters"

ORG_YAML="$(cat <<'YAML'
name: default
displayName: My Organization
createdAt: "2026-01-01T00:00:00Z"
environments:
  - name: staging
    displayName: Staging
    order: 1
    clusterRefs: [staging]
    activeClusterRef: staging
    baseDomain: localhost
    # {app} alone is enough here: staging and prod are separate clusters, so the
    # cluster boundary provides the isolation {app}-{env} was compensating for
    # on a shared cluster.
    appNamespacePattern: "{app}"
  - name: prod
    displayName: Production
    order: 2
    clusterRefs: [prod]
    activeClusterRef: prod
    baseDomain: localhost
    appNamespacePattern: "{app}"
# Org-wide resource naming. Multi-cluster: staging and prod are SEPARATE
# clusters, so project namespaces need no env suffix — the cluster boundary
# provides the isolation (same reasoning as the {app} app-namespace pattern
# above). ArgoCD Applications stay {project}-{app}-{cluster} in every
# topology.
resourceNaming:
  projectNamespace: "{project}"
  argoAppName: "{project}-{app}-{cluster}"
# Both tiers on the nginx class — without profiles exposed components render
# no Ingress at all (see config/seed/org.yaml for the fuller note). The
# workload clusters carry no ingress controller, so routes only materialise
# where one exists — but the profile must resolve for publish to emit them.
routingProfiles:
  external:
    ingressClassName: nginx
  internal:
    ingressClassName: nginx
teams:
  - name: admins
    displayName: Administrators
    members: [admin, admin@local]
roleBindings:
  - project: "*"
    team: admins
    role: org_admin
YAML
)"

TMP="$(mktemp)"
trap 'rm -f "$TMP"' EXIT
printf '%s\n' "$ORG_YAML" > "$TMP"

kubectl create configmap suparship-org-config -n "$NS" \
  --from-file=org.yaml="$TMP" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
ok "org environments bound: staging -> staging, prod -> prod"

# Drop the single-cluster placeholders. The real records were created by the
# registration API, which also wrote their kubeconfig + ArgoCD Secrets; these two
# never had either.
for stale in staging-cluster prod-cluster; do
  if kubectl get configmap "suparship-cluster-$stale" -n "$NS" >/dev/null 2>&1; then
    kubectl delete configmap "suparship-cluster-$stale" -n "$NS" >/dev/null
    ok "removed placeholder cluster record $stale"
  fi
done

# ── Republish every app against the real clusters ─────────────────────────
# Tilt runs `seed` before this, so any app seeded there was already published
# while the envs still pointed at the placeholder records — leaving a stale
# envs/<env>/<project>/<app>/_targets/staging-cluster/app.yaml behind, and an
# ArgoCD Application generated from it aimed at https://kubernetes.default.svc.
#
# A sync fixes it completely rather than needing surgery on the repo: the
# publisher does RemoveAll on the env's _targets directory before rewriting it
# (internal/gitops/publisher.go), so the dead per-cluster entry disappears and
# the ApplicationSet prunes the Application that was generated from it.
API="${SUPARSHIP_API:-http://localhost:8080}"
USER="${SUPARSHIP_DEV_USER:-admin@local}"
PASS="${SUPARSHIP_DEV_PASSWORD:-admin123}"

COOKIE="$(curl -s -c - -X POST "$API/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$USER\",\"password\":\"$PASS\"}" 2>/dev/null \
  | awk '/suparship_session/ {print $NF}')"

if [ -z "$COOKIE" ]; then
  printf "  \033[0;33m–\033[0m  could not reach %s — skipping republish.\n" "$API"
  printf "     Re-run this script, or sync apps from the UI, to clear any\n"
  printf "     _targets entry still pointing at a placeholder cluster.\n"
else
  PROJECTS="$(curl -s -b "suparship_session=$COOKIE" "$API/api/v1/projects" 2>/dev/null \
    | python3 -c 'import json,sys; print("\n".join(p["name"] for p in json.load(sys.stdin).get("projects",[])))' 2>/dev/null || true)"
  for proj in $PROJECTS; do
    APPS="$(curl -s -b "suparship_session=$COOKIE" "$API/api/v1/projects/$proj/apps" 2>/dev/null \
      | python3 -c 'import json,sys; print("\n".join(a["name"] for a in json.load(sys.stdin).get("apps",[])))' 2>/dev/null || true)"
    for app in $APPS; do
      code="$(curl -s -o /dev/null -w '%{http_code}' -X POST -b "suparship_session=$COOKIE" \
        "$API/api/v1/projects/$proj/apps/$app/sync")"
      case "$code" in
        2*) ok "republished $proj/$app against the real clusters" ;;
        *)  printf "  \033[0;33m–\033[0m  %s/%s sync returned HTTP %s (skipped)\n" "$proj" "$app" "$code" ;;
      esac
    done
  done
fi

printf "\n  Environments now deploy to separate clusters. Verify:\n"
printf "    kubectl --context kind-staging get ns\n"
printf "    kubectl --context kind-prod    get ns\n\n"
