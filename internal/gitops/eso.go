package gitops

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/suparcloud/suparship/internal/branding"
	"github.com/suparcloud/suparship/internal/envconfig"
)

// ESOStoreNames maps provider identifiers to their ClusterSecretStore names.
// These are the well-known store names referenced by ExternalSecret CRs.
var ESOStoreNames = map[string]string{
	"k8s":    "suparship-k8s-store",
	"vault":  "suparship-vault-store",
	"aws-sm": "suparship-aws-sm-store",
}

// WriteESOInfra writes the following files to gitops-output/_infra/:
//
//   - eso-stores.yaml        — ClusterSecretStore per supported provider
//
// This is idempotent and should be called once during cluster bootstrap or
// whenever the ESO configuration changes.
func (p *Publisher) WriteESOInfra(repoDir string) error {
	infraDir := filepath.Join(repoDir, "gitops-output", "_infra")
	content := buildESOStoresYAML(p.cfg.Branding)
	return p.writeFile(filepath.Join(infraDir, "eso-stores.yaml"), []byte(content))
}

// WriteUpperLevelExternalSecrets writes ExternalSecret CRs to
// gitops-output/_infra/ for Org, Environment, and Project level secret refs.
//
// Each ExternalSecret lives in suparship-system so ESO creates the K8s Secret
// there; the K8s Secret is then replicated to app namespaces via Stakater
// Replicator annotations (added by UpperLevelEnvWriter on the Secret).
//
// Naming convention: eso-secrets-{level}-{provider}.yaml
// e.g. eso-secrets-org-k8s.yaml, eso-secrets-env-staging-k8s.yaml
func (p *Publisher) WriteUpperLevelExternalSecrets(repoDir string, level string, refs []envconfig.SecretRef) error {
	if len(refs) == 0 {
		return nil
	}
	infraDir := filepath.Join(repoDir, "gitops-output", "_infra")

	// Group refs by provider.
	byProvider := make(map[string][]envconfig.SecretRef)
	for _, ref := range refs {
		byProvider[ref.Provider] = append(byProvider[ref.Provider], ref)
	}

	for provider, providerRefs := range byProvider {
		storeName, ok := ESOStoreNames[provider]
		if !ok {
			return fmt.Errorf("unknown secret provider %q for level %q", provider, level)
		}
		secretName := fmt.Sprintf("suparship-secrets-%s-%s", level, provider)
		content := buildExternalSecretYAML(secretName, envconfig.SystemNamespace, storeName, providerRefs, p.cfg.Branding)
		fileName := fmt.Sprintf("eso-secrets-%s-%s.yaml", level, provider)
		if err := p.writeFile(filepath.Join(infraDir, fileName), []byte(content)); err != nil {
			return fmt.Errorf("writing ExternalSecret %s: %w", fileName, err)
		}
	}
	return nil
}

// buildESOStoresYAML returns a YAML string containing the ClusterSecretStore
// for the Kubernetes backend. Vault and AWS SM stores are omitted because the
// v1 API requires a valid provider config — users should create them manually
// when their backend is ready.
func buildESOStoresYAML(brand branding.Config) string {
	return buildK8sClusterSecretStore(brand)
}

// buildK8sClusterSecretStore returns the YAML for the demo Kubernetes backend
// ClusterSecretStore. It reads K8s Secrets from suparship-system so that users
// can create real Secrets there and reference them from any level's SecretRefs.
func buildK8sClusterSecretStore(brand branding.Config) string {
	return fmt.Sprintf(`apiVersion: external-secrets.io/v1
kind: ClusterSecretStore
metadata:
  name: suparship-k8s-store
  labels:
%s
  annotations:
    %s/description: "Kubernetes Secrets backend for demo/default use. Reads Secrets from suparship-system namespace."
spec:
  provider:
    kubernetes:
      remoteNamespace: suparship-system
      auth:
        serviceAccount:`,
		branding.LabelsYAML(brand.ManagedByLabels(), 4),
		brand.EffectiveLabelDomain(),
	) + `
          name: suparship-eso-reader
          namespace: suparship-system
`
}

// buildExternalSecretYAML returns the YAML for a single ExternalSecret CR
// that pulls a set of secret refs into a K8s Secret in the given namespace.
func buildExternalSecretYAML(secretName, namespace, storeName string, refs []envconfig.SecretRef, brand branding.Config) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: %s
  namespace: %s
  labels:
%s
  annotations:
    # Stakater Replicator will copy the resulting K8s Secret to app namespaces.
    # The replication target annotation is added to the Secret by UpperLevelEnvWriter.
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: %s
    kind: ClusterSecretStore
  target:
    name: %s
    creationPolicy: Owner
  data:
`, secretName, namespace, branding.LabelsYAML(brand.ManagedByLabels(), 4), storeName, secretName))

	for _, ref := range refs {
		sb.WriteString(fmt.Sprintf(`  - secretKey: %s
    remoteRef:
      key: %s
      property: %s
`, ref.EnvKey, ref.Name, ref.Key))
	}
	return sb.String()
}
