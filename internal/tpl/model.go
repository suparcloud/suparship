// Package tpl defines the suparship template metadata schema.
//
// Templates describe golden paths for creating apps. Each template
// lives in its own directory and is defined by a template.yaml file.
// When a user picks a template and submits the form, suparShip creates
// an app — not a raw Kubernetes resource — that is then rendered into
// GitOps manifests by the appropriate engine (Helm in MVP).
//
// A template implicitly defines the app's component topology via its
// category field: a "web" template produces a single web component by
// default; "worker" and "cron" templates produce their respective
// components. More complex topologies (e.g. web + worker) are supported
// by specifying components explicitly at app-creation time.
//
// Component visibility: components are internal runtime units and are hidden
// from the default UI. Only the app-level health is surfaced by default;
// individual components appear in advanced views only.
//
// See docs/templates-components.md for how templates define component topology.
// See docs/templates.md for the full template authoring guide.
// See docs/app-model.md for the App / Environment / Component model.
//
//	templates/
//	├── web-service/
//	│   └── template.yaml
//	└── worker/
//	    └── template.yaml
//
// Example template.yaml:
//
//	apiVersion: suparship.io/v1alpha1
//	kind: Template
//	metadata:
//	  name: web-service
//	  version: "1.0.0"
//	spec:
//	  title: Web App
//	  description: Create a containerized web app
//	  category: web
//	  engine:
//	    type: helm
//	    chart: ./chart
//	  inputs:
//	    - name: image
//	      title: Container Image
//	      type: string
//	      required: true
//	  presets:
//	    - name: starter
//	      title: Starter
//	      values:
//	        replicas: 1
package tpl

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

const (
	CurrentAPIVersion = "suparship.io/v1alpha1"
	TemplateKind      = "Template"
	TemplateFileName  = "template.yaml"

	EngineHelm = "helm"
)

// TemplateComponentType enumerates the supported runtime roles for template
// components. Values are identical to domain.ComponentType and are kept as
// plain string constants to avoid a cross-package import in this layer.
type TemplateComponentType string

const (
	TemplateComponentWeb    TemplateComponentType = "web"
	TemplateComponentWorker TemplateComponentType = "worker"
	TemplateComponentCron   TemplateComponentType = "cron"
)

// InputType enumerates the supported input value types.
type InputType string

const (
	InputTypeString  InputType = "string"
	InputTypeNumber  InputType = "number"
	InputTypeBoolean InputType = "boolean"
	InputTypeEnum    InputType = "enum"
)

// Template is the top-level structure of a template.yaml file.
type Template struct {
	APIVersion string       `yaml:"apiVersion"`
	Kind       string       `yaml:"kind"`
	Metadata   Metadata     `yaml:"metadata"`
	Spec       TemplateSpec `yaml:"spec"`
}

// Metadata identifies a template.
type Metadata struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
}

