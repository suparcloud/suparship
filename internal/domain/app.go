package domain

import (
	"fmt"
	"sort"

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

// PreviewOverrideKey is the reserved AppSpec.EnvironmentDefaults key holding the
// per-app "preview band": values/env/secrets applied to every preview on top of
// its base env. It is not a real environment — no stable env may use this name.
const PreviewOverrideKey = "preview"

// DeliveryMode controls how an app is delivered across its environments.
type DeliveryMode string

const (
	// DeliveryPipeline (the default; "" is treated as this) wires the CI-image
	// promotion pipeline: a Kargo Warehouse watches the image, Stages promote it
	// staging→prod, and only the first env deploys on create.
	DeliveryPipeline DeliveryMode = "pipeline"
	// DeliveryDirect deploys each environment directly from its own Helm values —
	// no Kargo, no promotion. The image is a pinned tag in values. Intended for
	// off-the-shelf / external software (valkey, redis, postgres). Every stable
	// env deploys from create; editing an env's values re-deploys just that env.
	DeliveryDirect DeliveryMode = "direct"
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
	// ComponentJob is a one-shot component that runs to completion (a Kubernetes
	// Job) rather than a long-running Deployment — e.g. a database migration
	// (`alembic upgrade head`). Its command/args come from ComponentSpec; its
	// chart renders the Job as an ArgoCD PreSync hook so it runs before the
	// long-running components roll out.
	ComponentJob ComponentType = "job"
)

// ParseComponentType converts a raw string into a ComponentType,
// returning an error if the value is not one of the known MVP values.
func ParseComponentType(s string) (ComponentType, error) {
	switch ComponentType(s) {
	case ComponentWeb, ComponentWorker, ComponentCron, ComponentJob:
		return ComponentType(s), nil
	default:
		return "", fmt.Errorf("unknown component type %q: must be one of web, worker, cron, job", s)
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
	// Template, when set, gives this component its OWN chart — the app is then a
	// composition of heterogeneous components, each rendered by its own template
	// as a separate Helm source in one multi-source ArgoCD Application (e.g.
	// api→web-service, worker→worker, migrate→job). nil = inherit the app's
	// AppSpec.Template (the single-chart behaviour). An app is in "composed mode"
	// when at least one component sets this. See AppSpec.IsComposed.
	Template *AppTemplateRef `json:"template,omitempty" yaml:"template,omitempty"`
	// Values is this component's own Helm values overlay, deep-merged onto its
	// chart's values at publish (the value-based, schema-agnostic config, mirroring
	// the app-level RawValues). It is how a composed component sets its image, port,
	// command, resources, etc. — in the shape ITS chart expects, so a bring-your-own
	// chart works as naturally as a canonical one. Only meaningful for composed
	// components (those with a Template); the publisher applies it per component.
	Values map[string]any `json:"values,omitempty" yaml:"values,omitempty"`
	// InheritAppVars controls whether this component blanket-envFroms the two
	// app-wide objects <app>-config + <app>-secrets (getting EVERY app var). nil or
	// true = inherit (today's behaviour); false = the component sees ONLY the vars
	// in EnvVars (plus addon connection secrets and any EnvFrom* extras) — so a db
	// component need not be exposed to every web var.
	InheritAppVars *bool `json:"inheritAppVars,omitempty" yaml:"inheritAppVars,omitempty"`
	// EnvVars is the component's curated environment: each entry either a literal
	// value or a specific key selected (and optionally renamed) from the app's
	// <app>-config / <app>-secrets. Rendered by the chart as env[] (literals as
	// value, selections as valueFrom.configMapKeyRef/secretKeyRef).
	EnvVars []ComponentEnvVar `json:"envVars,omitempty" yaml:"envVars,omitempty"`
	// Images binds this component's container image(s) for Kargo: the repository to
	// watch and the tag-key path (in this component's own values overlay) where the
	// promoted tag is written. Explicit because composed components are often BYO
	// charts with no template-declared images to infer from. Empty = no CD tracking
	// for this component.
	Images []ComponentImage `json:"images,omitempty" yaml:"images,omitempty"`
	// Stateful marks a component (a database/cache — an "addon") whose lifecycle
	// must be decoupled from the app's shared auto-sync. Instead of a source in the
	// composed multi-source Application (which has one Application-level Prune:true
	// policy), it renders as its OWN ArgoCD Application with prune DISABLED. NOTE:
	// this protects against sync-time prune/drift, NOT against Application deletion
	// on component removal — surviving PVCs additionally require the chart to mark
	// them helm.sh/resource-policy: keep. Typically paired with InheritAppVars:false
	// and no Images (so it deploys pinned/direct, not Kargo-promoted).
	Stateful bool `json:"stateful,omitempty" yaml:"stateful,omitempty"`
}

// ComponentImage marks one of a composed component's container images as managed
// by external CD (Kargo). Like AppImageBinding it is a SELECTION keyed by TagKey:
// the repository is discovered from the component's effective values (chart
// defaults ⊕ its Values overlay) at publish, so the user picks images from a
// checklist rather than hand-typing repositories.
type ComponentImage struct {
	// Name is the discovered image's logical slot (display only; the match key is
	// TagKey). Optional.
	Name string `json:"name,omitempty" yaml:"name,omitempty"`
	// TagKey is the dotted Helm values path — in this component's own values.yaml —
	// where the promoted tag is written (e.g. "components.web.image.tag"). This is
	// the selection key matched against the discovered images.
	TagKey string `json:"tagKey" yaml:"tagKey"`
	// Repository is normally derived from discovery. It is retained only as a
	// legacy/BYO fallback: when a stored image's TagKey no longer matches any
	// discovered image, the publisher watches this repository directly. Empty for
	// discovery-selected images. Optional.
	Repository string `json:"repository,omitempty" yaml:"repository,omitempty"`
	// TagPattern optionally overrides Kargo's tag allow-list (allowTags).
	TagPattern string `json:"tagPattern,omitempty" yaml:"tagPattern,omitempty"`
	// SelectionStrategy optionally overrides how Kargo picks the tag to promote
	// ("NewestBuild", "SemVer", "Digest", "Lexical").
	SelectionStrategy string `json:"selectionStrategy,omitempty" yaml:"selectionStrategy,omitempty"`
}

// ComponentEnvVar is one entry in a component's curated environment. Name is the
// env-var name the container sees. Exactly one source must be set:
//   - Value      — a literal value.
//   - FromConfig — a key of the app's <app>-config ConfigMap (selection + rename).
//   - FromSecret — a key of the app's <app>-secrets Secret (selection + rename).
type ComponentEnvVar struct {
	Name       string `json:"name" yaml:"name"`
	Value      string `json:"value,omitempty" yaml:"value,omitempty"`
	FromConfig string `json:"fromConfig,omitempty" yaml:"fromConfig,omitempty"`
	FromSecret string `json:"fromSecret,omitempty" yaml:"fromSecret,omitempty"`
}

// CuratesSecrets reports whether any component opts out of the app-wide vars and
// selects a SUBSET of app secret keys (a FromSecret entry) into its own
// <app>-<component>-secrets. The publish adapter uses this to decide whether to
// pay for listing secret KEY NAMES per scope (needed only for the data[]
// projection) — the app-wide secret uses whole-item dataFrom and needs no names.
func (s AppSpec) CuratesSecrets() bool {
	for _, c := range s.Components {
		if c.InheritAppVars == nil || *c.InheritAppVars {
			continue
		}
		for _, e := range c.EnvVars {
			if e.FromSecret != "" {
				return true
			}
		}
	}
	return false
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
	// time (env wins). String leaves may reference ((platform.*))/((vars.*)) tokens,
	// resolved per (env, cluster). No secrets.
	RawValues map[string]any `json:"rawValues,omitempty" yaml:"rawValues,omitempty"`
	// Components holds per-component overrides for this environment, keyed by
	// component name — resources, envFrom, scaling, and env — overriding the
	// app-level ComponentSpec values for this env only.
	Components map[string]ComponentConfig `json:"components,omitempty" yaml:"components,omitempty"`
	// ComponentValues holds per-component freeform Helm values overlays for this
	// environment, keyed by component name — deep-merged on top of each composed
	// component's base ComponentSpec.Values for this env only (env wins). String
	// leaves may reference ((platform.*))/((vars.*)) tokens. No secrets. Only
	// meaningful for composed apps (each component is its own chart source).
	ComponentValues map[string]map[string]any `json:"componentValues,omitempty" yaml:"componentValues,omitempty"`
	// ClusterOverrides holds per-cluster value overrides keyed by cluster name,
	// applied on top of this env override for apps in a fan-out environment
	// (deployMode "all"). Each cluster's published values.yaml is the env values
	// deep-merged with its ClusterOverrides entry. Only meaningful in "all" mode.
	ClusterOverrides map[string]ClusterValueOverride `json:"clusterOverrides,omitempty" yaml:"clusterOverrides,omitempty"`
	// TargetClusters selects which of the environment's clusters this app deploys
	// to, overriding the env-level DeployMode default. nil/empty = inherit the env
	// default (active cluster, or all when the env is DeployMode="all"). The
	// sentinel []string{"*"} = all of the env's ClusterRefs (tracks the env).
	// Otherwise an explicit subset of the env's ClusterRefs (one or many).
	// Resolved via ResolveAppClusterTargets.
	TargetClusters []string `json:"targetClusters,omitempty" yaml:"targetClusters,omitempty"`
	// Deploy opts this environment in or out for the app. nil = default: for a
	// direct app the base env (lowest Order) deploys and higher envs are opt-in;
	// for a pipeline app every env is in the promotion chain.
	//
	// Setting it false decommissions the env for this app: the publisher stops
	// deploying it AND (for pipeline apps) drops it from the Kargo chain —
	// publishKargoCRs skips it, re-linking the neighbours below so the previous
	// env becomes terminal. Toggling the flag alone does not remove a running
	// workload; the explicit undeploy action (handleUndeployAppEnv) sets it false
	// AND removes the env's GitOps files (incl. its Kargo stage). See
	// AppSpec.DeploysToEnv.
	Deploy *bool `json:"deploy,omitempty" yaml:"deploy,omitempty"`
	// PinnedImageTag pins this environment to a specific image tag (e.g. a PR
	// preview's tag "pr-712-aae62d96") promoted without merging. While set, the
	// publisher writes this tag into the env's values.yaml on every republish and
	// Kargo auto-promotion to this stage is paused — so newer Warehouse freight
	// does NOT override it. Cleared by unpinning, which resumes normal CD.
	PinnedImageTag string `json:"pinnedImageTag,omitempty" yaml:"pinnedImageTag,omitempty"`
	// PinnedFrom records the preview (or source) the pinned tag came from. It is
	// the user-facing "is this env pinned?" flag (a transient restore write sets
	// PinnedImageTag but leaves this empty). Empty when not pinned.
	PinnedFrom string `json:"pinnedFrom,omitempty" yaml:"pinnedFrom,omitempty"`
	// PrePinImageTag captures the env's deployed image tag at the moment it was
	// pinned, so unpinning can restore it instead of leaving the pinned image in
	// place (the publisher would otherwise preserve the committed pinned tag).
	PrePinImageTag string `json:"prePinImageTag,omitempty" yaml:"prePinImageTag,omitempty"`
	// Suspend, when non-nil and true, suspends this environment's workload: the
	// publisher writes the template's suspend values key (or the "suspend"
	// convention default) as true so the chart scales it down, while the env
	// stays published (no data loss, unlike undeploy). nil/false = running.
	// Works for pipeline and direct apps; toggled by the suspend/resume ops.
	Suspend *bool `json:"suspend,omitempty" yaml:"suspend,omitempty"`
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

// AllClustersSentinel is the EnvironmentOverride.TargetClusters value meaning
// "all of the environment's clusters" — resolved dynamically against the env's
// current ClusterRefs so adding a cluster to the env extends a "*" app to it.
const AllClustersSentinel = "*"

// ResolveAppClusterTargets resolves the cluster names an app deploys to in one
// environment. Inputs:
//   - sel:        the app's EnvironmentOverride.TargetClusters (per-env override)
//   - envDefault: the env's DeployMode targets (OrgEnvironment.ResolveDeployTargets)
//   - envRefs:    the env's full ClusterRefs (every allowed cluster)
//
// Precedence:
//   - no selection (nil/empty) → envDefault (inherit the env's DeployMode)
//   - ["*"]                    → envRefs (all of the env's clusters)
//   - explicit list           → sel ∩ envRefs, order preserved, dups/unknowns
//     dropped (a stale ref can never generate a phantom Application)
//
// The result preserves envRefs/envDefault ordering and is deduplicated.
func ResolveAppClusterTargets(sel, envDefault, envRefs []string) []string {
	if len(sel) == 0 {
		return dedupe(envDefault)
	}
	for _, s := range sel {
		if s == AllClustersSentinel {
			return dedupe(envRefs)
		}
	}
	allowed := make(map[string]bool, len(envRefs))
	for _, r := range envRefs {
		allowed[r] = true
	}
	seen := make(map[string]bool, len(sel))
	out := make([]string, 0, len(sel))
	for _, s := range sel {
		if allowed[s] && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func dedupe(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
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
	// EnvironmentDefaults holds per-environment overrides keyed by environment
	// name (e.g. "staging", "prod"). Only set fields override app-level values.
	// The reserved key PreviewOverrideKey ("preview") holds the per-app preview
	// band applied to every preview on top of its base env.
	EnvironmentDefaults map[string]EnvironmentOverride `json:"environmentDefaults,omitempty" yaml:"environmentDefaults,omitempty"`
	// PreviewsEnabled controls whether this app supports preview (ephemeral PR)
	// environments. Defaults to true on create. When false, CreatePreview is
	// rejected. A preview deploys all the app's enabled components.
	PreviewsEnabled bool `json:"previewsEnabled" yaml:"previewsEnabled"`
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
	// RawValues. String leaves may reference ((platform.*))/((vars.*)) tokens, resolved
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
	// Images is the set of chart images this app has selected for external CD
	// (Kargo) management. Images are DISCOVERED from the app's effective Helm
	// values (every image block with a repository); the user picks which ones CD
	// should watch/promote, so unrelated images (sidecars, init, proxy) are simply
	// left out. Each entry identifies a discovered image by its tag key; the
	// repository is read from the Helm values at publish (not stored here, so it
	// never drifts). Empty = no image is CD-managed (legacy single-image fallback).
	Images []AppImageBinding `json:"images,omitempty" yaml:"images,omitempty"`
	// DeliveryMode controls how the app is delivered across environments:
	// "pipeline" (default) uses Kargo + staging→prod promotion; "direct" deploys
	// each env straight from its values with no Kargo or promotion (off-the-shelf
	// software with a pinned image tag). Empty is treated as pipeline.
	DeliveryMode DeliveryMode `json:"deliveryMode,omitempty" yaml:"deliveryMode,omitempty"`
}

// IsDirect reports whether the app deploys each environment directly from its
// values (no Kargo, no promotion). Empty DeliveryMode means pipeline.
func (s AppSpec) IsDirect() bool { return s.DeliveryMode == DeliveryDirect }

// IsComposed reports whether the app renders as a multi-source composition —
// true when it has MORE THAN ONE component. In the unified model every component
// carries its own Template (one template = one component), so the render choice
// is by count: a 1-component app renders from its single chart via the
// single-source path (AppSpec.Template, the primary mirror = Components[0].Template);
// a ≥2-component app renders one multi-source ArgoCD Application with a chart
// source per component. Two components using the same template (api + frontend
// both web-service) are still two sources.
func (s AppSpec) IsComposed() bool {
	return len(s.Components) > 1
}

// BackfillComponentTemplates stamps any component whose Template is nil (a legacy
// single-template app saved before the unified model) with the app's primary
// AppSpec.Template, so every component carries a Template uniformly. No-op when a
// component already has its own Template or when AppSpec.Template is empty.
// Idempotent — called on load (app stores) and safe to call repeatedly.
func (s *AppSpec) BackfillComponentTemplates() {
	if s.Template.Name == "" {
		return
	}
	for i := range s.Components {
		if s.Components[i].Template == nil {
			t := s.Template
			s.Components[i].Template = &t
		}
	}
}

// ComposedComponents returns the app's components in deterministic (name-sorted)
// order for composed rendering. Only meaningful when IsComposed is true.
func (s AppSpec) ComposedComponents() []ComponentSpec {
	out := append([]ComponentSpec(nil), s.Components...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// StatefulComponents returns the composed components marked Stateful (name-sorted).
// Each renders as its own prune-disabled ArgoCD Application rather than a source in
// the shared multi-source Application.
func (s AppSpec) StatefulComponents() []ComponentSpec {
	var out []ComponentSpec
	for _, c := range s.ComposedComponents() {
		if c.Stateful && c.Template != nil {
			out = append(out, c)
		}
	}
	return out
}

// DeploysToEnv reports whether a direct-delivery app should deploy (publish) to
// the named environment. An explicit per-env Deploy override wins; otherwise the
// default is "deploy the base env, not the higher ones" — isBase is true for the
// base env (lowest Order). Callers determine which env is base.
func (s AppSpec) DeploysToEnv(envName string, isBase bool) bool {
	if ov, ok := s.EnvironmentDefaults[envName]; ok && ov.Deploy != nil {
		return *ov.Deploy
	}
	return isBase
}

// AppImageBinding marks one discovered chart image as managed by external CD
// (Kargo). It carries no repository: the repository is read from the effective
// Helm values at publish, keyed by TagKey, so changing the image in values is
// followed automatically with no stale snapshot.
type AppImageBinding struct {
	// Name is the discovered image's logical name (component name, "image", or a
	// service key) — for display and as a secondary match alongside TagKey.
	Name string `json:"name" yaml:"name"`
	// TagKey is the dotted Helm values path of this image's tag (e.g.
	// "components.web.image.tag"). It is the stable identity of the selected
	// image and where Kargo writes the promoted tag.
	TagKey string `json:"tagKey" yaml:"tagKey"`
	// TagPattern optionally overrides Kargo's tag allow-list (allowTags).
	TagPattern string `json:"tagPattern,omitempty" yaml:"tagPattern,omitempty"`
	// SelectionStrategy optionally overrides how Kargo picks the tag to promote
	// ("NewestBuild", "SemVer", "Digest", "Lexical").
	SelectionStrategy string `json:"selectionStrategy,omitempty" yaml:"selectionStrategy,omitempty"`
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
//
// Which values tag-keys Kargo manages is declared by the template's image slots
// (tpl.TemplateSpec.Images); which repository each slot watches is bound per-app
// via AppSpec.Images (falling back to the legacy image_repository value). Not
// configured here, so a chart with one or many services is handled uniformly.
type CDConfig struct {
	// Managed enables external-CD tag ownership (see CDConfig docs). Default
	// false preserves the legacy behaviour where the platform writes the tag.
	Managed bool `json:"managed,omitempty" yaml:"managed,omitempty"`
	// AutoPromote opts this pipeline app into automatic promotion to prod: once
	// the same image is promoted to (and healthy in) the first stage (staging),
	// Kargo auto-promotes it down the chain to prod without a manual click.
	// Only meaningful for pipeline delivery; ignored for direct apps (no Kargo).
	AutoPromote bool `json:"autoPromote,omitempty" yaml:"autoPromote,omitempty"`
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
	// Components is the per-component live health of a composed app (each
	// component's own workloads, keyed by component name). Empty for single-
	// component apps, where the env-level fields above already describe the sole
	// workload. Lets the UI show which component of a composed app is degraded.
	Components []ComponentRuntimeStatus `json:"components,omitempty"`
}

// ComponentRuntimeStatus is one composed component's live health — the same
// coarse phase + replica counts as the env-level status, but scoped to that one
// component's workloads (selected by its app.kubernetes.io/instance label).
type ComponentRuntimeStatus struct {
	// Component is the suparship component name (ComponentSpec.Name).
	Component string `json:"component"`
	// Phase is a coarse health indicator. One of the domain Status* constants.
	Phase string `json:"phase"`
	// Replicas is the total number of desired pod replicas for this component.
	Replicas int32 `json:"replicas"`
	// Available is the number of ready replicas for this component.
	Available int32 `json:"available"`
}

// WorkloadInstance identifies one component's running workloads for live-status
// and log queries: the component name plus the app.kubernetes.io/instance label
// its pods carry (the Helm release name suparship uses for that component).
type WorkloadInstance struct {
	// Component is the suparship component name (ComponentSpec.Name).
	Component string
	// Instance is the app.kubernetes.io/instance label value (the Helm release
	// name) selecting this component's pods.
	Instance string
}

// WorkloadInstances returns each enabled component's workload identity. A
// single-source app (one component) uses the app name as its Helm release, so its
// sole workload is labelled instance={app}. A composed app gives each component
// its own release {app}-{component} — matching the publisher's per-component
// ReleaseName — so each is labelled instance={app}-{component}. This is the stable
// handle status/log queries select on (composed workloads are NOT labelled
// instance={app}).
func (a *App) WorkloadInstances() []WorkloadInstance {
	enabled := make([]ComponentSpec, 0, len(a.Spec.Components))
	for _, c := range a.Spec.Components {
		if c.Enabled {
			enabled = append(enabled, c)
		}
	}
	if !a.Spec.IsComposed() {
		comp := ""
		if len(enabled) > 0 {
			comp = enabled[0].Name
		} else if len(a.Spec.Components) > 0 {
			comp = a.Spec.Components[0].Name
		}
		return []WorkloadInstance{{Component: comp, Instance: a.Name}}
	}
	out := make([]WorkloadInstance, 0, len(enabled))
	for _, c := range enabled {
		out = append(out, WorkloadInstance{Component: c.Name, Instance: a.Name + "-" + c.Name})
	}
	return out
}

// InstanceForComponent returns the instance label for a named component, or ""
// when the component isn't one of the app's enabled components.
func (a *App) InstanceForComponent(component string) string {
	for _, wi := range a.WorkloadInstances() {
		if wi.Component == component {
			return wi.Instance
		}
	}
	return ""
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
	// Cluster names the destination cluster this diagnostic came from, set when
	// an env fans out to more than one cluster so the operator can tell which
	// cluster is unhealthy. Empty for single-cluster envs.
	Cluster string `json:"cluster,omitempty"`
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
	// BaseEnv is the stable environment a preview was cloned from (e.g.
	// "staging"). Set only for preview instances; empty for stable envs. Lets the
	// UI group a preview under the environment it derives from.
	BaseEnv string `json:"baseEnv,omitempty"`
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
