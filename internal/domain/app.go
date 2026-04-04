package domain

import "fmt"

// AppEnvironmentType classifies the kind of environment an app is running in.
// Only the three values below are valid in MVP.
type AppEnvironmentType string

const (
	AppEnvStaging AppEnvironmentType = "staging"
	AppEnvProd    AppEnvironmentType = "prod"
	AppEnvPreview AppEnvironmentType = "preview"
)

// ParseAppEnvironmentType converts a raw string into an AppEnvironmentType,
// returning an error if the value is not one of the known MVP values.
func ParseAppEnvironmentType(s string) (AppEnvironmentType, error) {
	switch AppEnvironmentType(s) {
	case AppEnvStaging, AppEnvProd, AppEnvPreview:
		return AppEnvironmentType(s), nil
	default:
		return "", fmt.Errorf("unknown app environment type %q: must be one of staging, prod, preview", s)
	}
}

// Valid reports whether t is a recognised AppEnvironmentType.
func (t AppEnvironmentType) Valid() bool {
	_, err := ParseAppEnvironmentType(string(t))
	return err == nil
}

// ComponentType classifies the runtime role of a component within an app.
// web, worker, and cron cover the primary MVP deployment patterns.
type ComponentType string

const (
	ComponentWeb    ComponentType = "web"
	ComponentWorker ComponentType = "worker"
	ComponentCron   ComponentType = "cron"
)

// ParseComponentType converts a raw string into a ComponentType,
// returning an error if the value is not one of the known MVP values.
func ParseComponentType(s string) (ComponentType, error) {
	switch ComponentType(s) {
	case ComponentWeb, ComponentWorker, ComponentCron:
		return ComponentType(s), nil
	default:
		return "", fmt.Errorf("unknown component type %q: must be one of web, worker, cron", s)
	}
}

// Valid reports whether t is a recognised ComponentType.
func (t ComponentType) Valid() bool {
	_, err := ParseComponentType(string(t))
	return err == nil
}

// AppTemplateRef identifies the golden-path template an app was created from.
type AppTemplateRef struct {
	// Name is the template identifier (e.g. "web-service").
	Name string `json:"name" yaml:"name"`
	// Version pins the template at a specific release. Empty means latest.
	Version string `json:"version,omitempty" yaml:"version,omitempty"`
}

// AppSecretRef maps a template secret input to a Kubernetes Secret key.
// No plaintext secret values are stored; only references are persisted.
type AppSecretRef struct {
	// Name matches the secret input name declared in the template schema.
	Name string `json:"name" yaml:"name"`
	// SecretRef is the reference in "k8s-secret-name.key" format resolved at runtime.
	SecretRef string `json:"secretRef" yaml:"secretRef"`
}

// SizePreset is a coarse resource-sizing hint that abstracts CPU/memory
// requests into a named t-shirt size. Mutually exclusive with an explicit
// Replicas override on the same ComponentSpec.
type SizePreset string

const (
	SizeSmall  SizePreset = "small"
	SizeMedium SizePreset = "medium"
	SizeLarge  SizePreset = "large"
)

// ParseSizePreset converts a raw string into a SizePreset, returning an error
// if the value is not one of the known values.
func ParseSizePreset(s string) (SizePreset, error) {
	switch SizePreset(s) {
	case SizeSmall, SizeMedium, SizeLarge:
		return SizePreset(s), nil
	default:
		return "", fmt.Errorf("unknown size preset %q: must be one of small, medium, large", s)
	}
}

// Valid reports whether p is a recognised SizePreset.
func (p SizePreset) Valid() bool {
	_, err := ParseSizePreset(string(p))
	return err == nil
}

