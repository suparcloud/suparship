#!/usr/bin/env bash
# charts-verify.sh — CI guard for chart artifact freshness.
#
# Verifies that:
#   1. Every templates/<name>/chart/values.schema.json matches what the
#      generator would produce from internal/helmvalues/values.go.
#   2. Every templates/<name>/chart/charts/suparship-common-*.tgz matches
#      the current charts/lib/suparship-common/ source.
#   3. helm lint succeeds on each template chart and the library chart.
#
# Exit code 0 = clean. Non-zero = drift; the message tells you which
# `task charts:vendor` / `task charts:schema` to run.

set -euo pipefail

cd "$(dirname "$0")/.."

# ---------------------------------------------------------------------------
# 1. values.schema.json freshness
# ---------------------------------------------------------------------------
echo "==> values.schema.json freshness"
if ! go run ./cmd/gen-values-schema --check; then
  echo "  FAIL: values.schema.json files are out of sync." >&2
  echo "  Run: task charts:schema" >&2
  exit 1
fi
echo "  OK"

# ---------------------------------------------------------------------------
# 2. Vendored library chart freshness
# ---------------------------------------------------------------------------
echo "==> vendored suparship-common .tgz freshness"

# Snapshot existing vendored files before re-vendoring.
TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

snapshot_dir() {
  local chart_dir="$1"
  local stash="$2"
  if [ -d "$chart_dir/charts" ]; then
    cp -R "$chart_dir/charts" "$stash"
  fi
  if [ -f "$chart_dir/Chart.lock" ]; then
    cp "$chart_dir/Chart.lock" "$stash.Chart.lock"
  fi
}

restore_dir() {
  local chart_dir="$1"
  local stash="$2"
  rm -rf "$chart_dir/charts"
  if [ -d "$stash" ]; then
    cp -R "$stash" "$chart_dir/charts"
  fi
  if [ -f "$stash.Chart.lock" ]; then
    cp "$stash.Chart.lock" "$chart_dir/Chart.lock"
  fi
}

stale=()
for chart_dir in templates/*/chart; do
  if ! grep -q "suparship-common" "$chart_dir/Chart.yaml" 2>/dev/null; then
    continue
  fi

  chart_name=$(basename "$(dirname "$chart_dir")")
  stash="$TMPDIR/$chart_name"

  # Snapshot, refresh, compare unpacked-content hashes.
  snapshot_dir "$chart_dir" "$stash"

  helm dep update "$chart_dir" >/dev/null 2>&1 || {
    echo "  FAIL: helm dep update failed for $chart_dir" >&2
    exit 2
  }

  fresh_tgz=$(ls "$chart_dir"/charts/suparship-common-*.tgz 2>/dev/null | head -1 || true)
  stash_tgz=$(ls "$stash"/suparship-common-*.tgz 2>/dev/null | head -1 || true)

  if [ -z "$fresh_tgz" ] || [ -z "$stash_tgz" ]; then
    stale+=("$chart_dir (vendored lib missing)")
    continue
  fi

  # Compare the unpacked contents — tgz timestamps differ run-to-run, but
  # the chart source inside should be identical when the lib hasn't changed.
  fresh_unpack="$TMPDIR/${chart_name}-fresh"
  stash_unpack="$TMPDIR/${chart_name}-stash"
  mkdir -p "$fresh_unpack" "$stash_unpack"
  tar -xzf "$fresh_tgz" -C "$fresh_unpack"
  tar -xzf "$stash_tgz" -C "$stash_unpack"

  if ! diff -r "$fresh_unpack" "$stash_unpack" >/dev/null 2>&1; then
    stale+=("$chart_dir")
  fi

  # Restore original snapshot so we don't leave bumped tgz timestamps in
  # the working tree.
  restore_dir "$chart_dir" "$stash"
done

if [ "${#stale[@]}" -gt 0 ]; then
  echo "  FAIL: vendored suparship-common is stale in:" >&2
  for c in "${stale[@]}"; do echo "    - $c" >&2; done
  echo "  Run: task charts:vendor" >&2
  exit 1
fi
echo "  OK"

# ---------------------------------------------------------------------------
# 3. helm lint
# ---------------------------------------------------------------------------
echo "==> helm lint"
helm lint charts/lib/suparship-common >/dev/null
for chart_dir in templates/*/chart; do
  helm lint "$chart_dir" >/dev/null
done
echo "  OK"

echo
echo "all chart artifacts are in sync."
