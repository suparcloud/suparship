package gitops

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/suparcloud/suparship/internal/secrets"
)

// ESOSecretStoreConfig captures the info needed to render one ClusterSecretStore.
type ESOSecretStoreConfig struct {
	Name        string // rendered from ResourceNaming.ClusterSecretStore
	Binding     secrets.EnvBinding
	BackendType secrets.BackendType
	// ESONamespace is the namespace where the auth Secret (sealed Connect token)
	// lives on the target cluster. Defaults to "external-secrets" when empty.
	ESONamespace string
	// ConnectEndpoint is the in-cluster URL of the 1Password Connect server
	// (e.g. http://onepassword-connect.1password.svc.cluster.local:8080).
	// Defaults to DefaultConnectEndpoint when empty.
	ConnectEndpoint string
}

// ESOExternalSecretConfig captures the info needed to render one collapsed
// ExternalSecret per app-env namespace.
type ESOExternalSecretConfig struct {
	Name      string // rendered from ResourceNaming.AppResource
	Namespace string // app-env namespace
	StoreName string // ClusterSecretStore name
	// ItemKeys are the vault item titles in precedence order (org, env-type,
	// project, app, app-env). Only scopes that actually have keys are included.
	ItemKeys []string
}

// BuildClusterSecretStoreYAML renders a ClusterSecretStore from cfg.
func BuildClusterSecretStoreYAML(cfg ESOSecretStoreConfig) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`apiVersion: external-secrets.io/v1
kind: ClusterSecretStore
metadata:
  name: %s
  labels:
    app.kubernetes.io/managed-by: suparship
spec:
  provider:
`, cfg.Name))

	switch cfg.BackendType {
	case secrets.BackendK8s:
		sb.WriteString(fmt.Sprintf(`    kubernetes:
      remoteNamespace: suparship-system
      auth:
        serviceAccount:
          name: suparship-eso-reader
          namespace: suparship-system
`))
	case secrets.Backend1Password:
		authName := secrets.ConnectTokenSecretName(cfg.Binding.Env)
		esoNS := cfg.ESONamespace
		if esoNS == "" {
			esoNS = "external-secrets"
		}
		connectEndpoint := cfg.ConnectEndpoint
		if connectEndpoint == "" {
			connectEndpoint = DefaultConnectEndpoint
		}
		sb.WriteString(fmt.Sprintf(`    onepassword:
      connectHost: %s
      vaults:
        %s: 1
      auth:
        secretRef:
          connectTokenSecretRef:
            name: %s
            key: %s
            namespace: %s
`,
			connectEndpoint,
			cfg.Binding.VaultID,
			authName,
			secrets.SATokenSecretKey,
			esoNS,
		))
	default:
		sb.WriteString(fmt.Sprintf("    # %s provider — configure manually\n", cfg.BackendType))
	}

	return sb.String()
}

// BuildCollapsedExternalSecretYAML renders a single ExternalSecret that merges
// all inherited scope items via dataFrom.extract in precedence order.
func BuildCollapsedExternalSecretYAML(cfg ESOExternalSecretConfig) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/managed-by: suparship
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: %s
    kind: ClusterSecretStore
  target:
    name: %s
    creationPolicy: Owner
  dataFrom:
