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
	// Expose indicates whether the chart should create an Ingress resource
	// for this component.
	Expose bool `json:"expose" yaml:"expose"`
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

// ImageValues identifies the container image for a component.
type ImageValues struct {
	// Repository is the image repository (e.g. "ghcr.io/org/app" or "nginx").
	Repository string `json:"repository" yaml:"repository"`
	// Tag is the image tag or digest (e.g. "v1.2.3", "sha256:abc…").
	Tag string `json:"tag" yaml:"tag"`
}

// ResourceValues selects a named resource tier for a component. The chart
// maps the size string to concrete CPU/memory requests and limits.
type ResourceValues struct {
	// Size is one of "small", "medium", or "large".
	Size string `json:"size" yaml:"size"`
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
