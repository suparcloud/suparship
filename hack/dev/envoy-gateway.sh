#!/usr/bin/env bash
# Install Envoy Gateway on the dev cluster and create the shared edge Gateway
# that platform-owned routes (docs/routing.md) attach to.
#
# After this, point the org's external routing profile at it:
#   Settings → Routing → external: gateway { name: edge, namespace: gateways, sectionName: http }
# (or PUT /api/v1/org/routing with the same fields). *.localhost resolves to the
# kind node; the Gateway listens on port 80 behind the dev ingress hostport.
set -euo pipefail

EG_VERSION="${EG_VERSION:-v1.3.0}"
GATEWAY_NS="${GATEWAY_NS:-gateways}"

echo "==> Envoy Gateway ${EG_VERSION}"
helm upgrade --install eg oci://docker.io/envoyproxy/gateway-helm \
  --version "${EG_VERSION}" -n envoy-gateway-system --create-namespace --wait

echo "==> GatewayClass eg + Gateway ${GATEWAY_NS}/edge"
kubectl apply -f - <<YAML
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: eg
spec:
  controllerName: gateway.envoyproxy.io/gatewayclass-controller
---
apiVersion: v1
kind: Namespace
metadata:
  name: ${GATEWAY_NS}
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: edge
  namespace: ${GATEWAY_NS}
spec:
  gatewayClassName: eg
  listeners:
    - name: http
      protocol: HTTP
      port: 80
      hostname: "*.localhost"
      allowedRoutes:
        namespaces:
          from: All
YAML

kubectl wait --for=condition=Programmed gateway/edge -n "${GATEWAY_NS}" --timeout=120s || true
echo "Gateway ready: $(kubectl get gateway edge -n "${GATEWAY_NS}" -o jsonpath='{.status.addresses[0].value}' 2>/dev/null || echo pending)"
echo "Set the org external routing profile gateway to {name: edge, namespace: ${GATEWAY_NS}, sectionName: http}."
