package gitops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/suparcloud/suparship/internal/branding"
	"github.com/suparcloud/suparship/internal/secrets"
)

// DefaultConnectEndpoint is the in-cluster URL of the managed 1Password
// Connect server ESO reads from.
const DefaultConnectEndpoint = "http://onepassword-connect." + secrets.DefaultConnectNamespace + ".svc.cluster.local:8080"

// ESOSecretStoreConfig captures the info needed to render one
// ClusterSecretStore — one per scope/vault (global, an env, a cluster).
type ESOSecretStoreConfig struct {
	// Scope identifies which vault this store reads from.
	Scope secrets.Scope
	// BackendType selects the provider stanza (k8s vs 1Password).
	BackendType secrets.BackendType
	// VaultID is the 1Password vault UUID for this scope. Ignored for k8s.
	VaultID string
	// ESONamespace is the namespace on the target cluster where the sealed
	// Connect-token Secret lives. Defaults to "external-secrets" when empty.
	ESONamespace string
	// ConnectEndpoint is the in-cluster 1Password Connect URL. Defaults to
	// DefaultConnectEndpoint when empty.
	ConnectEndpoint string
	// Branding stamps platform identity into the generated labels.
	Branding branding.Config
}

// Name returns the ClusterSecretStore resource name for this store's scope.
func (c ESOSecretStoreConfig) Name() string { return secrets.StoreName(c.Scope) }

