// Package project defines the data model for suparship projects and services.
//
// A project groups services that share environments and promotion pipelines.
// Each project is stored as a Kubernetes ConfigMap in the suparship-system
// namespace:
//
//	apiVersion: v1
//	kind: ConfigMap
//	metadata:
//	  name: suparship-project-myapi
//	  namespace: suparship-system
//	data:
//	  project.yaml: |
//	    apiVersion: suparship.io/v1alpha1
//	    kind: Project
//	    metadata:
//	      name: myapi
//	    spec:
//	      displayName: My API
//	      environments:
//	        - name: dev
//	          order: 1
//	        - name: staging
//	          order: 2
//	        - name: prod
//	          order: 3
//	      services:
//	        - name: api
//	          template:
//	            name: web-service
//	            version: "1.0.0"
//	          values:
//	            image_repository: ghcr.io/org/api
//	            size: small
//	          secretRefs:
//	            - name: database_url
//	              secretRef: api-db.url
//	          environmentOverrides:
//	            prod:
//	              values:
//	                size: large
package project

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

const (
	CurrentAPIVersion = "suparship.io/v1alpha1"
	ProjectKind       = "Project"
	ConfigMapKey      = "project.yaml"
	ConfigMapPrefix   = "suparship-project-"
	Namespace         = "suparship-system"
)

// Project is the top-level resource describing a project and its services.
type Project struct {
	APIVersion string      `yaml:"apiVersion"`
	Kind       string      `yaml:"kind"`
	Metadata   ProjectMeta `yaml:"metadata"`
	Spec       ProjectSpec `yaml:"spec"`
}

// ProjectMeta identifies a project.
type ProjectMeta struct {
	Name string `yaml:"name"`
}

// ProjectSpec defines the project's environments and services.
type ProjectSpec struct {
	DisplayName  string        `yaml:"displayName,omitempty"`
	Description  string        `yaml:"description,omitempty"`
	Environments []Environment `yaml:"environments"`
	Services     []Service     `yaml:"services,omitempty"`
}

// Environment represents a deployment target in the promotion chain.
type Environment struct {
	Name        string `yaml:"name"`
	DisplayName string `yaml:"displayName,omitempty"`
	Order       int    `yaml:"order"`
}

// Service describes a deployable workload within a project.
type Service struct {
	Name                 string                         `yaml:"name"`
	Template             TemplateRef                    `yaml:"template"`
	Values               map[string]any                 `yaml:"values,omitempty"`
	SecretRefs           []SecretRef                    `yaml:"secretRefs,omitempty"`
	EnvironmentOverrides map[string]EnvironmentOverride `yaml:"environmentOverrides,omitempty"`
}

// TemplateRef references a suparship template by name and optional version.
type TemplateRef struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version,omitempty"`
}

// SecretRef maps a template secret input to a Kubernetes Secret key.
type SecretRef struct {
	Name      string `yaml:"name"`
	SecretRef string `yaml:"secretRef"`
}

// EnvironmentOverride provides per-environment value and secret overrides.
type EnvironmentOverride struct {
	Values     map[string]any `yaml:"values,omitempty"`
	SecretRefs []SecretRef    `yaml:"secretRefs,omitempty"`
}

// Parse deserializes and validates a Project from YAML bytes.
func Parse(data []byte) (*Project, error) {
	var p Project
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parsing project: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("invalid project: %w", err)
	}
	return &p, nil
}

// Marshal serializes the Project to YAML.
func (p *Project) Marshal() ([]byte, error) {
	data, err := yaml.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("marshaling project: %w", err)
	}
	return data, nil
}

// ConfigMapName returns the expected ConfigMap name for this project.
func (p *Project) ConfigMapName() string {
	return ConfigMapPrefix + p.Metadata.Name
}
