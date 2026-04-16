// Package rbac provides the authorization data model for suparship.
//
// The org configuration is stored in a Kubernetes ConfigMap:
//
//	apiVersion: v1
//	kind: ConfigMap
//	metadata:
//	  name: suparship-org-config
//	  namespace: suparship-system
//	data:
//	  org.yaml: |
//	    name: default
//	    displayName: My Organization
//	    teams:
//	      - name: admins
//	        displayName: Administrators
//	        members: [admin]
//	    roleBindings:
//	      - project: "*"
//	        team: admins
//	        role: org_admin
package rbac

import (
	"fmt"
	"time"

	"github.com/suparcloud/suparship/internal/envconfig"
	"github.com/suparcloud/suparship/internal/secrets"
	"gopkg.in/yaml.v3"
)

// ConfigMap coordinates for the org configuration.
const (
	ConfigMapNamespace = "suparship-system"
	ConfigMapName      = "suparship-org-config"
	ConfigMapKey       = "org.yaml"
)

// Role represents a named authorization role.
type Role string

const (
	RoleOrgAdmin     Role = "org_admin"
	RoleProjectAdmin Role = "project_admin"
	RoleDeveloper    Role = "developer"
	RoleViewer       Role = "viewer"
)

// AllRoles lists all valid roles from highest to lowest privilege.
var AllRoles = []Role{RoleOrgAdmin, RoleProjectAdmin, RoleDeveloper, RoleViewer}

// IsValidRole reports whether r is a recognized role.
func IsValidRole(r Role) bool {
	for _, v := range AllRoles {
		if v == r {
			return true
		}
	}
	return false
}

// OrgEnvironment is the org-level canonical definition of a deployment
// environment (e.g. staging, prod). All projects inherit these environments
// by default; projects may store per-environment overrides in their own
// ConfigMap without duplicating the full definition.
type OrgEnvironment struct {
	// Name is the unique identifier (e.g. "staging", "prod").
	Name string `yaml:"name"`
	// DisplayName is a human-readable label shown in the UI.
	DisplayName string `yaml:"displayName,omitempty"`
	// Order controls the promotion pipeline sequence (lower = earlier).
	Order int `yaml:"order"`
	// ClusterRef is the name of the registered Cluster this environment
	// deploys to. When empty the environment is not yet bound to a cluster.
	ClusterRef string `yaml:"clusterRef,omitempty"`
	// BaseDomain is the ingress base domain for apps in this environment.
	// App URLs are derived as: http://{app}.{baseDomain}
	BaseDomain string `yaml:"baseDomain,omitempty"`
	// NamespacePattern controls Kubernetes namespace naming.
	// Tokens: {app}, {env}, {project}.
	// Default (empty) falls back to "{app}-{env}".
	NamespacePattern string `yaml:"namespacePattern,omitempty"`
	// EnvConfig holds env vars and secret refs that apply to all apps
	// deployed to this environment type (Environment level of the hierarchy).
	EnvConfig envconfig.EnvConfig `yaml:"envConfig,omitempty"`
}

// Org represents a single organization.
type Org struct {
	Name        string           `yaml:"name"`
	DisplayName string           `yaml:"displayName"`
	CreatedAt   string           `yaml:"createdAt,omitempty"`
	// Environments is the canonical deployment pipeline shared by all projects.
	// Projects may store per-environment overrides but inherit these defaults.
	Environments []OrgEnvironment `yaml:"environments,omitempty"`
	Teams        []Team           `yaml:"teams"`
	RoleBindings []RoleBinding    `yaml:"roleBindings"`
	// EnvConfig holds env vars and secret refs that apply to ALL apps across
	// ALL projects in the org (Org level of the hierarchy).
	EnvConfig envconfig.EnvConfig `yaml:"envConfig,omitempty"`
	// SecretBackend selects the backend used to store app-level secrets.
	// Defaults to "k8s" (native Kubernetes Secrets) when absent.
	SecretBackend secrets.BackendConfig `yaml:"secretBackend,omitempty"`
}

// Team represents a named group of users.
type Team struct {
	Name        string   `yaml:"name"`
	DisplayName string   `yaml:"displayName"`
	Members     []string `yaml:"members"`
}

// RoleBinding assigns a role to a team for a project.
// Project "*" matches all projects.
type RoleBinding struct {
	Project string `yaml:"project"`
	Team    string `yaml:"team"`
	Role    Role   `yaml:"role"`
}

// NewDefaultOrg creates a minimal org with a single admins team containing
// the given admin user, bound to org_admin on all projects.
func NewDefaultOrg(orgName, displayName, adminUsername string) *Org {
	return &Org{
		Name:        orgName,
		DisplayName: displayName,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		Teams: []Team{
			{
				Name:        "admins",
				DisplayName: "Administrators",
				Members:     []string{adminUsername},
			},
		},
		RoleBindings: []RoleBinding{
			{
				Project: "*",
				Team:    "admins",
				Role:    RoleOrgAdmin,
			},
		},
	}
}

// Validate checks the Org for structural correctness.
func (o *Org) Validate() error {
	if o.Name == "" {
		return fmt.Errorf("org name must not be empty")
	}

	envNames := make(map[string]bool, len(o.Environments))
	for i, e := range o.Environments {
		if e.Name == "" {
			return fmt.Errorf("environments[%d]: name must not be empty", i)
		}
		if envNames[e.Name] {
			return fmt.Errorf("duplicate environment name %q", e.Name)
		}
		envNames[e.Name] = true
	}

	teamNames := make(map[string]bool, len(o.Teams))
	for i, t := range o.Teams {
		if t.Name == "" {
			return fmt.Errorf("team[%d]: name must not be empty", i)
		}
		if teamNames[t.Name] {
			return fmt.Errorf("duplicate team name %q", t.Name)
		}
		teamNames[t.Name] = true
	}

	for i, rb := range o.RoleBindings {
		if rb.Project == "" {
			return fmt.Errorf("roleBindings[%d]: project must not be empty", i)
		}
		if rb.Team == "" {
			return fmt.Errorf("roleBindings[%d]: team must not be empty", i)
		}
		if !teamNames[rb.Team] {
			return fmt.Errorf("roleBindings[%d]: references unknown team %q", i, rb.Team)
		}
		if !IsValidRole(rb.Role) {
			return fmt.Errorf("roleBindings[%d]: unknown role %q", i, rb.Role)
		}
	}

	return nil
}

// ParseOrg deserializes and validates an Org from YAML bytes.
func ParseOrg(data []byte) (*Org, error) {
	var org Org
	if err := yaml.Unmarshal(data, &org); err != nil {
		return nil, fmt.Errorf("parsing org config: %w", err)
	}
	if err := org.Validate(); err != nil {
		return nil, fmt.Errorf("invalid org config: %w", err)
	}
	return &org, nil
}

// Marshal serializes the Org to YAML.
func (o *Org) Marshal() ([]byte, error) {
	data, err := yaml.Marshal(o)
	if err != nil {
		return nil, fmt.Errorf("marshaling org config: %w", err)
	}
	return data, nil
}
