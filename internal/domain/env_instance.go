package domain

import (
	"strings"
	"time"

	"github.com/suparcloud/suparship/internal/secrets"
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
//
//	AppEnvironment is a transitional bridge type used by the compat layer.
//	EnvironmentInstance is the intended long-term model. New code should use
//	EnvironmentInstance; AppEnvironment will be deprecated once the migration
//	from service-centric paths is complete.
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

// GenerateProjectNamespace derives the Kubernetes namespace including the
// project name using the "{project}-{app}-{env}" pattern. This is the default
// for new apps to avoid namespace collisions across projects on a shared cluster.
//
// Examples:
//
//	"acme", "api", "staging" → "acme-api-staging"
//	"acme", "api", "prod"    → "acme-api-prod"
func GenerateProjectNamespace(projectName, appName, envName string) string {
	return GenerateNamespaceFromPattern(appName, envName, projectName, "{project}-{app}-{env}")
}

// DefaultPreviewNamespacePattern is the namespace pattern used for preview
// environments when a project configures none. It is project-scoped (so
// previews of same-named apps in different projects never collide on a shared
// cluster) and carries the preview name so each PR gets a distinct namespace.
const DefaultPreviewNamespacePattern = "{project}-{app}-preview-{name}"

// GeneratePreviewNamespaceFromPattern resolves the Kubernetes namespace for a
// preview environment from a pattern. Supported tokens:
//
//	{project}  → projectName
//	{app}      → appName
//	{name}     → previewName (e.g. "pr-42")
//
// When pattern is empty, DefaultPreviewNamespacePattern is used. The pattern is
// expected to be validated with ValidatePreviewNamespacePattern (which requires
// the {name} token, so previews never collide) before reaching here.
//
// Examples:
//
//	GeneratePreviewNamespaceFromPattern("hello", "pr-42", "demo", "")               → "demo-hello-preview-pr-42"
//	GeneratePreviewNamespaceFromPattern("hello", "pr-42", "demo", "{app}-{name}")   → "hello-pr-42"
func GeneratePreviewNamespaceFromPattern(appName, previewName, projectName, pattern string) string {
	if pattern == "" {
		pattern = DefaultPreviewNamespacePattern
	}
	ns := strings.ReplaceAll(pattern, "{app}", appName)
	ns = strings.ReplaceAll(ns, "{name}", previewName)
	ns = strings.ReplaceAll(ns, "{project}", projectName)
	return ns
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
//
// baseDomain is the ROOT domain (e.g. "localhost", "acme.com"). The
// environment tier is always prepended so that staging and prod are
// reachable at different hostnames within the same root domain.
func GenerateURLWithDomain(appName, envName string, envType AppEnvironmentType, domain string) string {
	switch envType {
	case AppEnvPreview:
		return "http://" + envName + "." + appName + ".preview." + domain
	default:
		return "http://" + appName + "." + string(envType) + "." + domain
	}
}

// IsDedicatedClusterTopology returns true when every environment in
// stableClusterRefs has a distinct, non-empty cluster reference — meaning each
// environment runs on its own cluster and namespace names do not need an
// {env} suffix for uniqueness.
//
// stableClusterRefs should contain only the ClusterRef values of stable
// (non-preview) environments. Preview environments are excluded by callers
// because they always share a cluster.
//
// Returns false when the slice is empty or any ref is empty or duplicated.
func IsDedicatedClusterTopology(stableClusterRefs []string) bool {
	if len(stableClusterRefs) == 0 {
		return false
	}
	seen := make(map[string]bool, len(stableClusterRefs))
	for _, ref := range stableClusterRefs {
		if ref == "" || seen[ref] {
			return false
		}
		seen[ref] = true
	}
	return true
}

// NamespaceResolveInput carries all inputs needed to resolve the effective
// Kubernetes namespace for an app environment instance. Each pattern field
// corresponds to one level of the Org → Env → Project → App hierarchy;
// empty string means "not set at this level — fall through to next."
type NamespaceResolveInput struct {
	AppName     string
	EnvName     string
	ProjectName string
	OrgName     string

	// Scope controls whether the app deploys into a dedicated app namespace
	// ("app", default) or the shared project namespace ("project").
	// Empty is treated as NamespaceScopeApp.
	Scope NamespaceScope

	// Dedicated indicates that stable environments each run on their own
	// cluster, so the {env} suffix is not needed for uniqueness.
	// Derive this with IsDedicatedClusterTopology.
	Dedicated bool

	// StackName + StackShared co-locate an app's workloads in a shared stack
	// namespace when the app belongs to a stack with SharedNamespace=true. This
	// takes precedence over the app/project scope so stack members reach each
	// other by in-cluster DNS. StackPattern (StackSpec.NamespacePattern) overrides
	// the default {project}-{stack}[-{env}]. Tokens: {org},{project},{stack},{env}.
	StackName    string
	StackShared  bool
	StackPattern string

	// Pattern overrides — highest priority first.
	// Empty string = not set at this level; fall through to the next level.
	AppPattern        string // AppSpec.NamespacePattern
	ProjectPattern    string // ProjectSpec.NamespacePattern
	OrgEnvAppPattern  string // OrgEnvironment.EffectiveAppNamespacePattern (scope=app)
	OrgEnvProjPattern string // OrgEnvironment.EffectiveProjectNamespacePattern (scope=project)
	OrgAppDefault     string // ResourceNaming.EffectiveAppNamespace()
	OrgProjectDefault string // ResourceNaming.EffectiveProjectNamespace()
}

// ResolveNamespace resolves the effective Kubernetes namespace for an app
// environment instance by walking the precedence chain defined in
// NamespaceResolveInput. It validates the result against Kubernetes namespace
// naming rules (DNS-1123, max 63 chars).
//
// Per-env patterns are scope-specific: an OrgEnvironment can carry one
// override for app-scope and a different one for project-scope, because a
// pattern like "{app}" is meaningful for dedicated namespaces but produces
// confusing results when applied to a shared project namespace.
//
// Precedence (highest to lowest):
//
//	scope=app:
//	  1. AppPattern        (AppSpec.NamespacePattern)
//	  2. ProjectPattern    (ProjectSpec.NamespacePattern)
//	  3. OrgEnvAppPattern  (OrgEnvironment.AppNamespacePattern, with legacy fallback)
//	  4. OrgAppDefault     (ResourceNaming.AppNamespace)
//	  5. Topology default
//
//	scope=project:
//	  1. ProjectPattern    (ProjectSpec.NamespacePattern)
//	  2. OrgEnvProjPattern (OrgEnvironment.ProjectNamespacePattern)
//	  3. OrgProjectDefault (ResourceNaming.ProjectNamespace)
//	  4. Topology default
func ResolveNamespace(in NamespaceResolveInput) (string, error) {
	var pattern string
	switch {
	case in.StackShared && in.StackName != "":
		// Shared stack namespace: co-locate the stack's apps so they reach each
		// other by in-cluster DNS. Wins over app/project scope.
		pattern = in.StackPattern
		if pattern == "" {
			if in.Dedicated {
				pattern = "{project}-{stack}"
			} else {
				pattern = "{project}-{stack}-{env}"
			}
		}
		ns := secrets.RenderPattern(pattern, secrets.NamingParams{
			Org: in.OrgName, Env: in.EnvName, Project: in.ProjectName, Stack: in.StackName,
		})
		return ns, secrets.ValidateRenderedNamespace(ns, "namespace")
	}
	switch in.Scope {
	case NamespaceScopeProject:
		pattern = firstNonEmpty(in.ProjectPattern, in.OrgEnvProjPattern, in.OrgProjectDefault)
		if pattern == "" {
			if in.Dedicated {
				pattern = "{project}"
			} else {
				pattern = "{project}-{env}"
			}
		}
	default: // NamespaceScopeApp (default when empty)
		pattern = firstNonEmpty(in.AppPattern, in.ProjectPattern, in.OrgEnvAppPattern, in.OrgAppDefault)
		if pattern == "" {
			if in.Dedicated {
				pattern = "{project}-{app}"
			} else {
				pattern = "{project}-{app}-{env}"
			}
		}
	}

	ns := secrets.RenderPattern(pattern, secrets.NamingParams{
		Org:     in.OrgName,
		Env:     in.EnvName,
		Project: in.ProjectName,
		App:     in.AppName,
	})
	return ns, secrets.ValidateRenderedNamespace(ns, "namespace")
}

// firstNonEmpty returns the first non-empty string from vals, or "" if all
// are empty.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
