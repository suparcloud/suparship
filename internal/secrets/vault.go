package secrets

import "context"

// Scope identifies the hierarchy level and coordinates for a secret write.
// Used by VaultWriter to determine Item naming in the external vault.
type Scope struct {
	Level   string // LevelOrg | LevelEnvironment | LevelProject | LevelApp | LevelAppEnv | LevelCluster
	Org     string
	Env     string // empty for org-level
	Project string // empty for org/env levels
	App     string // empty unless app/app-env
	Cluster string // populated only for LevelCluster
}

// ItemMeta carries provider-supplied metadata about a vault item.
type ItemMeta struct {
	// Version is an opaque, provider-supplied identifier (e.g. 1Password
	// Item version). Used for optimistic concurrency control.
	Version string
}

// VaultWriter abstracts write/read/delete of secret data against an external
// vault provider (1Password via SA token) or native K8s Secrets for the
// demo profile. Suparship writes values into the vault; ESO pulls them back
// into the cluster at sync time.
type VaultWriter interface {
	// Upsert creates or merges key/value pairs into the scope's Item in the
	// vault identified by binding. If expectedVersion is non-empty, it must
	// match the current Item version or the call returns ErrStaleVersion.
	Upsert(ctx context.Context, binding EnvBinding, scope Scope, expectedVersion string, data map[string][]byte) (ItemMeta, error)

	// ListKeys returns the key names stored in the scope's Item.
	// Returns an empty slice (not an error) when the Item does not exist.
	ListKeys(ctx context.Context, binding EnvBinding, scope Scope) ([]SecretEntry, ItemMeta, error)

	// DeleteKey removes a single key from the scope's Item.
	// No-op when the key or Item does not exist.
	DeleteKey(ctx context.Context, binding EnvBinding, scope Scope, key, expectedVersion string) (ItemMeta, error)

	// DeleteItem removes the entire Item for the given scope from the vault.
	// No-op when the Item does not exist.
	DeleteItem(ctx context.Context, binding EnvBinding, scope Scope) error

	// Probe verifies connectivity and access to the vault for the given
	// binding.
	Probe(ctx context.Context, binding EnvBinding) error
}
