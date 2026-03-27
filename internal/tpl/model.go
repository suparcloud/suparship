// Package tpl defines the suparship template metadata schema.
//
// Templates describe golden paths for deploying services. Each template
// lives in its own directory and is defined by a template.yaml file:
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
//	  title: Web Service
//	  description: Deploy a containerized web service
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
	Title          string            `yaml:"title"`
	Description    string            `yaml:"description,omitempty"`
	Category       string            `yaml:"category"`
	Engine         Engine            `yaml:"engine"`
	Inputs         []Input           `yaml:"inputs,omitempty"`
	AdvancedInputs []Input           `yaml:"advancedInputs,omitempty"`
	SecretInputs   []SecretInput     `yaml:"secretInputs,omitempty"`
	Mappings       map[string]string `yaml:"mappings,omitempty"`
	Presets        []Preset          `yaml:"presets,omitempty"`
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
