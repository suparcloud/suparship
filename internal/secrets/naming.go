package secrets

// ── Vault names (physical storage) ────────────────────────────────────────
//
// For the k8s backend a "vault" is a namespace; for 1Password it is a real
// vault (these helpers give the k8s namespace / the conventional 1Password
// vault title). One global vault plus one vault per environment. Cluster
// overrides are items inside the env vault, not a vault of their own.

const (
	globalVaultName = "suparship-secrets-global"
	envVaultPrefix  = "suparship-secrets-env-"
)

// GlobalVaultName returns the vault name for global-scope secrets.
func GlobalVaultName() string { return globalVaultName }

// EnvVaultName returns the vault name for an environment's secrets.
func EnvVaultName(env string) string { return envVaultPrefix + env }

// VaultName returns the vault name for the given scope. Cluster-scope items
// live in their environment's vault.
func VaultName(scope Scope) string {
	switch scope.Kind {
	case ScopeEnv, ScopeCluster, ScopeProjectEnv, ScopeStackEnv:
		return EnvVaultName(scope.Env)
	default:
		// ScopeGlobal, ScopeProject and ScopeStack all live in the org global
		// vault — items are scope-unique by name, so they don't collide.
		return GlobalVaultName()
	}
}

// ── Item names (a vault holds a shared item plus one item per app; an env
// vault additionally holds cluster-override items per bound cluster) ────────

// scopeSuffix renders the scope-specific tail used in item and store names:
// "global", "env-<env>", "cluster-<cluster>", "project-<project>", or
// "project-<project>-env-<env>".
func scopeSuffix(scope Scope) string {
	switch scope.Kind {
	case ScopeEnv:
		return "env-" + scope.Env
	case ScopeCluster:
		return "cluster-" + scope.Cluster
	case ScopeProject:
		return "project-" + scope.Project
	case ScopeProjectEnv:
		return "project-" + scope.Project + "-env-" + scope.Env
	case ScopeStack:
		return "stack-" + scope.Project + "-" + scope.Stack
	case ScopeStackEnv:
		return "stack-" + scope.Project + "-" + scope.Stack + "-env-" + scope.Env
	default:
		return "global"
	}
}

// ScopeKey returns the scope-specific suffix used to key per-scope resources
// (file names, sealed-token names): "global", "env-<env>", "cluster-<cluster>".
func ScopeKey(scope Scope) string { return scopeSuffix(scope) }

// SharedItemName returns the item name for the org-admin shared tier in a
// scope's vault (e.g. "shared-global", "shared-env-staging").
func SharedItemName(scope Scope) string {
	return "shared-" + scopeSuffix(scope)
}

// AppItemName returns the item name for one app's secrets in a scope's vault
// (e.g. "myapp-global", "myapp-env-staging", "myapp-cluster-prod-us").
func AppItemName(scope Scope, app string) string {
	return app + "-" + scopeSuffix(scope)
}

// ItemName returns the item name for the given (scope, tier, app). For the
// shared tier app is ignored.
func ItemName(scope Scope, tier Tier, app string) string {
	if tier == TierApp {
		return AppItemName(scope, app)
	}
	return SharedItemName(scope)
}

// ── ClusterSecretStore names ────────────────────────────────────────────────
//
// 1Password backend: every workload cluster runs ONE ClusterSecretStore with
// the fixed name UnifiedStoreName, whose provider lists all the vaults that
// cluster reads (global + its bound envs). k8s backend: one store per
// vault/namespace (StoreName), since the kubernetes provider reads exactly
// one remoteNamespace per store.

// unifiedStoreName is the fixed per-cluster ClusterSecretStore name for the
// 1Password backend. Identical on every cluster, so app ExternalSecrets are
// cluster-agnostic and need no per-entry sourceRef.
const unifiedStoreName = "suparship-store"

// UnifiedStoreName returns the fixed ClusterSecretStore name used by the
// 1Password backend on every workload cluster.
func UnifiedStoreName() string { return unifiedStoreName }

// StoreName returns the per-vault ClusterSecretStore name for the given scope
// (k8s backend). Cluster scope reads from its environment's store — cluster
// items live in the env vault, so item and store suffixes intentionally
// diverge for cluster scope.
func StoreName(scope Scope) string {
	switch scope.Kind {
	case ScopeCluster, ScopeProjectEnv, ScopeStackEnv:
		// All live in the env vault → read from the env store.
		return "suparship-store-" + scopeSuffix(EnvScope(scope.Env))
	case ScopeProject, ScopeStack:
		// Live in the global vault → read from the global store.
		return "suparship-store-" + scopeSuffix(GlobalScope())
	default:
		return "suparship-store-" + scopeSuffix(scope)
	}
}

// GlobalStoreName returns the ClusterSecretStore name for the global vault.
func GlobalStoreName() string { return StoreName(GlobalScope()) }

// EnvStoreName returns the ClusterSecretStore name for an env vault.
func EnvStoreName(env string) string { return StoreName(EnvScope(env)) }

// ── Workload object names (one Secret + one ConfigMap per app pod) ─────────
//
// ESO merges all present scopes (global/env/cluster, shared+app) into a single
// Secret; the publisher writes a single ConfigMap. Application charts envFrom
// exactly these two names.

// AppSecretName returns the single K8s Secret name ESO materializes in the app
// namespace, merged across all scopes.
func AppSecretName(app string) string { return app + "-secrets" }

// AppConfigMapName returns the single ConfigMap name the publisher writes for
// the app's non-secret env vars.
func AppConfigMapName(app string) string { return app + "-config" }

// ── ConfigMap naming (resolved-namespace variants) ────────────────────────

// SecretNameForNamespace returns the K8s Secret name for an app's secrets
// derived from the resolved namespace.
func SecretNameForNamespace(namespace string) string {
	return "suparship-secrets-" + namespace
}

// ConfigNameForNamespace returns the K8s ConfigMap name for an app's config
// derived from the resolved namespace.
func ConfigNameForNamespace(namespace string) string {
	return "suparship-config-" + namespace
}