// TemplateSpec defines the template's behavior and user-facing configuration.
type TemplateSpec struct {
	Title       string `yaml:"title"`
	Description string `yaml:"description,omitempty"`
	Category    string `yaml:"category"`
	Engine      Engine `yaml:"engine"`
	// Components declares the named runtime units this template produces.
	// When absent, the platform derives a single default component from
	// Category (backwards-compatible behaviour). When present, each entry
	// defines defaults that the user can override at app-creation time.
	Components []TemplateComponent `yaml:"components,omitempty"`
	// Addons declares the addon shapes this template materialises when
	// installed. Wrapper templates (category=addon) declare exactly one
	// addon entry — the connection contract their chart produces.
	// Application templates leave this empty; addons are claimed
	// through AppSpec.Addons and resolved per env via the org's
	// AddonProfile catalog.
	Addons         []TemplateAddon   `yaml:"addons,omitempty"`
	Inputs         []Input           `yaml:"inputs,omitempty"`
	AdvancedInputs []Input           `yaml:"advancedInputs,omitempty"`
	SecretInputs   []SecretInput     `yaml:"secretInputs,omitempty"`
	Mappings       map[string]string `yaml:"mappings,omitempty"`
	Presets        []Preset          `yaml:"presets,omitempty"`
	// DefaultValues is a Platform-Engineer-authored Helm values overlay applied
	// to EVERY environment, layered on top of the chart's own default values
	// (and below per-env EnvValues and developer overrides). Arbitrary Helm
	// values (BYO-chart friendly); string leaves may use {platform.*}/{vars.*}
	// tokens resolved at publish.
	DefaultValues map[string]any `yaml:"defaultValues,omitempty"`
	// EnvValues holds per-environment Helm values overlays keyed by environment
	// name (e.g. "staging", "prod"), layered after DefaultValues — so an org can
	// set a smaller staging baseline and a larger prod one.
	EnvValues map[string]map[string]any `yaml:"envValues,omitempty"`
	// InjectCanonicalValues controls whether the publisher injects the canonical
	// suparship-common values base (app/platform/components/suparship/routing)
	// into the published values.yaml. nil/true (default) = canonical, for charts
	// built on suparship-common. Set false for BYO/passthrough charts that bring
	// their own values structure: the platform then emits only the overlays +
	// resolved {platform.*}/{vars.*} tokens, no injected schema.
	InjectCanonicalValues *bool `yaml:"injectCanonicalValues,omitempty"`
	// Images declares the container images this chart deploys, one entry per
	// service, so external-CD (Kargo) can be wired generically: each entry says
	// which image repository to watch and which Helm values key holds its tag.
	// Auto-detected at chart import and editable in template settings. Empty for
	// templates that don't opt into image-driven CD (the publisher then falls
	// back to the legacy single-image behaviour).
	Images []TemplateImage `yaml:"images,omitempty"`
	// DeliveryMode is the default delivery mode for apps created from this
	// template: "pipeline" (default; "" == this) for CI-image apps promoted via
	// Kargo, or "direct" for off-the-shelf software (valkey, redis, postgres)
	// deployed to each env straight from values with no Kargo/promotion. The
	// create wizard pre-selects this; the user can override per app.
	DeliveryMode string `yaml:"deliveryMode,omitempty"`
}

// TemplateImage maps one of a chart's services to its image source and the Helm
// values key that holds its tag. It carries everything Kargo needs: the repo to
// watch (Warehouse subscription), the values key to rewrite on promotion (Stage
// helm image update), and how to discover/select tags.
type TemplateImage struct {
	// Name is a logical service identifier (e.g. "agent", "web"). Unique per template.
	Name string `yaml:"name"`
	// Repository is the container image repository to watch (no tag).
	Repository string `yaml:"repository"`
	// TagKey is the dotted Helm values key that holds this image's tag
	// (e.g. "image.tag" or "components.web.image.tag").
	TagKey string `yaml:"tagKey"`
	// TagPattern is an optional regex limiting which tags Kargo considers
	// (maps to the Warehouse subscription's allowTags).
	TagPattern string `yaml:"tagPattern,omitempty"`
	// SelectionStrategy is how Kargo picks the tag to promote: one of
	// "NewestBuild", "SemVer", "Digest", "Lexical". Empty defaults to "SemVer"
	// (Kargo's own default).
	SelectionStrategy string `yaml:"selectionStrategy,omitempty"`
}

// CanonicalValues reports whether the canonical suparship-common values base is
// injected for this template. Defaults to true (back-compat) when unset.
func (s TemplateSpec) CanonicalValues() bool {
	return s.InjectCanonicalValues == nil || *s.InjectCanonicalValues
}

// TemplateAddon declares one addon shape this template's chart
// produces (only meaningful for wrapper templates with
// category=addon). Mirrors TemplateComponent for symmetry — same
// produces / optionallyProduces story so chart-validate can assert
// the wrapper actually renders the contract Secret.
type TemplateAddon struct {
	// Name is the addon's identifier within the wrapper. Apps refer
	// to it via the resolved AddonProfile's Type, not this name —
	// kept for symmetry with TemplateComponent and for documentation.
	Name string `yaml:"name"`
	// Type identifies the connection contract (e.g. "redis"). Must
	// match a registered contracts.Lookup entry.
	Type string `yaml:"type"`
	// DefaultEnabled mirrors TemplateComponent semantics: nil ↔ true.
	DefaultEnabled *bool `yaml:"defaultEnabled,omitempty"`
	// Produces lists kinds the wrapper MUST render — at minimum the
	// "Secret" matching the type's connection contract. Chart-
	// validate's contract pass uses contracts.Lookup(Type).RequiredKeys
	// to assert the rendered Secret has the right contents.
	Produces []string `yaml:"produces,omitempty"`
	// OptionallyProduces lists kinds the wrapper MAY render
	// (StatefulSet, Service, PVC) depending on values.
	OptionallyProduces []string `yaml:"optionallyProduces,omitempty"`
}

