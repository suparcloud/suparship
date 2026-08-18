//go:generate go run ../../cmd/gen-values-schema

// Package helmvalues defines the canonical Helm values structure for
// suparship app charts and the mapper that derives it from an AppSpec.
//
// # Design
//
// suparship charts use a single, well-known values schema regardless of which
// template the app was created from. The schema models apps as a set of named
// components and a single routing declaration. Template-specific mappings
// (template.yaml) are intentionally NOT leaked here: this package depends only
// on domain types.
//
// # YAML shape
//
//	app:
//	  name: hello
//	  env: staging
//
//	components:
//	  web:
//	    enabled: true
//	    image:
//	      repository: ghcr.io/org/hello
//	      tag: v1.0.0
//	    replicas: 2
//	    expose: true
//	    env:
//	      LOG_LEVEL: info
//	    resources:        # omitted when no size preset is set
//	      size: small
//	  worker:
//	    enabled: true
//	    image:
//	      repository: ghcr.io/org/hello
//	      tag: v1.0.0
//	    replicas: 1
//	    expose: false
//
//	routing:
//	  host: hello.staging.localhost
//	  component: web
//
// # Determinism guarantee
//
// MapToHelmValues is a pure function: same App + envName + envType always
// produces byte-identical output (maps are built with sorted keys when
// serialized via encoding/json or gopkg.in/yaml.v3).
package helmvalues

import (
	"github.com/suparcloud/suparship/internal/domain"
)

// HelmValues is the root of the canonical Helm values document for a
// suparship app chart. All fields are exported with both JSON and YAML tags
// so the struct can be serialized directly to either format.
type HelmValues struct {
	// App identifies which app and environment these values belong to.
	App AppContext `json:"app" yaml:"app"`
	// Platform carries suparship-injected platform metadata (identity + routing
	// context) for this app/env/cluster. Chart authors can reference it via
	// .Values.platform.*, and it is the source of truth for ((platform.*)) tokens
	// the publisher interpolates in user-supplied values. Never contains secrets.
	Platform PlatformValues `json:"platform" yaml:"platform"`
	// Components is a map from component name to its resolved configuration.
	// Keys are always sorted alphabetically by the mapper to ensure
	// deterministic output across Go runtime versions.
	Components map[string]*ComponentValues `json:"components" yaml:"components"`
	// Routing declares the primary ingress entry point for this deployment.
	Routing RoutingValues `json:"routing" yaml:"routing"`
	// Suparship holds well-known resource names that templates can reference
	// for secret and config injection via envFrom.
	Suparship SuparshipValues `json:"suparship" yaml:"suparship"`
}

// SuparshipValues carries the precedence-ordered hierarchy of ConfigMaps and
// Secrets that the chart should envFrom. Names are computed by the publisher
// from the active backend + naming patterns, so the chart needs no knowledge
// of either. Order is org → env-type → project → app → app-env → cluster;
// later entries win on key collision per Kubernetes envFrom semantics.
type SuparshipValues struct {
	EnvFromConfigMaps []string `json:"envFromConfigMaps,omitempty" yaml:"envFromConfigMaps,omitempty"`
	EnvFromSecrets    []string `json:"envFromSecrets,omitempty" yaml:"envFromSecrets,omitempty"`
}

// PlatformValues carries suparship platform metadata injected into every app
// chart's values: app/env identity and the resolved routing context for this
// (env, cluster). It is emitted as `.Values.platform` and is the source of truth
// for ((platform.*)) interpolation tokens. Secrets are never included.
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
	// ImageTag is the app's resolved image tag for this instance — the same tag
	// the canonical image mapping bakes into each component's image.tag. For a
	// preview it is the per-PR tag. Exposed as ((platform.imageTag)) so charts can
	// pin secondary images (sidecars, init containers) the canonical mapping
	// doesn't cover to the same build. Empty when the app sets no image_tag.
	ImageTag string `json:"imageTag,omitempty" yaml:"imageTag,omitempty"`
	// PreviewName is the preview (PR) identifier (e.g. "pr-42"), empty for stable
	// envs. Exposed as ((platform.previewName)) so a shared-namespace preview can
	// suffix its workload resource names (e.g. fullnameOverride) per preview.
	PreviewName string `json:"previewName,omitempty" yaml:"previewName,omitempty"`
	// ConfigMapName / SecretName are the resolved names of the platform-managed
	// env ConfigMap and ExternalSecret/target Secret for this instance. Empty
	// falls back to {app}-config / {app}-secrets. The publisher sets them to a
	// preview-name-suffixed value for shared-namespace previews so the chart's
	// envConfigMapName/envSecretName references resolve to the right objects.
	ConfigMapName string `json:"configMapName,omitempty" yaml:"configMapName,omitempty"`
	SecretName    string `json:"secretName,omitempty" yaml:"secretName,omitempty"`
}

