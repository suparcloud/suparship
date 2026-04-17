package gitops

import (
	"fmt"

	"github.com/suparcloud/suparship/internal/secrets"
)

const onePasswordStoreName = "suparship-1password-store"

func init() {
	ESOStoreNames["1password"] = onePasswordStoreName
}

// Build1PasswordClusterSecretStoreYAML returns the YAML for a
// ClusterSecretStore that connects ESO to 1Password.
//
// Connect mode uses the 1Password Connect server API.
// Service-account mode uses the 1Password CLI service account token.
func Build1PasswordClusterSecretStoreYAML(cfg *secrets.OnePasswordConfig) string {
	switch cfg.Mode {
	case secrets.OnePasswordModeConnect:
		return build1PasswordConnectStore(cfg)
	case secrets.OnePasswordModeServiceAccount:
		return build1PasswordServiceAccountStore(cfg)
	default:
		return fmt.Sprintf("# unsupported 1password mode: %s\n", cfg.Mode)
	}
}

func build1PasswordConnectStore(cfg *secrets.OnePasswordConfig) string {
	tokenSecretRef := ""
	if cfg.ExistingSecret != "" {
		tokenSecretRef = fmt.Sprintf(`      auth:
        secretRef:
          connectTokenSecretRef:
            name: %s
            key: token
            namespace: suparship-system`, cfg.ExistingSecret)
	}

	return fmt.Sprintf(`apiVersion: external-secrets.io/v1
kind: ClusterSecretStore
metadata:
  name: %s
  labels:
    suparship.io/managed-by: suparship
  annotations:
    suparship.io/description: "1Password Connect backend"
spec:
  provider:
    onepassword:
      connectHost: %s
%s
`, onePasswordStoreName, cfg.ConnectHost, tokenSecretRef)
}

func build1PasswordServiceAccountStore(cfg *secrets.OnePasswordConfig) string {
	tokenSecretRef := ""
	if cfg.ExistingSecret != "" {
		tokenSecretRef = fmt.Sprintf(`      auth:
        secretRef:
          serviceAccountTokenSecretRef:
            name: %s
            key: token
            namespace: suparship-system`, cfg.ExistingSecret)
	}

	return fmt.Sprintf(`apiVersion: external-secrets.io/v1
kind: ClusterSecretStore
metadata:
  name: %s
  labels:
    suparship.io/managed-by: suparship
  annotations:
    suparship.io/description: "1Password Service Account backend"
spec:
  provider:
    onepassword:
%s
`, onePasswordStoreName, tokenSecretRef)
}

// Build1PasswordExternalSecretYAML returns an ExternalSecret CR that pulls
// a set of items from a specific 1Password vault into a K8s Secret.
func Build1PasswordExternalSecretYAML(name, namespace, vaultUUID string, keys []string) string {
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
    suparship.io/managed-by: suparship
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: %s
    kind: ClusterSecretStore
  target:
    name: %s
    creationPolicy: Owner
  data:
%s`, name, namespace, onePasswordStoreName, name, dataEntries)
}
