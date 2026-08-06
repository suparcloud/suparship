// Package secrets provides a simple key/value secret management layer for
// suparship apps. Developers enter KEY=value pairs through the UI; the
// platform writes them to a backend (Kubernetes Secrets, or 1Password) under
// three scopes — global, env, cluster — that ESO pulls into app namespaces.
//
// The secret backend type is configured once at the org level. Developers
// never interact with provider details — they only see key names.
package secrets

import (
	"sort"
	"strings"
	"time"
)

// BackendType identifies the secret storage backend.
type BackendType string

const (
	// BackendK8s stores items as plain Secrets in namespaces on the TOOLING
	// cluster.
	//
	// Deprecated: demo/dev only. Its ClusterSecretStores are published to
	// _infra/, which the root App-of-Apps syncs solely to the tooling cluster,
	// so ExternalSecrets on a remote workload cluster reference a store that was
	// never delivered there and never resolve. Use BackendVault instead; see
	// `suparship secrets migrate`.
	BackendK8s       BackendType = "k8s"
	Backend1Password BackendType = "onepassword"
	// BackendVault stores items in a HashiCorp Vault KV v2 mount. Note this is
	// the Vault PRODUCT — distinct from suparship's own "vault" concept (the
	// per-scope item container named by VaultName).
	BackendVault BackendType = "vault"
)

// ValidBackendTypes is the set of recognized backend type strings.
var ValidBackendTypes = map[BackendType]bool{
	BackendK8s:       true,
	Backend1Password: true,
	BackendVault:     true,
}

// BackendTypeNames returns the recognized backend types, sorted, for help text
// and error messages. Derived from ValidBackendTypes rather than hand-listed so
// adding a backend can't leave a stale "use k8s or onepassword" behind.
func BackendTypeNames() []string {
	names := make([]string, 0, len(ValidBackendTypes))
	for t := range ValidBackendTypes {
		names = append(names, string(t))
	}
	sort.Strings(names)
	return names
}

// UsesPerClusterCredentials reports whether the backend ships a distinct
// credential to every workload cluster, sealed via sealed-secrets and published
// through the per-cluster ClusterSecretStore flow.
//
// True for 1Password (Connect token) and Vault (Vault token). False for k8s,
// whose ESO reads Secrets in-cluster via a ServiceAccount and therefore has no
// credential to distribute — which is also why it cannot serve remote clusters.
func (c BackendConfig) UsesPerClusterCredentials() bool {
	switch c.Effective() {
	case Backend1Password, BackendVault:
		return true
	default:
		return false
	}
}

// ── 1Password SA-driven config ────────────────────────────────────────────

// OnePasswordConfig holds 1Password-specific org-level configuration.
// The only admin-facing input is the SA token (pasted once in the UI and
// stored as a K8s Secret). Vault refs are populated as the global vault is
// set and env vaults are registered. Cluster overrides are items inside the
// env vault, so clusters need no vault of their own.
type OnePasswordConfig struct {
	// GroupName is the 1Password group that owns all suparship vaults.
	// Fixed at install time via helm values; default "Suparship".
	GroupName string `json:"groupName,omitempty" yaml:"groupName,omitempty"`

	// Connect describes the managed 1Password Connect server in the
	// tooling cluster.
	Connect ConnectStatus `json:"connect,omitempty" yaml:"connect,omitempty"`

	// GlobalVault is the vault holding global-scope secrets (org-admin shared
	// items plus one item per app). Operator-selected after SA-token paste.
	GlobalVault VaultRef `json:"globalVault,omitempty" yaml:"globalVault,omitempty"`

	// EnvVaults holds one vault per environment, keyed by VaultRef.Key (the
	// environment name). Provisioned when an environment is created.
	EnvVaults []VaultRef `json:"envVaults,omitempty" yaml:"envVaults,omitempty"`

	// ClusterTokens tracks, per workload cluster, the state of that cluster's
	// single Connect token: stashed when the operator pastes it, sealed once
	// the SealedSecret + unified ClusterSecretStore are published to gitops.
	ClusterTokens []ClusterTokenRef `json:"clusterTokens,omitempty" yaml:"clusterTokens,omitempty"`
}

