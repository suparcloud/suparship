//go:generate go run ../../cmd/gen-values-schema

// Package helmvalues defines the canonical Helm values structure for
// suparShip app charts and the mapper that derives it from an AppSpec.
//
// # Design
//
// suparShip charts use a single, well-known values schema regardless of which
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

import "github.com/suparcloud/suparship/internal/envconfig"

// HelmValues is the root of the canonical Helm values document for a
// suparShip app chart. All fields are exported with both JSON and YAML tags
// so the struct can be serialized directly to either format.
type HelmValues struct {
	// App identifies which app and environment these values belong to.
	App AppContext `json:"app" yaml:"app"`
	// Platform carries suparShip-injected platform metadata (identity + routing
	// context) for this app/env/cluster. Chart authors can reference it via
	// .Values.platform.*, and it is the source of truth for {platform.*} tokens
	// the publisher interpolates in user-supplied values. Never contains secrets.
	Platform PlatformValues `json:"platform" yaml:"platform"`
	// Components is a map from component name to its resolved configuration.
	// Keys are always sorted alphabetically by the mapper to ensure
	// deterministic output across Go runtime versions.
	Components map[string]*ComponentValues `json:"components" yaml:"components"`
	// Routing declares the primary ingress entry point for this deployment.
	Routing RoutingValues `json:"routing" yaml:"routing"`
	// EnvLayers holds the App and App Environment env config layers baked in
	// at GitOps publish time. Upper levels (Org, Environment, Project) are
	// replicated into app namespaces automatically via Stakater Replicator and
	// are not included here to avoid mass re-publish on upper-level changes.
	// Omitted when both App and AppEnv layers are empty.
	EnvLayers envconfig.HelmEnvLayers `json:"envLayers,omitempty" yaml:"envLayers,omitempty"`
	// Suparship holds well-known resource names that templates can reference
	// for secret and config injection via envFrom.
	Suparship SuparshipValues `json:"suparship" yaml:"suparship"`
	// Addons is a map from addon name to its resolved configuration —
	// only present in the values document the publisher renders for an
	// *addon wrapper chart's* release. App charts never see this field
	// (their AppSpec.Addons claims become parallel Application releases
	// each with their own addon-shaped values). Keys are sorted
	// alphabetically by the mapper.
	Addons map[string]*AddonValues `json:"addons,omitempty" yaml:"addons,omitempty"`
	// ServiceClaims describes addon→component bindings the publisher
	// resolved for this app+env, used by the consumer chart's values
	// (component charts) to discover which Secrets to envFrom in
	// addition to the org/env hierarchy. Sorted by addon name then
	// component name.
	ServiceClaims []ServiceClaimValues `json:"serviceClaims,omitempty" yaml:"serviceClaims,omitempty"`
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

// PlatformValues carries suparShip platform metadata injected into every app
// chart's values: app/env identity and the resolved routing context for this
// (env, cluster). It is emitted as `.Values.platform` and is the source of truth
// for {platform.*} interpolation tokens. Secrets are never included.
type PlatformValues struct {
	// Org is the organization name.
	Org string `json:"org" yaml:"org"`
	// Project is the project the app belongs to.
	Project string `json:"project" yaml:"project"`
	// App is the app name.
	App string `json:"app" yaml:"app"`
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
	// Resources is optional. When non-nil the chart uses the named size
	// preset to select CPU/memory requests and limits.
	Resources *ResourceValues `json:"resources,omitempty" yaml:"resources,omitempty"`
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

// AddonValues holds the resolved configuration for one addon claim
// within an app. The wrapper chart at templates/<chart-name>/chart/
// reads this to render the upstream subchart and the connection
// Secret matching the addon type's contract (see
// internal/addons/contracts).
type AddonValues struct {
	// Enabled controls whether the wrapper chart renders the addon's
	// resources. Disabled addons render zero resources.
	Enabled bool `json:"enabled" yaml:"enabled"`
	// Type is the connection-contract identifier ("redis", "postgres").
	// Mirrors AppSpec.Addons[].Type.
	Type string `json:"type" yaml:"type"`
	// Provider records which AddonProfile provider produced this
	// addon (e.g. "valkey-operator"). Surfaced for observability and
	// addon-health enrichment; never read by the wrapper chart.
	Provider string `json:"provider,omitempty" yaml:"provider,omitempty"`
	// Size is a chart-defined preset string (small/medium/large) the
	// wrapper maps to provider-specific sizing.
	Size string `json:"size,omitempty" yaml:"size,omitempty"`
	// Version pins a major engine version when the wrapper supports it.
	Version string `json:"version,omitempty" yaml:"version,omitempty"`
	// SecretName is the Secret the wrapper chart MUST produce. Holds
	// the connection-contract keys (e.g. REDIS_URL, REDIS_HOST). The
	// publisher computes the name (`<app>-addon-<addon-name>-conn`)
	// so consumer apps can reference it stably from
	// suparship.envFromSecrets[].
	SecretName string `json:"secretName" yaml:"secretName"`
}

// ServiceClaimValues describes one addon→component binding. In the
// MVP every addon binds to every component (implicit fan-out), so
// these entries are produced for documentation + future per-component
// scoping. Consumer components don't read this field directly — the
// publisher folds the bound Secret into Suparship.EnvFromSecrets so
// the existing envFrom hierarchy plumbing handles the wiring.
type ServiceClaimValues struct {
	// Addon is the local addon name (AppSpec.Addons[].Name).
	Addon string `json:"addon" yaml:"addon"`
	// Component is the consumer component name. Empty means "all
	// components in this app" (the implicit fan-out used today).
	Component string `json:"component,omitempty" yaml:"component,omitempty"`
	// SecretName is the Secret the addon publishes (mirrors
	// AddonValues.SecretName); duplicated here so a chart that wants
	// per-claim wiring instead of the implicit fan-out can read the
	// claim list directly.
	SecretName string `json:"secretName" yaml:"secretName"`
	// Type is the addon's connection-contract type (`redis`, …).
	// Lets a chart key behaviour off the type without re-reading the
	// addon entry by name.
	Type string `json:"type" yaml:"type"`
}

// AddonInstanceValues is the values shape passed to an *addon
// wrapper chart's release* — one wrapper-chart release per addon
// claim per app+env. Different shape from HelmValues (which targets
// app charts) because the wrapper renders one addon's resources, not
// a map of components.
//
// Publisher writes this under <app>/<env>/addons/<name>/values.yaml;
// the existing ApplicationSet generator picks it up alongside the
// main app's values.yaml.
type AddonInstanceValues struct {
	// App ties this wrapper release back to its consumer app + env.
	// Populated for label/annotation rendering and addon-health
	// attribution.
	App AppContext `json:"app" yaml:"app"`
	// Addon carries the resolved configuration for this single
	// claim. The wrapper chart reads Addon.Type / .Size / .Version
	// to render upstream-chart inputs and the connection Secret.
	Addon AddonValues `json:"addon" yaml:"addon"`
	// Suparship gives the wrapper access to the same envFrom
	// hierarchy app charts use, so addon images can opt into the
	// org/env config layers via the standard envFrom block helper.
	Suparship SuparshipValues `json:"suparship" yaml:"suparship"`
}
