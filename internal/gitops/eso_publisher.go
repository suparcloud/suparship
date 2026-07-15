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

// ESOSecretStoreConfig captures the info needed to render one per-vault
// ClusterSecretStore (k8s backend: one store per vault/namespace).
type ESOSecretStoreConfig struct {
	// Scope identifies which vault this store reads from.
	Scope secrets.Scope
	// BackendType selects the provider stanza. Only the k8s backend renders
	// per-vault stores — the 1Password backend uses the unified per-cluster
	// store (BuildUnifiedClusterSecretStoreYAML).
	BackendType secrets.BackendType
	// Branding stamps platform identity into the generated labels.
	Branding branding.Config
}

// UnifiedStoreConfig captures the single per-cluster ClusterSecretStore for
// the 1Password backend: fixed name (secrets.UnifiedStoreName), one Connect
// token, and the full list of vaults the cluster reads.
type UnifiedStoreConfig struct {
	// VaultIDs are the 1Password vault UUIDs (global first, then env vaults).
	// Rendered into the provider's vaults map in order (1-based).
	VaultIDs []string
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

// ESOItemRef is one dataFrom.extract entry: a vault item plus the
// ClusterSecretStore it is read from. When StoreName differs from the
// ExternalSecret's default store, a per-entry sourceRef is emitted so one
// ExternalSecret can merge items from several stores (the per-scope stores).
type ESOItemRef struct {
	Key       string
	StoreName string
}

// ESODataRef is one data[] entry: a single key selected (and renamed) from a
// vault item. SecretKey is the key in the target Secret; Property is the key
// within the vault item ItemKey. StoreName is the item's ClusterSecretStore (a
// per-entry sourceRef is emitted when it differs from the ExternalSecret default).
type ESODataRef struct {
	SecretKey string
	ItemKey   string
	Property  string
	StoreName string
}

// ESOExternalSecretConfig captures the single ExternalSecret per app-env. It
// materializes one merged Secret (<app>-secrets) by extracting every present
// scope/tier item in precedence order (later overwrites earlier).
type ESOExternalSecretConfig struct {
	Name      string // AppSecretName(app)
	Namespace string // app namespace
	StoreName string // default ClusterSecretStore (global store)
	Items     []ESOItemRef // dataFrom (whole-item) mode — the app-wide secret
	// Data is the data[] (per-key select/rename) mode — the per-component secret
	// projection. Mutually exclusive with Items.
	Data     []ESODataRef
	Branding branding.Config
	// RefreshInterval is the ExternalSecret spec.refreshInterval (org-configured).
	// Empty falls back to secrets.DefaultRefreshInterval.
	RefreshInterval string
}

// BuildClusterSecretStoreYAML renders one per-vault ClusterSecretStore
// (k8s backend).
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
	default:
		sb.WriteString(fmt.Sprintf("    # %s provider — configure manually\n", cfg.BackendType))
	}

	return sb.String()
}

// BuildUnifiedClusterSecretStoreYAML renders the single per-cluster
// ClusterSecretStore for the 1Password backend. The fixed name
// (secrets.UnifiedStoreName) is identical on every cluster, the vaults map
// lists every vault the cluster reads (lookup order is cosmetic — item names
// are scope-unique), and auth references the cluster's one sealed Connect
// token (secrets.ConnectTokenSecretName).
func BuildUnifiedClusterSecretStoreYAML(cfg UnifiedStoreConfig) string {
	esoNS := cfg.ESONamespace
	if esoNS == "" {
		esoNS = secrets.OnePasswordRemoteNamespace
	}
	connectEndpoint := cfg.ConnectEndpoint
	if connectEndpoint == "" {
		connectEndpoint = DefaultConnectEndpoint
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`apiVersion: external-secrets.io/v1
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
`, secrets.UnifiedStoreName(), branding.LabelsYAML(cfg.Branding.ManagedByLabels(), 4), connectEndpoint))
	for i, id := range cfg.VaultIDs {
		sb.WriteString(fmt.Sprintf("        %s: %d\n", id, i+1))
	}
	sb.WriteString(fmt.Sprintf(`      auth:
        secretRef:
          connectTokenSecretRef:
            name: %s
            key: %s
            namespace: %s
`, secrets.ConnectTokenSecretName, secrets.SATokenSecretKey, esoNS))
	return sb.String()
}

