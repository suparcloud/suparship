#!/usr/bin/env bash
# Validate install defaults, the Tilt overlay, configurable budgets and RBAC.
set -euo pipefail
cd "$(dirname "$0")/.."
chart=charts/suparship
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
helm lint "$chart" --strict
helm lint "$chart" -f hack/dev/values-dev.yaml --strict
helm template suparship "$chart" > "$scratch/default.yaml"
helm template suparship "$chart" -f hack/dev/values-dev.yaml > "$scratch/dev.yaml"
for manifest in "$scratch/default.yaml" "$scratch/dev.yaml"; do
  for flag in kube-qps=50 kube-burst=100 auth-kube-qps=100 auth-kube-burst=200; do
    grep -Fq -- "--$flag" "$manifest"
  done
  grep -Fq 'resources: ["deployments", "replicasets", "statefulsets", "daemonsets"]' "$manifest"
done
helm template suparship "$chart" \
  --set-json server.kubernetes.qps=12.5 --set server.kubernetes.burst=25 \
  --set server.kubernetes.authorizationQps=30,server.kubernetes.authorizationBurst=60 \
  --set ingress.enabled=true > "$scratch/custom.yaml"
for flag in kube-qps=12.5 kube-burst=25 auth-kube-qps=30 auth-kube-burst=60; do
  grep -Fq -- "--$flag" "$scratch/custom.yaml"
done
# Invalid values must be rejected before a pod is deployed.
for invalid in qps=0 burst=-1 authorizationQps=0 authorizationBurst=1.5 qps=oops; do
  if helm template suparship "$chart" --set "server.kubernetes.$invalid" > "$scratch/invalid.log" 2>&1; then
    echo "Expected schema rejection for $invalid" >&2
    exit 1
  fi
  grep -Fq 'values don' "$scratch/invalid.log"
done
helm package "$chart" --destination "$scratch"
echo 'Helm deployment checks passed.'