// ESOExternalSecretConfig captures one ExternalSecret — one per (scope, app).
// It materializes the <app>-global/-env/-cluster Secret in the app namespace
// by extracting the shared item then the app item (app keys win) from the
// scope's single vault.
type ESOExternalSecretConfig struct {
	Name      string   // WorkloadSecretName(scope, app)
	Namespace string   // app namespace
	StoreName string   // StoreName(scope)
	ItemKeys  []string // ordered [shared item, app item]; later wins
	Branding  branding.Config
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
`, cfg.Name(), branding.LabelsYAML(cfg.Branding.ManagedByLabels(), 4)))

	switch cfg.BackendType {
	case secrets.BackendK8s:
		// The "vault" is a namespace; ESO reads Secrets from it via the
		// suparship-eso-reader ServiceAccount in suparship-system.
		sb.WriteString(fmt.Sprintf(`    kubernetes:
      remoteNamespace: %s
      auth:
        serviceAccount:
          name: suparship-eso-reader
          namespace: suparship-system
`, secrets.VaultName(cfg.Scope)))
	case secrets.Backend1Password:
		esoNS := cfg.ESONamespace
		if esoNS == "" {
			esoNS = secrets.OnePasswordRemoteNamespace
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
			cfg.VaultID,
			secrets.ConnectTokenSecretName(cfg.Scope),
			secrets.SATokenSecretKey,
			esoNS,
		))
	default:
		sb.WriteString(fmt.Sprintf("    # %s provider — configure manually\n", cfg.BackendType))
	}

	return sb.String()
}

// BuildExternalSecretYAML renders one ExternalSecret. dataFrom.extract is
// applied in ItemKeys order, so listing the shared item before the app item
// makes the app's keys overwrite shared keys with the same name.
func BuildExternalSecretYAML(cfg ESOExternalSecretConfig) string {
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

	for _, key := range cfg.ItemKeys {
		sb.WriteString(fmt.Sprintf("  - extract:\n      key: %q\n", key))
	}
	return sb.String()
}

// BuildSecretStoresForConfig returns the full desired set of ClusterSecretStores
// to publish to _infra/secret-stores/ (synced to the tooling cluster): one
// global store plus one per environment and per cluster. Pass the complete
// env/cluster lists — WriteSecretStores prunes any store not returned.
//
// Only the k8s backend emits stores here: its "vault" is a namespace on the
// tooling cluster, so the stores belong there. 1Password stores are
// per-workload-cluster (they reference a sealed Connect-token Secret that only
// exists on the target cluster) and are published by PublishClusterSecretStores
// via the per-cluster sealing flow — emitting them here would target the wrong
// cluster.
func BuildSecretStoresForConfig(
	cfg secrets.BackendConfig,
	envNames []string,
	clusterNames []string,
	brand branding.Config,
) []ESOSecretStoreConfig {
	if cfg.Effective() != secrets.BackendK8s {
		return nil
	}
	scopes := []secrets.Scope{secrets.GlobalScope()}
	for _, e := range envNames {
		scopes = append(scopes, secrets.EnvScope(e))
	}
	for _, c := range clusterNames {
		scopes = append(scopes, secrets.ClusterScope(c))
	}

	out := make([]ESOSecretStoreConfig, 0, len(scopes))
	for _, scope := range scopes {
		out = append(out, ESOSecretStoreConfig{Scope: scope, BackendType: secrets.BackendK8s, Branding: brand})
	}
	return out
}

// ScopePresence reports which (scope, tier) items currently have at least one
// key, so the publisher only emits ExternalSecrets/dataFrom entries that
// resolve — ESO errors when asked to extract a non-existent vault item.
type ScopePresence struct {
	GlobalShared, GlobalApp   bool
	EnvShared, EnvApp         bool
	ClusterShared, ClusterApp bool
}

// WorkloadExternalSecretParams captures the per-app-env info for the three
// scope ExternalSecrets.
type WorkloadExternalSecretParams struct {
	App       string
	Namespace string
	Env       string
	// Cluster is the registered cluster bound to Env. Empty skips the cluster
	// scope regardless of presence.
	Cluster  string
	Presence ScopePresence
	Branding branding.Config
}

// BuildWorkloadExternalSecrets returns up to three ExternalSecret configs
// (global/env/cluster) for one app-env namespace. A scope is included only
// when at least one of its tiers has keys; within a scope the shared item is
// listed before the app item so app keys win.
func BuildWorkloadExternalSecrets(p WorkloadExternalSecretParams) []ESOExternalSecretConfig {
	var out []ESOExternalSecretConfig

	add := func(scope secrets.Scope, shared, app bool) {
		var keys []string
		if shared {
			keys = append(keys, secrets.SharedItemName(scope))
		}
		if app {
			keys = append(keys, secrets.AppItemName(scope, p.App))
		}
		if len(keys) == 0 {
			return
		}
		out = append(out, ESOExternalSecretConfig{
			Name:      secrets.WorkloadSecretName(scope, p.App),
			Namespace: p.Namespace,
			StoreName: secrets.StoreName(scope),
			ItemKeys:  keys,
			Branding:  p.Branding,
		})
	}

	add(secrets.GlobalScope(), p.Presence.GlobalShared, p.Presence.GlobalApp)
	add(secrets.EnvScope(p.Env), p.Presence.EnvShared, p.Presence.EnvApp)
	if p.Cluster != "" {
		add(secrets.ClusterScope(p.Cluster), p.Presence.ClusterShared, p.Presence.ClusterApp)
	}
	return out
}

// PublishSecretStores clones the gitops repo, writes the full desired set of
// ClusterSecretStores (pruning stale ones), and commits + pushes. Idempotent —
// no commit is produced when nothing changed. Called by the env/cluster
// lifecycle hooks so ESO stores exist before app ExternalSecrets reference them.
func (p *Publisher) PublishSecretStores(ctx context.Context, stores []ESOSecretStoreConfig) error {
	return p.withClonedRepo(ctx, func(repoDir string) error {
		if err := p.WriteSecretStores(repoDir, stores); err != nil {
			return err
		}
		return p.commitAndPush(ctx, repoDir, "feat(secrets): reconcile ClusterSecretStores")
	})
}

// WriteSecretStores writes ClusterSecretStore YAML files to
// _infra/secret-stores/ and prunes stale entries no longer in the desired set,
// so removing an env/cluster (or switching backend) cleanly drops its store
// from the repo and lets ArgoCD prune it.
//
// Ownership convention: _infra/secret-stores/ is owned by this writer.
func (p *Publisher) WriteSecretStores(repoDir string, stores []ESOSecretStoreConfig) error {
	storeDir := p.outputDir(repoDir, "_infra", "secret-stores")

	sort.Slice(stores, func(i, j int) bool { return stores[i].Name() < stores[j].Name() })

	wanted := make(map[string]bool, len(stores))
	for _, s := range stores {
		wanted[s.Name()+".yaml"] = true
		content := BuildClusterSecretStoreYAML(s)
		if err := p.writeFile(filepath.Join(storeDir, s.Name()+".yaml"), []byte(content)); err != nil {
			return fmt.Errorf("writing ClusterSecretStore %s: %w", s.Name(), err)
		}
	}

	entries, err := os.ReadDir(storeDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read _infra/secret-stores: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") || wanted[name] {
			continue
		}
		if err := os.Remove(filepath.Join(storeDir, name)); err != nil {
			return fmt.Errorf("prune stale ClusterSecretStore %s: %w", name, err)
		}
	}
	return nil
}

// WriteWorkloadExternalSecrets writes the per-scope ExternalSecret YAML files
// (external-secret-global.yaml / -env.yaml / -cluster.yaml) into dir, and
// prunes any scope file no longer wanted (e.g. all keys removed at a scope).
func (p *Publisher) WriteWorkloadExternalSecrets(dir string, configs []ESOExternalSecretConfig) error {
	// Map each config to its scope-suffixed filename via the target secret name.
	wanted := map[string]bool{}
	for _, cfg := range configs {
		var suffix string
		switch {
		case strings.HasSuffix(cfg.Name, "-global"):
			suffix = "global"
		case strings.HasSuffix(cfg.Name, "-cluster"):
			suffix = "cluster"
		default:
			suffix = "env"
		}
		fname := "external-secret-" + suffix + ".yaml"
		wanted[fname] = true
		if err := p.writeFile(filepath.Join(dir, fname), []byte(BuildExternalSecretYAML(cfg))); err != nil {
			return err
		}
	}
	// Prune scope files that are no longer wanted.
	for _, suffix := range []string{"global", "env", "cluster"} {
		fname := "external-secret-" + suffix + ".yaml"
		if wanted[fname] {
			continue
		}
		path := filepath.Join(dir, fname)
		if _, err := os.Stat(path); err == nil {
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("prune stale ExternalSecret %s: %w", fname, err)
			}
		}
	}
	return nil
}

// BuildAppConfigMapYAML renders a ConfigMap YAML for non-secret env vars.
// vars may be nil or empty — an empty-data ConfigMap is written so ArgoCD can
// always resolve the envFrom reference.
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

// WriteAppConfigMap writes env-configmap.yaml to dir.
func (p *Publisher) WriteAppConfigMap(dir, name, namespace string, vars map[string]string) error {
	content := BuildAppConfigMapYAML(name, namespace, vars, p.cfg.Branding)
	return p.writeFile(filepath.Join(dir, "env-configmap.yaml"), []byte(content))
}