// BuildExternalSecretYAML renders the single merged ExternalSecret. dataFrom
// is applied in Items order (later overwrites earlier), and each item emits a
// per-entry sourceRef.storeRef when its store differs from the default
// secretStoreRef — letting one ExternalSecret pull from the global, env, and
// cluster ClusterSecretStores into one target Secret.
func BuildExternalSecretYAML(cfg ESOExternalSecretConfig) string {
	refreshInterval := cfg.RefreshInterval
	if strings.TrimSpace(refreshInterval) == "" {
		refreshInterval = secrets.DefaultRefreshInterval
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: %s
  namespace: %s
  labels:
%s
spec:
  refreshInterval: %s
  secretStoreRef:
    name: %s
    kind: ClusterSecretStore
  target:
    name: %s
    creationPolicy: Owner
`, cfg.Name, cfg.Namespace, branding.LabelsYAML(cfg.Branding.ManagedByLabels(), 4), refreshInterval, cfg.StoreName, cfg.Name))

	if len(cfg.Data) > 0 {
		// Per-key select/rename (the per-component projection).
		sb.WriteString("  data:\n")
		for _, d := range cfg.Data {
			sb.WriteString(fmt.Sprintf("  - secretKey: %q\n    remoteRef:\n      key: %q\n      property: %q\n", d.SecretKey, d.ItemKey, d.Property))
			if d.StoreName != "" && d.StoreName != cfg.StoreName {
				sb.WriteString(fmt.Sprintf("    sourceRef:\n      storeRef:\n        name: %s\n        kind: ClusterSecretStore\n", d.StoreName))
			}
		}
		return sb.String()
	}

	sb.WriteString("  dataFrom:\n")
	for _, item := range cfg.Items {
		sb.WriteString(fmt.Sprintf("  - extract:\n      key: %q\n", item.Key))
		// Per-entry storeRef overrides the top-level secretStoreRef. Only emit
		// when it differs so single-store output stays terse.
		if item.StoreName != "" && item.StoreName != cfg.StoreName {
			sb.WriteString(fmt.Sprintf("    sourceRef:\n      storeRef:\n        name: %s\n        kind: ClusterSecretStore\n", item.StoreName))
		}
	}
	return sb.String()
}

// BuildSecretStoresForConfig returns the full desired set of ClusterSecretStores
// to publish to _infra/secret-stores/ (synced to the tooling cluster): one
// global store plus one per environment. Cluster-override items live inside the
// env vault, so clusters get no store of their own. Pass the complete env list
// — WriteSecretStores prunes any store not returned.
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
	brand branding.Config,
) []ESOSecretStoreConfig {
	if cfg.Effective() != secrets.BackendK8s {
		return nil
	}
	scopes := []secrets.Scope{secrets.GlobalScope()}
	for _, e := range envNames {
		scopes = append(scopes, secrets.EnvScope(e))
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
	// ProjectShared / ProjectEnvShared mark project-scope secrets (shared by
	// every app in the project) present in the global / env vaults.
	ProjectShared, ProjectEnvShared bool
	// StackShared / StackEnvShared mark stack-scope secrets (shared by every app
	// in the stack) present in the global / env vaults.
	StackShared, StackEnvShared bool
	// PreviewShared / PreviewApp mark the per-app preview band (applied to every
	// preview on top of the base env). PreviewPRShared / PreviewPRApp mark a
	// single preview's (PR's) override. All live in the base env vault.
	PreviewShared, PreviewApp     bool
	PreviewPRShared, PreviewPRApp bool
}

// ScopeSecretKeys mirrors ScopePresence but carries the actual key NAMES in each
// (scope, tier) item, so a per-component data[] projection can resolve a selected
// key to the right vault item. Populated only when a component curates secrets;
// the plain <app>-secrets path leaves it zero (dataFrom needs no key names).
type ScopeSecretKeys struct {
	GlobalShared, GlobalApp         []string
	EnvShared, EnvApp               []string
	ClusterShared, ClusterApp       []string
	ProjectShared, ProjectEnvShared []string
	StackShared, StackEnvShared     []string
	PreviewShared, PreviewApp       []string
	PreviewPRShared, PreviewPRApp   []string
}

// WorkloadExternalSecretParams captures the per-app-env info for the single
// merged ExternalSecret.
type WorkloadExternalSecretParams struct {
	App       string
	Namespace string
	Env       string
	// Project is the app's project, used to locate project-scope shared items.
	// Empty skips the project scopes regardless of presence.
	Project string
	// Stack is the app's stack (if any), used to locate stack-scope shared items.
	// Empty skips the stack scopes regardless of presence.
	Stack string
	// Cluster is the registered cluster bound to Env. Empty skips the cluster
	// scope regardless of presence.
	Cluster  string
	Presence ScopePresence
	// IsPreview marks this as a preview ExternalSecret. Env then holds the base
	// env (whose vault/store the preview reuses) and PreviewName the preview's
	// name. The preview band + per-PR items are appended on top of the base env.
	IsPreview bool
	// PreviewName is the preview (PR) name, used to key the per-PR override item.
	// Only consulted when IsPreview is true.
	PreviewName string
	// UnifiedStore selects the 1Password layout: every item extracts from the
	// single per-cluster store (secrets.UnifiedStoreName), so no per-entry
	// sourceRef is emitted. False = k8s layout with per-vault stores.
	UnifiedStore bool
	Branding     branding.Config
	// RefreshInterval is the org-configured ExternalSecret refresh interval.
	// Empty falls back to secrets.DefaultRefreshInterval at render time.
	RefreshInterval string
	// SecretName overrides the ExternalSecret + target Secret name. Empty falls
	// back to AppSecretName(App). The vault item keys (remote refs) always use
	// App, so a shared-namespace preview can give its Secret a preview-suffixed
	// name while still reading the app's items.
	SecretName string
	// SecretKeys carries the key names per (scope, tier) item — only needed (and
	// populated) for a per-component data[] secret projection.
	SecretKeys ScopeSecretKeys
}

// BuildAppExternalSecret returns the single merged ExternalSecret config for an
// app-env, or nil when no scope has keys. dataFrom items are ordered by band
// (global → env → cluster) and within each band org-shared → project-shared →
// app, so the later (more specific) entry wins on a key collision. Project-
// shared items live in the global (project-global) / env (project-env) vault;
// cluster items live in the env vault and are included only when the env is
// bound to a cluster.
//
// Store wiring depends on the backend: with UnifiedStore (1Password) every
// item extracts from the single per-cluster store, so no sourceRef is emitted;
// otherwise (k8s) each item carries its scope's per-vault store and non-global
// items emit a per-entry sourceRef.
func BuildAppExternalSecret(p WorkloadExternalSecretParams) *ESOExternalSecretConfig {
	items := buildScopeItems(p)
	if len(items) == 0 {
		return nil
	}
	esName := p.SecretName
	if esName == "" {
		esName = secrets.AppSecretName(p.App)
	}
	refs := make([]ESOItemRef, len(items))
	for i, it := range items {
		refs[i] = it.ref
	}
	return &ESOExternalSecretConfig{
		Name:            esName,
		Namespace:       p.Namespace,
		StoreName:       storeForScope(p, secrets.GlobalScope()),
		Items:           refs,
		Branding:        p.Branding,
		RefreshInterval: p.RefreshInterval,
	}
}

// scopeItem is one vault item in precedence order: its dataFrom ref plus the key
// names it holds (populated from WorkloadExternalSecretParams.SecretKeys, used by
// the per-component data[] projection to resolve a selected key to its item).
type scopeItem struct {
	ref  ESOItemRef
	keys []string
}

func storeForScope(p WorkloadExternalSecretParams, scope secrets.Scope) string {
	if p.UnifiedStore {
		return secrets.UnifiedStoreName()
	}
	return secrets.StoreName(scope)
}

// buildScopeItems returns the present (scope, tier) vault items in precedence
// order (low → high) — the single source of truth for the app ExternalSecret's
// dataFrom order AND the per-component projection's key resolution.
func buildScopeItems(p WorkloadExternalSecretParams) []scopeItem {
	var items []scopeItem
	sharedItem := func(scope secrets.Scope, keys []string) {
		items = append(items, scopeItem{ref: ESOItemRef{Key: secrets.SharedItemName(scope), StoreName: storeForScope(p, scope)}, keys: keys})
	}
	appItem := func(scope secrets.Scope, keys []string) {
		// App-tier items are project-qualified (WithProject) so a same-named app
		// in another project never collides on the shared org/env vault. StoreName
		// keys on Kind only, so the un-tagged scope still selects the right store.
		items = append(items, scopeItem{ref: ESOItemRef{Key: secrets.AppItemName(scope.WithProject(p.Project), p.App), StoreName: storeForScope(p, scope)}, keys: keys})
	}
	hasProject := p.Project != ""
	hasStack := p.Stack != ""
	sk := p.SecretKeys

	// Global band: org-shared → project-global-shared → stack-global-shared → app.
	if p.Presence.GlobalShared {
		sharedItem(secrets.GlobalScope(), sk.GlobalShared)
	}
	if hasProject && p.Presence.ProjectShared {
		sharedItem(secrets.ProjectScope(p.Project), sk.ProjectShared)
	}
	if hasStack && p.Presence.StackShared {
		sharedItem(secrets.StackScope(p.Project, p.Stack), sk.StackShared)
	}
	if p.Presence.GlobalApp {
		appItem(secrets.GlobalScope(), sk.GlobalApp)
	}

	// Env band: org-shared → project-env-shared → stack-env-shared → app.
	if p.Presence.EnvShared {
		sharedItem(secrets.EnvScope(p.Env), sk.EnvShared)
	}
	if hasProject && p.Presence.ProjectEnvShared {
		sharedItem(secrets.ProjectEnvScope(p.Project, p.Env), sk.ProjectEnvShared)
	}
	if hasStack && p.Presence.StackEnvShared {
		sharedItem(secrets.StackEnvScope(p.Project, p.Stack, p.Env), sk.StackEnvShared)
	}
	if p.Presence.EnvApp {
		appItem(secrets.EnvScope(p.Env), sk.EnvApp)
	}

	// Cluster band: shared → app (highest precedence escape hatch).
	if p.Cluster != "" {
		cluster := secrets.ClusterScope(p.Env, p.Cluster)
		if p.Presence.ClusterShared {
			sharedItem(cluster, sk.ClusterShared)
		}
		if p.Presence.ClusterApp {
			appItem(cluster, sk.ClusterApp)
		}
	}

	// Preview bands: applied on top of the base env for previews only.
	if p.IsPreview {
		band := secrets.PreviewScope(p.Env)
		if p.Presence.PreviewShared {
			sharedItem(band, sk.PreviewShared)
		}
		if p.Presence.PreviewApp {
			appItem(band, sk.PreviewApp)
		}
		if p.PreviewName != "" {
			pr := secrets.PreviewPRScope(p.Env, p.PreviewName)
			if p.Presence.PreviewPRShared {
				sharedItem(pr, sk.PreviewPRShared)
			}
			if p.Presence.PreviewPRApp {
				appItem(pr, sk.PreviewPRApp)
			}
		}
	}
	return items
}

// BuildComponentExternalSecret returns the ExternalSecret for a component that
// curates a SUBSET of the app's secret keys (renames maps target-key → source-key
// in <app>-secrets). Each requested source key is resolved to the highest-
// precedence vault item that holds it (matching the merged <app>-secrets value)
// and emitted as a data[] entry with an optional per-item sourceRef. Returns nil
// when no requested key resolves (so no empty ExternalSecret is written).
func BuildComponentExternalSecret(p WorkloadExternalSecretParams, name string, renames map[string]string) *ESOExternalSecretConfig {
	items := buildScopeItems(p)
	// Resolve each key against items from highest precedence (last) to lowest.
	resolve := func(sourceKey string) (ESOItemRef, bool) {
		for i := len(items) - 1; i >= 0; i-- {
			for _, k := range items[i].keys {
				if k == sourceKey {
					return items[i].ref, true
				}
			}
		}
		return ESOItemRef{}, false
	}
	// Deterministic order by target key.
	targets := make([]string, 0, len(renames))
	for t := range renames {
		targets = append(targets, t)
	}
	sort.Strings(targets)
	var data []ESODataRef
	for _, target := range targets {
		src := renames[target]
		if ref, ok := resolve(src); ok {
			data = append(data, ESODataRef{SecretKey: target, ItemKey: ref.Key, Property: src, StoreName: ref.StoreName})
		}
	}
	if len(data) == 0 {
		return nil
	}
	return &ESOExternalSecretConfig{
		Name:            name,
		Namespace:       p.Namespace,
		StoreName:       storeForScope(p, secrets.GlobalScope()),
		Data:            data,
		Branding:        p.Branding,
		RefreshInterval: p.RefreshInterval,
	}
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

// WriteAppExternalSecret writes the single merged ExternalSecret to
// dir/external-secret.yaml (or removes it when cfg is nil — no scope has keys),
// and prunes the prior layout's per-scope files for idempotent migration.
func (p *Publisher) WriteAppExternalSecret(dir string, cfg *ESOExternalSecretConfig) error {
	prune := func(name string) error {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("prune stale ExternalSecret %s: %w", name, err)
			}
		}
		return nil
	}
	// Remove the old 3-file layout if present.
	for _, suffix := range []string{"global", "env", "cluster"} {
		if err := prune("external-secret-" + suffix + ".yaml"); err != nil {
			return err
		}
	}
	if cfg == nil {
		return prune("external-secret.yaml")
	}
	return p.writeFile(filepath.Join(dir, "external-secret.yaml"), []byte(BuildExternalSecretYAML(*cfg)))
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
