package gitops

import (
	"fmt"

	"github.com/suparcloud/suparship/internal/branding"
	"github.com/suparcloud/suparship/internal/secrets"
)

const onePasswordStoreName = "suparship-1password-store"

func init() {
	ESOStoreNames["1password"] = onePasswordStoreName
}

// Build1PasswordClusterSecretStoreYAML returns the YAML for a
// ClusterSecretStore that connects ESO to 1Password via Connect.
// The Connect URL points to the managed Connect server in the tooling
// cluster; the auth secret is the sealed per-env Connect token.
func Build1PasswordClusterSecretStoreYAML(storeName, connectURL, vaultID, tokenSecretName, tokenSecretKey, tokenSecretNS string, brand branding.Config) string {
	return fmt.Sprintf(`apiVersion: external-secrets.io/v1
kind: ClusterSecretStore
metadata:
  name: %s
  labels:
%s
spec:
  provider:
    onepassword:
      connectHost: %s
      vaults:
        %s: 1
      auth:
        secretRef:
          connectTokenSecretRef:
            name: %s
            key: %s
            namespace: %s
`, storeName, branding.LabelsYAML(brand.ManagedByLabels(), 4), connectURL, vaultID, tokenSecretName, tokenSecretKey, tokenSecretNS)
}

// Build1PasswordExternalSecretYAML returns an ExternalSecret CR that pulls
// a set of items from a specific 1Password vault into a K8s Secret.
func Build1PasswordExternalSecretYAML(name, namespace, vaultUUID string, keys []string, brand branding.Config) string {
	var dataEntries string
	for _, key := range keys {
		dataEntries += fmt.Sprintf(`  - secretKey: %s
    remoteRef:
      key: %s
      property: %s
`, key, vaultUUID, key)
	}

	return fmt.Sprintf(`apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: %s
  namespace: %s
  labels:
%s
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: %s
    kind: ClusterSecretStore
  target:
    name: %s
    creationPolicy: Owner
  data:
%s`, name, namespace, branding.LabelsYAML(brand.ManagedByLabels(), 4), onePasswordStoreName, name, dataEntries)
}

// DefaultConnectEndpoint is the in-cluster URL of the managed Connect
// server in the tooling cluster. Used in ClusterSecretStore specs.
const DefaultConnectEndpoint = "http://onepassword-connect." + secrets.DefaultConnectNamespace + ".svc.cluster.local:8080"