// ClusterTokenRef tracks the per-cluster Connect token + unified store state.
// Each workload cluster carries ONE Connect token with access to every vault
// it reads (the global vault + the env vaults bound to it) and ONE
// ClusterSecretStore (UnifiedStoreName) listing those vaults.
type ClusterTokenRef struct {
	// Cluster is the registered cluster name.
	Cluster string `json:"cluster" yaml:"cluster"`
	// ConnectEndpoint overrides the org-level Connect URL for this cluster.
	ConnectEndpoint string `json:"connectEndpoint,omitempty" yaml:"connectEndpoint,omitempty"`
	// Sealed is true once the token was sealed and the unified store was
	// published to gitops for this cluster.
	Sealed bool `json:"sealed,omitempty" yaml:"sealed,omitempty"`
	// LastSealed records when sealing last completed.
	LastSealed time.Time `json:"lastSealed,omitempty" yaml:"lastSealed,omitempty"`
	// LastError stores the most recent seal/publish error for UI display.
	LastError string `json:"lastError,omitempty" yaml:"lastError,omitempty"`
}

// ConnectStatus tracks the state of the managed 1Password Connect server.
type ConnectStatus struct {
	// Endpoint is the in-cluster URL ESO uses to reach Connect.
	Endpoint string `json:"endpoint,omitempty" yaml:"endpoint,omitempty"`
	// Installed is true once the Connect ArgoCD Application exists.
	Installed bool `json:"installed,omitempty" yaml:"installed,omitempty"`
	// Healthy is the result of the last liveness check.
	Healthy bool `json:"healthy,omitempty" yaml:"healthy,omitempty"`
	// LastProbe records when the last health check ran.
	LastProbe time.Time `json:"lastProbe,omitempty" yaml:"lastProbe,omitempty"`
}

// VaultRef tracks the registration of one vault (global or an environment).
// Registration only records which 1Password vault backs the scope — Connect
// tokens are per cluster (see ClusterTokenRef), not per vault.
type VaultRef struct {
	// Key is the environment name (for EnvVaults). Empty for the global vault.
	Key string `json:"key,omitempty" yaml:"key,omitempty"`
	// VaultID is the 1Password vault UUID.
	VaultID string `json:"vaultId,omitempty" yaml:"vaultId,omitempty"`
	// VaultName is the human-readable vault name.
	VaultName string `json:"vaultName,omitempty" yaml:"vaultName,omitempty"`
	// Provisioned is true once the vault is registered (its ID recorded and
	// validated against the SA token).
	Provisioned bool `json:"provisioned,omitempty" yaml:"provisioned,omitempty"`
	// LastProvisioned records when registration last completed.
	LastProvisioned time.Time `json:"lastProvisioned,omitempty" yaml:"lastProvisioned,omitempty"`
	// LastError stores the most recent registration error for UI display.
	LastError string `json:"lastError,omitempty" yaml:"lastError,omitempty"`
}

// ── HashiCorp Vault config ────────────────────────────────────────────────

// HCVaultConfig holds HashiCorp Vault org-level configuration.
//
// Unlike 1Password, Vault needs NO per-scope registration step: a suparship
// vault is a path prefix inside one KV v2 mount, so global and env containers
// are derived from the scope (see VaultName) rather than being created and
// recorded by an operator. That removes the global-vault pick and the env-vault
// registration entirely — the only setup is the address, the mount, and one
// sealed token per workload cluster.
//
// Non-secret configuration only. The token suparship itself writes with lives
// in the VaultTokenSecretName K8s Secret, mirroring how the 1Password SA token
// is stored.
type HCVaultConfig struct {
	// Address is the Vault server URL ESO and suparship both dial,
	// e.g. "https://vault.example.com:8200".
	Address string `json:"address,omitempty" yaml:"address,omitempty"`

	// Mount is the KV v2 mount that holds every suparship item.
	// Empty means DefaultVaultMount.
	Mount string `json:"mount,omitempty" yaml:"mount,omitempty"`

	// Namespace is the Vault Enterprise namespace. Empty for Vault OSS.
	Namespace string `json:"namespace,omitempty" yaml:"namespace,omitempty"`

	// CACert is a PEM bundle for a Vault served with a private CA. Empty uses
	// the system trust store.
	CACert string `json:"caCert,omitempty" yaml:"caCert,omitempty"`

	// ClusterTokens tracks, per workload cluster, the state of that cluster's
	// Vault token: stashed when the operator pastes it, sealed once the
	// SealedSecret + ClusterSecretStore are published to gitops. Same shape and
	// lifecycle as the 1Password Connect tokens, so the seal pipeline is shared.
	ClusterTokens []ClusterTokenRef `json:"clusterTokens,omitempty" yaml:"clusterTokens,omitempty"`
}

