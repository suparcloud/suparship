#!/usr/bin/env bash
# hack/dev/demo-shipnotes.sh — the one-command shipnotes demo: CI + a working
# app, end to end, on the dev cluster.
#
#   1. mirrors github.com/suparcloud/suparship-demo into the local Gitea and
#      wires the Actions secret/vars its .gitea/workflows/ci.yaml needs
#   2. registers the example-charts template source (adds `postgres`)
#   3. waits for CI's first main-<7sha> images in the local registry
#   4. creates the composed `shipnotes` app (frontend + api + db) with CD
#      managed and per-component image bindings
#   5. sets the DATABASE_URL app secret
#   6. prints the tour: browse staging, open a PR → preview, promote to prod
#
# Prereqs: `task up` green (gitea + act-runner + suparship + ingress) and,
# for browser access, one-time `task dev:dns` (macOS). Overridables:
# SHIPNOTES_REPO (source repo URL), SUPARSHIP_DEV_USER/PASSWORD.
# Idempotent: re-run to push a fresh mirror and rotate the CI token; the app
# is left alone once it exists.
set -euo pipefail

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_dev_context

SRC_REPO="${SHIPNOTES_REPO:-https://github.com/suparcloud/suparship-demo.git}"
GITEA="${GITEA_HOST_URL:-http://localhost:3000}"
GITEA_USER="${GITEA_ADMIN_USER:-gitops}"
GITEA_PASS="${GITEA_ADMIN_PASS:-gitops-dev-only}"
REPO_NAME="suparship-demo"
API="${SUPARSHIP_API:-http://localhost:8080/api/v1}"
SUPARSHIP_USER="${SUPARSHIP_DEV_USER:-admin@local}"
SUPARSHIP_PASS="${SUPARSHIP_DEV_PASSWORD:-admin123}"
PROJECT="demo"
APP="shipnotes"
# What CI jobs (containers on the host daemon) reach the suparship API at.
CI_API="http://host.docker.internal:8080/api/v1"
# In-cluster registry host — image references in values are FULL refs (the
# platform does not prefix the org registry onto component images).
REGISTRY_HOST="${REGISTRY_HOST:-kind-registry:5000}"

info() { printf "  \033[0;36m%s\033[0m\n" "$*"; }
ok()   { printf "  \033[0;32m✓\033[0m  %s\n" "$*"; }
warn() { printf "  \033[0;33m–\033[0m  %s\n" "$*"; }
die()  { printf "  \033[0;31mERROR:\033[0m %s\n" "$*" >&2; exit 1; }

gitea_api() { # method path [json-body]
  local method="$1" path="$2" body="${3:-}"
  local args=(-sS -u "$GITEA_USER:$GITEA_PASS" -X "$method" \
    -H 'Content-Type: application/json' -o /dev/null -w '%{http_code}' \
    "$GITEA/api/v1$path")
  [ -n "$body" ] && args+=(-d "$body")
  curl "${args[@]}"
}

curl -sS -o /dev/null --max-time 5 "$GITEA/api/v1/version" -u "$GITEA_USER:$GITEA_PASS" \
  || die "Gitea unreachable at $GITEA — is 'task up' running?"

# ── 1. Mirror the repo into Gitea ──────────────────────────────────────────
code="$(gitea_api POST /user/repos "{\"name\":\"$REPO_NAME\",\"private\":false,\"default_branch\":\"main\"}")"
case "$code" in
  201) ok "created Gitea repo $GITEA_USER/$REPO_NAME" ;;
  409) ok "Gitea repo $GITEA_USER/$REPO_NAME already exists" ;;
  *)   die "creating repo failed (HTTP $code)" ;;
esac

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
info "cloning $SRC_REPO"
git clone -q "$SRC_REPO" "$tmp/repo"
GITEA_HOST="${GITEA#http://}"; GITEA_HOST="${GITEA_HOST#https://}"
git -C "$tmp/repo" push -qf \
  "http://$GITEA_USER:$GITEA_PASS@$GITEA_HOST/$GITEA_USER/$REPO_NAME.git" \
  main --tags
ok "pushed main (+tags) to $GITEA/$GITEA_USER/$REPO_NAME"

# ── 2. Mint a suparship project token for CI ───────────────────────────────
cookies="$tmp/cookies.txt"
curl -sS -o /dev/null --max-time 5 -c "$cookies" \
  -X POST "$API/auth/login" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$SUPARSHIP_USER\",\"password\":\"$SUPARSHIP_PASS\"}" \
  || die "suparship API unreachable at $API"