// ComponentSpec describes a single runtime unit within an app (e.g. web
// server, background worker, or scheduled job). Component topology is derived
// from the template by default; this struct allows explicit overrides.
//
// Replicas and SizePreset are mutually exclusive: set at most one.
// Secret values MUST NOT appear in Config; use AppSpec.SecretRefs instead.
type ComponentSpec struct {
	// Name uniquely identifies the component within the app (e.g. "web", "worker").
	Name string `json:"name" yaml:"name"`
	// Type classifies the runtime role. One of web, worker, cron.
	Type ComponentType `json:"type" yaml:"type"`
	// Enabled controls whether this component is active. Disabled components
	// are not deployed to any environment.
	Enabled bool `json:"enabled" yaml:"enabled"`
	// Replicas is the desired replica count. Zero means use the platform
	// default. Mutually exclusive with SizePreset.
	Replicas int32 `json:"replicas,omitempty" yaml:"replicas,omitempty"`
	// SizePreset selects a named resource tier (small, medium, large).
	// Mutually exclusive with Replicas.
	SizePreset SizePreset `json:"sizePreset,omitempty" yaml:"sizePreset,omitempty"`
	// Expose indicates that this component should be reachable via an ingress
	// or external service endpoint. Typically true for web components.
	Expose bool `json:"expose" yaml:"expose"`
	// PreviewEnabled controls whether this component is deployed in preview
	// environments. Heavy or non-essential components can opt out by setting
	// this to false.
	PreviewEnabled bool `json:"previewEnabled" yaml:"previewEnabled"`
	// Config holds non-secret key/value configuration for the component
	// (e.g. environment variable defaults, feature flags). Secret values
	// MUST NOT appear here; use AppSpec.SecretRefs instead.
	Config map[string]string `json:"config,omitempty" yaml:"config,omitempty"`
}

// AppMetadata carries optional labelling and annotation data attached to an
// app spec. Both maps are optional; nil and empty are treated equivalently.
type AppMetadata struct {
	// Labels are arbitrary key/value pairs used for filtering and grouping.
	Labels map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
	// Annotations are arbitrary key/value pairs used for tooling and auditing.
	Annotations map[string]string `json:"annotations,omitempty" yaml:"annotations,omitempty"`
}

// EnvironmentOverride holds per-environment tuning applied on top of the
// app-level defaults. Only non-zero fields override the app-level value.
//
// Replicas and SizePreset are mutually exclusive: set at most one.
type EnvironmentOverride struct {
	// Replicas overrides the replica count for this environment.
	// Zero means inherit the component or platform default.
	Replicas int32 `json:"replicas,omitempty" yaml:"replicas,omitempty"`
	// SizePreset overrides the resource tier for this environment.
	SizePreset SizePreset `json:"sizePreset,omitempty" yaml:"sizePreset,omitempty"`
	// Values overrides specific template input values for this environment.
	Values map[string]any `json:"values,omitempty" yaml:"values,omitempty"`
	// Config overrides non-secret key/value configuration for this environment.
	Config map[string]string `json:"config,omitempty" yaml:"config,omitempty"`
}

