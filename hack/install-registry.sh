#!/usr/bin/env bash
# hack/install-registry.sh — set up a local container registry for the kind dev cluster.
#
# Idempotent: safe to run multiple times. Skips steps already completed.
#
# Creates a local Docker registry container (registry:2) accessible at:
#   - localhost:5001     (from the host, for `docker push`)
#   - kind-registry:5000 (from inside the kind cluster)
#
# The kind cluster's containerdConfigPatches (config/kind/cluster.yaml) mirror
# kind-registry:5000 so images pushed to localhost:5001 are pullable by pods
# without any TLS or auth setup.
#
# Usage:
#   ./hack/install-registry.sh      # run directly
#   task dev:cluster:registry        # preferred: via Taskfile
set -euo pipefail

REGISTRY_NAME="kind-registry"
REGISTRY_HOST_PORT="5001"
REGISTRY_CONTAINER_PORT="5000"
KIND_CLUSTER_NAME="suparship-dev"

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

CERTS_DIR="${REPO_ROOT}/config/kind/registry-certs"

# ── Helpers ───────────────────────────────────────────────────────────────
info()  { printf "  \033[0;36m%s\033[0m\n" "$*"; }
ok()    { printf "  \033[0;32m✓\033[0m  %s\n" "$*"; }
skip()  { printf "  \033[0;33m–\033[0m  %s\n" "$*"; }
die()   { printf "  \033[0;31mERROR:\033[0m %s\n" "$*" >&2; exit 1; }

echo ""
echo "  suparShip — local registry setup"
echo "  ──────────────────────────────────────────────────────────────────"
echo ""

# ── 0. Generate TLS certificate if not present ───────────────────────────
# Kargo requires HTTPS to discover images from the Warehouse. The registry
# serves TLS with a self-signed cert; Kargo's insecureSkipTLSVerify handles it.
info "TLS certificate for '${REGISTRY_NAME}'..."
if [ -f "${CERTS_DIR}/registry.crt" ] && [ -f "${CERTS_DIR}/registry.key" ]; then
  skip "already exists"
else
  mkdir -p "${CERTS_DIR}"
  openssl req -newkey rsa:2048 -nodes -sha256 \
    -keyout "${CERTS_DIR}/registry.key" \
    -x509 -days 3650 \
    -out "${CERTS_DIR}/registry.crt" \
    -subj "/CN=${REGISTRY_NAME}" \
    -addext "subjectAltName=DNS:${REGISTRY_NAME},DNS:localhost" 2>/dev/null
  ok "generated self-signed cert"
fi
echo ""

# ── 1. Start the registry container ──────────────────────────────────────
info "Registry container '${REGISTRY_NAME}'..."
if docker inspect "${REGISTRY_NAME}" >/dev/null 2>&1; then
  running=$(docker inspect -f '{{.State.Running}}' "${REGISTRY_NAME}" 2>/dev/null || true)
  if [ "$running" = "true" ]; then
    skip "already running on port ${REGISTRY_HOST_PORT}"
  else
    docker start "${REGISTRY_NAME}" >/dev/null
    ok "started (was stopped)"
  fi
else
  docker run -d \
    --name "${REGISTRY_NAME}" \
    --restart=always \
    -p "127.0.0.1:${REGISTRY_HOST_PORT}:${REGISTRY_CONTAINER_PORT}" \
    -v "${CERTS_DIR}:/certs:ro" \
    -e REGISTRY_HTTP_TLS_CERTIFICATE=/certs/registry.crt \
    -e REGISTRY_HTTP_TLS_KEY=/certs/registry.key \
    registry:2 >/dev/null
  ok "created and running on port ${REGISTRY_HOST_PORT} (TLS)"
fi
echo ""

# ── 2. Connect registry to the kind network ─────────────────────────────
info "Connecting to 'kind' docker network..."
if docker network inspect kind >/dev/null 2>&1; then
  if docker network inspect kind -f '{{range .Containers}}{{.Name}} {{end}}' | grep -qw "${REGISTRY_NAME}"; then
    skip "already connected"
  else
    docker network connect kind "${REGISTRY_NAME}" 2>/dev/null || true
    ok "connected"
  fi
else
  skip "kind network not found yet (will connect when cluster is created)"
fi
echo ""

