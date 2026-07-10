// Package domain defines the MVP view types and storage interfaces for
// suparship.
//
// All types in this package are plain Go structs with JSON tags — no
// Kubernetes imports, no YAML tags, no external framework dependencies.
// This keeps the domain layer cheap to test and trivial to stub.
//
// Every interface here is expected to have two implementations:
//
//   - A fake implementation (internal/fake) that returns deterministic
//     fixture data. No cluster required. Used for local development
//     (SUPARSHIP_CLUSTER_MODE=fake) and unit tests.
//
//   - A real implementation backed by Kubernetes ConfigMaps, Deployments,
//     and eventually ArgoCD / Kargo. Used in core and full install profiles.
//
// Start here when adding a new feature: define the domain type, extend
// the relevant interface, then wire fake first and real after.
package domain

import (
	"strings"
	"time"
)

// Org represents the single organization in a suparship installation.
type Org struct {
	Name        string    `json:"name"`
	DisplayName string    `json:"displayName"`
	CreatedAt   time.Time `json:"createdAt"`
}

// Project is a logical grouping of services sharing environments and a
// common promotion pipeline (e.g. dev → staging → prod).
type Project struct {
	Name         string        `json:"name"`
	DisplayName  string        `json:"displayName,omitempty"`
	Description  string        `json:"description,omitempty"`
	Environments []Environment `json:"environments"`
}

// Environment is an ordered deployment target within a project.
type Environment struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Order       int    `json:"order"`

	// ClusterRefs lists every registered Cluster this environment is allowed
	// to deploy to. Empty means the environment is not yet bound. Today only
	// ActiveClusterRef receives deploys; the rest are reserved for future
	// multi-cluster fan-out.
	ClusterRefs []string `json:"clusterRefs,omitempty"`

	// ActiveClusterRef is the single member of ClusterRefs that currently
	// receives deploys. Must be a member of ClusterRefs (or empty, in which
	// case EffectiveClusterRef falls back to ClusterRefs[0]). The split lets
	// operators register a multi-cluster topology now without changing their
	// deploy target until multi-cluster fan-out lands.
	ActiveClusterRef string `json:"activeClusterRef,omitempty"`

	// BaseDomain is the ingress base domain for apps in this environment.
	// App URLs are derived as: http://{app}.{baseDomain}
	// Defaults to "localhost" when empty.
	BaseDomain string `json:"baseDomain,omitempty"`

	// NamespacePattern controls how Kubernetes namespaces are generated for
	// apps in this environment. Supported tokens: {app}, {env}, {project}.
	//
	// Examples:
	//   "{app}"         — dedicated cluster, namespace = "hello"
	//   "{app}-{env}"   — shared cluster,    namespace = "hello-staging"
	//
	// Default (empty): "{app}-{env}" — safe for shared clusters.
	// Must always contain the {app} token.
	NamespacePattern string `json:"namespacePattern,omitempty"`
}

// EffectiveClusterRef returns the cluster this environment currently deploys
// to. Resolution order:
//
//  1. ActiveClusterRef when set (and present in ClusterRefs — callers must
//     validate at write time).
//  2. ClusterRefs[0] when ActiveClusterRef is empty.
//  3. "" when no clusters are registered (env is unbound).
//
// All publish-side code reads through this method so the future addition of
// a multi-cluster deploy mode only changes the publisher loop, not the call
// sites that ask "what cluster am I targeting?".
func (e Environment) EffectiveClusterRef() string {
	if e.ActiveClusterRef != "" {
		return e.ActiveClusterRef
	}
	if len(e.ClusterRefs) > 0 {
		return e.ClusterRefs[0]
	}
	return ""
}

