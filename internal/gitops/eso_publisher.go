package gitops

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/suparcloud/suparship/internal/branding"
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
	// PlatformVaultID is an additional 1Password vault ID to include in the
	// store's `vaults:` map so org/project items kept in the platform-shared
	// vault are resolvable from the same store. Ignored for non-1Password
	// backends and when empty.
	PlatformVaultID string
	// Branding stamps the platform identity into the generated labels.
	// Zero value applies "suparship" / "suparship.io" defaults.
	Branding branding.Config
}

// ESOItemRef is one dataFrom.extract entry in the collapsed ExternalSecret.
// Carries both the vault item title and the ClusterSecretStore that backs it
// so a single ExternalSecret can pull from multiple stores (e.g. the platform
// vault for org/project items and the per-env vault for env-type/cluster/app/
// app-env items).
type ESOItemRef struct {
	// Key is the vault item title (e.g. "org", "{project}", "{project}-{app}-{env}").
	Key string
	// StoreName is the ClusterSecretStore name to read this item from. When
	// empty, BuildCollapsedExternalSecretYAML falls back to the top-level
	// ESOExternalSecretConfig.StoreName.
	StoreName string
}

// ESOExternalSecretConfig captures the info needed to render one collapsed
// ExternalSecret per app-env namespace.
type ESOExternalSecretConfig struct {
	Name      string // rendered from ResourceNaming.AppResource
	Namespace string // app-env namespace
	// StoreName is the default ClusterSecretStore for items that don't carry
	// their own StoreName. Kept as a fallback for single-store backends and
	// to satisfy the ExternalSecret CRD's required spec.secretStoreRef.
	StoreName string
	// Items lists the vault items to pull, in precedence order (org first,
	// most-specific last — Kubernetes ESO applies dataFrom in order, later
	// entries overwriting earlier ones for duplicate keys). Only scopes that
	// actually have keys are included.
	Items []ESOItemRef
	// Branding stamps the platform identity into the generated labels.
	// Zero value applies "suparship" / "suparship.io" defaults.
	Branding branding.Config
}

// BuildClusterSecretStoreYAML renders a ClusterSecretStore from cfg.
func BuildClusterSecretStoreYAML(cfg ESOSecretStoreConfig) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`apiVersion: external-secrets.io/v1
kind: ClusterSecretStore
metadata:
  name: %s
  labels:
%s
spec:
  provider:
`, cfg.Name, branding.LabelsYAML(cfg.Branding.ManagedByLabels(), 4)))

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
`,
			connectEndpoint,
			cfg.Binding.VaultID,
		))
		// Include the platform-shared vault when configured so org/project
		// items can be resolved from the same store. Lower priority (2) so
		// per-env items still win when titles collide.
		if cfg.PlatformVaultID != "" && cfg.PlatformVaultID != cfg.Binding.VaultID {
			sb.WriteString(fmt.Sprintf("        %s: 2\n", cfg.PlatformVaultID))
		}
		sb.WriteString(fmt.Sprintf(`      auth:
        secretRef:
          connectTokenSecretRef:
            name: %s
            key: %s
            namespace: %s
`,
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
// all inherited scope items via dataFrom.extract in precedence order. Each
// extract entry can target its own ClusterSecretStore — used to pull org/
// project items from the platform-shared vault and env-type/cluster/app/
// app-env items from the per-env vault in the same Secret.
func BuildCollapsedExternalSecretYAML(cfg ESOExternalSecretConfig) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`apiVersion: external-secrets.io/v1
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
  dataFrom:
