package domain

import "time"

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
}

// GenerateNamespace derives the Kubernetes namespace for an environment
// instance from its app name, environment name, and environment type.
//
// Convention:
//
//	staging/prod  →  {appName}-{envName}          e.g. "hello-staging"
//	preview       →  {appName}-preview-{envName}  e.g. "hello-preview-pr-42"
//
// Both appName and envName are expected to be valid DNS labels; callers should
// validate them with ValidateAppName / ValidatePreviewName before calling this
// function if the values are user-supplied.
func GenerateNamespace(appName, envName string, envType AppEnvironmentType) string {
	if envType == AppEnvPreview {
		return appName + "-preview-" + envName
	}
	return appName + "-" + envName
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
