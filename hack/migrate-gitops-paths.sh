#!/usr/bin/env bash
# migrate-gitops-paths.sh
#
# Converts the gitops-output/ directory from the old app-centric layout
# (Model A) to the new env/cluster-centric layout (Model B).
#
# Old layout (Model A):
#   gitops-output/{project}/{app}/{env}/argocd-app.yaml
#
# New layout (Model B):
#   gitops-output/{env}/{project}/{app}/app.yaml
#   gitops-output/{env}/{project}/{app}/values.yaml
#   gitops-output/{env}/appset.yaml
#   gitops-output/{env}/{project}/appproject.yaml
#
# This script:
#   1. Finds all argocd-app.yaml files under gitops-output/
#   2. Extracts project, app, env from the path
#   3. Extracts metadata from the Application CRD (template name, project name)
#   4. Writes minimal app.yaml under the new path
#   5. Moves values.yaml (if present) to the new path
#   6. Removes old argocd-app.yaml files
#   7. Leaves appset.yaml and appproject.yaml for suparship to generate
#      (run `suparship publish env-infra --project <project>` after migration)
#
# Usage:
#   hack/migrate-gitops-paths.sh [--dry-run] [--gitops-dir /path/to/gitops-output]
#
# Options:
#   --dry-run       Show what would be done without making changes
#   --gitops-dir    Path to gitops-output directory (default: ./gitops-output)
#
# Requirements:
#   - bash ≥ 4.0
#   - yq (https://github.com/mikefarah/yq) for YAML parsing
#   - git (for committing the result)
#
# Run from the root of the gitops repository.

set -euo pipefail

DRY_RUN=false
GITOPS_DIR="./gitops-output"

# ── argument parsing ──────────────────────────────────────────────────────────

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run)
      DRY_RUN=true
      shift
      ;;
    --gitops-dir)
      GITOPS_DIR="$2"
      shift 2
      ;;
    *)
      echo "Unknown argument: $1" >&2
      exit 1
      ;;
  esac
done

# ── helpers ───────────────────────────────────────────────────────────────────

log() { echo "[migrate] $*"; }
drylog() { echo "[migrate][DRY-RUN] $*"; }

run() {
  if [[ "$DRY_RUN" == "true" ]]; then
    drylog "$*"
  else
    "$@"
  fi
}

check_dep() {
  if ! command -v "$1" &>/dev/null; then
    echo "Error: '$1' is required but not found in PATH." >&2
    echo "Install with: $2" >&2
    exit 1
  fi
}

# ── preflight ─────────────────────────────────────────────────────────────────

check_dep yq "brew install yq   OR   wget https://github.com/mikefarah/yq/releases/latest"

if [[ ! -d "$GITOPS_DIR" ]]; then
  echo "Error: gitops-output directory not found at '$GITOPS_DIR'." >&2
  echo "Run from the root of your gitops repository, or pass --gitops-dir <path>." >&2
  exit 1
fi

log "Starting Model A → Model B migration"
log "gitops-output: $GITOPS_DIR"
[[ "$DRY_RUN" == "true" ]] && log "DRY-RUN mode: no files will be changed"

# ── discovery and migration ───────────────────────────────────────────────────

MIGRATED=0
SKIPPED=0

# Find old argocd-app.yaml files: gitops-output/{project}/{app}/{env}/argocd-app.yaml
while IFS= read -r -d '' OLD_PATH; do
  # Strip the gitops-output/ prefix and the /argocd-app.yaml suffix.
  REL="${OLD_PATH#"$GITOPS_DIR/"}"           # {project}/{app}/{env}/argocd-app.yaml
  REL="${REL%/argocd-app.yaml}"              # {project}/{app}/{env}

  IFS='/' read -r PROJECT APP ENV <<< "$REL"

  if [[ -z "$PROJECT" || -z "$APP" || -z "$ENV" ]]; then
    log "SKIP: $OLD_PATH — could not parse project/app/env from path"
    ((SKIPPED++))
    continue
  fi

  # Extract template name from Application CRD (spec.source.path = charts/{template}).
  CHART_PATH=$(yq '.spec.source.path // ""' "$OLD_PATH" 2>/dev/null || true)
  TEMPLATE="${CHART_PATH#charts/}"
  if [[ -z "$TEMPLATE" ]]; then
    # Try multi-source format.
    TEMPLATE=$(yq '.spec.sources[] | select(.path | test("^charts/")) | .path | ltrimstr("charts/")' "$OLD_PATH" 2>/dev/null | head -1 || true)
  fi
  if [[ -z "$TEMPLATE" ]]; then
    TEMPLATE="unknown"
    log "WARN: $OLD_PATH — could not determine template name; using 'unknown'"
  fi

  # New paths.
  NEW_DIR="$GITOPS_DIR/$ENV/$PROJECT/$APP"
  NEW_APP_YAML="$NEW_DIR/app.yaml"
  NEW_VALUES_YAML="$NEW_DIR/values.yaml"

  log "Migrating: $OLD_PATH → $NEW_APP_YAML"

  # Write app.yaml.
  run mkdir -p "$NEW_DIR"
  if [[ "$DRY_RUN" != "true" ]]; then
    cat > "$NEW_APP_YAML" <<EOF
name: $APP
project: $PROJECT
template: $TEMPLATE
EOF
  else
    drylog "Would write: $NEW_APP_YAML"
    drylog "  name: $APP"
    drylog "  project: $PROJECT"
    drylog "  template: $TEMPLATE"
  fi

  # Move values.yaml if it exists alongside argocd-app.yaml.
  OLD_VALUES="${OLD_PATH%/argocd-app.yaml}/values.yaml"
  if [[ -f "$OLD_VALUES" ]]; then
    log "Moving values.yaml: $OLD_VALUES → $NEW_VALUES_YAML"
    run mv "$OLD_VALUES" "$NEW_VALUES_YAML"
  fi

  # Remove old argocd-app.yaml.
  log "Removing: $OLD_PATH"
  run rm -f "$OLD_PATH"

  # Remove empty parent directory (old {project}/{app}/{env}) if now empty.
  OLD_DIR="${OLD_PATH%/argocd-app.yaml}"
  run rmdir --ignore-fail-on-non-empty "$OLD_DIR" 2>/dev/null || true
  run rmdir --ignore-fail-on-non-empty "$(dirname "$OLD_DIR")" 2>/dev/null || true
  run rmdir --ignore-fail-on-non-empty "$(dirname "$(dirname "$OLD_DIR")")" 2>/dev/null || true

  ((MIGRATED++))

done < <(find "$GITOPS_DIR" -name "argocd-app.yaml" -print0)

# ── summary ───────────────────────────────────────────────────────────────────

log ""
log "Migration complete:"
log "  Migrated: $MIGRATED argocd-app.yaml files"
log "  Skipped:  $SKIPPED files (see warnings above)"
log ""
log "Next steps:"
log "  1. For each environment, have suparship generate appset.yaml:"
log "     suparship cluster publish-infra --project <project>"
log "  2. Review the generated files in gitops-output/"
log "  3. Commit and push:"
log "     git add gitops-output/ && git commit -m 'chore: migrate gitops-output to Model B layout'"
log "  4. Update config/gitops/root-app.yaml (already done if you updated suparship)."