// EffectiveMount returns the configured KV v2 mount, or DefaultVaultMount.
func (v *HCVaultConfig) EffectiveMount() string {
	if v == nil || strings.TrimSpace(v.Mount) == "" {
		return DefaultVaultMount
	}
	return strings.TrimSpace(v.Mount)
}

// ── BackendConfig ─────────────────────────────────────────────────────────

// BackendConfig is the org-level configuration for the secret storage backend.
// Stored as part of the Org struct in the suparship-org-config ConfigMap.
type BackendConfig struct {
	// Type selects the secret backend. Defaults to "k8s" when empty.
	Type BackendType `json:"type" yaml:"type"`
	// OnePassword holds 1Password-specific configuration.
	// Only used when Type is "onepassword".
	//
	// Every backend's config is retained regardless of which Type is active, so
	// switching backends and switching back does not lose settings. The PUT
	// handlers merge rather than replace for exactly this reason.
	OnePassword *OnePasswordConfig `json:"onePassword,omitempty" yaml:"onePassword,omitempty"`
	// Vault holds HashiCorp Vault configuration.
	// Only used when Type is "vault".
	Vault *HCVaultConfig `json:"vault,omitempty" yaml:"vault,omitempty"`
	// ExternalSecrets holds org-level settings for the ExternalSecret resources
	// suparship generates (independent of the backend type).
	ExternalSecrets ExternalSecretSettings `json:"externalSecrets,omitempty" yaml:"externalSecrets,omitempty"`
}

// ExternalSecretSettings holds org-level knobs applied to every generated
// ExternalSecret.
type ExternalSecretSettings struct {
	// RefreshInterval is how often ESO re-pulls secret values from the backend
	// (ExternalSecret spec.refreshInterval, a Go duration like "1m", "30s",
	// "1h"). Empty defaults to DefaultRefreshInterval.
	RefreshInterval string `json:"refreshInterval,omitempty" yaml:"refreshInterval,omitempty"`
}

// DefaultRefreshInterval is the ExternalSecret refresh interval used when the
// org hasn't configured one.
const DefaultRefreshInterval = "1m"

// EffectiveRefreshInterval returns the configured refresh interval, or
// DefaultRefreshInterval when unset.
func (s ExternalSecretSettings) EffectiveRefreshInterval() string {
	if strings.TrimSpace(s.RefreshInterval) == "" {
		return DefaultRefreshInterval
	}
	return strings.TrimSpace(s.RefreshInterval)
}

// Effective returns the backend type, falling back to "k8s" when empty.
func (c BackendConfig) Effective() BackendType {
	if c.Type == "" {
		return BackendK8s
	}
	return c.Type
}

// SetupComplete reports whether the secret backend is fully configured for the
// given org environments and the clusters they're bound to, returning a
// human-readable reason when it is not. Pure (no network) — derived from
// stored config — so it can drive a fast, polled setup checklist.
//
// k8s backend needs no operator setup. 1Password needs: a global vault, an env
// vault per bound environment, and a sealed Connect token for each cluster that
// runs at least one environment. Vault needs only a server address and a sealed
// token per bound cluster — its per-scope containers are derived paths, so
// there is nothing to register.
func (c BackendConfig) SetupComplete(envNames, boundClusters []string) (bool, string) {
	if c.Effective() == BackendVault {
		if c.Vault == nil || strings.TrimSpace(c.Vault.Address) == "" {
			return false, "Set the Vault server address under Settings → Secrets Backend."
		}
		if unsealed := c.unsealedClusters(boundClusters); len(unsealed) > 0 {
			return false, "Paste a Vault token for cluster(s): " + strings.Join(unsealed, ", ") + "."
		}
		return true, ""
	}
	if c.Effective() != Backend1Password {
		return true, ""
	}
	op := c.OnePassword
	if op == nil || op.GlobalVault.VaultID == "" {
		return false, "Pick the global vault under Settings → Secrets Backend."
	}
	var missingEnvVaults []string
	for _, env := range envNames {
		if c.FindVault(EnvScope(env)) == nil {
			missingEnvVaults = append(missingEnvVaults, env)
		}
	}
	if len(missingEnvVaults) > 0 {
		return false, "Register an env vault for: " + strings.Join(missingEnvVaults, ", ") + "."
	}
	if unsealed := c.unsealedClusters(boundClusters); len(unsealed) > 0 {
		return false, "Paste a Connect token for cluster(s): " + strings.Join(unsealed, ", ") + "."
	}
	return true, ""
}

