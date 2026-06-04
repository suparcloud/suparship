package secrets

import "fmt"

// ── Vault names (physical storage) ────────────────────────────────────────
//
// For the k8s backend a "vault" is a namespace; for 1Password it is a real
// vault (these helpers give the k8s namespace / the conventional 1Password
// vault title). One global vault, one vault per environment, one per cluster.

const (
	globalVaultName    = "suparship-secrets-global"
	envVaultPrefix     = "suparship-secrets-env-"
	clusterVaultPrefix = "suparship-secrets-cluster-"

	configAppEnvFmt = "suparship-config-%s-%s-%s"
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

// ── Workload secret names (the 3 K8s Secrets mounted into each app pod) ────

// WorkloadGlobalSecretName returns the K8s Secret name ESO materializes in the
// app namespace for global-scope secrets.
func WorkloadGlobalSecretName(app string) string { return app + "-global" }

// WorkloadEnvSecretName returns the K8s Secret name for env-scope secrets.
func WorkloadEnvSecretName(app string) string { return app + "-env" }

// WorkloadClusterSecretName returns the K8s Secret name for cluster-scope secrets.
func WorkloadClusterSecretName(app string) string { return app + "-cluster" }

// WorkloadSecretName returns the materialized K8s Secret name for the given
// scope and app.
func WorkloadSecretName(scope Scope, app string) string {
	switch scope.Kind {
	case ScopeEnv:
		return WorkloadEnvSecretName(app)
	case ScopeCluster:
		return WorkloadClusterSecretName(app)
	default:
		return WorkloadGlobalSecretName(app)
	}
}

// ── ConfigMap naming (non-secret env vars; unchanged) ──────────────────────

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

// AppConfigName returns the deterministic ConfigMap name for an app's
// non-secret config in a given environment.
//
// Pattern: suparship-config-{project}-{app}-{env}
func AppConfigName(project, app, env string) string {
	return fmt.Sprintf(configAppEnvFmt, project, app, env)
}
