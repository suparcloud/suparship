#!/usr/bin/env bash
# hack/dev/vault-storage-migrate.sh — one-time migration for dev clusters created
# before the dev Vault became persistent.
#
# The dev Vault used to run `server.dev.enabled=true` (in-memory, no PVC). It now
# runs standalone with file storage on a PVC, which adds volumeClaimTemplates to
# the StatefulSet — and the API forbids that on an existing StatefulSet:
#
#   StatefulSet.apps "vault" is invalid: spec: Forbidden: updates to statefulset
#   spec for fields other than 'replicas', 'ordinals', 'template',
#   'updateStrategy', 'revisionHistoryLimit',
#   'persistentVolumeClaimRetentionPolicy' and 'minReadySeconds' are forbidden
#
# So `helm upgrade` fails and the vault resource goes red. Delete the
# incompatible StatefulSet and let helm recreate it with the volume. Nothing is
# lost: the StatefulSet being replaced is the in-memory one, whose contents died
# with its pod anyway.
#
# No-op when there is no StatefulSet, or when it already has a volume — so it is
# safe on a fresh cluster and on every subsequent run.
#
# Used by: Tiltfile (local_resource 'vault-storage-migrate', vault mode), which
# runs it before the `vault` helm_resource.
set -euo pipefail

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_dev_context

VAULT_NS="${SUPARSHIP_VAULT_NS:-vault}"

if ! kubectl -n "$VAULT_NS" get statefulset vault >/dev/null 2>&1; then
  echo "  no existing vault StatefulSet — nothing to migrate"
  exit 0
fi

claims="$(kubectl -n "$VAULT_NS" get statefulset vault \
  -o jsonpath='{.spec.volumeClaimTemplates[*].metadata.name}' 2>/dev/null || true)"

if [ -n "$claims" ]; then
  echo "  vault StatefulSet already has storage (${claims}) — nothing to migrate"
  exit 0
fi

echo "  found a storage-less (dev-mode) vault StatefulSet — deleting it so helm can"
echo "  recreate it with a PVC. Its data was in-memory, so nothing is lost."
kubectl -n "$VAULT_NS" delete statefulset vault --wait=true
# The keys stash describes a Vault that no longer exists; leaving it behind would
# make vault-bootstrap try to unseal a freshly-initialised server with a stale key.
kubectl -n "$VAULT_NS" delete secret vault-dev-keys --ignore-not-found >/dev/null
echo "  done — the vault resource will reinstall with persistent storage"
