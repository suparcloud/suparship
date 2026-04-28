// Package onepassword — upper_level.go implements secrets.UpperLevelWriter
// using the 1Password SA client.
//
// Routing:
//
//	org      → platform vault (one shared vault, read-only from every cluster)
//	project  → platform vault
//	env-type → env vault (the binding whose Env matches the env-type name)
//	cluster  → env vault (the binding whose ClusterRef matches the cluster name)
//
// App and app-env scopes are handled separately by SAVaultItemWriter and live
// in the env vault for the corresponding binding.
//
// This split mirrors the trust-boundary model: shared org/project values are
// readable from every cluster (platform vault), while env-type and cluster
// values are only readable from clusters bound to that env (env vault).
package onepassword

import (
	"context"
	"fmt"
	"sort"

	"github.com/suparcloud/suparship/internal/secrets"
)

// SAUpperLevelWriter implements secrets.UpperLevelWriter using the SA client.
//
// Construct via NewSAUpperLevelWriter so callers pass the org config snapshot
// (platform vault ID, env bindings, naming patterns, and the cluster→env
// resolver) atomically.
type SAUpperLevelWriter struct {
	client          SAClient
	platformVaultID string
	bindings        []secrets.EnvBinding
	orgName         string
	naming          secrets.ResourceNaming

	// envForCluster maps a cluster name to its bound env name. Used to find
	// the right env vault for cluster-scope items. When the resolver returns
	// "", cluster writes are no-ops (cluster is unknown / unbound).
	envForCluster func(cluster string) string
}

// SAUpperLevelWriterConfig captures the org snapshot needed by the writer.
type SAUpperLevelWriterConfig struct {
	Client          SAClient
	PlatformVaultID string
	Bindings        []secrets.EnvBinding
	OrgName         string
	Naming          secrets.ResourceNaming
	// EnvForCluster resolves cluster name → bound env name (typically reads
	// org.Environments at startup). Returning "" means the cluster is unknown
	// and cluster-scope writes for it are no-ops.
	EnvForCluster func(cluster string) string
}

// NewSAUpperLevelWriter constructs an SAUpperLevelWriter from cfg.
func NewSAUpperLevelWriter(cfg SAUpperLevelWriterConfig) *SAUpperLevelWriter {
	if cfg.EnvForCluster == nil {
		cfg.EnvForCluster = func(string) string { return "" }
	}
	return &SAUpperLevelWriter{
		client:          cfg.Client,
		platformVaultID: cfg.PlatformVaultID,
		bindings:        cfg.Bindings,
		orgName:         cfg.OrgName,
		naming:          cfg.Naming,
		envForCluster:   cfg.EnvForCluster,
	}
}

// bindingFor returns the binding for env, or false when no provisioned
// binding exists.
func (w *SAUpperLevelWriter) bindingFor(env string) (secrets.EnvBinding, bool) {
	for _, b := range w.bindings {
		if b.Env == env && b.VaultID != "" {
			return b, true
		}
	}
	return secrets.EnvBinding{}, false
}

// itemTitleFor renders the vault-item title for level using the configured
// naming patterns. orgName falls back to "default" when empty.
func (w *SAUpperLevelWriter) itemTitleFor(level string, params secrets.NamingParams) string {
	if params.Org == "" {
		params.Org = w.orgName
	}
	if params.Org == "" {
		params.Org = "default"
	}
	return w.naming.RenderVaultItem(level, params)
}

// upsertItem writes data into vaultID under title, merging with existing
// fields so callers can write a single key without clobbering siblings.
func (w *SAUpperLevelWriter) upsertItem(ctx context.Context, vaultID, title string, data map[string][]byte) error {
	merged, err := w.mergeWithExisting(ctx, vaultID, title, data)
	if err != nil {
		return err
	}
	if _, err := w.client.UpsertItem(ctx, vaultID, title, merged); err != nil {
		return fmt.Errorf("upsert item %q: %w", title, err)
	}
	return nil
}

