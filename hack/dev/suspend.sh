#!/usr/bin/env bash
# hack/dev/suspend.sh — freeze the dev cluster without tearing it down.
#
#   task suspend                    # this script (pause mode)
#   SUSPEND_MODE=stop task suspend  # stop mode, see below
#   task resume                     # hack/dev/resume.sh (+ tilt up if needed)
#
# The gap this fills: `task down` runs `tilt down`, which DELETES the in-cluster
# workloads. Coming back then means re-running every Helm install, re-pulling
# images and re-seeding — minutes. But leaving the cluster up is not free
# either: an idle kind node still runs a full control plane (etcd, apiserver,
# controller-manager, scheduler) plus ArgoCD, Kargo, Gitea and Vault, which on a
# laptop is a steady CPU draw and a warm machine.
#
# Two ways to freeze, picked by SUSPEND_MODE:
#
#   pause (default)  `docker pause`: every process in the node goes into the
#                    cgroup freezer. 0% CPU, RAM stays allocated. Nothing is
#                    killed, so on resume every pod continues exactly where it
#                    was — measured: apiserver answering 2s after unpause, node
#                    Ready in 11s, zero container restarts across 44 pods, and a
#                    running Tilt session reconnects on its own. This is the
#                    same thing that happens to the cluster when the laptop lid
#                    closes, and clusters survive that every day.
#
#   stop             `docker stop`: processes are killed and the container's
#                    filesystem (etcd, registry blobs, Gitea repos, Vault
#                    storage) is kept. Frees the RAM too. The price is a restart
#                    storm on resume: pods whose dependencies are not up yet
#                    crash on first start and land in Kubernetes' exponential
#                    crash back-off — measured 5-6 MINUTES until the last pod
#                    was Ready, even though the apiserver was back in 10s. Use
#                    this only when you need the memory back.
#
# What it touches: only the clusters THIS repo defines (hack/dev/cluster.yaml
# and hack/dev/clusters-workload.yaml) plus the ctlptl registry. Other kind
# clusters on the machine are left alone — see DEV_CLUSTERS below.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../.."

RED=$'\033[0;31m'; YELLOW=$'\033[0;33m'; GREEN=$'\033[0;32m'; DIM=$'\033[2m'; OFF=$'\033[0m'

MODE="${SUSPEND_MODE:-pause}"
case "$MODE" in
  pause|stop) ;;
  *) printf "  %s SUSPEND_MODE must be 'pause' or 'stop' (got '%s')\n" "${RED}ERROR:${OFF}" "$MODE" >&2; exit 2 ;;
esac

# The kind clusters this repo owns, by `io.x-k8s.kind.cluster` label value:
# the tooling cluster from hack/dev/cluster.yaml, and the two optional workload
# clusters from hack/dev/clusters-workload.yaml (`task up:multi`).
#
# Deliberately an explicit list rather than "every kind container". Contributors
# routinely have unrelated kind clusters running — this repo's own maintainer
# machine has a `suparship-v010` one — and stopping somebody's other cluster
# because it happens to be kind is not a reasonable thing for `task suspend` to
# do.
DEV_CLUSTERS=(suparship-dev staging prod)

# ctlptl names the registry in hack/dev/cluster.yaml `kind-registry`. Matched by
# name AND by ctlptl's own role label, so an unrelated container called
# "kind-registry" is not swept up.
REGISTRY_NAME="kind-registry"

STATE_FILE="tmp/dev-suspend.state"

tilt_pids() { pgrep -f '(^|/)tilt up( |$)' 2>/dev/null || true; }

# ── Stop Tilt (stop mode only), but do NOT run `tilt down` ───────────────────
# `tilt down` is the thing we are specifically avoiding: it deletes the
# workloads. Exiting the `tilt up` session leaves every Kubernetes object in
# place, which is precisely the state we want frozen. Tilt handles SIGTERM as a
# clean session exit, the same as Ctrl-C in the terminal it runs in.
#
# In pause mode Tilt is left running on purpose: its watches and port-forwards
# just hang while the apiserver is frozen and pick up again on unpause, so
# resume needs no `tilt up` and nothing gets re-run. If you close the terminal
# Tilt lives in, `task resume` notices and starts it.
stop_tilt() {
  local pids
  pids="$(tilt_pids)"
  if [ -z "$pids" ]; then
    printf "  %s\n" "${DIM}tilt is not running${OFF}"
    return 0
  fi

  printf "  stopping tilt (pid %s)\n" "$(tr '\n' ' ' <<<"$pids" | sed 's/ $//')"
  # shellcheck disable=SC2086
  kill -TERM $pids 2>/dev/null || true

  # Give it a moment to exit on its own. We never escalate to SIGKILL: Tilt
  # flushes its engine state on the way out, and a half-killed session is more
  # confusing on resume than a still-running one.
  for _ in $(seq 1 20); do
    [ -n "$(tilt_pids)" ] || return 0
    sleep 0.5
  done

  printf "  %s tilt did not exit within 10s — leaving it alone.\n" "${YELLOW}warning:${OFF}" >&2
  printf "         Stop it yourself (Ctrl-C in its terminal), then re-run.\n" >&2
  printf "         %sDo not use \`tilt down\` — that deletes the workloads this is trying to preserve.%s\n" "$DIM" "$OFF" >&2
  return 1
}

