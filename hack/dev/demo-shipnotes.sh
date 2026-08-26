#!/usr/bin/env bash
# hack/dev/demo-shipnotes.sh — the one-command shipnotes demo: CI + a working
# app, end to end, on the dev cluster.
#
#   1. mirrors github.com/suparcloud/suparship-demo into the local Gitea and
#      wires the Actions secret/vars its .gitea/workflows/ci.yaml needs
#   2. ensures the example-charts template catalog is seeded (delegates to
#      hack/dev/seed-example-charts.sh; Tilt's `seed-templates` resource
#      normally already ran it)
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

# ── 4. Template catalog (examples/charts via the registry) ─────────────────
# Delegated to the shared seeder — the Tilt `seed-templates` resource runs it
# on `task up`, so this is just an idempotent safety net for standalone runs.
"$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/seed-example-charts.sh"

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
# Generic `web` chart, configured entirely through ITS OWN values — the
# platform contributes only resolved ((platform.*)) tokens and the env
# ConfigMap/Secret objects behind them.
def web(name, repo, port, health, expose):
    full = registry + "/" + repo
    return {
        "name": name, "type": "web", "enabled": True, "exposeMode": expose,
        "template": {"name": "web"},
        "values": {
            # Deterministic Service name (the chart's fullname would append
            # "-web" to the release name otherwise).
            "fullnameOverride": "shipnotes-" + name,
            "image": {"repository": full, "tag": tag},
            "containerPort": port,
            "service": {"port": port},
            "healthCheck": {"path": health},
            "envFrom": {
                "configMaps": ["((platform.configMapName))"],
                "secrets": ["((platform.secretName))"],
            },
            # Demo-grade images (stock nginx / uvicorn) run as root. An empty
            # map is a NO-OP under deep-merge — the chart's runAsNonRoot
            # default would survive and the kubelet rejects the container
            # (CreateContainerConfigError). Override the key explicitly.
            "podSecurityContext": {"runAsNonRoot": False},
        },
        "images": [{"name": name, "tagKey": "image.tag",
                    "repository": full, "tagPattern": "^main-"}],
    }
