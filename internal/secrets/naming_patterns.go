package secrets

import (
	"fmt"
	"regexp"
	"strings"
)

// ResourceNaming holds configurable naming patterns for the Kubernetes
// namespaces suparship generates. Secret/vault-item names are no longer
// configurable — they are deterministic (see naming.go).
type ResourceNaming struct {
	// ProjectNamespace is the org-wide default pattern for project-level
	// Kubernetes namespaces. Tokens: {org}, {project}, {env}.
	// Empty: topology-aware default is applied at resolution time.
	//   Shared cluster:    "{project}-{env}" → "myproject-staging"
	//   Dedicated cluster: "{project}"       → "myproject"
	ProjectNamespace string `json:"projectNamespace,omitempty" yaml:"projectNamespace,omitempty"`

	// AppNamespace is the org-wide default pattern for app-level
	// Kubernetes namespaces. Tokens: {org}, {project}, {app}, {env}.
	// Empty: topology-aware default is applied at resolution time.
	//   Shared cluster:    "{project}-{app}-{env}" → "myproject-api-staging"
	//   Dedicated cluster: "{project}-{app}"       → "myproject-api"
	AppNamespace string `json:"appNamespace,omitempty" yaml:"appNamespace,omitempty"`

	// ArgoAppName is the org-wide pattern for ArgoCD Application names.
	// Tokens: {project}, {app}, {env}, {cluster}. Empty → DefaultArgoAppName
	// ("{project}-{app}-{cluster}"). Per-cluster by default so an app deployed to
	// N clusters in an env yields N stably-named Applications and adding a cluster
	// never renames the existing one. Must contain {app} and {cluster} so names
	// stay unique across projects and across the clusters an env fans out to;
	// cluster names are expected to carry an env prefix (e.g. "staging-eastus"),
	// so {env} is omitted from the default to avoid redundancy.
	ArgoAppName string `json:"argoAppName,omitempty" yaml:"argoAppName,omitempty"`
}

// DefaultArgoAppName is the ArgoCD Application name pattern used when
// ResourceNaming.ArgoAppName is unset. See the ArgoAppName field docs.
const DefaultArgoAppName = "{project}-{app}-{cluster}"

// ── Hardcoded conventions ─────────────────────────────────────────────────

const (
	// OnePasswordRemoteNamespace is the namespace on TARGET clusters where
	// ESO and the sealed Connect-token Secret live.
	OnePasswordRemoteNamespace = "external-secrets"
)

// NamingParams holds the concrete values that replace tokens in patterns.
type NamingParams struct {
	Org     string
	Env     string
	Project string
	App     string
	Cluster string
	Stack   string
}

// dns1123 validates K8s resource names (lowercase, alphanumeric, '-').
var dns1123 = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// RenderPattern replaces tokens in pattern with values from params.
func RenderPattern(pattern string, params NamingParams) string {
	r := strings.NewReplacer(
		"{org}", params.Org,
		"{env}", params.Env,
		"{project}", params.Project,
		"{app}", params.App,
		"{cluster}", params.Cluster,
		"{stack}", params.Stack,
	)
	return r.Replace(pattern)
}

// ValidateRendered checks that a rendered name is non-empty and whitespace-free.
func ValidateRendered(rendered, context string) error {
	if rendered == "" {
		return fmt.Errorf("naming: %s rendered to empty string", context)
	}
	if strings.ContainsAny(rendered, " \t\n") {
		return fmt.Errorf("naming: %s contains whitespace: %q", context, rendered)
	}
	return nil
}

// ValidateRenderedDNS1123 validates a rendered K8s resource name.
func ValidateRenderedDNS1123(rendered, context string) error {
	if err := ValidateRendered(rendered, context); err != nil {
		return err
	}
	if !dns1123.MatchString(rendered) {
		return fmt.Errorf("naming: %s is not a valid DNS-1123 name: %q", context, rendered)
	}
	return nil
}

// ValidateRenderedNamespace validates a rendered Kubernetes namespace name.
// Extends ValidateRenderedDNS1123 with the 63-character RFC 1123 label limit.
func ValidateRenderedNamespace(rendered, context string) error {
	if err := ValidateRenderedDNS1123(rendered, context); err != nil {
		return err
	}
	if len(rendered) > 63 {
		return fmt.Errorf("naming: %s exceeds 63-char Kubernetes limit: %q (%d chars)", context, rendered, len(rendered))
	}
	return nil
}

// EffectiveProjectNamespace returns the org-wide project namespace pattern.
// Empty string means no org default is set; topology decides at resolution time.
func (n ResourceNaming) EffectiveProjectNamespace() string { return n.ProjectNamespace }

// EffectiveAppNamespace returns the org-wide app namespace pattern.
// Empty string means no org default is set; topology decides at resolution time.
func (n ResourceNaming) EffectiveAppNamespace() string { return n.AppNamespace }

// EffectiveArgoAppName returns the configured ArgoCD Application name pattern,
// or DefaultArgoAppName when unset.
func (n ResourceNaming) EffectiveArgoAppName() string {
	if n.ArgoAppName == "" {
		return DefaultArgoAppName
	}
	return n.ArgoAppName
}

// Validate checks that the configured namespace patterns produce valid names.
func (n ResourceNaming) Validate() error {
	sample := NamingParams{Org: "default", Env: "prod", Project: "acme", App: "web", Cluster: "prod-us-east"}
	if n.ProjectNamespace != "" {
		if err := ValidateRenderedNamespace(RenderPattern(n.ProjectNamespace, sample), "projectNamespace"); err != nil {
			return err
		}
	}
	if n.AppNamespace != "" {
		if err := ValidateRenderedNamespace(RenderPattern(n.AppNamespace, sample), "appNamespace"); err != nil {
			return err
		}
	}
	if n.ArgoAppName != "" {
		// {app} + {cluster} are required: {app} for cross-project uniqueness,
		// {cluster} so the N Applications of a multi-cluster env don't collide.
		if !strings.Contains(n.ArgoAppName, "{app}") || !strings.Contains(n.ArgoAppName, "{cluster}") {
			return fmt.Errorf("naming: argoAppName must contain {app} and {cluster} tokens, got %q", n.ArgoAppName)
		}
		if err := ValidateRenderedDNS1123(RenderPattern(n.ArgoAppName, sample), "argoAppName"); err != nil {
			return err
		}
	}
	return nil
}