// AppSpec is the desired configuration for an app. It is deterministic and
// serializable: the same inputs always produce byte-identical output.
//
// Secret values MUST NOT appear here; use SecretRefs instead.
type AppSpec struct {
	// DisplayName is a human-friendly label shown in the UI.
	DisplayName string `json:"displayName,omitempty" yaml:"displayName,omitempty"`
	// Description provides optional context about the app's purpose.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	// Template is the golden-path template this app was created from.
	Template AppTemplateRef `json:"template" yaml:"template"`
	// Values holds curated template input values (no secrets).
	// Keys correspond to Input.name fields in the template schema.
	Values map[string]any `json:"values,omitempty" yaml:"values,omitempty"`
	// SecretRefs maps secret input names to Kubernetes Secret references.
	// Values are resolved at runtime by the target cluster.
	SecretRefs []AppSecretRef `json:"secretRefs,omitempty" yaml:"secretRefs,omitempty"`
	// Components describes the runtime units that make up this app.
	// When empty, the component topology is derived from the template defaults.
	// Components are internal units: the default UI shows app-level health only;
	// individual components are surfaced in advanced views. Hidden from top-level
	// navigation. See docs/app-model.md — "Component — internal runtime unit".
	Components []ComponentSpec `json:"components,omitempty" yaml:"components,omitempty"`
	// EnvironmentDefaults holds per-environment overrides keyed by environment
	// name (e.g. "staging", "prod"). Only set fields override app-level values.
	EnvironmentDefaults map[string]EnvironmentOverride `json:"environmentDefaults,omitempty" yaml:"environmentDefaults,omitempty"`
	// Metadata carries optional labels and annotations for the app spec.
	Metadata *AppMetadata `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

// App is a deployable unit owned by a project. It combines identity metadata
// with an AppSpec that describes what should be deployed.
//
// Runtime state for each environment is tracked separately in AppEnvironment.
type App struct {
	// Name is the unique identifier for this app within its project.
	Name string `json:"name" yaml:"name"`
	// ProjectName is the owning project.
	ProjectName string `json:"projectName" yaml:"projectName"`
	// Spec holds the desired configuration for this app.
	Spec AppSpec `json:"spec" yaml:"spec"`
}

// AppReleaseRef is a lightweight, serializable pointer to a deployed release.
// It captures enough information to identify what version is running without
// storing the full deployment manifest.
type AppReleaseRef struct {
	// Image is the fully-qualified container image reference including tag or digest.
	Image string `json:"image,omitempty" yaml:"image,omitempty"`
	// Tag is the image tag or semantic version string (e.g. "v1.2.3", "pr-42-abc1234").
	Tag string `json:"tag,omitempty" yaml:"tag,omitempty"`
	// Commit is the Git commit SHA that produced this release, if known.
	Commit string `json:"commit,omitempty" yaml:"commit,omitempty"`
}

// AppRuntimeStatus is a summary of the live runtime state for one app
// environment instance. It is derived from cluster observations and MUST NOT
// be stored as desired config.
type AppRuntimeStatus struct {
	// Phase is a coarse health indicator. One of the domain Status* constants.
	Phase string `json:"phase"`
	// Replicas is the total number of desired pod replicas.
	Replicas int32 `json:"replicas"`
	// Available is the number of ready replicas.
	Available int32 `json:"available"`
	// LastDeployed is the RFC 3339 timestamp of the most recent successful sync.
	LastDeployed string `json:"lastDeployed,omitempty"`
}

// AppEnvironment is a running instance of an App in a specific environment.
// It combines the desired release intent (what should be running) with the
// live runtime status observed from the cluster (what is actually running).
//
// Key rules:
//   - Desired config lives in App/AppSpec — it is stored in Git-backed
//     ConfigMaps and describes *what should be deployed*.
//   - Runtime state (Status, URLs) lives here — it is derived from live cluster
//     observations and MUST NOT be stored back as desired config.
//   - Environment is a runtime context for an app, not a top-level navigation
//     object. Developers navigate to the app, then switch environment.
//
// See docs/app-model.md — "Environment — runtime context, not a navigation object".
type AppEnvironment struct {
	// AppName is the parent app this instance belongs to.
	AppName string `json:"appName"`
	// ProjectName is the owning project.
	ProjectName string `json:"projectName"`
	// EnvName is the logical environment name (e.g. "staging", "prod", "pr-42").
	EnvName string `json:"envName"`
	// EnvType classifies the environment as staging, prod, or preview.
	EnvType AppEnvironmentType `json:"envType"`
	// Namespace is the Kubernetes namespace for this environment instance.
	// Convention: {app}-{envName} for all environments, e.g. "hello-staging",
	// "hello-prod", "hello-pr-42".
	Namespace string `json:"namespace"`
	// Release is the release currently targeted for this environment.
	// Nil means no release has been promoted here yet.
	Release *AppReleaseRef `json:"release,omitempty"`
	// URLs are the ingress hostnames assigned to this environment instance.
	URLs []string `json:"urls,omitempty"`
	// Status is the live runtime health summary observed from the cluster.
	Status AppRuntimeStatus `json:"status"`
}