token_resp="$(curl -sS -b "$cookies" -X POST "$API/projects/$PROJECT/tokens" \
  -H 'Content-Type: application/json' \
  -d '{"name":"gitea-actions-shipnotes","role":"developer"}')"
CI_TOKEN="$(printf '%s' "$token_resp" | grep -o '"token"[[:space:]]*:[[:space:]]*"[^"]*"' | head -1 | sed 's/.*"\([^"]*\)"$/\1/')"
[ -n "$CI_TOKEN" ] || die "could not mint a project token: $token_resp"
ok "minted suparship token 'gitea-actions-shipnotes' (project $PROJECT, developer)"

# ── 3. Wire Actions secret + variables on the Gitea repo ───────────────────
code="$(gitea_api PUT "/repos/$GITEA_USER/$REPO_NAME/actions/secrets/SUPARSHIP_TOKEN" \
  "{\"data\":\"$CI_TOKEN\"}")"
case "$code" in 201|204) ok "secret SUPARSHIP_TOKEN set" ;; *) die "setting secret failed (HTTP $code)" ;; esac

set_var() { # name value
  local code
  code="$(gitea_api POST "/repos/$GITEA_USER/$REPO_NAME/actions/variables/$1" "{\"value\":\"$2\"}")"
  [ "$code" = "201" ] && { ok "variable $1=$2"; return; }
  code="$(gitea_api PUT "/repos/$GITEA_USER/$REPO_NAME/actions/variables/$1" "{\"value\":\"$2\"}")"
  case "$code" in 200|204) ok "variable $1=$2 (updated)" ;; *) die "setting variable $1 failed (HTTP $code)" ;; esac
}
set_var SUPARSHIP_API "$CI_API"
set_var SUPARSHIP_PROJECT "$PROJECT"
set_var SUPARSHIP_APP "$APP"

# ── 4. Example-charts template source (provides the `postgres` template) ───
# Mirrors examples/charts into Gitea under charts/ (the gitcharts default
# scan path) and registers it. Only the collision-free charts: worker/cronjob
# names are owned by the built-in templates and the sync guard would refuse
# them anyway.
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
charts_tmp="$(mktemp -d)"
mkdir -p "$charts_tmp/charts"
cp -R "$REPO_ROOT/examples/charts/web" "$REPO_ROOT/examples/charts/gateway" \
      "$REPO_ROOT/examples/charts/postgres" "$charts_tmp/charts/"
git -C "$charts_tmp" init -q -b main
git -C "$charts_tmp" add -A
git -C "$charts_tmp" -c user.name=dev -c user.email=dev@local commit -qm "example charts"
gitea_api POST /user/repos '{"name":"example-charts","private":false,"default_branch":"main"}' >/dev/null || true
git -C "$charts_tmp" push -qf \
  "http://$GITEA_USER:$GITEA_PASS@$GITEA_HOST/$GITEA_USER/example-charts.git" main
rm -rf "$charts_tmp"
ok "example charts pushed to $GITEA/$GITEA_USER/example-charts"

registry_json="$(curl -sS -b "$cookies" "$API/templates/registry")"
if printf '%s' "$registry_json" | grep -q '"name":"example-charts"'; then
  ok "template source example-charts already registered"
else
  # Preserve existing sources; append ours. jq-free merge: rewrite the
  # registry with the stored external list + our entry.
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
curl -sS -b "$cookies" "$API/templates" | grep -q '"postgres"' \
  || die "postgres template did not appear after source sync"
ok "postgres template available"

# ── 5. Wait for CI's first main-<sha> images ───────────────────────────────
# The mirror push above triggers .gitea/workflows/ci.yaml; the first run
# pulls the job image and does cold docker builds (several minutes).
info "waiting for CI to build demo/shipnotes-{frontend,api} (first run takes a few minutes)..."
MAIN_TAG=""
for _ in $(seq 1 120); do
  MAIN_TAG="$(curl -s "http://localhost:5001/v2/demo/shipnotes-frontend/tags/list" 2>/dev/null \
    | grep -o '"main-[0-9a-f]\{7\}"' | tr -d '"' | sort | tail -1 || true)"
  if [ -n "$MAIN_TAG" ] \
    && curl -s "http://localhost:5001/v2/demo/shipnotes-api/tags/list" 2>/dev/null | grep -q "\"$MAIN_TAG\""; then
    break
  fi
  MAIN_TAG=""
  sleep 10
done
[ -n "$MAIN_TAG" ] || die "no main-<sha> images after 20m — check the run at $GITEA/$GITEA_USER/$REPO_NAME/actions and the act-runner Tilt resource"
ok "CI images ready at tag $MAIN_TAG"

