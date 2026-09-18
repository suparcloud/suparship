#!/usr/bin/env bash
# hack/dev/resume.sh — wake a cluster frozen by hack/dev/suspend.sh.
#
# Handles both freeze modes: paused containers are unpaused (instant, nothing
# restarts), stopped ones are started (the control plane needs a few seconds,
# the pods a few minutes). Then it waits for the apiserver to answer, so
# whatever runs next (`tilt up`, `ctlptl apply`) meets a live cluster rather
# than a connection refused.
#
# IDEMPOTENT AND SILENT WHEN THERE IS NOTHING TO DO. That is what lets `task up`
# call it unconditionally: on a normal day nothing is suspended, this exits 0
# having printed one dim line, and `task up` proceeds as it always has. The
# alternative — making contributors remember which of `task up` and
# `task resume` applies today — is exactly the friction this is meant to remove.
#
# It does NOT start Tilt. `task resume` does that, and only if Tilt is not
# already running (pause mode leaves it running; it reconnects by itself).
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../.."

RED=$'\033[0;31m'; YELLOW=$'\033[0;33m'; GREEN=$'\033[0;32m'; DIM=$'\033[2m'; OFF=$'\033[0m'

# Kept in step with suspend.sh — see the comment there on why this is an
# explicit list and not "every kind cluster on the machine".
DEV_CLUSTERS=(suparship-dev staging prod)
REGISTRY_NAME="kind-registry"
STATE_FILE="tmp/dev-suspend.state"

DEV_KUBE_CONTEXT="${DEV_KUBE_CONTEXT:-kind-suparship-dev}"

# How long to wait for the apiserver. After an unpause it answers within a
# couple of seconds. After a stop, etcd replays its write-ahead log and the
# apiserver re-establishes leases: usually 10-30s on a laptop, occasionally
# more if the machine slept with the containers stopped and the clock jumped.
API_TIMEOUT_SECS="${API_TIMEOUT_SECS:-120}"

# ── Which of our containers are frozen, and how? ─────────────────────────────
# $1 is the docker status filter: "paused" or "exited".
# Registry first: the kubelet starts pulling as soon as the node is up, and a
# node that comes back before its registry logs a flurry of ImagePullBackOff
# that then has to age out of the backoff timer.
frozen_containers() {
  local status="$1" cluster
  docker ps -a --filter "name=^/${REGISTRY_NAME}$" \
               --filter "label=dev.tilt.ctlptl.role=registry" \
               --filter "status=${status}" --format '{{.Names}}'
  for cluster in "${DEV_CLUSTERS[@]}"; do
    docker ps -a --filter "label=io.x-k8s.kind.cluster=${cluster}" \
                 --filter "status=${status}" --format '{{.Names}}'
  done
}

# ── Did an address move while stopped? (stop mode only) ──────────────────────
# suspend.sh recorded each container's IP. Docker hands out addresses on the
# kind network first-come-first-served, so a node that sat stopped next to
# another running kind cluster routinely comes back one address over (seen on
# the first test of this script: .3 -> .4). kind has handled that since v0.11:
# the node's entrypoint rewrites the etcd peer URL and apiserver config for the
# new address on boot, and the cluster comes up healthy. So this is
# informational — it only matters if the wait below times out, and then it is
# the first thing to suspect, which is why it is printed before the wait.
note_moved_ips() {
  [ -f "$STATE_FILE" ] || return 0
  local name was now
  while IFS=$'\t' read -r name was; do
    [ -n "$name" ] || continue
    [ -n "$was" ] || continue
    now="$(docker inspect "$name" --format '{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}' 2>/dev/null | awk '{print $1}')"
    if [ -n "$now" ] && [ "$now" != "$was" ]; then
      printf "  %s\n" "${DIM}${name} moved from ${was} to ${now} while stopped — kind rewrites the node config for the new address on boot; if the wait below times out, this is the suspect: task cluster:delete && task up${OFF}"
    fi
  done <"$STATE_FILE"
}

wait_for_api() {
  local deadline=$((SECONDS + API_TIMEOUT_SECS))
  printf "  waiting for the apiserver (up to %ss)" "$API_TIMEOUT_SECS"
  while [ $SECONDS -lt $deadline ]; do
    if kubectl --context "$DEV_KUBE_CONTEXT" get --raw=/readyz >/dev/null 2>&1; then
      printf "\n  %s apiserver is ready\n" "${GREEN}✓${OFF}"
      return 0
    fi
    printf "."
    sleep 1
  done

  printf "\n  %s apiserver did not become ready within %ss.\n" "${RED}ERROR:${OFF}" "$API_TIMEOUT_SECS" >&2
  printf "         Check it directly:  docker logs %s --tail 50\n" "${DEV_CLUSTERS[0]}-control-plane" >&2
  printf "         Or start over:      task cluster:delete && task up\n" >&2
  return 1
}

main() {
  if ! docker info >/dev/null 2>&1; then
    printf "  %s docker is not running — start Docker and try again.\n" "${RED}ERROR:${OFF}" >&2
    exit 1
  fi

  local paused=() stopped=() c
  while IFS= read -r c; do [ -n "$c" ] && paused+=("$c"); done < <(frozen_containers paused)
  while IFS= read -r c; do [ -n "$c" ] && stopped+=("$c"); done < <(frozen_containers exited)

  if [ ${#paused[@]} -eq 0 ] && [ ${#stopped[@]} -eq 0 ]; then
    printf "  %s\n" "${DIM}nothing suspended — continuing${OFF}"
    exit 0
  fi

  # Both can be non-empty at once: a Docker Desktop restart turns paused
  # containers into exited ones, and the registry and node may have diverged.
  if [ ${#paused[@]} -gt 0 ]; then
    printf "  unpausing %d container(s): %s\n" "${#paused[@]}" "$(printf '%s ' "${paused[@]}" | sed 's/ $//')"
    docker unpause "${paused[@]}" >/dev/null
  fi
  if [ ${#stopped[@]} -gt 0 ]; then
    printf "  starting %d stopped container(s): %s\n" "${#stopped[@]}" "$(printf '%s ' "${stopped[@]}" | sed 's/ $//')"
    docker start "${stopped[@]}" >/dev/null
    note_moved_ips
  fi

  wait_for_api

  # The state file has served its purpose; leaving it behind would make the
  # NEXT resume compare against stale addresses.
  rm -f "$STATE_FILE"

  if [ ${#stopped[@]} -gt 0 ]; then
    # Pods that were mid-flight when the node was killed come back as the
    # kubelet re-syncs. The ones whose dependencies are not up yet crash once
    # and enter exponential back-off, which is why the last of them can take
    # 5 minutes. Nothing to do but wait; the Tilt UI shows red until then.
    printf "  %s\n" "${DIM}Workloads are restarting — pods that crashed on first start sit in back-off; expect up to ~5 min until everything is Ready.${OFF}"
  else
    printf "  %s\n" "${DIM}Pods continued where they were — no restarts.${OFF}"
  fi
}

main "$@"