// TemplateComponent declares one runtime unit within a template.
// It sets the defaults that feed into the app's ComponentSpec at creation
// time, while leaving per-app overrides to the user.
//
// DefaultEnabled uses a pointer so the YAML author can explicitly write
// "defaultEnabled: false" to opt out, distinguishing it from a field that
// was simply omitted (which defaults to true via IsDefaultEnabled).
type TemplateComponent struct {
	// Name uniquely identifies the component within this template.
	// Must be a lowercase alphanumeric-and-hyphen string (DNS label).
	Name string `yaml:"name"`
	// Type is the runtime role: web, worker, or cron.
	Type TemplateComponentType `yaml:"type"`
	// Required marks the component as non-removable: users cannot disable it
	// when creating an app from this template.
	Required bool `yaml:"required,omitempty"`
	// DefaultEnabled controls whether the component is enabled by default.
	// nil (omitted in YAML) is treated as true by IsDefaultEnabled.
	DefaultEnabled *bool `yaml:"defaultEnabled,omitempty"`
	// Exposed declares whether this component should receive an ingress
	// endpoint by default. Typically true only for web components. This
	// controls the *initial state* of the expose toggle; whether the toggle
	// is rendered at all is governed by Capabilities.Expose.
	Exposed bool `yaml:"exposed,omitempty"`
	// Produces lists Kubernetes resource kinds (e.g. "Deployment",
	// "Service", "CronJob") that this component MUST render when enabled.
	// Chart-validation at template import asserts the rendered chart
	// produces at least one of each kind for this component.
	//
	// Empty omits the assertion — useful for components where the
	// produced shape varies (e.g. an addon wrapper that delegates to
	// different upstream charts depending on env binding). New web /
	// worker / cron components SHOULD declare this; absence is tolerated
	// for backwards compatibility.
	Produces []string `yaml:"produces,omitempty"`
	// OptionallyProduces lists kinds the chart MAY render based on
	// values (Ingress / HTTPRoute / ScaledObject / PodDisruptionBudget).
	// Documents what's possible without enforcing presence. Used by the
	// UI to render capability-aware input groups.
	OptionallyProduces []string `yaml:"optionallyProduces,omitempty"`
	// Capabilities declares which UI input groups apply to this
	// component. Templates declare only the capabilities they want to
	// *override* from the type-based defaults; ResolvedCapabilities()
	// fills in the rest. See ComponentCapabilities for the vocabulary.
	Capabilities ComponentCapabilities `yaml:"capabilities,omitempty"`
	// Defaults seeds the per-component config (raw resources, envFrom, KEDA
	// scaling, env overrides) into the app's ComponentSpec at creation.
	// Operators override per app / per environment afterward in the UI.
	Defaults *ComponentDefaults `yaml:"defaults,omitempty"`
}

// ComponentDefaults are the per-component config defaults a template declares.
// Copied into domain.ComponentSpec at app creation. Mirrors the per-component
// knobs (resources / envFrom / scaling / env).
type ComponentDefaults struct {
	Resources         *ComponentResources `yaml:"resources,omitempty"`
	EnvFromSecrets    []string            `yaml:"envFromSecrets,omitempty"`
	EnvFromConfigMaps []string            `yaml:"envFromConfigMaps,omitempty"`
	Scaling           *ComponentScaling   `yaml:"scaling,omitempty"`
	Env               map[string]string   `yaml:"env,omitempty"`
}