# ── 3. Document the registry for kind (configmap convention) ─────────────
# kind uses a well-known ConfigMap to advertise local registries to tools
# that follow the KEP-1755 convention.
info "Registering ConfigMap for local-registry..."
if kubectl get configmap local-registry-hosting -n kube-public >/dev/null 2>&1; then
  skip "ConfigMap already exists"
else
  cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: local-registry-hosting
  namespace: kube-public
data:
  localRegistryHosting.v1: |
    host: "localhost:${REGISTRY_HOST_PORT}"
    hostFromContainerRuntime: "${REGISTRY_NAME}:${REGISTRY_CONTAINER_PORT}"
    hostFromClusterNetwork: "${REGISTRY_NAME}:${REGISTRY_CONTAINER_PORT}"
    help: "https://kind.sigs.k8s.io/docs/user/local-registry/"
EOF
  ok "ConfigMap created"
fi
echo ""

# ── 4. Re-connect registry if cluster was created before registry ────────
# If the kind cluster exists but the registry was started after, ensure
# the network connection is established.
if docker inspect "${KIND_CLUSTER_NAME}-control-plane" >/dev/null 2>&1; then
  if ! docker network inspect kind -f '{{range .Containers}}{{.Name}} {{end}}' 2>/dev/null | grep -qw "${REGISTRY_NAME}"; then
    docker network connect kind "${REGISTRY_NAME}" 2>/dev/null || true
    ok "connected registry to kind network (post-cluster)"
  fi
fi

# ── 4. Configure containerd certs.d in the Kind node ────────────────────
# containerd v2 uses certs.d/<host>/hosts.toml for per-registry TLS config.
# The cluster.yaml patch sets config_path=/etc/containerd/certs.d.
# We write the hosts.toml so containerd skips TLS verification for the
# self-signed registry cert. This is idempotent.
NODE_CONTAINER="${KIND_CLUSTER_NAME}-control-plane"
info "Configuring containerd certs.d for '${REGISTRY_NAME}:${REGISTRY_CONTAINER_PORT}'..."
if docker inspect "${NODE_CONTAINER}" >/dev/null 2>&1; then
  docker exec "${NODE_CONTAINER}" sh -c "
    mkdir -p /etc/containerd/certs.d/${REGISTRY_NAME}:${REGISTRY_CONTAINER_PORT}
    cat > /etc/containerd/certs.d/${REGISTRY_NAME}:${REGISTRY_CONTAINER_PORT}/hosts.toml << 'TOML'
server = \"https://${REGISTRY_NAME}:${REGISTRY_CONTAINER_PORT}\"

[host.\"https://${REGISTRY_NAME}:${REGISTRY_CONTAINER_PORT}\"]
  capabilities = [\"pull\", \"resolve\"]
  skip_verify = true
TOML
  " 2>&1
  ok "hosts.toml written — containerd will skip TLS verification"
else
  skip "Kind node not running yet — will be configured at cluster creation via containerdConfigPatches"
fi
echo ""

# ── 5. Append to .env.cluster ────────────────────────────────────────────
ENV_FILE="${REPO_ROOT}/.env.cluster"
if [ -f "$ENV_FILE" ] && grep -q "^REGISTRY_HOST=" "$ENV_FILE"; then
  skip "REGISTRY_HOST already in .env.cluster"
else
  {
    echo ""
    echo "# ── Local container registry ────────────────────────────────────"
    echo "REGISTRY_HOST=${REGISTRY_NAME}:${REGISTRY_CONTAINER_PORT}"
    echo "REGISTRY_HOST_URL=localhost:${REGISTRY_HOST_PORT}"
  } >> "$ENV_FILE"
  ok "wrote REGISTRY_HOST to .env.cluster"
fi
echo ""

# ── Done ──────────────────────────────────────────────────────────────────
cat <<EOF
  ──────────────────────────────────────────────────────────────────
  Local registry is ready.

  Push images from host:
    docker tag myimage localhost:${REGISTRY_HOST_PORT}/myimage:tag
    docker push localhost:${REGISTRY_HOST_PORT}/myimage:tag

  Pull from inside kind cluster:
    image: ${REGISTRY_NAME}:${REGISTRY_CONTAINER_PORT}/myimage:tag

EOF