// Cluster represents a Kubernetes cluster registered with suparship.
// suparship itself runs on a tooling cluster; registered clusters are the
// workload targets where apps are deployed via ArgoCD.
type Cluster struct {
	// Name is the unique identifier for this cluster (DNS label).
	Name string `json:"name"`
	// DisplayName is a human-readable label shown in the UI.
	DisplayName string `json:"displayName,omitempty"`
	// APIServer is the Kubernetes API server URL (e.g. "https://10.0.0.1:6443").
	// This value is written into ArgoCD ApplicationSet destination.server.
	APIServer string `json:"apiServer"`
	// Status reflects the last known reachability of this cluster.
	// Values: "ready" | "unknown".
	Status string `json:"status,omitempty"`
	// ArgoCDOwned is true when suparship created and manages the ArgoCD cluster
	// Secret for this cluster. When false (or absent), the ArgoCD Secret was
	// pre-existing and suparship only linked to it — the Secret will not be
	// deleted when this cluster is unregistered from suparship.
	ArgoCDOwned bool `json:"argoCDOwned,omitempty"`
	// ESONamespace is the Kubernetes namespace where External Secrets Operator
	// is installed on this cluster. Defaults to "external-secrets" when empty.
	// SealedSecrets and ClusterSecretStore resources for this cluster are
	// created in this namespace.
	ESONamespace string `json:"esoNamespace,omitempty"`
	// BaseDomain is this cluster's default ingress DNS zone (e.g.
	// "aws.example.com"). In a multi-cloud fan-out it makes each cluster's app
	// hosts distinct. Empty inherits the environment's base domain. A per-mode
	// RoutingProfiles entry's baseDomain overrides this.
	BaseDomain string `json:"baseDomain,omitempty"`
	// RoutingProfiles overrides the env/org routing profiles for apps deployed
	// to this cluster, keyed by ExposeMode ("internal"/"external"), each with
	// its own ingressClassName, clusterIssuer, and optional baseDomain. Lets a
	// cluster on a different cloud use its own ingress controller + cert issuer.
	// Sparse: modes not present here inherit env → org.
	RoutingProfiles RoutingProfiles `json:"routingProfiles,omitempty"`
}

// ArgoCDClusterCandidate describes a cluster that ArgoCD already has registered
// (one ArgoCD cluster Secret), surfaced as a candidate for import into suparship.
type ArgoCDClusterCandidate struct {
	// Name is the ArgoCD cluster name (the Secret's data.name).
	Name string `json:"name"`
	// Server is the Kubernetes API server URL (the Secret's data.server).
	Server string `json:"server"`
	// AuthType is how ArgoCD authenticates to the cluster: "token", "clientCert",
	// "basic", "exec" (cloud-IAM), or "unknown".
	AuthType string `json:"authType"`
	// Importable is true when suparship can reconstruct a usable kubeconfig from
	// the ArgoCD credentials (bearer token, client cert, or basic auth).
	Importable bool `json:"importable"`
	// Reason explains why an entry is not importable (exec/cloud-IAM auth, etc.).
	Reason string `json:"reason,omitempty"`
	// AlreadyRegistered is true when a suparship cluster already targets this server.
	AlreadyRegistered bool `json:"alreadyRegistered"`
}

// EffectiveESONamespace returns ESONamespace, falling back to "external-secrets"
// when the field is empty (the upstream default installation namespace for ESO).
func (c Cluster) EffectiveESONamespace() string {
	if c.ESONamespace != "" {
		return c.ESONamespace
	}
	return "external-secrets"
}

// Normalize trims stray whitespace from user-entered fields. APIServer is
// written into ArgoCD destination.server (quoted YAML preserves spaces), where
// a leading/trailing space makes ArgoCD's cluster lookup fail with
// `cluster " https://..." not found`. Called on registration and when loading
// stored records, so previously mis-entered values heal on read.
func (c *Cluster) Normalize() {
	c.Name = strings.TrimSpace(c.Name)
	c.DisplayName = strings.TrimSpace(c.DisplayName)
	c.APIServer = strings.TrimSpace(c.APIServer)
	c.ESONamespace = strings.TrimSpace(c.ESONamespace)
}

