// Package preview defines the data model for suparship preview environments.
//
// A preview is an ephemeral, branch-scoped deployment of a service within a
// project. Each preview is stored as a Kubernetes ConfigMap in the
// suparship-system namespace:
//
//	apiVersion: v1
//	kind: ConfigMap
//	metadata:
//	  name: suparship-preview-pr-42
//	  namespace: suparship-system
//	  labels:
//	    suparship.io/type: preview
//	    suparship.io/project: myapi
//	data:
//	  preview.yaml: |
//	    apiVersion: suparship.io/v1alpha1
//	    kind: Preview
//	    metadata:
//	      name: pr-42
//	      createdAt: "2026-03-27T12:00:00Z"
//	    spec:
//	      project: myapi
//	      service: api
//
// The namespace convention for preview environments is
// {project}-preview-{name}, e.g. myapi-preview-pr-42.
package preview

import (
	"fmt"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	CurrentAPIVersion = "suparship.io/v1alpha1"
	PreviewKind       = "Preview"
	ConfigMapKey      = "preview.yaml"
	ConfigMapPrefix   = "suparship-preview-"
	Namespace         = "suparship-system"
)

// dns-compatible name: lowercase alphanumeric and hyphens, 2-63 chars.
var dnsNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

// Preview describes an ephemeral preview environment for a service.
type Preview struct {
	APIVersion string      `yaml:"apiVersion"`
	Kind       string      `yaml:"kind"`
	Metadata   PreviewMeta `yaml:"metadata"`
	Spec       PreviewSpec `yaml:"spec"`
}

// PreviewMeta identifies a preview.
type PreviewMeta struct {
	Name      string `yaml:"name"`
	CreatedAt string `yaml:"createdAt"`
}

// PreviewSpec defines the preview target.
type PreviewSpec struct {
	Project string `yaml:"project"`
	Service string `yaml:"service"`
}

// New creates a validated Preview with the current timestamp.
func New(name, projectName, service string) *Preview {
	return &Preview{
		APIVersion: CurrentAPIVersion,
		Kind:       PreviewKind,
		Metadata: PreviewMeta{
			Name:      name,
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		},
		Spec: PreviewSpec{
			Project: projectName,
			Service: service,
		},
	}
}

// Validate checks the Preview for structural correctness.
func (p *Preview) Validate() error {
	if p.Metadata.Name == "" {
		return fmt.Errorf("metadata.name must not be empty")
	}
	if !dnsNameRE.MatchString(p.Metadata.Name) {
		return fmt.Errorf("metadata.name %q must be a valid DNS label (lowercase alphanumeric and hyphens, 2-63 chars)", p.Metadata.Name)
	}
	if p.Spec.Project == "" {
		return fmt.Errorf("spec.project must not be empty")
	}
	if p.Spec.Service == "" {
		return fmt.Errorf("spec.service must not be empty")
	}
	return nil
}

// PreviewNamespace returns the Kubernetes namespace for this preview.
func (p *Preview) PreviewNamespace() string {
	return PreviewNS(p.Spec.Project, p.Metadata.Name)
}

// PreviewNS returns the conventional namespace for a preview.
func PreviewNS(project, name string) string {
	return project + "-preview-" + name
}

// ConfigMapName returns the expected ConfigMap name for this preview.
func (p *Preview) ConfigMapName() string {
	return ConfigMapPrefix + p.Metadata.Name
}

// Parse deserializes and validates a Preview from YAML bytes.
func Parse(data []byte) (*Preview, error) {
	var p Preview
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parsing preview: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("invalid preview: %w", err)
	}
	return &p, nil
}

// Marshal serializes the Preview to YAML.
func (p *Preview) Marshal() ([]byte, error) {
	data, err := yaml.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("marshaling preview: %w", err)
	}
	return data, nil
}
