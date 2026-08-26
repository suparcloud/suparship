#!/usr/bin/env bash
# hack/dev/seed-example-charts.sh — make the generic charts in examples/charts
# available as templates on the dev cluster.
#
# There are no built-in templates: every template reaches suparship through
# the template registry, exactly as a user's own charts would. This script is
# the dev-loop bootstrap for that model:
#
#   1. mirrors examples/charts into Gitea repo gitops/example-charts under
#      charts/ (the gitcharts default scan path)
#   2. registers the `example-charts` gitcharts source (preserving any other
#      sources) and triggers a sync
#   3. polls /templates until every chart has been indexed
#   4. sets the web template's preview defaults (image.tag → ((platform.imageTag)))
#   5. curates developer-values projections for the web + postgres templates
#
# Idempotent: re-run to push updated charts and re-sync. Overridables:
# SUPARSHIP_DEV_USER/PASSWORD, GITEA_ADMIN_USER/PASS, EXAMPLE_CHARTS (space-
# separated subset of examples/charts to seed).
#
# Used by: Tiltfile (local_resource 'seed-templates') and
# hack/dev/demo-shipnotes.sh (prereq).
set -euo pipefail

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_dev_context

GITEA="${GITEA_HOST_URL:-http://localhost:3000}"
GITEA_USER="${GITEA_ADMIN_USER:-gitops}"
GITEA_PASS="${GITEA_ADMIN_PASS:-gitops-dev-only}"
API="${SUPARSHIP_API:-http://localhost:8080/api/v1}"
SUPARSHIP_USER="${SUPARSHIP_DEV_USER:-admin@local}"
SUPARSHIP_PASS="${SUPARSHIP_DEV_PASSWORD:-admin123}"
# The whole generic catalog (there are no built-in templates to collide with).
CHARTS="${EXAMPLE_CHARTS:-web worker cronjob job gateway postgres}"

info() { printf "  \033[0;36m%s\033[0m\n" "$*"; }
ok()   { printf "  \033[0;32m✓\033[0m  %s\n" "$*"; }
die()  { printf "  \033[0;31mERROR:\033[0m %s\n" "$*" >&2; exit 1; }

curl -sS -o /dev/null --max-time 5 "$GITEA/api/v1/version" -u "$GITEA_USER:$GITEA_PASS" \
  || die "Gitea unreachable at $GITEA — is 'task up' running?"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

cookies="$tmp/cookies.txt"
curl -sS -o /dev/null --max-time 5 -c "$cookies" \
  -X POST "$API/auth/login" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$SUPARSHIP_USER\",\"password\":\"$SUPARSHIP_PASS\"}" \
  || die "suparship API unreachable at $API"

# ── 1. Mirror examples/charts into Gitea ───────────────────────────────────
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
mkdir -p "$tmp/repo/charts"
for c in $CHARTS; do
  [ -d "$REPO_ROOT/examples/charts/$c" ] || die "examples/charts/$c does not exist"
  cp -R "$REPO_ROOT/examples/charts/$c" "$tmp/repo/charts/"
done
git -C "$tmp/repo" init -q -b main
git -C "$tmp/repo" add -A
git -C "$tmp/repo" -c user.name=dev -c user.email=dev@local commit -qm "example charts"
curl -sS -u "$GITEA_USER:$GITEA_PASS" -X POST -H 'Content-Type: application/json' \
  -o /dev/null "$GITEA/api/v1/user/repos" \
  -d '{"name":"example-charts","private":false,"default_branch":"main"}' || true
GITEA_HOST="${GITEA#http://}"; GITEA_HOST="${GITEA_HOST#https://}"
git -C "$tmp/repo" push -qf \
  "http://$GITEA_USER:$GITEA_PASS@$GITEA_HOST/$GITEA_USER/example-charts.git" main
ok "example charts pushed to $GITEA/$GITEA_USER/example-charts ($CHARTS)"

# ── 2. Register the gitcharts source (preserving other sources) ────────────
registry_json="$(curl -sS -b "$cookies" "$API/templates/registry")"
if printf '%s' "$registry_json" | grep -q '"name":"example-charts"'; then
  ok "template source example-charts already registered"
else
  # jq-free merge: rewrite the registry with the stored external list + ours.
  external="$(printf '%s' "$registry_json" | python3 -c "
import json,sys
reg=json.load(sys.stdin).get('registry') or {}
ext=[e for e in reg.get('external') or [] if e.get('name')!='example-charts']
ext.append({'name':'example-charts','type':'gitcharts',
  'repoURL':'http://gitea-http.gitea.svc.cluster.local:3000/$GITEA_USER/example-charts.git',
  'ref':'main','path':''})
