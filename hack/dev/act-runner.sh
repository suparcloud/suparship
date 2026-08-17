#!/usr/bin/env bash
# hack/dev/act-runner.sh — Gitea Actions runner for the dev loop, on the HOST
# docker daemon (started by the Tiltfile `act-runner` resource).
#
# Why host docker and not an in-cluster runner: no privileged dind pods, and
# workflow `docker build/push` reuses the exact registry path the color-app
# demo uses — the runner mounts the host daemon socket, act_runner mounts it
# into job containers (container.docker_host: "" in act-runner-config.yaml),
# and `docker push localhost:5001/...` resolves from the DAEMON to the ctlptl
# kind registry (localhost registries are insecure-allowed, no daemon config).
#
# Registration: a token is minted via the gitea CLI inside the gitea pod and
# handed to the official act_runner image, whose entrypoint registers ONCE
# (state persists in the named volume) and then polls. RE-TRIGGER THIS after
# `task cluster:delete` + recreate — the old registration points at a dead
# instance; wipe with:  docker volume rm suparship-act-runner-data
#
# The runner and job containers reach Gitea at host.docker.internal:3000
# (the Tilt port-forward). That name resolves inside containers on Docker
# Desktop / OrbStack natively and on Linux via the host-gateway add-host below.
#
# Idempotent. Used by: Tiltfile (local_resource 'act-runner', serve_cmd).
set -euo pipefail

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_dev_context

RUNNER_NAME="suparship-act-runner"
RUNNER_VOLUME="suparship-act-runner-data"
RUNNER_IMAGE="${SUPARSHIP_ACT_RUNNER_IMAGE:-docker.io/gitea/act_runner:0.2.11}"
GITEA_NS="${SUPARSHIP_GITEA_NAMESPACE:-gitea}"
GITEA_URL="${SUPARSHIP_GITEA_RUNNER_URL:-http://host.docker.internal:3000}"
CONFIG="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/act-runner-config.yaml"

info() { printf "  \033[0;36m%s\033[0m\n" "$*"; }
ok()   { printf "  \033[0;32m✓\033[0m  %s\n" "$*"; }
die()  { printf "  \033[0;31mERROR:\033[0m %s\n" "$*" >&2; exit 1; }

command -v docker >/dev/null 2>&1 || die "docker not found on PATH"

# ── Registration token (only consumed on first registration) ───────────────
# The act_runner entrypoint ignores the token once .runner exists in the
# volume, so minting one on every start is harmless and keeps this stateless.
GITEA_POD="$(kubectl -n "$GITEA_NS" get pod -l app.kubernetes.io/name=gitea \
  -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
[ -n "$GITEA_POD" ] || die "no gitea pod found in namespace $GITEA_NS (is 'task up' running?)"

TOKEN="$(kubectl -n "$GITEA_NS" exec "$GITEA_POD" -c gitea -- \
  gitea --config /data/gitea/conf/app.ini actions generate-runner-token 2>/dev/null | tail -1 | tr -d '[:space:]')"
[ -n "$TOKEN" ] || die "could not mint a runner registration token (is actions.ENABLED=true and the gitea release upgraded?)"
ok "runner registration token minted"

# ── Run (foreground — Tilt owns the lifecycle via serve_cmd) ───────────────
docker rm -f "$RUNNER_NAME" >/dev/null 2>&1 || true
docker volume create "$RUNNER_VOLUME" >/dev/null

info "starting $RUNNER_NAME ($RUNNER_IMAGE) against $GITEA_URL"
exec docker run --rm --name "$RUNNER_NAME" \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v "$RUNNER_VOLUME":/data \
  -v "$CONFIG":/config.yaml:ro \
  --add-host=host.docker.internal:host-gateway \
  -e CONFIG_FILE=/config.yaml \
  -e GITEA_INSTANCE_URL="$GITEA_URL" \
  -e GITEA_RUNNER_REGISTRATION_TOKEN="$TOKEN" \
  -e GITEA_RUNNER_NAME="$RUNNER_NAME" \
  "$RUNNER_IMAGE"
