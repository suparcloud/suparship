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
	// ScopeProject holds secrets shared by every app in a project, in every
	// environment. Items live in the org global vault (names are scope-unique).
	ScopeProject ScopeKind = "project"
	// ScopeProjectEnv holds secrets shared by every app in a project, in one
	// environment. Items live in that env's vault (alongside cluster overrides).
	ScopeProjectEnv ScopeKind = "projectenv"
	// ScopeStack holds secrets shared by every app in a stack, in every
	// environment. Items live in the org global vault (names are scope-unique).
	ScopeStack ScopeKind = "stack"
	// ScopeStackEnv holds secrets shared by every app in a stack, in one
	// environment. Items live in that env's vault.
	ScopeStackEnv ScopeKind = "stackenv"
	// ScopePreview holds the per-app "preview band": values folded into every
	// preview of an app on top of its base env. Items live in the base env's
	// vault (named with a preview suffix), so previews need no vault of their
	// own — the same pattern cluster overrides use.
	ScopePreview ScopeKind = "preview"
	// ScopePreviewPR holds a single preview's (PR's) override. Items live in the
	// base env's vault alongside the preview band, keyed by the preview name.
	ScopePreviewPR ScopeKind = "previewpr"
)

// Scope identifies which vault a secret lives in. Env is set for ScopeEnv,
// ScopeCluster, ScopeProjectEnv and ScopeStackEnv; Cluster only for ScopeCluster;
// Project for the project/stack scopes; Stack for the stack scopes. Cluster,
// project-env and stack-env items are per-key items stored inside the env vault.
type Scope struct {
	Kind    ScopeKind
	Env     string
	Cluster string
	Project string
	Stack   string
	// Preview is the preview (PR) name for ScopePreviewPR. Empty otherwise.
	Preview string
}

// WithProject returns a copy of the scope tagged with the owning project. It is
// used for app-tier items in the global/env/cluster/preview scopes (whose
// constructors leave Project empty): AppItemName project-qualifies the item name
// when Project is set, so a same-named app in a different project never collides
// on the shared org/env vault. Project does not affect vault selection
// (VaultName/FindVault/StoreName key on Kind only), so tagging it is inert
// everywhere except the app-tier item name.
func (s Scope) WithProject(project string) Scope {
	s.Project = project
	return s
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

// ProjectScope returns the scope for a project's secrets shared across every
// environment. The items live in the org global vault.
func ProjectScope(project string) Scope {
	return Scope{Kind: ScopeProject, Project: project}
}

// ProjectEnvScope returns the scope for a project's secrets in one environment.
// The items live in that environment's vault.
func ProjectEnvScope(project, env string) Scope {
	return Scope{Kind: ScopeProjectEnv, Project: project, Env: env}
}

// StackScope returns the scope for a stack's secrets shared across every
// environment. The items live in the org global vault.
func StackScope(project, stack string) Scope {
	return Scope{Kind: ScopeStack, Project: project, Stack: stack}
}

// StackEnvScope returns the scope for a stack's secrets in one environment.
// The items live in that environment's vault.
func StackEnvScope(project, stack, env string) Scope {
	return Scope{Kind: ScopeStackEnv, Project: project, Stack: stack, Env: env}
}

// PreviewScope returns the scope for an app's preview band — values applied to
// every preview on top of the base env. Items live in the base env's vault.
func PreviewScope(baseEnv string) Scope {
	return Scope{Kind: ScopePreview, Env: baseEnv}
}

// PreviewPRScope returns the scope for a single preview's (PR's) override.
// Items live in the base env's vault, keyed by the preview name.
func PreviewPRScope(baseEnv, preview string) Scope {
	return Scope{Kind: ScopePreviewPR, Env: baseEnv, Preview: preview}
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
// suparship writes values into the vault; ESO pulls them back into app
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

// LegacyItemMigrator is implemented by the concrete vault stores to support the
// one-shot app-tier item rename migration ({app}-* → {project}-{app}-*). It
// operates at the item-name level — below the value-blind VaultStore surface, so
// values never leak through the general API — and is consumed only by the
// migration (copy) and prune paths. Kept off VaultStore so the common interface
// and its many fakes stay unchanged; the migration uses the raw backing store
// (not the cached publish wrapper) which is a concrete type.
type LegacyItemMigrator interface {
	// CopyItem copies every key from item fromName into item toName within the
	// scope's vault (merge semantics — existing keys on toName are preserved).
	// No-op (nil) when fromName does not exist.
	CopyItem(ctx context.Context, scope Scope, fromName, toName string) error
	// DeleteItem removes the whole item named itemName from the scope's vault.
	// No-op (nil) when it does not exist. Used by prune-legacy-items after cutover.
	DeleteItem(ctx context.Context, scope Scope, itemName string) error
}