print(json.dumps({'builtIn': reg.get('builtIn') or [], 'external': ext}))")"
  curl -sS -b "$cookies" -o /dev/null -X PUT "$API/templates/registry" \
    -H 'Content-Type: application/json' -d "$external"
  ok "template source example-charts registered"
fi
curl -sS -b "$cookies" -o /dev/null -X POST "$API/templates/registry/sources/example-charts/sync"

# ── 3. Wait until every chart is indexed ───────────────────────────────────
synced=""
for i in $(seq 1 30); do
  templates_json="$(curl -sS -b "$cookies" "$API/templates")"
  missing=""
  for c in $CHARTS; do
    printf '%s' "$templates_json" | grep -q "\"$c\"" || missing="$missing $c"
  done
  [ -z "$missing" ] && { synced=1; break; }
  [ "$i" = 1 ] && info "waiting for template sync (missing:$missing)"
  sleep 2
done
[ -n "$synced" ] || die "templates never appeared:$missing — check the example-charts source in Settings → Templates"
ok "templates available:$(printf ' %s' $CHARTS)"

# ── 4. Preview wiring for the web template ─────────────────────────────────
# Every preview of a web app should deploy its PR build: the org-level template
# override (sync-safe — survives re-syncs of the source) sets
# previewDefaultValues image.tag → ((platform.imageTag)) (always resolved; the
# per-PR tag in previews, "" in stable envs where Kargo owns the tag).
override_json="$(curl -sS -b "$cookies" "$API/templates/web/overrides")"
merged="$(printf '%s' "$override_json" | python3 -c "
import json,sys
try:
    ov=json.load(sys.stdin) or {}
except Exception:
    ov={}
pdv=ov.get('previewDefaultValues') or {}
img=pdv.get('image') or {}
img['tag']='((platform.imageTag))'
pdv['image']=img
ov['previewDefaultValues']=pdv
print(json.dumps(ov))")"
curl -sS -b "$cookies" -o /dev/null -X PUT "$API/templates/web/overrides" \
  -H 'Content-Type: application/json' -d "$merged"
ok "web template preview defaults set (image.tag → ((platform.imageTag)))"

# ── 5. Developer values projections for web + postgres ─────────────────────
# Curate the small set of Helm values a DEVELOPER owns in the app editor;
# everything else stays platform-owned (and reachable via the Advanced
# toggle). Saved via PATCH /templates/{name} — stored as a sync-safe override
# for these synced charts, so a re-sync of example-charts can't clobber it.
patch_dev_values() { # template json
  local code
  code="$(curl -sS -b "$cookies" -o /dev/null -w '%{http_code}' \
    -X PATCH "$API/templates/$1" -H 'Content-Type: application/json' -d "$2")"
  case "$code" in
    200|204) ok "developer values set on template $1" ;;
    *) die "setting developer values on $1 failed (HTTP $code)" ;;
  esac
}

# web: the image is yours, the tag belongs to the pipeline (CD/previews own
# image.tag, so it is deliberately NOT projected). containerPort mirrors
# service.port — one question, two keys.
patch_dev_values web '{"developerValues":[
  {"path":"image.repository","title":"Image repository","type":"string","required":true,
   "description":"Container image to run (no tag — CD owns the tag)."},
  {"path":"containerPort","title":"Container port","type":"number","default":8080,
   "mirrors":["service.port"],"min":1,"max":65535,
   "description":"Port your process listens on; the Service follows it."},
  {"path":"healthCheck.path","title":"Health check path","type":"string","default":"/",
   "description":"HTTP path probed for liveness/readiness."},
  {"path":"replicaCount","title":"Replicas","type":"number","default":2,"min":1,"max":10},
  {"path":"env","title":"Environment variables",
   "description":"Plain key/value env for the container (secrets belong in App → Settings → Variables & secrets)."}
]}'

# postgres: version pin, initial database, and storage — auth stays out (the
# platform injects POSTGRES_USER/PASSWORD via envFrom, never chart values).
patch_dev_values postgres '{"developerValues":[
  {"path":"image.tag","title":"Postgres version","type":"enum",
   "options":["15-alpine","16-alpine","17-alpine"],"default":"16-alpine"},
  {"path":"auth.database","title":"Database name","type":"string","default":"app",
   "description":"Initial database created at first boot; leave empty when the platform injects POSTGRES_DB."},
  {"path":"persistence.enabled","title":"Persistent storage","type":"boolean","default":true,
   "description":"Disable for throwaway data — the database then lives and dies with the pod."},
  {"path":"persistence.size","title":"Storage size","type":"string","default":"1Gi",
   "pattern":"^[0-9]+(Ei|Pi|Ti|Gi|Mi|Ki)$",
   "description":"PVC size (Kubernetes quantity, e.g. 1Gi, 10Gi)."}
]}'
