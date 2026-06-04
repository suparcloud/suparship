package secrets

// ── Vault names (physical storage) ────────────────────────────────────────
//
// For the k8s backend a "vault" is a namespace; for 1Password it is a real
// vault (these helpers give the k8s namespace / the conventional 1Password
// vault title). One global vault, one vault per environment, one per cluster.

const (
	globalVaultName    = "suparship-secrets-global"
	envVaultPrefix     = "suparship-secrets-env-"
	clusterVaultPrefix = "suparship-secrets-cluster-"
)

// GlobalVaultName returns the vault name for global-scope secrets.
func GlobalVaultName() string { return globalVaultName }

// EnvVaultName returns the vault name for an environment's secrets.
func EnvVaultName(env string) string { return envVaultPrefix + env }

// ClusterVaultName returns the vault name for a cluster's secrets.
func ClusterVaultName(cluster string) string { return clusterVaultPrefix + cluster }

// VaultName returns the vault name for the given scope.
func VaultName(scope Scope) string {
	switch scope.Kind {
	case ScopeEnv:
		return EnvVaultName(scope.Env)
	case ScopeCluster:
		return ClusterVaultName(scope.Cluster)
	default:
		return GlobalVaultName()
	}
}

// ── Item names (a vault holds a shared item plus one item per app) ─────────

// scopeSuffix renders the scope-specific tail used in item and store names:
// "global", "env-<env>", or "cluster-<cluster>".
func scopeSuffix(scope Scope) string {
	switch scope.Kind {
	case ScopeEnv:
		return "env-" + scope.Env
	case ScopeCluster:
		return "cluster-" + scope.Cluster
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

// ── ClusterSecretStore names (one ESO store per vault/scope) ───────────────

// StoreName returns the ClusterSecretStore name for the given scope.
func StoreName(scope Scope) string {
	return "suparship-store-" + scopeSuffix(scope)
}

// GlobalStoreName returns the ClusterSecretStore name for the global vault.
func GlobalStoreName() string { return StoreName(GlobalScope()) }

// EnvStoreName returns the ClusterSecretStore name for an env vault.
func EnvStoreName(env string) string { return StoreName(EnvScope(env)) }

// ClusterStoreName returns the ClusterSecretStore name for a cluster vault.
func ClusterStoreName(cluster string) string { return StoreName(ClusterScope(cluster)) }

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