// mergeWithExisting reads the current item (when present) and merges the
// caller's data on top. Existing keys not present in data are preserved —
// matches the K8s UpperLevelSecretWriter semantics.
func (w *SAUpperLevelWriter) mergeWithExisting(ctx context.Context, vaultID, title string, data map[string][]byte) ([]ItemField, error) {
	existing, err := w.findItem(ctx, vaultID, title)
	if err != nil {
		return nil, err
	}

	merged := make(map[string]string)
	if existing != nil {
		for _, f := range existing.Fields {
			merged[f.Label] = f.Value
		}
	}
	for k, v := range data {
		merged[k] = string(v)
	}

	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fields := make([]ItemField, 0, len(keys))
	for _, k := range keys {
		fields = append(fields, ItemField{
			Label: k,
			Value: merged[k],
			Type:  "concealed",
		})
	}
	return fields, nil
}

// findItem returns the item with the given title from the vault, or nil when
// no item with that title exists.
func (w *SAUpperLevelWriter) findItem(ctx context.Context, vaultID, title string) (*Item, error) {
	items, err := w.client.ListItems(ctx, vaultID)
	if err != nil {
		return nil, fmt.Errorf("list items in vault %q: %w", vaultID, err)
	}
	for _, it := range items {
		if it.Title == title {
			full, err := w.client.GetItem(ctx, vaultID, it.ID)
			if err != nil {
				return nil, fmt.Errorf("get item %q: %w", title, err)
			}
			return &full, nil
		}
	}
	return nil, nil
}

// listKeys returns the field labels of the named item, sorted, or empty when
// the item does not exist.
func (w *SAUpperLevelWriter) listKeys(ctx context.Context, vaultID, title string) ([]secrets.SecretEntry, error) {
	item, err := w.findItem(ctx, vaultID, title)
	if err != nil {
		return nil, err
	}
	if item == nil {
		return nil, nil
	}
	keys := make([]string, 0, len(item.Fields))
	for _, f := range item.Fields {
		keys = append(keys, f.Label)
	}
	sort.Strings(keys)
	out := make([]secrets.SecretEntry, len(keys))
	for i, k := range keys {
		out[i] = secrets.SecretEntry{Key: k}
	}
	return out, nil
}

// deleteKey removes a single field from the named item. No-op when the item
// or key does not exist.
func (w *SAUpperLevelWriter) deleteKey(ctx context.Context, vaultID, title, key string) error {
	item, err := w.findItem(ctx, vaultID, title)
	if err != nil {
		return err
	}
	if item == nil {
		return nil
	}
	fields := make([]ItemField, 0, len(item.Fields))
	for _, f := range item.Fields {
		if f.Label == key {
			continue
		}
		fields = append(fields, ItemField{
			Label: f.Label,
			Value: f.Value,
			Type:  f.Type,
		})
	}
	if len(fields) == len(item.Fields) {
		return nil // key not present
	}
	if _, err := w.client.UpsertItem(ctx, vaultID, title, fields); err != nil {
		return fmt.Errorf("delete key %q from %q: %w", key, title, err)
	}
	return nil
}

// ── Org scope (platform vault) ────────────────────────────────────────────

func (w *SAUpperLevelWriter) WriteOrgSecrets(ctx context.Context, data map[string][]byte) error {
	if w.platformVaultID == "" {
		return fmt.Errorf("onepassword: platform vault not provisioned — paste SA token in Settings")
	}
	title := w.itemTitleFor(secrets.LevelOrg, secrets.NamingParams{})
	return w.upsertItem(ctx, w.platformVaultID, title, data)
}

func (w *SAUpperLevelWriter) ReadOrgSecretKeys(ctx context.Context) ([]secrets.SecretEntry, error) {
	if w.platformVaultID == "" {
		return nil, nil
	}
	title := w.itemTitleFor(secrets.LevelOrg, secrets.NamingParams{})
	return w.listKeys(ctx, w.platformVaultID, title)
}

func (w *SAUpperLevelWriter) DeleteOrgSecretKey(ctx context.Context, key string) error {
	if w.platformVaultID == "" {
		return nil
	}
	title := w.itemTitleFor(secrets.LevelOrg, secrets.NamingParams{})
	return w.deleteKey(ctx, w.platformVaultID, title, key)
}

// ── Project scope (platform vault) ────────────────────────────────────────

