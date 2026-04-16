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
	BackendK8s   BackendType = "k8s"
	BackendVault BackendType = "vault"
	BackendAWSSM BackendType = "aws-sm"
)

// ValidBackendTypes is the set of recognized backend type strings.
var ValidBackendTypes = map[BackendType]bool{
	BackendK8s:   true,
	BackendVault: true,
	BackendAWSSM: true,
}

// BackendConfig is the org-level configuration for the secret storage backend.
// Stored as part of the Org struct in the suparship-org-config ConfigMap.
type BackendConfig struct {
	// Type selects the secret backend. Defaults to "k8s" when empty.
	Type BackendType `json:"type" yaml:"type"`
}

// Effective returns the backend type, falling back to "k8s" when empty.
func (c BackendConfig) Effective() BackendType {
	if c.Type == "" {
		return BackendK8s
	}
	return c.Type
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
