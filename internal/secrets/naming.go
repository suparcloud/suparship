package secrets

import "fmt"

const (
	secretsOrgName       = "suparship-secrets-org"
	secretsEnvPrefix     = "suparship-secrets-envtype-"
	secretsProjPrefix    = "suparship-secrets-project-"
	secretsAppPrefix     = "suparship-secrets-app-"
	secretsClusterPrefix = "suparship-secrets-cluster-"
	secretsAppEnvFmt     = "suparship-secrets-%s-%s-%s"

	configAppEnvFmt = "suparship-config-%s-%s-%s"
)

// OrgSecretName returns the K8s Secret name for org-level secrets.
// Stored in suparship-system, replicated to all app namespaces.
func OrgSecretName() string { return secretsOrgName }

// EnvTypeSecretName returns the K8s Secret name for environment-type secrets
// (e.g. "staging", "prod").
func EnvTypeSecretName(envType string) string {
	return secretsEnvPrefix + envType
}

// ProjectSecretName returns the K8s Secret name for project-level secrets.
func ProjectSecretName(project string) string {
	return secretsProjPrefix + project
}

// AppLevelSecretName returns the K8s Secret name for app-level secrets
// (shared across all environments of the app). Stored in suparship-system.
func AppLevelSecretName(project, app string) string {
	return fmt.Sprintf("%s%s-%s", secretsAppPrefix, project, app)
}

// ClusterSecretName returns the K8s Secret name for cluster-level secrets.
// Stored in suparship-system, replicated to namespaces of apps deployed to
// the named cluster.
func ClusterSecretName(cluster string) string {
	return secretsClusterPrefix + cluster
}

// AppEnvSecretName returns the deterministic K8s Secret name for an app's
// per-environment secrets. Templates reference this via
// {{ .Values.suparship.secretName }}.
//
// Pattern: suparship-secrets-{project}-{app}-{env}
func AppEnvSecretName(project, app, env string) string {
	return fmt.Sprintf(secretsAppEnvFmt, project, app, env)
}

// AppSecretName is an alias for AppEnvSecretName for backward compatibility.
func AppSecretName(project, app, env string) string {
	return AppEnvSecretName(project, app, env)
}

// SecretNameForNamespace returns the K8s Secret name for an app's secrets
// derived from the resolved namespace. Use this variant when the caller has
// already resolved the Kubernetes namespace (e.g. via domain.ResolveNamespace)
// so the secretName is consistent with the namespace name.
//
// Pattern: suparship-secrets-{namespace}
func SecretNameForNamespace(namespace string) string {
	return "suparship-secrets-" + namespace
}

// ConfigNameForNamespace returns the K8s ConfigMap name for an app's config
// derived from the resolved namespace.
//
// Pattern: suparship-config-{namespace}
func ConfigNameForNamespace(namespace string) string {
	return "suparship-config-" + namespace
}

// AppConfigName returns the deterministic ConfigMap name for an app's
// non-secret config in a given environment. Templates reference this name
// via {{ .Values.suparship.configName }}.
//
// Pattern: suparship-config-{project}-{app}-{env}
func AppConfigName(project, app, env string) string {
	return fmt.Sprintf(configAppEnvFmt, project, app, env)
}
