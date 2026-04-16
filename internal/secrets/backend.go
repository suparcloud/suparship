// Package secrets provides a simple key/value secret management layer for
// suparShip apps. Developers enter KEY=value pairs through the UI; the
// platform writes them to a backend (Kubernetes Secrets in MVP) using
// deterministic, well-known resource names that Helm templates consume.
//
// The secret backend type is configured once at the org level. Developers
// never interact with provider details — they only see key names.
package secrets

import "context"

// BackendType identifies the secret storage backend.
type BackendType string

const (
	BackendK8s        BackendType = "k8s"
	Backend1Password  BackendType = "1password"
	BackendVault      BackendType = "vault"
	BackendAWSSM      BackendType = "aws-sm"
)

// ValidBackendTypes is the set of recognized backend type strings.
var ValidBackendTypes = map[BackendType]bool{
	BackendK8s:       true,
	Backend1Password: true,
	BackendVault:     true,
	BackendAWSSM:     true,
}

// OnePasswordMode selects how suparship connects to 1Password.
type OnePasswordMode string

const (
	// OnePasswordModeConnect uses 1Password Connect Server (self-hosted).
	OnePasswordModeConnect OnePasswordMode = "connect"
	// OnePasswordModeServiceAccount uses 1Password Service Accounts (cloud).
	OnePasswordModeServiceAccount OnePasswordMode = "service-account"
)

// OnePasswordConfig holds 1Password-specific configuration.
type OnePasswordConfig struct {
	// Mode selects the connection mode: "connect" or "service-account".
	Mode OnePasswordMode `json:"mode" yaml:"mode"`
	// ConnectHost is the URL of the 1Password Connect server (connect mode only).
	ConnectHost string `json:"connectHost,omitempty" yaml:"connectHost,omitempty"`
	// ExistingSecret is the name of a K8s Secret containing the 1Password
	// token. Expected key: "token".
	ExistingSecret string `json:"existingSecret,omitempty" yaml:"existingSecret,omitempty"`
	// Vaults maps environment names to 1Password vault UUIDs.
	// Each environment's secrets are read from the corresponding vault.
	Vaults map[string]string `json:"vaults,omitempty" yaml:"vaults,omitempty"`
}

// Validate returns an error if the 1Password config is incomplete.
func (c *OnePasswordConfig) Validate() error {
	switch c.Mode {
	case OnePasswordModeConnect:
		if c.ConnectHost == "" {
			return ErrOnePasswordMissingConnectHost
		}
	case OnePasswordModeServiceAccount:
		// service-account mode needs only the token secret
	case "":
		return ErrOnePasswordMissingMode
	default:
		return ErrOnePasswordInvalidMode
	}
	if c.ExistingSecret == "" {
		return ErrOnePasswordMissingSecret
	}
	return nil
}

// BackendConfig is the org-level configuration for the secret storage backend.
// Stored as part of the Org struct in the suparship-org-config ConfigMap.
type BackendConfig struct {
	// Type selects the secret backend. Defaults to "k8s" when empty.
	Type BackendType `json:"type" yaml:"type"`
	// OnePassword holds 1Password-specific configuration.
	// Only used when Type is "1password".
	OnePassword *OnePasswordConfig `json:"onePassword,omitempty" yaml:"onePassword,omitempty"`
}

// Effective returns the backend type, falling back to "k8s" when empty.
func (c BackendConfig) Effective() BackendType {
	if c.Type == "" {
		return BackendK8s
	}
	return c.Type
}

// Validate checks the backend config for completeness.
func (c BackendConfig) Validate() error {
	if !ValidBackendTypes[c.Effective()] {
		return ErrInvalidBackendType
	}
	if c.Effective() == Backend1Password {
		if c.OnePassword == nil {
			return ErrOnePasswordMissingConfig
		}
		return c.OnePassword.Validate()
	}
	return nil
}

// SecretEntry is a single key returned by ListKeys. Values are never returned
// through the API — only key names.
type SecretEntry struct {
	Key string `json:"key"`
}

// Backend abstracts writing and reading app-level secrets. The MVP
// implementation uses Kubernetes Secrets directly; future implementations
// may target Vault or AWS Secrets Manager.
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