# ── Record node IPs so resume can explain a broken wake-up (stop mode) ───────
# A stopped container gets a fresh address from Docker on start, and on the
# kind network that is first-come-first-served. kind rewrites the node config
# for a new IP on boot, so it normally works, but if the control plane does not
# come back this is the first thing to suspect and resume.sh says so. A paused
# container keeps its address, so pause mode records nothing.
record_state() {
  mkdir -p "$(dirname "$STATE_FILE")"
  : >"$STATE_FILE"
  local c ip
  for c in "$@"; do
    ip="$(docker inspect "$c" --format '{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}' 2>/dev/null | awk '{print $1}')"
    printf "%s\t%s\n" "$c" "${ip:-}" >>"$STATE_FILE"
  done
}

main() {
  if ! docker info >/dev/null 2>&1; then
    printf "  %s docker is not running — nothing to suspend.\n" "${RED}ERROR:${OFF}" >&2
    exit 1
  fi

  # Collect the RUNNING (not already paused, not exited) containers to freeze:
  # kind nodes first, registry last (resume brings them back in the reverse
  # order — see resume.sh).
  local targets=() c cluster
  for cluster in "${DEV_CLUSTERS[@]}"; do
    while IFS= read -r c; do
      [ -n "$c" ] && targets+=("$c")
    done < <(docker ps --filter "label=io.x-k8s.kind.cluster=${cluster}" --filter "status=running" --format '{{.Names}}')
  done
  while IFS= read -r c; do
    [ -n "$c" ] && targets+=("$c")
  done < <(docker ps --filter "name=^/${REGISTRY_NAME}$" --filter "label=dev.tilt.ctlptl.role=registry" --filter "status=running" --format '{{.Names}}')

  if [ ${#targets[@]} -eq 0 ]; then
    printf "  %s\n" "${DIM}no running dev containers — already suspended, or the cluster was never created${OFF}"
    printf "  %s\n" "${DIM}(bring it up with: task up)${OFF}"
    exit 0
  fi

  if [ "$MODE" = "pause" ]; then
    printf "  pausing %d container(s): %s\n" "${#targets[@]}" "$(printf '%s ' "${targets[@]}" | sed 's/ $//')"
    docker pause "${targets[@]}" >/dev/null
    if [ -n "$(tilt_pids)" ]; then
      printf "  %s\n" "${DIM}tilt left running — it reconnects on resume; Ctrl-C it if you are closing the terminal${OFF}"
    fi
    printf "\n  %s dev cluster paused.\n" "${GREEN}✓${OFF}"
    printf "    %s\n" "${DIM}0% CPU. Every pod is frozen in place and continues on resume — no restarts.${OFF}"
    printf "    %s\n" "${DIM}Wake it with: task resume   (or task up — it resumes automatically)${OFF}"
    printf "    %s\n" "${DIM}Need the RAM back instead? SUSPEND_MODE=stop task suspend (pods restart; ~5 min to settle)${OFF}"
    return 0
  fi

  # ── stop mode ──────────────────────────────────────────────────────────────
  stop_tilt
  record_state "${targets[@]}"

  # Stop timeout. The registry exits on SIGTERM instantly. The kind node does
  # not: its STOPSIGNAL is systemd's halt, and measured on a 16-pod dev cluster
  # the halt never finishes on its own — it sits until systemd's own 90s unit
  # stop timeout kills the hung kubelet/containerd and the container exits.
  # Waiting the full 90s therefore buys nothing over letting docker SIGKILL at
  # 30s: either way the same processes get killed, and etcd's write-ahead log
  # makes a kill safe (kind's own delete path and every Docker Desktop restart
  # do exactly this). Both paths were tested and the cluster came back healthy.
  # 30s still gives pods their normal grace period. Override for experiments:
  #   SUSPEND_STOP_TIMEOUT=120 SUSPEND_MODE=stop task suspend
  local stop_timeout="${SUSPEND_STOP_TIMEOUT:-30}"
  printf "  stopping %d container(s): %s\n" "${#targets[@]}" "$(printf '%s ' "${targets[@]}" | sed 's/ $//')"
  printf "  %s\n" "${DIM}(the kind node takes the full ${stop_timeout}s: its systemd halt hangs on the kubelet, and etcd is safe to kill)${OFF}"
  docker stop -t "$stop_timeout" "${targets[@]}" >/dev/null

  printf "\n  %s dev cluster stopped.\n" "${GREEN}✓${OFF}"
  printf "    %s\n" "${DIM}0% CPU, 0 RAM. Cluster state, Gitea repos, Vault data and registry blobs are kept.${OFF}"
  printf "    %s\n" "${DIM}Wake it with: task resume   (or task up — it resumes automatically)${OFF}"
  printf "    %s\n" "${DIM}Expect pods to take a few minutes to settle after a stop; plain \`task suspend\` (pause) avoids that.${OFF}"
}

main "$@"