// ComponentResources mirrors domain.ComponentResources for template YAML.
type ComponentResources struct {
	Requests map[string]string `yaml:"requests,omitempty"`
	Limits   map[string]string `yaml:"limits,omitempty"`
}

// KEDATrigger mirrors domain.KEDATrigger for template YAML.
type KEDATrigger struct {
	Type       string            `yaml:"type"`
	MetricType string            `yaml:"metricType,omitempty"`
	Metadata   map[string]string `yaml:"metadata,omitempty"`
}

// ComponentScaling mirrors domain.ComponentScaling for template YAML.
type ComponentScaling struct {
	Triggers    []KEDATrigger `yaml:"triggers,omitempty"`
	MinReplicas *int32        `yaml:"minReplicas,omitempty"`
	MaxReplicas *int32        `yaml:"maxReplicas,omitempty"`
}

// ComponentCapabilities declares which input groups the UI should
// render for a component, replacing the prior "every web has
// autoscaling, every cron has schedule" hardcoding in the frontend.
//
// Bool fields are pointers so chart authors can distinguish "not
// declared (use type default)" from "explicitly off / on". String
// fields use the empty string for "not declared".
//
// Type-based defaults (filled in by ResolvedCapabilities when fields
// are unset):
//
//	web    — expose=true, routing=ingress, autoscaling=keda, pdb=true,
//	         resources=true, replicas=true, schedule=false
//	worker — expose=false, routing=none, autoscaling=keda, pdb=true,
//	         resources=true, replicas=true, schedule=false
//	cron   — expose=false, routing=none, autoscaling=none, pdb=false,
//	         resources=true, replicas=false, schedule=true
type ComponentCapabilities struct {
	// Expose controls whether the UI shows the externally-expose toggle
	// for this component. Default-on for type=web; off for worker / cron.
	Expose *bool `yaml:"expose,omitempty" json:"expose,omitempty"`
	// Routing declares which routing fabric the chart wires up when
	// the component is exposed. UI surfaces fabric-specific inputs
	// (gateway name+namespace, ingress class, …) based on this.
	//
	// "" → use type default. "none" → suppress host input even when
	// expose=true (e.g. internal-only services). "ingress" / "gateway".
	Routing string `yaml:"routing,omitempty" json:"routing,omitempty"`
	// Autoscaling declares which autoscaling backend the chart wires
	// for this component. Drives whether the UI shows the scaling input
	// group and which fields (HPA = CPU% only; KEDA = cpu+memory + free-
	// form triggers list).
	//
	// "" → use type default. "none" → no input group rendered. "hpa" /
	// "keda".
	Autoscaling string `yaml:"autoscaling,omitempty" json:"autoscaling,omitempty"`
	// PDB declares whether the chart renders a PodDisruptionBudget for
	// this component. UI shows minAvailable / maxUnavailable inputs
	// (advanced) when true.
	PDB *bool `yaml:"pdb,omitempty" json:"pdb,omitempty"`
	// Resources declares whether the chart honors
	// components.<name>.resources.size. UI shows the small/medium/large
	// dropdown when true. Stateful workloads with explicit
	// requests/limits set this false.
	Resources *bool `yaml:"resources,omitempty" json:"resources,omitempty"`
	// Replicas declares whether the chart honors
	// components.<name>.replicas. UI shows the replicas slider when
	// true. Components with policy-driven replica counts (always 1,
	// quorum-bound) set this false.
	Replicas *bool `yaml:"replicas,omitempty" json:"replicas,omitempty"`
	// Schedule declares whether the component takes a cron schedule
	// input. Default-on for type=cron; off otherwise.
	Schedule *bool `yaml:"schedule,omitempty" json:"schedule,omitempty"`
}