// AppContext carries top-level app identity injected into every chart.
type AppContext struct {
	// Name is the app's unique identifier (DNS label).
	Name string `json:"name" yaml:"name"`
	// Env is the target environment name (e.g. "staging", "prod", "pr-42").
	Env string `json:"env" yaml:"env"`
}

// ComponentValues holds the resolved configuration for one component within
// an app chart. The chart uses these values to render its Deployment (or
// CronJob/Job for cron components), Service, and optional Ingress.
type ComponentValues struct {
	// Enabled controls whether the component's resources are rendered by
	// the chart. Disabled components have all their manifests suppressed.
	Enabled bool `json:"enabled" yaml:"enabled"`
	// Image is the container image to deploy for this component.
	Image ImageValues `json:"image" yaml:"image"`
	// Replicas is the desired pod replica count. Always ≥ 1 in output.
	Replicas int32 `json:"replicas" yaml:"replicas"`
	// Ingress carries the resolved RoutingProfile for this component when
	// it should be exposed via an Ingress. Nil means no ingress is created.
	// Charts read Ingress.ClassName for the IngressClassName field, and
	// Ingress.ClusterIssuer for the cert-manager annotation + tls block —
	// an empty ClusterIssuer means plain HTTP (no annotation, no tls).
	Ingress *IngressValues `json:"ingress,omitempty" yaml:"ingress,omitempty"`
	// Port is the TCP port the container listens on. Zero means "let the
	// chart's default kick in" so charts can declare their own sane
	// defaults (8080, 80, etc.) without suparship having to know the
	// runtime semantics.
	Port int32 `json:"port,omitempty" yaml:"port,omitempty"`
	// HealthCheck overrides the chart's default liveness/readiness probe
	// path. Nil = chart default. Set when the operator's image doesn't
	// serve the chart's hardcoded path (e.g. nginx serving "/" instead
	// of "/healthz").
	HealthCheck *HealthCheckValues `json:"healthCheck,omitempty" yaml:"healthCheck,omitempty"`
	// Env is a flat map of environment variable key/value pairs injected
	// into the container at runtime. Secret values MUST NOT appear here;
	// inject them via Kubernetes SecretKeyRef at the chart level.
	Env map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
	// EnvFrom lists extra Secret/ConfigMap sources the component envFroms,
	// beyond the platform hierarchy. Charts append these to their envFrom block.
	EnvFrom []EnvFromSource `json:"envFrom,omitempty" yaml:"envFrom,omitempty"`
	// Command / Args override the component container's entrypoint and
	// arguments. Empty = the image's default entrypoint. Needed for a one-shot
	// component such as a migration (`alembic upgrade head`); charts that render
	// a workload read these into the container's command/args.
	Command []string `json:"command,omitempty" yaml:"command,omitempty"`
	Args    []string `json:"args,omitempty" yaml:"args,omitempty"`
	// Resources is optional. The chart uses Size (preset) or the raw
	// Requests/Limits when present.
	Resources *ResourceValues `json:"resources,omitempty" yaml:"resources,omitempty"`
	// Autoscaling carries per-component KEDA triggers + min/max. Charts that
	// support KEDA read this; others ignore it.
	Autoscaling *ComponentAutoscaling `json:"autoscaling,omitempty" yaml:"autoscaling,omitempty"`
}