// Service is a deployable workload belonging to a project.
//
// Deprecated: Service is a legacy compatibility type retained for the
// service-centric code paths that existed before the app model was introduced.
// New code should use App and AppEnvironment instead.
// See docs/migration-app-model.md for the transition guide.
type Service struct {
	Name         string `json:"name"`
	ProjectName  string `json:"projectName"`
	TemplateName string `json:"templateName"`
	DisplayName  string `json:"displayName,omitempty"`
	Description  string `json:"description,omitempty"`
}

// ServiceStatus describes the live runtime state of a service in one
// environment. Status is one of the Status* constants below.
//
// Deprecated: ServiceStatus is a legacy compatibility type. New code should
// use AppRuntimeStatus (via AppEnvironment) instead.
// See docs/migration-app-model.md for the transition guide.
type ServiceStatus struct {
	Status       string   `json:"status"`
	Environment  string   `json:"environment"`
	Replicas     int32    `json:"replicas"`
	Available    int32    `json:"available"`
	Image        string   `json:"image,omitempty"`
	IngressURLs  []string `json:"ingressUrls"`
	Namespace    string   `json:"namespace"`
	LastDeployed string   `json:"lastDeployed,omitempty"`
}

// Status values for ServiceStatus.Status.
const (
	StatusHealthy     = "healthy"
	StatusDegraded    = "degraded"
	StatusProgressing = "progressing"
	StatusNotDeployed = "not_deployed"
	StatusUnknown     = "unknown"
)

// Template describes a golden path for deploying an app.
// The full template schema (inputs, presets, engine config) lives in
// internal/tpl; Template here is the lightweight summary used by list
// and status views.
type Template struct {
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Category    string `json:"category,omitempty"`
}

// Preview is an ephemeral, branch-scoped deployment of an app.
// The namespace convention is {project}-preview-{name}.
//
// Deprecated: Preview is a legacy compatibility type. It is used by the
// service-oriented preview endpoints and the internal/compat bridge. New code
// should model previews as AppEnvironment values with EnvType=AppEnvPreview.
// See docs/migration-app-model.md for the transition guide.
type Preview struct {
	Name        string `json:"name"`
	ProjectName string `json:"projectName"`
	// ServiceName identifies the service (i.e. app) that owns this preview.
	// Deprecated: ServiceName will be renamed AppName once the service→app
	// migration is complete. For now, treat ServiceName == AppName.
	ServiceName string    `json:"serviceName"`
	Namespace   string    `json:"namespace"`
	Status      string    `json:"status"`
	URL         string    `json:"url,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

// LogLine is a single line of log output from a running container.
type LogLine struct {
	Timestamp string `json:"timestamp,omitempty"`
	Text      string `json:"text"`
	Pod       string `json:"pod,omitempty"`
	Container string `json:"container,omitempty"`
}

// RoutingProfile carries the ingress/TLS configuration for one routing tier
// (e.g. "internal", "external"). The org defines a RoutingProfiles map keyed
// by ExposeMode name; per-environment overrides may further refine specific
// tiers without restating the rest.
//
// ClusterIssuer is optional: when empty, charts emit a plain HTTP ingress
// with no cert-manager annotation and no tls block. Set it to a cert-manager
// ClusterIssuer name (e.g. "letsencrypt-prod", "selfsigned-cluster-issuer")
// to enable TLS.
//
// BaseDomain is optional: when set, it overrides the Environment.BaseDomain
// for components routed through this profile (useful when internal services
// need to live on a different DNS zone than external ones).
type RoutingProfile struct {
	// IngressClassName is the Kubernetes IngressClass to use for routes in
	// this tier (e.g. "nginx", "nginx-internal"). Required.
	IngressClassName string `json:"ingressClassName" yaml:"ingressClassName"`
	// ClusterIssuer is the cert-manager ClusterIssuer name. Empty means no
	// TLS — the chart will emit a plain HTTP ingress.
	ClusterIssuer string `json:"clusterIssuer,omitempty" yaml:"clusterIssuer,omitempty"`
	// BaseDomain overrides Environment.BaseDomain for components routed via
	// this profile. Empty means inherit the environment's base domain.
	BaseDomain string `json:"baseDomain,omitempty" yaml:"baseDomain,omitempty"`
	// Gateway names the Gateway API Gateway apps in this tier attach their
	// HTTPRoutes to (parentRefs), for charts that route via Gateway API /
	// Envoy Gateway instead of Ingress. Empty means the tier has no gateway
	// configured. Resolved with the same cluster→env→org precedence as the
	// rest of the profile, so a per-cluster gateway (e.g. a different Envoy
	// install per cloud) overrides the env/org default.
	Gateway *GatewayRef `json:"gateway,omitempty" yaml:"gateway,omitempty"`
}

// GatewayRef identifies a Gateway API Gateway for a routing tier. It is exposed
// to app configs as ((platform.{internal,external}Gateway{Name,Namespace,SectionName}))
// so a chart's HTTPRoute parentRefs can be authored against the platform-resolved
// gateway rather than hardcoding it per app.
type GatewayRef struct {
	// Name is the Gateway resource name (e.g. "envoy-internal"). Required when
	// a GatewayRef is present.
	Name string `json:"name" yaml:"name"`
	// Namespace is the Gateway's namespace (e.g. "envoy-gateway-system"). Empty
	// means the app authors its own (or a same-namespace) reference.
	Namespace string `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	// SectionName is the Gateway listener section (e.g. "https"). Empty means
	// the app omits sectionName (attaches to all listeners).
	SectionName string `json:"sectionName,omitempty" yaml:"sectionName,omitempty"`
}

