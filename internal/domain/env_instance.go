package domain

import (
	"strings"
	"time"
)

// EnvironmentInstance is a running deployment of an app in one environment.
// It is the canonical runtime-instance type for the app model.
//
// Design principles:
//   - Desired configuration always lives in App.Spec (AppSpec). This type
//     holds only observed/assigned runtime state.
//   - Deterministic: given the same app name, env name, and env type the
//     helpers GenerateNamespace and GenerateURL always produce the same output.
//   - No Kubernetes-specific fields. Namespace is a plain string; the cluster
//     layer translates it into a real Kubernetes namespace object.
//
// Relationship to AppEnvironment:
//   AppEnvironment is a transitional bridge type used by the compat layer.
//   EnvironmentInstance is the intended long-term model. New code should use
//   EnvironmentInstance; AppEnvironment will be deprecated once the migration
//   from service-centric paths is complete.
type EnvironmentInstance struct {
	// AppName is the app this instance belongs to.
	AppName string `json:"appName"`
	// ProjectName is the owning project.
	ProjectName string `json:"projectName"`
	// EnvType classifies this instance as staging, prod, or preview.
	EnvType AppEnvironmentType `json:"envType"`
	// EnvName is the logical name of this environment instance.
	// For stable environments this mirrors EnvType (e.g. "staging", "prod").
	// For previews this is the preview name (e.g. "pr-42").
	EnvName string `json:"envName"`
	// Namespace is the Kubernetes namespace assigned to this instance.
	// Derive deterministically with GenerateNamespace when not set from storage.
	Namespace string `json:"namespace"`
	// URL is the primary ingress hostname for this instance.
	// Derive deterministically with GenerateURL when not set from storage.
	// Empty when the instance has not been exposed or no ingress is configured.
	URL string `json:"url,omitempty"`
	// Release identifies the version currently deployed to this instance.
	// Nil when nothing has been promoted here yet.
	Release *AppReleaseRef `json:"release,omitempty"`
	// Status is the live runtime health summary observed from the cluster.
	// MUST NOT be stored as desired config.
	Status AppRuntimeStatus `json:"status"`
	// CreatedAt is the time this environment instance was first provisioned.
	CreatedAt time.Time `json:"createdAt"`

	// ClusterName is the name of the registered Cluster this instance runs on.
	// Populated at read time from the ClusterStore; not persisted in storage.
	ClusterName string `json:"clusterName,omitempty"`
	// ClusterServer is the Kubernetes API server URL for this instance's cluster.
	// Populated at read time from the ClusterStore; used by the GitOps publisher
	// to set spec.destination.server in ArgoCD ApplicationSets.
	ClusterServer string `json:"clusterServer,omitempty"`
}

// GenerateNamespaceFromPattern resolves a Kubernetes namespace for an app in
// an environment using a pattern string. Supported tokens:
//
//	{app}      → appName
//	{env}      → envName
//	{project}  → projectName
//
// When pattern is empty, the default "{app}-{env}" is used, which is safe for
// both shared and dedicated clusters. Must be called with a pattern that
// contains at least the {app} token.
//
// Examples:
//
//	GenerateNamespaceFromPattern("hello", "staging", "demo", "{app}")         → "hello"
//	GenerateNamespaceFromPattern("hello", "staging", "demo", "{app}-{env}")   → "hello-staging"
//	GenerateNamespaceFromPattern("hello", "staging", "demo", "{project}-{app}") → "demo-hello"
func GenerateNamespaceFromPattern(appName, envName, projectName, pattern string) string {
	if pattern == "" {
		pattern = "{app}-{env}"
	}
	ns := strings.ReplaceAll(pattern, "{app}", appName)
	ns = strings.ReplaceAll(ns, "{env}", envName)
	ns = strings.ReplaceAll(ns, "{project}", projectName)
	return ns
}

// GenerateNamespace derives the Kubernetes namespace for an environment
// instance using the default "{app}-{env}" pattern.
//
// Convention (all env types):
//
//	staging  →  {appName}-staging     e.g. "hello-staging"
//	prod     →  {appName}-prod        e.g. "hello-prod"
//	preview  →  {appName}-{envName}   e.g. "hello-pr-42"
//
// For environments with a dedicated cluster, prefer calling
// GenerateNamespaceFromPattern with the environment's NamespacePattern, which
// allows a clean "{app}"-only namespace when no env suffix is needed.
//
// Both appName and envName are expected to be valid DNS labels; callers should
// validate them with ValidateAppName / ValidatePreviewName before calling this
// function if the values are user-supplied.
func GenerateNamespace(appName, envName string, _ AppEnvironmentType) string {
	return GenerateNamespaceFromPattern(appName, envName, "", "")
}

// GenerateURL derives the primary ingress URL for an environment instance.
//
// URL patterns (using the default "localhost" base domain for local/demo):
//
//	staging  →  http://{appName}.staging.localhost
//	prod     →  http://{appName}.prod.localhost
//	preview  →  http://{envName}.{appName}.preview.localhost
//
// The localhost domain is the MVP default for demo and local development
// clusters. Production deployments override this at the ingress layer, so
// callers that need a configurable domain should call GenerateURLWithDomain.
func GenerateURL(appName, envName string, envType AppEnvironmentType) string {
	return GenerateURLWithDomain(appName, envName, envType, "localhost")
}

// GenerateURLWithDomain is like GenerateURL but accepts an explicit base
// domain, enabling callers to generate URLs for non-localhost clusters.
//
// URL patterns:
//
//	staging  →  http://{appName}.staging.{domain}
//	prod     →  http://{appName}.prod.{domain}
//	preview  →  http://{envName}.{appName}.preview.{domain}
func GenerateURLWithDomain(appName, envName string, envType AppEnvironmentType, domain string) string {
	switch envType {
	case AppEnvPreview:
		return "http://" + envName + "." + appName + ".preview." + domain
	default:
		return "http://" + appName + "." + string(envType) + "." + domain
	}
}
