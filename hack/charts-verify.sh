#!/usr/bin/env bash
# charts-verify.sh — CI guard for the generic chart catalog + platform chart.
#
# Every chart in examples/charts/ is a plain BYO Helm chart; the platform's
# only contract with it is ((platform.*))/((vars.*)) tokens in values overlays
# plus the env ConfigMap/Secret names they resolve to. This script asserts:
#
#   1. helm lint passes for every examples/charts/<name> and charts/suparship.
#   2. Every chart renders non-empty output with its DEFAULT values.
#   3. Every chart renders with its ci/platform-values.yaml (the contract
#      expressed as an app overlay, tokens as literal strings), and:
#        - the token strings land in the rendered output verbatim,
#        - suspend: true scales web/worker to zero and omits the HPA,
#        - the cronjob renders spec.suspend: true,
#        - the job renders its ArgoCD PreSync hook annotations.
#
# Exit code 0 = clean.

set -euo pipefail

cd "$(dirname "$0")/.."

fail() { echo "  FAIL: $*" >&2; exit 1; }

CHARTS_DIR=examples/charts
TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

# ---------------------------------------------------------------------------
# 1. helm lint
# ---------------------------------------------------------------------------
echo "==> helm lint"
for c in "$CHARTS_DIR"/*/; do
  name=$(basename "$c")
  helm lint "$c" >/dev/null || fail "helm lint $name"
  echo "  OK  $name"
done
helm lint charts/suparship >/dev/null || fail "helm lint charts/suparship"
echo "  OK  charts/suparship"

# ---------------------------------------------------------------------------
# 2. default-values render
# ---------------------------------------------------------------------------
echo "==> default-values render"
for c in "$CHARTS_DIR"/*/; do
  name=$(basename "$c")
  out="$TMPDIR/$name-default.yaml"
  helm template t "$c" >"$out" || fail "helm template $name (defaults)"
  grep -q 'kind:' "$out" || fail "$name renders no resources with defaults"
  echo "  OK  $name"
done

# ---------------------------------------------------------------------------
# 3. platform-contract render (ci/platform-values.yaml)
# ---------------------------------------------------------------------------
echo "==> platform-contract render"
for c in "$CHARTS_DIR"/*/; do
  name=$(basename "$c")
  ci="$c/ci/platform-values.yaml"
  [ -f "$ci" ] || fail "$name is missing ci/platform-values.yaml"
  out="$TMPDIR/$name-ci.yaml"
  helm template t "$c" -f "$ci" >"$out" || fail "helm template $name (ci values)"
  echo "  OK  $name"
done

# Token strings must land verbatim where the overlay put them.
for name in web worker cronjob job postgres; do
  grep -q '((platform.configMapName))' "$TMPDIR/$name-ci.yaml" \
    || fail "$name: ((platform.configMapName)) did not reach the rendered envFrom"
done
grep -q '((platform.routingHost))' "$TMPDIR/web-ci.yaml" \
  || fail "web: ((platform.routingHost)) did not reach the rendered Ingress"
echo "  OK  tokens land verbatim"

# Suspend contract: web/worker scale to zero, no HPA; cronjob sets spec.suspend.
for name in web worker; do
  grep -q 'replicas: 0' "$TMPDIR/$name-ci.yaml" || fail "$name: suspend must render replicas: 0"
  if grep -q 'kind: HorizontalPodAutoscaler' "$TMPDIR/$name-ci.yaml"; then
    fail "$name: suspended render must omit the HPA"
  fi
done
grep -q 'suspend: true' "$TMPDIR/cronjob-ci.yaml" || fail "cronjob: suspend must render spec.suspend: true"
# job/gateway/postgres honor suspend too (their ci values render unsuspended,
# so give each a dedicated suspended render):
#   - postgres scales to zero but KEEPS the PVC (data survives),
#   - job omits the Job entirely (a scaled-down env must not run migrations),
#   - gateway omits the Gateway and HTTPRoutes (traffic stops until resume).
helm template t "$CHARTS_DIR/postgres" -f "$CHARTS_DIR/postgres/ci/platform-values.yaml" --set suspend=true >"$TMPDIR/postgres-susp.yaml" \
  || fail "helm template postgres (suspended)"
grep -q 'replicas: 0' "$TMPDIR/postgres-susp.yaml" || fail "postgres: suspend must render replicas: 0"
grep -q 'kind: PersistentVolumeClaim' "$TMPDIR/postgres-susp.yaml" || fail "postgres: suspend must keep the PVC"
helm template t "$CHARTS_DIR/job" -f "$CHARTS_DIR/job/ci/platform-values.yaml" --set suspend=true >"$TMPDIR/job-susp.yaml" \
  || fail "helm template job (suspended)"
if grep -q 'kind: Job' "$TMPDIR/job-susp.yaml"; then fail "job: suspended render must omit the Job"; fi
helm template t "$CHARTS_DIR/gateway" -f "$CHARTS_DIR/gateway/ci/platform-values.yaml" --set suspend=true >"$TMPDIR/gateway-susp.yaml" \
  || fail "helm template gateway (suspended)"
if grep -qE 'kind: (Gateway|HTTPRoute)$' "$TMPDIR/gateway-susp.yaml"; then fail "gateway: suspended render must omit Gateway/HTTPRoutes"; fi
echo "  OK  suspend contract"

# Job hook contract: PreSync + BeforeHookCreation, backoffLimit 0.
grep -q 'argocd.argoproj.io/hook: PreSync' "$TMPDIR/job-ci.yaml" || fail "job: missing PreSync hook annotation"
grep -q 'argocd.argoproj.io/hook-delete-policy: BeforeHookCreation' "$TMPDIR/job-ci.yaml" || fail "job: missing hook-delete-policy"
grep -q 'backoffLimit: 0' "$TMPDIR/job-ci.yaml" || fail "job: backoffLimit must default to 0"
echo "  OK  job hook contract"

echo "charts-verify: all clean"