`, cfg.Name, cfg.Namespace, cfg.StoreName, cfg.Name))

	for _, key := range cfg.ItemKeys {
		sb.WriteString(fmt.Sprintf("  - extract:\n      key: %q\n", key))
	}

	return sb.String()
}

// BuildSecretStoresForConfig generates ClusterSecretStore YAML for all bindings
// in the backend config.
func BuildSecretStoresForConfig(
	cfg secrets.BackendConfig,
	naming secrets.ResourceNaming,
	orgName string,
) []ESOSecretStoreConfig {
	if cfg.Effective() == secrets.BackendK8s {
		name := naming.RenderClusterSecretStore(secrets.NamingParams{
			Provider: string(secrets.BackendK8s),
			Env:      "default",
			Org:      orgName,
		})
		return []ESOSecretStoreConfig{{
			Name:        name,
			BackendType: secrets.BackendK8s,
		}}
	}

	if cfg.OnePassword == nil {
		return nil
	}
	var stores []ESOSecretStoreConfig
	for _, b := range cfg.OnePassword.Bindings {
		if !b.Provisioned {
			continue
		}
		name := naming.RenderClusterSecretStore(secrets.NamingParams{
			Provider: string(cfg.Effective()),
			Env:      b.Env,
			Org:      orgName,
		})
		stores = append(stores, ESOSecretStoreConfig{
			Name:        name,
			Binding:     b,
			BackendType: cfg.Effective(),
		})
	}
	return stores
}

// AppEnvPublishParams captures the info needed to generate one ExternalSecret.
type AppEnvPublishParams struct {
	Project   string
	App       string
	Env       string
	Namespace string
	// ScopeKeys maps scope level names to whether keys exist at that level.
	// Only scopes with keys get a dataFrom entry.
	ScopeKeys map[string]bool
}

// BuildCollapsedExternalSecretForApp generates the ExternalSecret config
// for one app-env namespace using the naming patterns.
func BuildCollapsedExternalSecretForApp(
	params AppEnvPublishParams,
	naming secrets.ResourceNaming,
	cfg secrets.BackendConfig,
	orgName string,
) *ESOExternalSecretConfig {
	np := secrets.NamingParams{
		Org:      orgName,
		Env:      params.Env,
		Project:  params.Project,
		App:      params.App,
		Provider: string(cfg.Effective()),
	}

	resourceName := naming.RenderAppResource(np)
	storeName := naming.RenderClusterSecretStore(np)

	levels := []string{
		secrets.LevelOrg,
		secrets.LevelEnvironment,
		secrets.LevelProject,
		secrets.LevelApp,
		secrets.LevelAppEnv,
	}

	var itemKeys []string
	for _, level := range levels {
		if params.ScopeKeys != nil && !params.ScopeKeys[level] {
			continue
		}
		itemKeys = append(itemKeys, naming.RenderVaultItem(level, np))
	}

	if len(itemKeys) == 0 {
		return nil
	}

	return &ESOExternalSecretConfig{
		Name:      resourceName,
		Namespace: params.Namespace,
		StoreName: storeName,
		ItemKeys:  itemKeys,
	}
}

// WriteSecretStores writes ClusterSecretStore YAML files to
// gitops-output/_infra/secret-stores/.
func (p *Publisher) WriteSecretStores(repoDir string, stores []ESOSecretStoreConfig) error {
	storeDir := filepath.Join(repoDir, "gitops-output", "_infra", "secret-stores")

	sort.Slice(stores, func(i, j int) bool {
		return stores[i].Name < stores[j].Name
	})

	for _, s := range stores {
		content := BuildClusterSecretStoreYAML(s)
		filename := filepath.Join(storeDir, s.Name+".yaml")
		if err := p.writeFile(filename, []byte(content)); err != nil {
			return fmt.Errorf("writing ClusterSecretStore %s: %w", s.Name, err)
		}
	}
	return nil
}

// WriteCollapsedExternalSecret writes a single ExternalSecret YAML to
// gitops-output/{project}/{app}/{env}/external-secret.yaml.
func (p *Publisher) WriteCollapsedExternalSecret(repoDir string, cfg ESOExternalSecretConfig) error {
	dir := filepath.Join(repoDir, "gitops-output")
	parts := strings.Split(cfg.Namespace, "-")
	if len(parts) >= 3 {
		dir = filepath.Join(dir, parts[0], parts[1], strings.Join(parts[2:], "-"))
	} else {
		dir = filepath.Join(dir, cfg.Namespace)
	}
	content := BuildCollapsedExternalSecretYAML(cfg)
	return p.writeFile(filepath.Join(dir, "external-secret.yaml"), []byte(content))
}