func (w *SAUpperLevelWriter) WriteProjectSecrets(ctx context.Context, project string, data map[string][]byte) error {
	if w.platformVaultID == "" {
		return fmt.Errorf("onepassword: platform vault not provisioned — paste SA token in Settings")
	}
	title := w.itemTitleFor(secrets.LevelProject, secrets.NamingParams{Project: project})
	return w.upsertItem(ctx, w.platformVaultID, title, data)
}

func (w *SAUpperLevelWriter) ReadProjectSecretKeys(ctx context.Context, project string) ([]secrets.SecretEntry, error) {
	if w.platformVaultID == "" {
		return nil, nil
	}
	title := w.itemTitleFor(secrets.LevelProject, secrets.NamingParams{Project: project})
	return w.listKeys(ctx, w.platformVaultID, title)
}

func (w *SAUpperLevelWriter) DeleteProjectSecretKey(ctx context.Context, project, key string) error {
	if w.platformVaultID == "" {
		return nil
	}
	title := w.itemTitleFor(secrets.LevelProject, secrets.NamingParams{Project: project})
	return w.deleteKey(ctx, w.platformVaultID, title, key)
}

// ── Env-type scope (env vault) ────────────────────────────────────────────

func (w *SAUpperLevelWriter) WriteEnvTypeSecrets(ctx context.Context, envType string, data map[string][]byte) error {
	binding, ok := w.bindingFor(envType)
	if !ok {
		return fmt.Errorf("onepassword: no provisioned binding for env %q — bind a vault in Settings first", envType)
	}
	title := w.itemTitleFor(secrets.LevelEnvironment, secrets.NamingParams{Env: envType})
	return w.upsertItem(ctx, binding.VaultID, title, data)
}

func (w *SAUpperLevelWriter) ReadEnvTypeSecretKeys(ctx context.Context, envType string) ([]secrets.SecretEntry, error) {
	binding, ok := w.bindingFor(envType)
	if !ok {
		return nil, nil
	}
	title := w.itemTitleFor(secrets.LevelEnvironment, secrets.NamingParams{Env: envType})
	return w.listKeys(ctx, binding.VaultID, title)
}

func (w *SAUpperLevelWriter) DeleteEnvTypeSecretKey(ctx context.Context, envType, key string) error {
	binding, ok := w.bindingFor(envType)
	if !ok {
		return nil
	}
	title := w.itemTitleFor(secrets.LevelEnvironment, secrets.NamingParams{Env: envType})
	return w.deleteKey(ctx, binding.VaultID, title, key)
}

// ── Cluster scope (env vault for the cluster's bound env) ────────────────

func (w *SAUpperLevelWriter) WriteClusterSecrets(ctx context.Context, cluster string, data map[string][]byte) error {
	envName := w.envForCluster(cluster)
	if envName == "" {
		return fmt.Errorf("onepassword: cluster %q is not bound to any env — bind a cluster in Settings > Environments first", cluster)
	}
	binding, ok := w.bindingFor(envName)
	if !ok {
		return fmt.Errorf("onepassword: env %q (cluster %q) has no provisioned vault binding", envName, cluster)
	}
	title := w.itemTitleFor(secrets.LevelCluster, secrets.NamingParams{Env: envName, Cluster: cluster})
	return w.upsertItem(ctx, binding.VaultID, title, data)
}

func (w *SAUpperLevelWriter) ReadClusterSecretKeys(ctx context.Context, cluster string) ([]secrets.SecretEntry, error) {
	envName := w.envForCluster(cluster)
	if envName == "" {
		return nil, nil
	}
	binding, ok := w.bindingFor(envName)
	if !ok {
		return nil, nil
	}
	title := w.itemTitleFor(secrets.LevelCluster, secrets.NamingParams{Env: envName, Cluster: cluster})
	return w.listKeys(ctx, binding.VaultID, title)
}

func (w *SAUpperLevelWriter) DeleteClusterSecretKey(ctx context.Context, cluster, key string) error {
	envName := w.envForCluster(cluster)
	if envName == "" {
		return nil
	}
	binding, ok := w.bindingFor(envName)
	if !ok {
		return nil
	}
	title := w.itemTitleFor(secrets.LevelCluster, secrets.NamingParams{Env: envName, Cluster: cluster})
	return w.deleteKey(ctx, binding.VaultID, title, key)
}

// Compile-time check.
var _ secrets.UpperLevelWriter = (*SAUpperLevelWriter)(nil)
