package domain

import (
	"fmt"

	"github.com/suparcloud/suparship/internal/envconfig"
)

// AppEnvironmentType classifies the kind of environment an app is running in.
// Only the three values below are valid in MVP.
type AppEnvironmentType string

const (
	AppEnvStaging AppEnvironmentType = "staging"
	AppEnvProd    AppEnvironmentType = "prod"
	AppEnvPreview AppEnvironmentType = "preview"
)

// NamespaceScope controls where an app's workloads are deployed.
type NamespaceScope string

const (
	// NamespaceScopeApp gives the app a dedicated namespace per environment
	// (default — safe for preview teardown and RBAC).
	NamespaceScopeApp NamespaceScope = "app"
	// NamespaceScopeProject shares the project's namespace across apps in the
	// same environment. Requires the project to have a NamespacePattern set,
	// or the org ResourceNaming.ProjectNamespace to be configured.
	NamespaceScopeProject NamespaceScope = "project"
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

// ExposeMode declares how a component is reachable from outside the cluster.
// The exact ingress class and TLS issuer come from the org's RoutingProfiles
// keyed by the mode name; the component only declares its tier.
type ExposeMode string

const (
	// ExposeDisabled means no ingress is created for the component.
	ExposeDisabled ExposeMode = "disabled"
	// ExposeInternal routes the component through the org's "internal"
	// routing profile (e.g. internal load balancer, internal CA).
	ExposeInternal ExposeMode = "internal"
	// ExposeExternal routes the component through the org's "external"
	// routing profile (e.g. public ingress, public CA like Let's Encrypt).
	ExposeExternal ExposeMode = "external"
)

// ParseExposeMode converts a raw string into an ExposeMode, returning an
// error for unknown values. Empty string is treated as ExposeDisabled.
func ParseExposeMode(s string) (ExposeMode, error) {
	if s == "" {
		return ExposeDisabled, nil
	}
	switch ExposeMode(s) {
	case ExposeDisabled, ExposeInternal, ExposeExternal:
		return ExposeMode(s), nil
	default:
		return "", fmt.Errorf("unknown expose mode %q: must be one of disabled, internal, external", s)
	}
}

// Valid reports whether m is a recognised ExposeMode. Empty (zero value) is
// treated as ExposeDisabled and is valid.
func (m ExposeMode) Valid() bool {
	_, err := ParseExposeMode(string(m))
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
	// ExposeMode selects which routing profile (disabled/internal/external)
	// the chart should use for this component. The exact ingress class and
	// TLS issuer come from the org's RoutingProfiles map keyed by this name;
	// ExposeDisabled (the zero value) means no ingress is created.
	ExposeMode ExposeMode `json:"exposeMode,omitempty" yaml:"exposeMode,omitempty"`
	// PreviewEnabled controls whether this component is deployed in preview
	// environments. Heavy or non-essential components can opt out by setting
	// this to false.
	PreviewEnabled bool `json:"previewEnabled" yaml:"previewEnabled"`
	// Config holds non-secret key/value configuration for the component
	// (e.g. environment variable defaults, feature flags). Secret values
	// MUST NOT appear here; use AppSpec.SecretRefs instead.
	Config map[string]string `json:"config,omitempty" yaml:"config,omitempty"`
	// Resources sets raw CPU/memory requests+limits for the component
	// container. Set instead of (or alongside, taking precedence over)
	// SizePreset when a chart consumes raw resources.
	Resources *ComponentResources `json:"resources,omitempty" yaml:"resources,omitempty"`
	// EnvFromSecrets / EnvFromConfigMaps are extra Secret / ConfigMap names the
	// component should envFrom, appended after the platform envFrom hierarchy.
	EnvFromSecrets    []string `json:"envFromSecrets,omitempty" yaml:"envFromSecrets,omitempty"`
	EnvFromConfigMaps []string `json:"envFromConfigMaps,omitempty" yaml:"envFromConfigMaps,omitempty"`
	// Scaling holds per-component KEDA autoscaling (triggers + min/max).
	Scaling *ComponentScaling `json:"scaling,omitempty" yaml:"scaling,omitempty"`
}

// ComponentResources holds raw Kubernetes resource quantities for a component
// container — set directly rather than via a size preset. Keys are resource
// names (cpu, memory, ephemeral-storage) mapped to quantity strings.
type ComponentResources struct {
	Requests map[string]string `json:"requests,omitempty" yaml:"requests,omitempty"`
	Limits   map[string]string `json:"limits,omitempty" yaml:"limits,omitempty"`
}

// KEDATrigger is one KEDA ScaledObject trigger. Metadata values are all strings
// (KEDA's contract). Advanced fields (authenticationRef, fallback, …) are out of
// scope here — use the raw-values overlay for those.
type KEDATrigger struct {
	Type       string            `json:"type" yaml:"type"`
	MetricType string            `json:"metricType,omitempty" yaml:"metricType,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

// ComponentScaling holds per-component KEDA autoscaling config. A non-empty
// Triggers list replaces the chart's defaults for that component.
type ComponentScaling struct {
	Triggers    []KEDATrigger `json:"triggers,omitempty" yaml:"triggers,omitempty"`
	MinReplicas *int32        `json:"minReplicas,omitempty" yaml:"minReplicas,omitempty"`
	MaxReplicas *int32        `json:"maxReplicas,omitempty" yaml:"maxReplicas,omitempty"`
}

// ComponentConfig carries the per-component knobs that can be overridden per
// environment (EnvironmentOverride.Components) and declared as template defaults
// (ComponentDefaults). All fields optional; only set ones apply over the
// app-level ComponentSpec values.
type ComponentConfig struct {
	Resources         *ComponentResources `json:"resources,omitempty" yaml:"resources,omitempty"`
	EnvFromSecrets    []string            `json:"envFromSecrets,omitempty" yaml:"envFromSecrets,omitempty"`
	EnvFromConfigMaps []string            `json:"envFromConfigMaps,omitempty" yaml:"envFromConfigMaps,omitempty"`
	Scaling           *ComponentScaling   `json:"scaling,omitempty" yaml:"scaling,omitempty"`
	// Env is a key/value env-override map for this component, merged over the
	// component's app-level Config (env override wins per key).
	Env map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
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
	// EnvConfig holds env vars and secret refs specific to this app+environment
	// combination (App Environment level of the hierarchy — wins all other levels).
	EnvConfig envconfig.EnvConfig `json:"envConfig,omitempty" yaml:"envConfig,omitempty"`
	// RawValues is a freeform Helm values overlay for this environment, deep-merged
	// on top of the app-level RawValues and the generated chart values at publish
	// time (env wins). String leaves may reference {platform.*}/{vars.*} tokens,
	// resolved per (env, cluster). No secrets.
	RawValues map[string]any `json:"rawValues,omitempty" yaml:"rawValues,omitempty"`
	// Components holds per-component overrides for this environment, keyed by
	// component name — resources, envFrom, scaling, and env — overriding the
	// app-level ComponentSpec values for this env only.
	Components map[string]ComponentConfig `json:"components,omitempty" yaml:"components,omitempty"`
	// ClusterOverrides holds per-cluster value overrides keyed by cluster name,
	// applied on top of this env override for apps in a fan-out environment
	// (deployMode "all"). Each cluster's published values.yaml is the env values
	// deep-merged with its ClusterOverrides entry. Only meaningful in "all" mode.
	ClusterOverrides map[string]ClusterValueOverride `json:"clusterOverrides,omitempty" yaml:"clusterOverrides,omitempty"`
}

// ClusterValueOverride holds per-(env, cluster) value overrides, applied on top
// of the environment override. Same shape as the value-bearing subset of
// EnvironmentOverride; wins over env-level values for that cluster only.
type ClusterValueOverride struct {
	Replicas   int32             `json:"replicas,omitempty" yaml:"replicas,omitempty"`
	SizePreset SizePreset        `json:"sizePreset,omitempty" yaml:"sizePreset,omitempty"`
	Values     map[string]any    `json:"values,omitempty" yaml:"values,omitempty"`
	Config     map[string]string `json:"config,omitempty" yaml:"config,omitempty"`
}

// AppSpec is the desired configuration for an app. It is deterministic and
// serializable: the same inputs always produce byte-identical output.
//
// Secret values MUST NOT appear here; use SecretRefs or EnvConfig.SecretRefs instead.
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
	// Addons declares managed dependencies (databases, caches, queues)
	// the app consumes. Each claim is bound at publish time to an
	// AddonProfile from the org/env catalog; the resolved provider
	// renders a wrapper chart that produces a connection Secret
	// matching the type's contract. Connection details flow into
	// every component via the existing suparship.envFromSecrets[]
	// hierarchy. See internal/addons/contracts and
	// docs/templates-components.md.
	Addons []AddonSpec `json:"addons,omitempty" yaml:"addons,omitempty"`
	// EnvironmentDefaults holds per-environment overrides keyed by environment
	// name (e.g. "staging", "prod"). Only set fields override app-level values.
	EnvironmentDefaults map[string]EnvironmentOverride `json:"environmentDefaults,omitempty" yaml:"environmentDefaults,omitempty"`
	// Metadata carries optional labels and annotations for the app spec.
	Metadata *AppMetadata `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	// EnvConfig holds cross-cutting env vars and secret refs that apply to all
	// environments of this app (App level of the hierarchy).
	// These are loaded via envFrom and are overridden by App Environment level.
	// Component-specific vars should use ComponentSpec.Config instead, which
	// renders as direct env: entries and wins over all envFrom layers.
	EnvConfig envconfig.EnvConfig `json:"envConfig,omitempty" yaml:"envConfig,omitempty"`
	// RawValues is a freeform Helm values overlay (escape hatch) deep-merged on
	// top of the generated chart values at publish time, below any per-env
	// RawValues. String leaves may reference {platform.*}/{vars.*} tokens, resolved
	// per (env, cluster). No secrets — use SecretRefs/EnvConfig.SecretRefs.
	RawValues map[string]any `json:"rawValues,omitempty" yaml:"rawValues,omitempty"`
	// NamespaceScope controls whether this app deploys into a dedicated app
	// namespace ("app", default) or the shared project namespace ("project").
	// Empty is treated as "app".
	NamespaceScope NamespaceScope `json:"namespaceScope,omitempty" yaml:"namespaceScope,omitempty"`
	// NamespacePattern overrides the org/project defaults for app namespace
	// naming. Only applies when NamespaceScope is "app".
	// Tokens: {org}, {project}, {app}, {env}.
	// Empty: inherits from project, org environment, or org-level defaults.
	NamespacePattern string `json:"namespacePattern,omitempty" yaml:"namespacePattern,omitempty"`
	// Stack is the name of the stack this app belongs to within its project
	// (empty = the app sits directly in the project, not in any stack). A stack
	// is a logical grouping of tightly-coupled apps that share an override layer
	// (org → project → stack → app), optionally a namespace, and batch lifecycle
	// actions. Membership is just this label — the app keeps its own identity,
	// ArgoCD Application, and Kargo pipeline.
	Stack string `json:"stack,omitempty" yaml:"stack,omitempty"`
	// CD holds continuous-delivery settings for this app.
	CD CDConfig `json:"cd,omitempty" yaml:"cd,omitempty"`
}

// CDConfig configures who owns the deployed image tag in the published
// values.yaml. When Managed is true, an external CD controller (Kargo) drives
// the image tag: it commits the discovered/promoted tag into values.yaml and
// the publisher PRESERVES that committed tag on every subsequent republish,
// instead of re-rendering the create-time seed from the app's stored overrides.
// Without this, any republish (a values save, rename, config change, or a
// promote of another env) would overwrite the live tag and roll the deployment
// back to the seed. Preservation applies only to stable (non-preview)
// environments — preview envs always deploy the tag from their own pipeline.
type CDConfig struct {
	// Managed enables external-CD tag ownership (see CDConfig docs). Default
	// false preserves the legacy behaviour where the platform writes the tag.
	Managed bool `json:"managed,omitempty" yaml:"managed,omitempty"`
	// ImageTagPath is the dotted Helm-values key that holds the deploy image
	// tag (e.g. "image.tag" for a root-level image, or
	// "components.web.image.tag" for the canonical suparship layout). The CD
	// controller writes this key and the publisher preserves it, so the two
	// MUST agree. Empty defaults to the canonical "components.web.image.tag".
	ImageTagPath string `json:"imageTagPath,omitempty" yaml:"imageTagPath,omitempty"`
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
	// Diagnostics are human-readable problem reports gathered from the
	// delivery pipeline (ArgoCD Application conditions/health, ExternalSecret
	// status). Empty when everything is healthy. Surfaced so an operator can
	// understand a stuck/"not deployed" env without leaving suparship.
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

// DiagnosticLevel classifies a Diagnostic by severity.
type DiagnosticLevel string

const (
	DiagnosticError   DiagnosticLevel = "error"
	DiagnosticWarning DiagnosticLevel = "warning"
	// DiagnosticInfo is non-failure context (e.g. a per-cluster status
	// breakdown for a fan-out environment).
	DiagnosticInfo DiagnosticLevel = "info"
)

// Diagnostic is one problem report about an app environment's delivery,
// gathered from ArgoCD / External Secrets. Title is a short summary; Detail
// is the raw upstream message; Hint is suparship's plain-language suggestion
// for fixing it (may be empty when no pattern matched).
type Diagnostic struct {
	// Source identifies where the diagnostic came from, e.g. "argocd",
	// "argocd-platform", "external-secrets".
	Source string `json:"source"`
	// Level is "error" or "warning".
	Level DiagnosticLevel `json:"level"`
	// Title is a short human-readable summary.
	Title string `json:"title"`
	// Detail is the raw upstream message.
	Detail string `json:"detail,omitempty"`
	// Hint is suparship's suggested fix, when a known pattern matched.
	Hint string `json:"hint,omitempty"`
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
	// This is a soft hint for UI rendering only. Pipeline ordering is
	// controlled by Order, not by this field.
	EnvType AppEnvironmentType `json:"envType"`
	// Order defines the position of this environment in the promotion pipeline.
	// Lower values are deployed earlier (e.g. staging=1, prod=2). Preview
	// environments always have Order=0 and are excluded from the pipeline chain.
	Order int `json:"order,omitempty"`
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