// unsealedClusters returns the clusters with no sealed per-cluster credential.
func (c BackendConfig) unsealedClusters(boundClusters []string) []string {
	var unsealed []string
	for _, cl := range boundClusters {
		ref := c.FindClusterToken(cl)
		if ref == nil || !ref.Sealed {
			unsealed = append(unsealed, cl)
		}
	}
	return unsealed
}

// Validate checks the backend config for save-time correctness.
func (c BackendConfig) Validate() error {
	if !ValidBackendTypes[c.Effective()] {
		return ErrInvalidBackendType
	}
	if c.Effective() == BackendVault {
		// Only validated when Vault is the ACTIVE backend: a retained config
		// from a previous selection is allowed to be incomplete, otherwise
		// switching away from a half-configured Vault would be impossible.
		if c.Vault == nil || strings.TrimSpace(c.Vault.Address) == "" {
			return ErrVaultAddressRequired
		}
	}
	return nil
}

// FindVault returns the VaultRef for the given scope, or nil if not set.
// Cluster scope resolves to its environment's vault — cluster overrides are
// items inside the env vault.
func (c BackendConfig) FindVault(scope Scope) *VaultRef {
	if c.OnePassword == nil {
		return nil
	}
	op := c.OnePassword
	switch scope.Kind {
	case ScopeEnv, ScopeCluster, ScopeProjectEnv, ScopeStackEnv, ScopePreview, ScopePreviewPR:
		// Env-bound scopes — including project-env, stack-env and the preview
		// bands — live in the env vault. (ScopeProject/ScopeStack are
		// env-agnostic → global vault.)
		for i := range op.EnvVaults {
			if op.EnvVaults[i].Key == scope.Env {
				return &op.EnvVaults[i]
			}
		}
	default:
		if op.GlobalVault.VaultID != "" || op.GlobalVault.VaultName != "" {
			return &op.GlobalVault
		}
	}
	return nil
}

// UpsertVault adds or replaces the vault ref for the given scope. Only global
// and env scopes have vaults of their own; cluster scope is never persisted.
func (c *BackendConfig) UpsertVault(scope Scope, ref VaultRef) {
	if c.OnePassword == nil {
		c.OnePassword = &OnePasswordConfig{}
	}
	op := c.OnePassword
	switch scope.Kind {
	case ScopeEnv:
		ref.Key = scope.Env
		op.EnvVaults = upsertVaultRef(op.EnvVaults, ref)
	default:
		ref.Key = ""
		op.GlobalVault = ref
	}
}

// RemoveVault removes the vault ref for the given scope. No-op if absent.
func (c *BackendConfig) RemoveVault(scope Scope) {
	if c.OnePassword == nil {
		return
	}
	op := c.OnePassword
	switch scope.Kind {
	case ScopeEnv:
		op.EnvVaults = removeVaultRef(op.EnvVaults, scope.Env)
	default:
		op.GlobalVault = VaultRef{}
	}
}

func upsertVaultRef(refs []VaultRef, ref VaultRef) []VaultRef {
	for i := range refs {
		if refs[i].Key == ref.Key {
			refs[i] = ref
			return refs
		}
	}
	refs = append(refs, ref)
	sort.Slice(refs, func(i, j int) bool { return refs[i].Key < refs[j].Key })
	return refs
}

