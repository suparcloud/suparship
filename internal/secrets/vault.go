package secrets

import "context"

// ScopeKind identifies the variability axis of a secret: the same value
// everywhere (global), one value per environment (env), or one value per
// cluster within an environment (cluster). These three scopes replace the
// former 6-level hierarchy.
type ScopeKind string

const (
	ScopeGlobal  ScopeKind = "global"
	ScopeEnv     ScopeKind = "env"
	ScopeCluster ScopeKind = "cluster"
)

// Scope identifies which vault a secret lives in. Env is set for ScopeEnv and
// ScopeCluster (cluster overrides are per-(env, cluster) items stored inside
// the env vault); Cluster only for ScopeCluster.
type Scope struct {
	Kind    ScopeKind
	Env     string
	Cluster string
}

// GlobalScope returns the global scope.
func GlobalScope() Scope { return Scope{Kind: ScopeGlobal} }

// EnvScope returns the scope for one environment.
func EnvScope(env string) Scope { return Scope{Kind: ScopeEnv, Env: env} }

// ClusterScope returns the scope for one cluster's overrides within one
// environment. The items live in the env vault, named after the cluster.
func ClusterScope(env, cluster string) Scope {
	return Scope{Kind: ScopeCluster, Env: env, Cluster: cluster}
}

// Tier identifies ownership within a scope: TierShared holds org-admin,
// platform-wide values folded into every app; TierApp holds one app's own
// values. App values override shared values on key collision.
type Tier string

const (
	TierShared Tier = "shared"
	TierApp    Tier = "app"
)

// VaultStore abstracts read/write/delete of secret data against the configured
// backend. A "vault" is a Kubernetes namespace for the k8s backend and a real
// vault for the 1Password backend; the Scope selects which vault. Within a
// vault, items are keyed by (tier, app): the shared tier ignores app, the app
// tier scopes to a single app so apps never see each other's secrets.
//
// Suparship writes values into the vault; ESO pulls them back into app
// namespaces at sync time. Values are never returned through this interface —
// ListKeys returns key names only.
type VaultStore interface {
	// Upsert creates or merges key/value pairs into the item identified by
	// (scope, tier, app). Existing keys not present in data are preserved.
	Upsert(ctx context.Context, scope Scope, tier Tier, app string, data map[string][]byte) error

	// EnsureItem creates an empty item for (scope, tier, app) when it does not
	// yet exist, so an ExternalSecret can reference it before any keys are set.
	// No-op when the item already exists — existing keys are left untouched.
	EnsureItem(ctx context.Context, scope Scope, tier Tier, app string) error

	// ListKeys returns the key names stored in the (scope, tier, app) item.
	// Returns an empty slice (not an error) when the item does not exist.
	ListKeys(ctx context.Context, scope Scope, tier Tier, app string) ([]SecretEntry, error)

	// DeleteKey removes a single key from the (scope, tier, app) item.
	// No-op when the key or item does not exist.
	DeleteKey(ctx context.Context, scope Scope, tier Tier, app, key string) error

	// Probe verifies connectivity and access to the vault for the given scope.
	Probe(ctx context.Context, scope Scope) error
}