`, cfg.Name, cfg.Namespace, branding.LabelsYAML(cfg.Branding.ManagedByLabels(), 4), cfg.StoreName, cfg.Name))

	for _, item := range cfg.Items {
		sb.WriteString(fmt.Sprintf("  - extract:\n      key: %q\n", item.Key))
		// Per-entry storeRef overrides the top-level secretStoreRef. Only
		// emit when it differs so single-store output stays terse.
		if item.StoreName != "" && item.StoreName != cfg.StoreName {
			sb.WriteString(fmt.Sprintf("    sourceRef:\n      storeRef:\n        name: %s\n        kind: ClusterSecretStore\n", item.StoreName))
		}
	}

	return sb.String()
}

// BuildSecretStoresForConfig generates ClusterSecretStore YAML for all bindings
// in the backend config.
func BuildSecretStoresForConfig(
	cfg secrets.BackendConfig,
	naming secrets.ResourceNaming,
	orgName string,
	brand branding.Config,
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
			Branding:    brand,
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
			Name:            name,
			Binding:         b,
			BackendType:     cfg.Effective(),
			PlatformVaultID: cfg.OnePassword.PlatformVaultID,
			Branding:        brand,
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
	// Cluster is the registered cluster name bound to Env. Used to render the
	// cluster-scope vault item title. Empty when the env is unbound — the
	// cluster scope is then skipped regardless of ScopeKeys.
	Cluster string
	// ScopeKeys maps scope level names to whether keys exist at that level.
	// Only scopes with keys get a dataFrom entry.
	ScopeKeys map[string]bool
	// PlatformStoreName is the ClusterSecretStore that backs the platform
	// (org-shared) vault. When set, org and project entries route to this
	// store instead of the env store. Leave empty for single-store backends
	// (K8s, or 1Password without a platform vault provisioned).
	PlatformStoreName string
}

// BuildCollapsedExternalSecretForApp generates the ExternalSecret config
// for one app-env namespace using the naming patterns.
//
// Precedence order (later wins): org → env-type → project → app → app-env →
// cluster. Org and project route to PlatformStoreName when set; everything
// else routes to the env's ClusterSecretStore.
func BuildCollapsedExternalSecretForApp(
	params AppEnvPublishParams,
	naming secrets.ResourceNaming,
	cfg secrets.BackendConfig,
	orgName string,
	brand branding.Config,
) *ESOExternalSecretConfig {
	np := secrets.NamingParams{
		Org:      orgName,
		Env:      params.Env,
		Project:  params.Project,
		App:      params.App,
		Cluster:  params.Cluster,
		Provider: string(cfg.Effective()),
	}

	resourceName := naming.RenderAppResource(np)
	envStoreName := naming.RenderClusterSecretStore(np)

	// platformStoreFor returns the store that backs scopes shared across all
	// clusters (org, project). Falls back to the env store when no platform
	// store is provisioned — single-store backends keep working unchanged.
	platformStoreFor := func() string {
		if params.PlatformStoreName != "" {
			return params.PlatformStoreName
		}
		return envStoreName
	}

	type levelEntry struct {
		level string
		store string
	}
	ordered := []levelEntry{
		{secrets.LevelOrg, platformStoreFor()},
		{secrets.LevelEnvironment, envStoreName},
		{secrets.LevelProject, platformStoreFor()},
		{secrets.LevelApp, envStoreName},
		{secrets.LevelAppEnv, envStoreName},
		{secrets.LevelCluster, envStoreName},
	}

	var items []ESOItemRef
	for _, le := range ordered {
		if le.level == secrets.LevelCluster && params.Cluster == "" {
			continue
		}
		if params.ScopeKeys != nil && !params.ScopeKeys[le.level] {
			continue
		}
		items = append(items, ESOItemRef{
			Key:       naming.RenderVaultItem(le.level, np),
			StoreName: le.store,
		})
	}

	if len(items) == 0 {
		return nil
	}

	return &ESOExternalSecretConfig{
		Name:      resourceName,
		Namespace: params.Namespace,
		StoreName: envStoreName,
		Items:     items,
		Branding:  brand,
	}
}

// WriteSecretStores writes ClusterSecretStore YAML files to
// gitops-output/_infra/secret-stores/.
func (p *Publisher) WriteSecretStores(repoDir string, stores []ESOSecretStoreConfig) error {
	storeDir := p.outputDir(repoDir, "_infra", "secret-stores")

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
	parts := strings.Split(cfg.Namespace, "-")
	var dir string
	if len(parts) >= 3 {
		dir = p.outputDir(repoDir, parts[0], parts[1], strings.Join(parts[2:], "-"))
	} else {
		dir = p.outputDir(repoDir, cfg.Namespace)
	}
	content := BuildCollapsedExternalSecretYAML(cfg)
	return p.writeFile(filepath.Join(dir, "external-secret.yaml"), []byte(content))
}

// BuildAppConfigMapYAML renders a ConfigMap YAML for non-secret env vars.
// vars may be nil or empty — in that case an empty-data ConfigMap is written
// so ArgoCD can always resolve the envFrom reference without errors.
//
// brand stamps the platform identity onto the ConfigMap labels. Zero value
// applies "suparship" defaults so existing callers remain unchanged.
func BuildAppConfigMapYAML(name, namespace string, vars map[string]string, brand branding.Config) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: %s
  labels:
%s
data:
`, name, namespace, branding.LabelsYAML(brand.ManagedByLabels(), 4)))

	if len(vars) == 0 {
		sb.WriteString("  {}\n")
		return sb.String()
	}

	// Sort keys for deterministic output.
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sb.WriteString(fmt.Sprintf("  %s: %q\n", k, vars[k]))
	}
	return sb.String()
}

// BuildAppKustomizationYAML returns a kustomization.yaml that lists the
// per-app platform manifests. Picked up automatically by ArgoCD's
// directory source — when this file is present in the dir, ArgoCD
// switches to kustomize mode and applies only what's listed in
// resources, ignoring app.yaml + values.yaml (which would otherwise be
// mistaken for k8s manifests).
//
// Regenerated on every publish; operator extensions to the kustomization
// are clobbered. Operators needing extra manifests should drop them in
// the same dir AND list them in the regenerated file (or — better — use
// a separate ArgoCD Application that overlays this one). See
// gitops-output/README.md for the take-over recipes.
//
// The order of resources matches what kustomize will emit; sorting is
// the caller's job (BuildAppKustomizationYAML preserves insertion order
// so the publisher controls it).
func BuildAppKustomizationYAML(resources []string) string {
	var sb strings.Builder
	sb.WriteString("apiVersion: kustomize.config.k8s.io/v1beta1\n")
	sb.WriteString("kind: Kustomization\n")
	sb.WriteString("resources:\n")
	for _, r := range resources {
		sb.WriteString("  - ")
		sb.WriteString(r)
		sb.WriteString("\n")
	}
	return sb.String()
}

// WriteAppConfigMap writes env-configmap.yaml to dir.
// dir should be the per-app-env directory, e.g.
// gitops-output/{envName}/{project}/{app}/ or
// gitops-output/previews/{project}/{previewName}/.
func (p *Publisher) WriteAppConfigMap(dir, name, namespace string, vars map[string]string) error {
	content := BuildAppConfigMapYAML(name, namespace, vars, p.cfg.Branding)
	return p.writeFile(filepath.Join(dir, "env-configmap.yaml"), []byte(content))
}