frontend = web("frontend", "demo/shipnotes-frontend", 80, "/", "external")
# Routing the BYO way: the chart's own ingress values, wired to the
# platform-resolved host + class via tokens. TLS off — the dev loop serves
# plain HTTP (no cert-manager) and nginx would otherwise redirect to https.
frontend["values"]["ingress"] = {
    "enabled": True,
    "host": "((platform.routingHost))",
    "className": "((platform.ingressClassName))",
    "tls": {"enabled": False},
}
# The frontend nginx proxies /api to this env var; shipnotes-api is the api
# component's Service (fullnameOverride above).
frontend["values"]["env"] = {"API_UPSTREAM": "http://shipnotes-api:8000"}
# Root nginx needs these capabilities back (the chart drops ALL): setuid/
# setgid/chown for its workers + cache dirs, NET_BIND_SERVICE for :80.
# K8s applies drop before add, so add wins for exactly these.
frontend["values"]["securityContext"] = {
    "capabilities": {"add": ["CHOWN", "SETGID", "SETUID", "NET_BIND_SERVICE"]},
}
api = web("api", "demo/shipnotes-api", 8000, "/healthz", "disabled")
db = {
    "name": "db", "type": "worker", "enabled": True, "stateful": True,
    "inheritAppVars": False,
    "template": {"name": "postgres"},
    # Stateful components default OUT of previews; the demo promises "the
    # env's own database", so opt the db IN — each preview gets its own
    # throwaway Postgres, torn down with the PR.
    "previewEnabled": True,
    # The secrets & variables showcase: the db's credentials come from the
    # platform, not from chart values in git. POSTGRES_DB maps from an app
    # VARIABLE, POSTGRES_USER/PASSWORD from app SECRETS — the curation below
    # renders per-component shipnotes-db-config / shipnotes-db-secrets
    # objects holding exactly these keys, which the chart envFroms via
    # ((platform.*)) tokens.
    "envVars": [
        {"name": "POSTGRES_DB", "fromConfig": "POSTGRES_DB"},
        {"name": "POSTGRES_USER", "fromSecret": "POSTGRES_USER"},
        {"name": "POSTGRES_PASSWORD", "fromSecret": "POSTGRES_PASSWORD"},
    ],
    "values": {
        # Deterministic Service name for DATABASE_URL below.
        "fullnameOverride": "shipnotes-db",
        # Blank the inline auth so everything arrives via envFrom.
        "auth": {"database": "", "username": "", "password": ""},
        "envFrom": {
            "configMaps": ["((platform.configMapName))"],
            "secrets": ["((platform.secretName))"],
        },
    },
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

# ── 7. Variables & secrets showcase (vault-backed; idempotent upserts) ─────
# The db credentials live in the PLATFORM, not in chart values in git:
#   variable POSTGRES_DB            → app-level config (shipnotes-config)
#   secrets  POSTGRES_USER/PASSWORD → app secrets (Vault), curated into the
#                                     db component's shipnotes-db-secrets
#   secret   DATABASE_URL           → consumed by the api via inherit-all
# Fixed demo password on purpose: postgres only reads credentials at initdb,
# so rotating it on re-runs would strand an existing PVC.
DB_USER="app"; DB_PASS="shipnotes-demo-pw"; DB_NAME="app"
var_code="$(curl -sS -b "$cookies" -o /dev/null -w '%{http_code}' \
  -X PUT "$API/projects/$PROJECT/apps/$APP/envconfig" \
  -H 'Content-Type: application/json' \
  -d "{\"vars\":{\"POSTGRES_DB\":\"$DB_NAME\"}}")"
case "$var_code" in 200|202) ok "app variable POSTGRES_DB=$DB_NAME" ;; *) warn "setting POSTGRES_DB returned HTTP $var_code" ;; esac
sec_code="$(curl -sS -b "$cookies" -o /dev/null -w '%{http_code}' \
  -X POST "$API/projects/$PROJECT/apps/$APP/secrets/global" \
  -H 'Content-Type: application/json' \
  -d "{\"entries\":{\"DATABASE_URL\":\"postgresql://$DB_USER:$DB_PASS@shipnotes-db:5432/$DB_NAME\",\"POSTGRES_USER\":\"$DB_USER\",\"POSTGRES_PASSWORD\":\"$DB_PASS\"}}")"
[ "$sec_code" = "200" ] || warn "setting app secrets returned HTTP $sec_code (set them in the UI: App → Settings → Variables & secrets)"
[ "$sec_code" = "200" ] && ok "app secrets DATABASE_URL, POSTGRES_USER, POSTGRES_PASSWORD set"
# Republish so the rendered config/secret objects pick up everything above
# in one pass (the envconfig PUT schedules its own async republish; this
# makes the seed deterministic instead).
curl -sS -b "$cookies" -o /dev/null -X POST "$API/projects/$PROJECT/apps/$APP/sync" || true

# ── 8. The tour ────────────────────────────────────────────────────────────
printf '\n'
ok "shipnotes demo is wired. The tour:"
info "app (staging):    http://shipnotes-frontend.staging.localhost   (ArgoCD sync takes a minute or two)"
info "suparship UI:     http://localhost:5173  →  demo / shipnotes   (admin@local / admin123)"
info "the code:         $GITEA/$GITEA_USER/$REPO_NAME   (gitops / gitops-dev-only)"
info "CI runs:          $GITEA/$GITEA_USER/$REPO_NAME/actions"
info ""
info "developer values showcase: the web and postgres templates ship a curated"
info "  developer projection (seeded by seed-example-charts.sh). See it in action:"
info "  - shipnotes → a component card → Values: a form with just the fields a"
info "    developer owns (web: image repo, port [mirrored into service.port],"
info "    health path, replicas, env; postgres: version, database, storage) —"
info "    untouched fields keep inheriting; Advanced still shows everything."
info "  - create a new app from the web template: the wizard seeds the same form."
info "  - authoring lives at Settings → Templates → web → Developer values."
info ""
info "variables & secrets showcase: App → Settings → Variables & secrets —"
info "  POSTGRES_DB is an app VARIABLE; DATABASE_URL, POSTGRES_USER and"
info "  POSTGRES_PASSWORD are Vault-backed SECRETS. The db component maps the"
info "  credentials into its own curated shipnotes-db-config/-secrets (see"
info "  the db card → Variables); nothing sensitive lives in chart values."
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
