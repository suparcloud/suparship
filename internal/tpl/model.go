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
// individual components appear in advanced views only. Templates control which
// components participate in preview environments via PreviewEnabled.
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
	Title          string              `yaml:"title"`
	Description    string              `yaml:"description,omitempty"`
	Category       string              `yaml:"category"`
	Engine         Engine              `yaml:"engine"`
	// Components declares the named runtime units this template produces.
	// When absent, the platform derives a single default component from
	// Category (backwards-compatible behaviour). When present, each entry
	// defines defaults that the user can override at app-creation time.
	Components     []TemplateComponent `yaml:"components,omitempty"`
	Inputs         []Input             `yaml:"inputs,omitempty"`
	AdvancedInputs []Input             `yaml:"advancedInputs,omitempty"`
	SecretInputs   []SecretInput       `yaml:"secretInputs,omitempty"`
	Mappings       map[string]string   `yaml:"mappings,omitempty"`
	Presets        []Preset            `yaml:"presets,omitempty"`
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
	// PreviewEnabled declares whether this component is deployed in preview
	// environments by default. Typically true for web, false for worker/cron.
	PreviewEnabled bool `yaml:"previewEnabled,omitempty"`
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
	Type  string `yaml:"type"`
	Chart string `yaml:"chart,omitempty"`
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
