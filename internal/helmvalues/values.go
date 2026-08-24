// Package helmvalues builds the per-instance platform context for suparship
// apps: the PlatformValues block behind every ((platform.*)) interpolation
// token, derived deterministically from an App and its target (env, cluster).
//
// suparship never injects a values schema into user charts. Published
// values.yaml is the user's own overlay; the platform↔chart contract is the
// ((platform.*))/((vars.*)) tokens this package's mappers feed (identity,
// routing context, env ConfigMap/Secret names, preview name/tag), resolved by
// internal/platform at publish time.
package helmvalues

// PlatformValues carries the platform metadata for one app instance
// (app, env, cluster[, component]): identity plus the resolved routing
// context. It is the source of truth for ((platform.*)) interpolation tokens.
// Secrets are never included, and the block itself is never published — only
// token resolutions derived from it.
type PlatformValues struct {
	// Org is the organization name.
	Org string `json:"org" yaml:"org"`
	// Project is the project the app belongs to.
	Project string `json:"project" yaml:"project"`
	// App is the app name.
	App string `json:"app" yaml:"app"`
	// Component is the component's user-facing name (e.g. "express-caller") for a
	// composed app's per-component values, or the sole component of a single-component
	// app. Empty for app-level contexts (a multi-component app has no single
	// component). Exposed as ((platform.component)) so a component's chart values can
	// reference its own name.
	Component string `json:"component,omitempty" yaml:"component,omitempty"`
	// Env is the environment name (e.g. "staging", "prod", "pr-42").
	Env string `json:"env" yaml:"env"`
	// EnvType is the environment classification ("staging", "prod", "preview").
	EnvType string `json:"envType" yaml:"envType"`
	// Cluster is the target cluster name. Empty in single-cluster active mode.
	Cluster string `json:"cluster,omitempty" yaml:"cluster,omitempty"`
	// Namespace is the Kubernetes namespace the app deploys into.
	Namespace string `json:"namespace" yaml:"namespace"`
	// BaseDomain is the resolved ingress DNS zone for this (env, cluster).
	BaseDomain string `json:"baseDomain" yaml:"baseDomain"`
	// RoutingHost is the resolved external host (no scheme), e.g.
	// "hello.staging.acme.com".
	RoutingHost string `json:"routingHost" yaml:"routingHost"`
	// IngressClassName is the resolved IngressClass for the routing component.
	// Empty when routing is disabled / no profile resolved.
	IngressClassName string `json:"ingressClassName,omitempty" yaml:"ingressClassName,omitempty"`
	// ClusterIssuer is the resolved cert-manager ClusterIssuer. Empty for plain
	// HTTP or no profile.
	ClusterIssuer string `json:"clusterIssuer,omitempty" yaml:"clusterIssuer,omitempty"`

	// Per-tier routing context. Unlike the single BaseDomain/IngressClassName/
	// ClusterIssuer above (which reflect the app's one routing component), these
	// expose the resolved "internal" and "external" routing profiles directly, so
	// a chart that authors BOTH an internal and an external route (e.g. a gateway
	// app) can reference each tier independently. Each field resolves through the
	// cluster→env→org precedence; a tier's base domain falls back to the profile's
	// own baseDomain, then the (env, cluster) base domain. Empty when the tier has
	// no profile configured.
	InternalBaseDomain       string `json:"internalBaseDomain,omitempty" yaml:"internalBaseDomain,omitempty"`
	ExternalBaseDomain       string `json:"externalBaseDomain,omitempty" yaml:"externalBaseDomain,omitempty"`
	InternalIngressClassName string `json:"internalIngressClassName,omitempty" yaml:"internalIngressClassName,omitempty"`
	ExternalIngressClassName string `json:"externalIngressClassName,omitempty" yaml:"externalIngressClassName,omitempty"`
	InternalClusterIssuer    string `json:"internalClusterIssuer,omitempty" yaml:"internalClusterIssuer,omitempty"`
	ExternalClusterIssuer    string `json:"externalClusterIssuer,omitempty" yaml:"externalClusterIssuer,omitempty"`
	// Per-tier Gateway API references (Envoy Gateway), from each tier's resolved
	// RoutingProfile.Gateway. Exposed so a chart's HTTPRoute parentRefs are
	// authored against the platform-resolved gateway instead of hardcoding it.
	InternalGatewayName        string `json:"internalGatewayName,omitempty" yaml:"internalGatewayName,omitempty"`
	InternalGatewayNamespace   string `json:"internalGatewayNamespace,omitempty" yaml:"internalGatewayNamespace,omitempty"`
	InternalGatewaySectionName string `json:"internalGatewaySectionName,omitempty" yaml:"internalGatewaySectionName,omitempty"`
	ExternalGatewayName        string `json:"externalGatewayName,omitempty" yaml:"externalGatewayName,omitempty"`
	ExternalGatewayNamespace   string `json:"externalGatewayNamespace,omitempty" yaml:"externalGatewayNamespace,omitempty"`
	ExternalGatewaySectionName string `json:"externalGatewaySectionName,omitempty" yaml:"externalGatewaySectionName,omitempty"`
	// ImageTag is the instance's resolved image tag when the PLATFORM owns it —
	// today that is previews only (the per-PR CI tag, set by the preview
	// publisher). Exposed as ((platform.imageTag)) so an overlay can write
	// `image.tag: "((platform.imageTag))"`. Empty in stable envs, where Kargo
	// owns tags via each binding's TagKey; the token still resolves (to ""), so
	// charts default the tag themselves.
	ImageTag string `json:"imageTag,omitempty" yaml:"imageTag,omitempty"`
	// PreviewName is the preview (PR) identifier (e.g. "pr-42"), empty for stable
	// envs. Exposed as ((platform.previewName)) so a shared-namespace preview can
	// suffix its workload resource names (e.g. fullnameOverride) per preview.
	PreviewName string `json:"previewName,omitempty" yaml:"previewName,omitempty"`
	// ConfigMapName / SecretName are the resolved names of the platform-managed
	// env ConfigMap and ExternalSecret/target Secret for this instance —
	// {app}-config / {app}-secrets by convention, preview-name-suffixed for
	// shared-namespace previews, or a component's curated projection objects.
	// The ((platform.configMapName))/((platform.secretName)) tokens use them
	// VERBATIM; an explicitly empty SecretName means "no app secrets" (an
	// opt-out component without a curated secret subset).
	ConfigMapName string `json:"configMapName,omitempty" yaml:"configMapName,omitempty"`
	SecretName    string `json:"secretName,omitempty" yaml:"secretName,omitempty"`
}
