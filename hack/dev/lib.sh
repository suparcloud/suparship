# hack/dev/lib.sh — shared context guard for dev-cluster scripts.
#
# Source this from any script that MUTATES the local dev cluster:
#
#     source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/dev/lib.sh"
#
# Why this exists: these scripts are shelled out to by the Tiltfile's
# local_resource blocks, which run in the ambient environment. Tilt validates
# its OWN context (see EXPECTED_CONTEXT in the Tiltfile) but cannot police a
# subprocess, so a bare `kubectl apply` here would hit whatever context your
# kubeconfig happens to be on. For most contributors that is a real cluster —
# this repo's own maintainer kubeconfig carries production EKS and AKS entries.
# Pinning is the difference between seeding a dev namespace and seeding prod.
#
# The shadowing functions below mean call sites need no changes: every
# `kubectl` / `helm` invocation in a sourcing script is routed to the dev
# cluster automatically. `command` avoids recursing into the function.
#
# Override the target with DEV_KUBE_CONTEXT (e.g. to point at a differently
# named local cluster). It is deliberately NOT possible to opt out silently.

DEV_KUBE_CONTEXT="${DEV_KUBE_CONTEXT:-kind-suparship-dev}"

kubectl() { command kubectl --context "$DEV_KUBE_CONTEXT" "$@"; }
helm() { command helm --kube-context "$DEV_KUBE_CONTEXT" "$@"; }

# require_dev_context fails fast with an actionable message when the dev
# cluster is missing, rather than letting kubectl emit a raw context error.
require_dev_context() {
  if ! command kubectl config get-contexts -o name 2>/dev/null | grep -qx "$DEV_KUBE_CONTEXT"; then
    printf "  \033[0;31mERROR:\033[0m kube context %s not found.\n" "$DEV_KUBE_CONTEXT" >&2
    printf "         Create the dev cluster first:  task up   (or: ctlptl apply -f hack/dev/cluster.yaml)\n" >&2
    exit 1
  fi
}