// ResolvedCapabilities returns the component's capabilities with
// type-based defaults filled in. Authors only declare what they want
// to override; everything else falls back to the per-type default.
//
// Returned values use bool (not *bool), so the UI gets a fully
// resolved view ready to drive form rendering.
func (c TemplateComponent) ResolvedCapabilities() ResolvedCapabilities {
	d := defaultCapabilities(c.Type)

	out := ResolvedCapabilities{
		Expose:      d.Expose,
		Routing:     d.Routing,
		Autoscaling: d.Autoscaling,
		PDB:         d.PDB,
		Resources:   d.Resources,
		Replicas:    d.Replicas,
		Schedule:    d.Schedule,
	}
	if c.Capabilities.Expose != nil {
		out.Expose = *c.Capabilities.Expose
	}
	if c.Capabilities.Routing != "" {
		out.Routing = c.Capabilities.Routing
	}
	if c.Capabilities.Autoscaling != "" {
		out.Autoscaling = c.Capabilities.Autoscaling
	}
	if c.Capabilities.PDB != nil {
		out.PDB = *c.Capabilities.PDB
	}
	if c.Capabilities.Resources != nil {
		out.Resources = *c.Capabilities.Resources
	}
	if c.Capabilities.Replicas != nil {
		out.Replicas = *c.Capabilities.Replicas
	}
	if c.Capabilities.Schedule != nil {
		out.Schedule = *c.Capabilities.Schedule
	}
	return out
}

// ResolvedCapabilities is the UI-facing flat view: every field set,
// no nils. Pure values, deterministic serialisation.
type ResolvedCapabilities struct {
	Expose      bool   `json:"expose"`
	Routing     string `json:"routing"`
	Autoscaling string `json:"autoscaling"`
	PDB         bool   `json:"pdb"`
	Resources   bool   `json:"resources"`
	Replicas    bool   `json:"replicas"`
	Schedule    bool   `json:"schedule"`
}

// defaultCapabilities returns the baseline capability set for a given
// component type. Templates override individual fields via
// TemplateComponent.Capabilities.
func defaultCapabilities(t TemplateComponentType) ResolvedCapabilities {
	switch t {
	case TemplateComponentWeb:
		return ResolvedCapabilities{
			Expose:      true,
			Routing:     "ingress",
			Autoscaling: "keda",
			PDB:         true,
			Resources:   true,
			Replicas:    true,
		}
	case TemplateComponentWorker:
		return ResolvedCapabilities{
			Routing:     "none",
			Autoscaling: "keda",
			PDB:         true,
			Resources:   true,
			Replicas:    true,
		}
	case TemplateComponentCron:
		return ResolvedCapabilities{
			Routing:   "none",
			Resources: true,
			Schedule:  true,
		}
	}
	// Unknown type: permissive default so authors of new types aren't
	// stuck behind a code change.
	return ResolvedCapabilities{
		Resources: true,
		Replicas:  true,
	}
}

// IsDefaultEnabled returns true when the component is enabled by default.
// A nil DefaultEnabled (field omitted in YAML) is treated as true.
func (c TemplateComponent) IsDefaultEnabled() bool {
	return c.DefaultEnabled == nil || *c.DefaultEnabled
}

// Engine specifies the rendering backend for the template.
type Engine struct {
	Type  string       `yaml:"type"`
	Chart ChartLocator `yaml:"chart,omitempty"`
}

// ChartLocator is a oneOf describing where the Helm chart for this template
// lives. Three legal shapes (the field is omitted from YAML in the bundled
// case via IsZero):
//
//   - Bundled  (zero value):  The chart is the .tgz this template was loaded
//     from. Used when a chart author ships a template.yaml inside their chart
//     and it gets pulled from a registry, or when the BYO upload flow imports
//     a chart that ships its own template.yaml.
//   - Inline   (Path != ""):  Relative path (e.g. "./chart") inside the
//     templates repo this template was loaded from. Today's convention.
//   - External (Ref != nil): Reference to a chart in a Helm registry. The
//     publisher emits an Argo-native multi-source Application; the chart is
//     never copied into the gitops repo.
//
// At most one of Path / Ref may be set; the validator enforces this.
//
// The custom YAML marshaller emits the right shape for each mode and accepts
// either a scalar (string → Path) or a mapping (→ Ref) on parse.
type ChartLocator struct {
	Path string
	Ref  *ChartRef
}

