// Package secrets provides a simple key/value secret management layer for
// suparShip apps. Developers enter KEY=value pairs through the UI; the
// platform writes them to a backend (Kubernetes Secrets in MVP) using
// deterministic, well-known resource names that Helm templates consume.
//
// The secret backend type is configured once at the org level. Developers
// never interact with provider details — they only see key names.
package secrets

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// BackendType identifies the secret storage backend.
type BackendType string

const (
	BackendK8s        BackendType = "k8s"
	Backend1Password  BackendType = "onepassword"
)

// ValidBackendTypes is the set of recognized backend type strings.
var ValidBackendTypes = map[BackendType]bool{
	BackendK8s:       true,
	Backend1Password: true,
}

// ── 1Password SA-driven config ────────────────────────────────────────────

// OnePasswordConfig holds 1Password-specific org-level configuration.
// The only admin-facing input is the SA token (pasted once in the UI and
// stored as a K8s Secret). All other fields are populated by the Provision
// flow.
type OnePasswordConfig struct {
	// GroupName is the 1Password group that owns all suparship vaults.
	// Fixed at install time via helm values; default "Suparship".
	GroupName string `json:"groupName,omitempty" yaml:"groupName,omitempty"`

	// Connect describes the managed 1Password Connect server in the
	// tooling cluster. Populated by the provision flow.
	Connect ConnectStatus `json:"connect,omitempty" yaml:"connect,omitempty"`

	// Bindings maps environments to provisioned vault + Connect-token
	// state. Populated by the Provision flow.
	Bindings []EnvBinding `json:"bindings,omitempty" yaml:"bindings,omitempty"`
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

// EnvBinding tracks the provisioned state for a single environment.
type EnvBinding struct {
	// Env is the target environment name ("staging", "prod").
	Env string `json:"env" yaml:"env"`
	// VaultID is the 1Password vault UUID.
	VaultID string `json:"vaultId,omitempty" yaml:"vaultId,omitempty"`
	// VaultName is the human-readable vault name.
	VaultName string `json:"vaultName,omitempty" yaml:"vaultName,omitempty"`
	// Provisioned is true once the full bind chain has completed
	// (Connect token sealed, ClusterSecretStore published to gitops).
	Provisioned bool `json:"provisioned,omitempty" yaml:"provisioned,omitempty"`
	// LastProvisioned records when binding last completed.
	LastProvisioned time.Time `json:"lastProvisioned,omitempty" yaml:"lastProvisioned,omitempty"`
	// LastError stores the most recent bind error for UI display.
	LastError string `json:"lastError,omitempty" yaml:"lastError,omitempty"`
	// ClusterSecretStoreName is the name of the ClusterSecretStore resource
	// created for this environment. Used when generating ExternalSecrets.
	ClusterSecretStoreName string `json:"clusterSecretStoreName,omitempty" yaml:"clusterSecretStoreName,omitempty"`
	// ConnectEndpoint is the per-binding override for the 1Password Connect
	// server URL. When set, it takes precedence over the org-level endpoint
	// and the built-in default when regenerating the ClusterSecretStore.
	ConnectEndpoint string `json:"connectEndpoint,omitempty" yaml:"connectEndpoint,omitempty"`
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

// FindBinding returns the EnvBinding for the given environment, or nil.
func (c BackendConfig) FindBinding(env string) *EnvBinding {
	if c.OnePassword == nil {
		return nil
	}
	for i := range c.OnePassword.Bindings {
		if c.OnePassword.Bindings[i].Env == env {
			return &c.OnePassword.Bindings[i]
		}
	}
	return nil
}

// UpsertBinding adds or replaces the binding for the given env.
func (c *BackendConfig) UpsertBinding(b EnvBinding) {
	if c.OnePassword == nil {
		c.OnePassword = &OnePasswordConfig{}
	}
	for i := range c.OnePassword.Bindings {
		if c.OnePassword.Bindings[i].Env == b.Env {
			c.OnePassword.Bindings[i] = b
			return
		}
	}
	c.OnePassword.Bindings = append(c.OnePassword.Bindings, b)
	sort.Slice(c.OnePassword.Bindings, func(i, j int) bool {
		return c.OnePassword.Bindings[i].Env < c.OnePassword.Bindings[j].Env
	})
}

// RemoveBinding removes the binding for the given env. No-op if not found.
func (c *BackendConfig) RemoveBinding(env string) {
	if c.OnePassword == nil {
		return
	}
	filtered := c.OnePassword.Bindings[:0]
	for _, b := range c.OnePassword.Bindings {
		if b.Env != env {
			filtered = append(filtered, b)
		}
	}
	c.OnePassword.Bindings = filtered
}

// ── Legacy migration ──────────────────────────────────────────────────────

// MigrateFromLegacy converts persisted configs that used the old schema
// (Bindings on BackendConfig, IsolationMode, OnePasswordMode, etc.) into
// the new schema. It is safe to call multiple times (idempotent).
func (c *BackendConfig) MigrateFromLegacy() {
	// Map old "1password" type to new "onepassword" value.
	if c.Type == "1password" {
		c.Type = Backend1Password
	}
	// Nothing else to do — old fields are dropped by Go's YAML
	// unmarshaler (they no longer exist in the struct).
}

// ── K8s-Backend interface (app-level CRUD, unchanged) ─────────────────────

// SecretEntry is a single key returned by ListKeys. Values are never returned
// through the API — only key names.
type SecretEntry struct {
	Key string `json:"key"`
}

// Backend abstracts writing and reading app-level secrets. The MVP
// implementation uses Kubernetes Secrets directly; the 1Password flow uses
// the SA client.
type Backend interface {
	// Upsert creates or merges key/value pairs into the named secret in ns.
	// Existing keys not present in data are preserved (merge semantics).
	Upsert(ctx context.Context, ns, name string, data map[string][]byte) error

	// ListKeys returns the key names stored in the named secret in ns.
	// Returns an empty slice (not an error) when the secret does not exist.
	ListKeys(ctx context.Context, ns, name string) ([]SecretEntry, error)

	// DeleteKey removes a single key from the named secret in ns.
	// No-op when the key or secret does not exist.
	DeleteKey(ctx context.Context, ns, name, key string) error
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
	// VaultNameFmt is the naming convention for per-env vaults.
	// Args: org, env.
	VaultNameFmt = "suparship-%s-%s"
	// RotateGraceSeconds is the default grace window between issuing a
	// new Connect token and revoking the old one.
	RotateGraceSeconds = 60
)

// VaultName returns the conventional vault name for an org + environment.
func VaultName(org, env string) string {
	return fmt.Sprintf(VaultNameFmt, org, env)
}

// ConnectTokenSecretName returns the K8s Secret name that holds the per-env
// Connect token on target clusters (after being unsealed by sealed-secrets).
func ConnectTokenSecretName(env string) string {
	return "op-connect-token-" + env
}

// ClusterSecretStoreNameForEnv returns the ClusterSecretStore name for an env.
func ClusterSecretStoreNameForEnv(env string) string {
	return "onepassword-" + env
}