func removeVaultRef(refs []VaultRef, key string) []VaultRef {
	filtered := refs[:0]
	for _, r := range refs {
		if r.Key != key {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

// clusterTokensOf returns a pointer to the ACTIVE backend's per-cluster token
// slice, so the seal pipeline works the same whether the credential is a
// 1Password Connect token or a Vault token. Returns nil for backends that carry
// no per-cluster credential (k8s). When create is true the backend's config
// block is allocated if absent.
//
// The returned pointer aliases the shared config even on a value receiver,
// because OnePassword and Vault are themselves pointers.
func (c *BackendConfig) clusterTokensOf(create bool) *[]ClusterTokenRef {
	switch c.Effective() {
	case Backend1Password:
		if c.OnePassword == nil {
			if !create {
				return nil
			}
			c.OnePassword = &OnePasswordConfig{}
		}
		return &c.OnePassword.ClusterTokens
	case BackendVault:
		if c.Vault == nil {
			if !create {
				return nil
			}
			c.Vault = &HCVaultConfig{}
		}
		return &c.Vault.ClusterTokens
	default:
		return nil
	}
}

// FindClusterToken returns the ClusterTokenRef for the cluster, or nil.
func (c BackendConfig) FindClusterToken(cluster string) *ClusterTokenRef {
	toks := c.clusterTokensOf(false)
	if toks == nil {
		return nil
	}
	for i := range *toks {
		if (*toks)[i].Cluster == cluster {
			return &(*toks)[i]
		}
	}
	return nil
}

// UpsertClusterToken adds or replaces the per-cluster token ref.
func (c *BackendConfig) UpsertClusterToken(ref ClusterTokenRef) {
	toks := c.clusterTokensOf(true)
	if toks == nil {
		return
	}
	for i := range *toks {
		if (*toks)[i].Cluster == ref.Cluster {
			(*toks)[i] = ref
			return
		}
	}
	*toks = append(*toks, ref)
	sort.Slice(*toks, func(i, j int) bool {
		return (*toks)[i].Cluster < (*toks)[j].Cluster
	})
}

// RemoveClusterToken removes the per-cluster token ref. No-op if absent.
func (c *BackendConfig) RemoveClusterToken(cluster string) {
	toks := c.clusterTokensOf(false)
	if toks == nil {
		return
	}
	filtered := (*toks)[:0]
	for _, t := range *toks {
		if t.Cluster != cluster {
			filtered = append(filtered, t)
		}
	}
	*toks = filtered
}

// ── App-level read result ─────────────────────────────────────────────────

// SecretEntry is a single key returned by ListKeys. Values are never returned
// through the API — only key names.
type SecretEntry struct {
	Key string `json:"key"`
}

// ── SA token storage conventions ──────────────────────────────────────────

const (
	// SATokenSecretName is the well-known K8s Secret in suparship-system
	// that holds the 1Password Service Account token.
	SATokenSecretName = "suparship-op-sa-token"
	// SATokenSecretKey is the data key inside SATokenSecretName.
	SATokenSecretKey = "token"
	// DefaultOnePasswordGroup is the default 1Password group name.
	DefaultOnePasswordGroup = "Suparship"
	// DefaultConnectNamespace is where the managed Connect server runs.
	DefaultConnectNamespace = "onepassword-connect"
	// RotateGraceSeconds is the default grace window between issuing a
	// new Connect token and revoking the old one.
	RotateGraceSeconds = 60
)

// ConnectTokenSecretName is the K8s Secret name that holds the cluster's
// single Connect token on target clusters (after being unsealed by
// sealed-secrets). One token per cluster, covering every vault it reads.
const ConnectTokenSecretName = "op-connect-token"

// ── HashiCorp Vault storage conventions ───────────────────────────────────

const (
	// VaultTokenSecretName is the well-known K8s Secret in suparship-system
	// holding the Vault token suparship itself writes items with — the data
	// plane, analogous to SATokenSecretName.
	VaultTokenSecretName = "suparship-vault-token"
	// VaultTokenSecretKey is the data key inside VaultTokenSecretName.
	VaultTokenSecretKey = "token"
	// DefaultVaultMount is the KV v2 mount holding every suparship item when
	// the org has not configured one.
	DefaultVaultMount = "suparship"
)

// VaultTokenClusterSecretName is the K8s Secret name holding a workload
// cluster's own Vault token (after sealed-secrets decrypts it), which that
// cluster's ESO authenticates with. One per cluster, mirroring
// ConnectTokenSecretName.
const VaultTokenClusterSecretName = "vault-token"

// ClusterCredentialSecretName returns the on-cluster Secret name holding the
// active backend's per-cluster credential, or "" when the backend has none.
func (c BackendConfig) ClusterCredentialSecretName() string {
	switch c.Effective() {
	case Backend1Password:
		return ConnectTokenSecretName
	case BackendVault:
		return VaultTokenClusterSecretName
	default:
		return ""
	}
}