// ChartRef is the registry-ref form of a chart locator.
type ChartRef struct {
	// Repository is the chart registry URL. Examples:
	//   oci://ghcr.io/myorg/charts
	//   https://charts.acme.io
	Repository string `yaml:"repository" json:"repository"`
	// Name is the chart name within the registry.
	Name string `yaml:"name" json:"name"`
	// Version pins the chart to a specific release. SemVer string.
	Version string `yaml:"version" json:"version"`
}

// IsBundled reports whether the locator is the bundled (zero) form.
func (l ChartLocator) IsBundled() bool { return l.Path == "" && l.Ref == nil }

// IsInline reports whether the locator is the relative-path form.
func (l ChartLocator) IsInline() bool { return l.Path != "" }

// IsExternal reports whether the locator is the registry-ref form.
func (l ChartLocator) IsExternal() bool { return l.Ref != nil }

// IsZero satisfies yaml.IsZeroer so `omitempty` drops the field for bundled
// locators. Without this the encoder emits `chart: {}` for the zero struct.
func (l ChartLocator) IsZero() bool { return l.IsBundled() }

// MarshalYAML emits the locator in the right shape for its current mode.
// Bundled is handled by omitempty + IsZero (this method isn't called for
// the zero value).
func (l ChartLocator) MarshalYAML() (any, error) {
	if l.IsExternal() {
		return l.Ref, nil
	}
	if l.IsInline() {
		return l.Path, nil
	}
	// Defensive: shouldn't be reached because IsZero covers bundled.
	return nil, nil
}

// UnmarshalYAML accepts either a scalar (relative path) or a mapping
// (ChartRef). An absent field leaves the locator zero (bundled).
func (l *ChartLocator) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		// Empty scalar → treat as bundled. Lets `chart:` (with nothing
		// after the colon) parse as bundled rather than as path "".
		if node.Tag == "!!null" || node.Value == "" {
			return nil
		}
		l.Path = node.Value
		return nil
	case yaml.MappingNode:
		var ref ChartRef
		if err := node.Decode(&ref); err != nil {
			return fmt.Errorf("engine.chart: %w", err)
		}
		l.Ref = &ref
		return nil
	default:
		return fmt.Errorf("engine.chart: expected string or mapping, got node kind %d", node.Kind)
	}
}

// Input defines a user-configurable parameter.
type Input struct {
	Name        string    `yaml:"name"`
	Title       string    `yaml:"title"`
	Type        InputType `yaml:"type"`
	Description string    `yaml:"description,omitempty"`
	Required    bool      `yaml:"required,omitempty"`
	Default     any       `yaml:"default,omitempty"`
	Options     []string  `yaml:"options,omitempty"`
	Min         *float64  `yaml:"min,omitempty"`
	Max         *float64  `yaml:"max,omitempty"`
	Pattern     string    `yaml:"pattern,omitempty"`
	// Component scopes this input to a specific named component defined in
	// spec.components. When set, the input value configures only that
	// component. Must match a name in spec.components when non-empty.
	// Omit for app-level inputs that apply across all components.
	Component string `yaml:"component,omitempty"`
}

// SecretInput defines a parameter whose value is a reference to a
// Kubernetes Secret key, never a literal value.
type SecretInput struct {
	Name        string `yaml:"name"`
	Title       string `yaml:"title"`
	Description string `yaml:"description,omitempty"`
	SecretRef   string `yaml:"secretRef"`
}

// Preset provides a named set of default input values.
type Preset struct {
	Name        string         `yaml:"name"`
	Title       string         `yaml:"title"`
	Description string         `yaml:"description,omitempty"`
	Values      map[string]any `yaml:"values"`
}

// Parse deserializes and validates a Template from YAML bytes.
func Parse(data []byte) (*Template, error) {
	var t Template
	if err := yaml.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("parsing template: %w", err)
	}
	if err := t.Validate(); err != nil {
		return nil, fmt.Errorf("invalid template: %w", err)
	}
	return &t, nil
}

// Marshal serializes a validated Template back to YAML.
func Marshal(t *Template) ([]byte, error) {
	if t == nil {
		return nil, fmt.Errorf("nil template")
	}
	out, err := yaml.Marshal(t)
	if err != nil {
		return nil, fmt.Errorf("marshal template: %w", err)
	}
	return out, nil
}