# ── 6. Create the composed app (skip if it exists) ─────────────────────────
app_code="$(curl -sS -b "$cookies" -o /dev/null -w '%{http_code}' "$API/projects/$PROJECT/apps/$APP")"
if [ "$app_code" = "200" ]; then
  ok "app $APP already exists — leaving it alone"
else
  info "creating composed app $APP (frontend + api + db)..."
  # Top-level heredoc (not inside $()): macOS bash 3.2 can't parse heredocs
  # nested in command substitution.
  python3 - "$MAIN_TAG" "$REGISTRY_HOST" >"$tmp/create.json" <<'PY'
import json, sys
tag, registry = sys.argv[1], sys.argv[2]
def web(name, repo, port, health, expose):
    full = registry + "/" + repo
    return {
        "name": name, "type": "web", "enabled": True, "exposeMode": expose,
        "template": {"name": "web-service"},
        "values": {"components": {"web": {
            "image": {"repository": full, "tag": tag},
            "port": port, "healthCheck": {"path": health}}}},
        "images": [{"name": name, "tagKey": "components.web.image.tag",
                    "repository": full, "tagPattern": "^main-"}],
    }
frontend = web("frontend", "demo/shipnotes-frontend", 80, "/", "external")
# The frontend nginx proxies /api to this env var; shipnotes-api is the
# composed component's Service (publisher names releases <app>-<component>).
frontend["envVars"] = [{"name": "API_UPSTREAM", "value": "http://shipnotes-api:8000"}]
api = web("api", "demo/shipnotes-api", 8000, "/healthz", "disabled")
db = {
    "name": "db", "type": "worker", "enabled": True, "stateful": True,
    "inheritAppVars": False,
    "template": {"name": "postgres"},
    # Deterministic Service name for DATABASE_URL below.
    "values": {"fullnameOverride": "shipnotes-db"},
}
print(json.dumps({
    "name": "shipnotes",
    "displayName": "Shipnotes",
    "description": "Demo: React + FastAPI + Postgres through the whole golden path",
    "components": [frontend, api, db],
    "cd": {"managed": True, "autoPromote": False},
}))
PY
  create_code="$(curl -sS -b "$cookies" -o "$tmp/create-resp.json" -w '%{http_code}' \
    -X POST "$API/projects/$PROJECT/apps" \
    -H 'Content-Type: application/json' -d @"$tmp/create.json")"
  [ "$create_code" = "201" ] || die "app create failed (HTTP $create_code): $(cat "$tmp/create-resp.json")"
  ok "app $APP created (staging deploys via GitOps now)"
fi

# ── 7. DATABASE_URL app secret (vault-backed; idempotent upsert) ───────────
sec_code="$(curl -sS -b "$cookies" -o /dev/null -w '%{http_code}' \
  -X POST "$API/projects/$PROJECT/apps/$APP/secrets/global" \
  -H 'Content-Type: application/json' \
  -d '{"entries":{"DATABASE_URL":"postgresql://app:app@shipnotes-db:5432/app"}}')"
[ "$sec_code" = "200" ] || warn "setting DATABASE_URL returned HTTP $sec_code (set it in the UI: App → Settings → Variables & secrets)"
[ "$sec_code" = "200" ] && ok "app secret DATABASE_URL set"

# ── 8. The tour ────────────────────────────────────────────────────────────
printf '\n'
ok "shipnotes demo is wired. The tour:"
info "app (staging):    http://shipnotes-frontend.staging.localhost   (ArgoCD sync takes a minute or two)"
info "suparship UI:     http://localhost:5173  →  demo / shipnotes   (admin@local / admin123)"
info "the code:         $GITEA/$GITEA_USER/$REPO_NAME   (gitops / gitops-dev-only)"
info "CI runs:          $GITEA/$GITEA_USER/$REPO_NAME/actions"
info ""
info "try the loop:"
info "  1. edit something, push a branch, open a PR in Gitea → CI builds pr-<n>-<sha>"
info "     and a preview appears at http://pr-<n>.shipnotes-frontend.preview.localhost"
info "  2. merge → main build → staging follows the new tag (CD managed)"
info "  3. promote: suparship UI → shipnotes → Promote (or:"
info "     POST $API/projects/$PROJECT/apps/$APP/promote {\"targetEnvironment\":\"prod\"})"
info "     → http://shipnotes-frontend.prod.localhost serves the same immutable tag"
info ""
info "If *.localhost doesn't resolve: run \`task dev:dns\` once (macOS) or see docs/local-dns.md."
