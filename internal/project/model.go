// Package project defines the persistence model for suparship projects.
//
// A project groups apps that share environments and a common promotion
// pipeline. Each project is stored as a Kubernetes ConfigMap in the
// suparship-system namespace. The "services" key in the YAML spec is a legacy
// field that maps to apps; new code should prefer the domain.App model via
// domain.AppStore. See docs/migration-app-model.md for the transition guide.
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
//	        - name: staging
//	          order: 1
//	        - name: prod
//	          order: 2
//	      services:   # legacy field — treated as apps
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

	"github.com/suparcloud/suparship/internal/envconfig"
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

// ProjectSpec defines the project's environments and apps.
type ProjectSpec struct {
	DisplayName  string        `yaml:"displayName,omitempty"`
	Description  string        `yaml:"description,omitempty"`
	Environments []Environment `yaml:"environments"`
	// Services is the legacy YAML key for apps. The field name is preserved for
	// backward-compatible parsing of existing ConfigMaps. New code should use
	// domain.App and domain.AppStore; do not add fields to this struct.
	// See docs/app-model.md and docs/migration-app-model.md.
	Services []Service `yaml:"services,omitempty"`
	// EnvConfig holds env vars and secret refs that apply to all apps within
	// this project (Project level of the hierarchy).
	EnvConfig envconfig.EnvConfig `yaml:"envConfig,omitempty"`
	// NamespacePattern overrides the org-level default for namespace naming.
	// Controls both the project's own Kubernetes namespace (when apps use
	// NamespaceScopeProject) and the fallback for app namespaces.
	// Tokens: {org}, {project}, {env}.
	// Empty = inherit from OrgEnvironment.NamespacePattern or org ResourceNaming.
	NamespacePattern string `yaml:"namespacePattern,omitempty"`
}

// Environment represents a deployment target in the promotion chain.
type Environment struct {
	Name        string `yaml:"name"`
	DisplayName string `yaml:"displayName,omitempty"`
	Order       int    `yaml:"order"`

	// ClusterRef is the name of the registered Cluster this environment
	// deploys to. When empty the environment is not yet bound to a cluster.
	ClusterRef string `yaml:"clusterRef,omitempty"`

	// BaseDomain is the ingress base domain for apps in this environment.
	// App URLs are derived as: http://{app}.{baseDomain}
	// Defaults to "localhost" when empty.
	BaseDomain string `yaml:"baseDomain,omitempty"`

	// NamespacePattern controls how Kubernetes namespaces are generated.
	// Tokens: {app}, {env}, {project}.
	// Default (empty string) falls back to "{app}-{env}".
	NamespacePattern string `yaml:"namespacePattern,omitempty"`
}

// Service describes a deployable workload (app) persisted in the legacy
// services: YAML block. It is the on-disk representation only; at runtime this
// maps to domain.App via the compatibility bridge in internal/compat.
//
// Do not extend this struct with new product fields. Add those to domain.App
// and domain.AppSpec instead. See docs/app-model.md.
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
