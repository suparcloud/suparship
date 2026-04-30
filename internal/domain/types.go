// Package domain defines the MVP view types and storage interfaces for
// suparShip.
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

import "time"

// Org represents the single organization in a suparShip installation.
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

// Cluster represents a Kubernetes cluster registered with suparShip.
// suparShip itself runs on a tooling cluster; registered clusters are the
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
}

// EffectiveESONamespace returns ESONamespace, falling back to "external-secrets"
// when the field is empty (the upstream default installation namespace for ESO).
func (c Cluster) EffectiveESONamespace() string {
	if c.ESONamespace != "" {
		return c.ESONamespace
	}
	return "external-secrets"
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
	Name        string    `json:"name"`
	ProjectName string    `json:"projectName"`
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
}

// RoutingProfiles maps ExposeMode names to their resolved configuration.
// Org-level profiles set the defaults; environment-level profiles override
// individual entries by name (sparse — unspecified names inherit the org
// value).
type RoutingProfiles map[string]RoutingProfile
