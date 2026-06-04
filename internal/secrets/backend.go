// Package secrets provides a simple key/value secret management layer for
// suparShip apps. Developers enter KEY=value pairs through the UI; the
// platform writes them to a backend (Kubernetes Secrets, or 1Password) under
// three scopes — global, env, cluster — that ESO pulls into app namespaces.
//
// The secret backend type is configured once at the org level. Developers
// never interact with provider details — they only see key names.
package secrets

import (
	"sort"
	"time"
)

// BackendType identifies the secret storage backend.
type BackendType string

const (
	BackendK8s       BackendType = "k8s"
	Backend1Password BackendType = "onepassword"
)

// ValidBackendTypes is the set of recognized backend type strings.
var ValidBackendTypes = map[BackendType]bool{
	BackendK8s:       true,
	Backend1Password: true,
}

// ── 1Password SA-driven config ────────────────────────────────────────────

// OnePasswordConfig holds 1Password-specific org-level configuration.
// The only admin-facing input is the SA token (pasted once in the UI and
// stored as a K8s Secret). Vault refs are populated as the global vault is
// set and env/cluster vaults are provisioned on environment/cluster creation.
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

	// ClusterVaults holds one vault per cluster, keyed by VaultRef.Key (the
	// cluster name). Provisioned when a cluster is created.
	ClusterVaults []VaultRef `json:"clusterVaults,omitempty" yaml:"clusterVaults,omitempty"`
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

// VaultRef tracks the provisioned state of one vault (global, an environment,
// or a cluster).
type VaultRef struct {
	// Key is the environment name (for EnvVaults) or cluster name (for
	// ClusterVaults). Empty for the global vault.
	Key string `json:"key,omitempty" yaml:"key,omitempty"`
	// VaultID is the 1Password vault UUID.
	VaultID string `json:"vaultId,omitempty" yaml:"vaultId,omitempty"`
	// VaultName is the human-readable vault name.
	VaultName string `json:"vaultName,omitempty" yaml:"vaultName,omitempty"`
	// ClusterSecretStoreName is the ESO ClusterSecretStore resource created
	// for this vault. Used when generating ExternalSecrets.
	ClusterSecretStoreName string `json:"clusterSecretStoreName,omitempty" yaml:"clusterSecretStoreName,omitempty"`
	// ConnectEndpoint overrides the org-level Connect URL for this vault.
	ConnectEndpoint string `json:"connectEndpoint,omitempty" yaml:"connectEndpoint,omitempty"`
	// Provisioned is true once the full provision chain has completed
	// (Connect token sealed, ClusterSecretStore published to gitops).
	Provisioned bool `json:"provisioned,omitempty" yaml:"provisioned,omitempty"`
	// LastProvisioned records when provisioning last completed.
	LastProvisioned time.Time `json:"lastProvisioned,omitempty" yaml:"lastProvisioned,omitempty"`
	// LastError stores the most recent provision error for UI display.
	LastError string `json:"lastError,omitempty" yaml:"lastError,omitempty"`
}

// ── BackendConfig ─────────────────────────────────────────────────────────

// BackendConfig is the org-level configuration for the secret storage backend.
// Stored as part of the Org struct in the suparship-org-config ConfigMap.
type BackendConfig struct {
	// Type selects the secret backend. Defaults to "k8s" when empty.
	Type BackendType `json:"type" yaml:"type"`
	// OnePassword holds 1Password-specific configuration.
	// Only used when Type is "onepassword".
	OnePassword *OnePasswordConfig `json:"onePassword,omitempty" yaml:"onePassword,omitempty"`
}

// Effective returns the backend type, falling back to "k8s" when empty.
func (c BackendConfig) Effective() BackendType {
	if c.Type == "" {
		return BackendK8s
	}
	return c.Type
}

// Validate checks the backend config for save-time correctness.
func (c BackendConfig) Validate() error {
	if !ValidBackendTypes[c.Effective()] {
		return ErrInvalidBackendType
	}
	return nil
}

// FindVault returns the VaultRef for the given scope, or nil if not set.
func (c BackendConfig) FindVault(scope Scope) *VaultRef {
	if c.OnePassword == nil {
		return nil
	}
	op := c.OnePassword
	switch scope.Kind {
	case ScopeEnv:
		for i := range op.EnvVaults {
			if op.EnvVaults[i].Key == scope.Env {
				return &op.EnvVaults[i]
			}
		}
	case ScopeCluster:
		for i := range op.ClusterVaults {
			if op.ClusterVaults[i].Key == scope.Cluster {
				return &op.ClusterVaults[i]
			}
		}
	default:
		if op.GlobalVault.VaultID != "" || op.GlobalVault.VaultName != "" {
			return &op.GlobalVault
		}
	}
	return nil
}

// UpsertVault adds or replaces the vault ref for the given scope.
func (c *BackendConfig) UpsertVault(scope Scope, ref VaultRef) {
	if c.OnePassword == nil {
		c.OnePassword = &OnePasswordConfig{}
	}
	op := c.OnePassword
	switch scope.Kind {
	case ScopeEnv:
		ref.Key = scope.Env
		op.EnvVaults = upsertVaultRef(op.EnvVaults, ref)
	case ScopeCluster:
		ref.Key = scope.Cluster
		op.ClusterVaults = upsertVaultRef(op.ClusterVaults, ref)
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
	case ScopeCluster:
		op.ClusterVaults = removeVaultRef(op.ClusterVaults, scope.Cluster)
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

// ConnectTokenSecretName returns the K8s Secret name that holds the per-scope
// Connect token on target clusters (after being unsealed by sealed-secrets).
func ConnectTokenSecretName(scope Scope) string {
	return "op-connect-token-" + scopeSuffix(scope)
}