// RoutingProfiles maps ExposeMode names to their resolved configuration.
// Org-level profiles set the defaults; environment-level profiles override
// individual entries by name (sparse — unspecified names inherit the org
// value).
type RoutingProfiles map[string]RoutingProfile

// AddonProfile selects which provider implementation backs a given addon
// type in an environment. App developers declare intent with
// AppSpec.Addons (`type: redis`); ops decide here whether that's
// served by valkey-operator (K8s-local) or Crossplane-ElastiCache
// (managed AWS) per environment. The wrapper chart referenced by
// `Chart` produces a Secret matching the type's connection contract
// (see internal/addons/contracts) regardless of provider, so app code
// remains environment-agnostic.
type AddonProfile struct {
	// Type matches AppSpec.Addons[].Type and a registered contract
	// (e.g. "redis", "postgres"). Required; the same string is used
	// as this profile's key in AddonProfiles.
	Type string `json:"type" yaml:"type"`
	// Provider identifies the implementation choice (e.g.
	// "valkey-operator", "cloudnative-pg", "crossplane-rds"). Used
	// only for human readability and observability; the actual
	// rendering comes from Chart.
	Provider string `json:"provider" yaml:"provider"`
	// Chart is the suparship template name whose chart wraps the
	// upstream/operator deployment for this provider (e.g. "valkey").
	// Must be present in templates.builtIn or imported via
	// chartimport.
	Chart string `json:"chart" yaml:"chart"`
	// Defaults are passed to the wrapper chart as additional Helm
	// values. Per-app overrides in AppSpec.Addons[].Values win on
	// key conflict. Secret values MUST NOT appear here; route them
	// through the env-config hierarchy or chart-side SecretRefs.
	Defaults map[string]any `json:"defaults,omitempty" yaml:"defaults,omitempty"`
}

// AddonProfiles maps addon types to their resolved provider/chart
// configuration. Org-level profiles set the defaults; per-environment
// overrides replace entries by type sparsely.
type AddonProfiles map[string]AddonProfile
