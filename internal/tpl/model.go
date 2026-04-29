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
	// endpoint by default. Typically true only for web components.
	Exposed bool `yaml:"exposed,omitempty"`
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