// HealthCheckValues lets an operator override the chart's liveness/
// readiness probe HTTP path without forking the chart. Mirrors the
// `components.<name>.healthCheck.path` key the built-in charts already
// read.
type HealthCheckValues struct {
	Path string `json:"path" yaml:"path"`
}

// IngressValues carries the resolved routing-profile fields a chart needs
// to render its Ingress: which IngressClass to attach the route to, and
// (optionally) which cert-manager ClusterIssuer to use for TLS. ClusterIssuer
// is intentionally optional — orgs that don't have cert-manager installed,
// or that run plain HTTP for internal traffic, leave it empty and charts
// emit a non-TLS ingress.
type IngressValues struct {
	// ClassName is the Kubernetes IngressClass name for this route
	// (e.g. "nginx", "nginx-internal"). Required when IngressValues is set.
	ClassName string `json:"className" yaml:"className"`
	// ClusterIssuer is the cert-manager ClusterIssuer name. Empty means no
	// TLS — the chart emits a plain HTTP ingress with no cert-manager
	// annotation and no tls block.
	ClusterIssuer string `json:"clusterIssuer,omitempty" yaml:"clusterIssuer,omitempty"`
}

// ImageValues identifies the container image for a component.
type ImageValues struct {
	// Repository is the image repository (e.g. "ghcr.io/org/app" or "nginx").
	Repository string `json:"repository" yaml:"repository"`
	// Tag is the image tag or digest (e.g. "v1.2.3", "sha256:abc…").
	Tag string `json:"tag" yaml:"tag"`
}

// ResourceValues selects a named resource tier for a component. The chart
// maps the size string to concrete CPU/memory requests and limits.
//
// Size is optional in the canonical schema: workloads with chart-specific
// resources (e.g. voiceai-agent's capacity-manager declaring explicit
// requests/limits) leave it empty and let the chart's `_helpers.tpl`
// produce the resources block directly.
type ResourceValues struct {
	// Size is one of "small", "medium", or "large".
	Size string `json:"size,omitempty" yaml:"size,omitempty"`
	// Requests / Limits are raw Kubernetes resource quantities (cpu, memory,
	// ephemeral-storage → quantity string), set instead of Size when a chart
	// consumes raw resources. When present, charts should prefer these over Size.
	Requests map[string]string `json:"requests,omitempty" yaml:"requests,omitempty"`
	Limits   map[string]string `json:"limits,omitempty" yaml:"limits,omitempty"`
}

// EnvFromRef names a Secret or ConfigMap to envFrom, with the optional flag so
// pods start before the source is populated.
type EnvFromRef struct {
	Name     string `json:"name" yaml:"name"`
	Optional bool   `json:"optional,omitempty" yaml:"optional,omitempty"`
}

// EnvFromSource is one entry in a component's envFrom list (exactly one of the
// two refs set). Mirrors the k8s EnvFromSource shape charts render directly.
type EnvFromSource struct {
	SecretRef    *EnvFromRef `json:"secretRef,omitempty" yaml:"secretRef,omitempty"`
	ConfigMapRef *EnvFromRef `json:"configMapRef,omitempty" yaml:"configMapRef,omitempty"`
}

// ComponentAutoscaling carries per-component KEDA autoscaling values. A non-empty
// Triggers list replaces the chart's default triggers for the component; the
// chart reads minReplicaCount/maxReplicaCount when set.
type ComponentAutoscaling struct {
	Triggers        []domain.KEDATrigger `json:"triggers,omitempty" yaml:"triggers,omitempty"`
	MinReplicaCount *int32               `json:"minReplicaCount,omitempty" yaml:"minReplicaCount,omitempty"`
	MaxReplicaCount *int32               `json:"maxReplicaCount,omitempty" yaml:"maxReplicaCount,omitempty"`
}

// RoutingValues declares the primary public entry point for the deployment.
// The chart uses this to configure the Ingress host and to annotate the
// primary Service.
type RoutingValues struct {
	// Host is the ingress hostname without scheme (e.g. "hello.staging.localhost").
	Host string `json:"host" yaml:"host"`
	// Component is the name of the component that serves external traffic.
	// Must match a key in HelmValues.Components.
	Component string `json:"component" yaml:"component"`
}
