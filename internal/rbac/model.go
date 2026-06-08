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

	"github.com/suparcloud/suparship/internal/branding"
	"github.com/suparcloud/suparship/internal/domain"
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
	// ClusterRefs lists every registered Cluster this environment is bound
	// to. Empty means the environment is not yet bound. Today only
	// ActiveClusterRef receives deploys; the rest are reserved for future
	// multi-cluster fan-out (deploy-mode = "all" or "select").
	ClusterRefs []string `yaml:"clusterRefs,omitempty"`
	// ActiveClusterRef is the single cluster from ClusterRefs that currently
	// receives deploys. Validated to be a member of ClusterRefs (or empty).
	// EffectiveClusterRef falls back to ClusterRefs[0] when empty.
	ActiveClusterRef string `yaml:"activeClusterRef,omitempty"`
	// BaseDomain is the ingress base domain for apps in this environment.
	// App URLs are derived as: http://{app}.{baseDomain}
	BaseDomain string `yaml:"baseDomain,omitempty"`
	// AppNamespacePattern overrides the K8s namespace pattern for apps in
	// this environment that use NamespaceScopeApp ("Dedicated namespace").
	// Tokens: {app}, {env}, {project}, {org}.
	AppNamespacePattern string `yaml:"appNamespacePattern,omitempty"`
	// ProjectNamespacePattern overrides the K8s namespace pattern for apps
	// in this environment that use NamespaceScopeProject ("Project namespace").
	// Tokens: {env}, {project}, {org}. Note: {app} is intentionally not
	// honoured here — a project-scope namespace is shared across apps.
	ProjectNamespacePattern string `yaml:"projectNamespacePattern,omitempty"`
	// NamespacePattern is deprecated; retained so configs written by older
	// suparship versions still load. Treated as AppNamespacePattern when
	// the new fields are empty (preserves backward-compatible behaviour
	// for app-scope; project-scope ignores it because it usually contains
	// {app}, which doesn't make sense for shared project namespaces).
	//
	// Deprecated: use AppNamespacePattern.
	NamespacePattern string `yaml:"namespacePattern,omitempty"`
	// EnvConfig holds env vars and secret refs that apply to all apps
	// deployed to this environment type (Environment level of the hierarchy).
	EnvConfig envconfig.EnvConfig `yaml:"envConfig,omitempty"`
	// RoutingProfiles is a sparse override map keyed by ExposeMode name
	// (e.g. "internal", "external"). Entries here replace the org-level
	// profile of the same name for apps deployed to this environment;
	// names not present here inherit the org default. Use this for
	// per-env differences like "staging uses letsencrypt-staging, prod
	// uses letsencrypt-prod".
	RoutingProfiles domain.RoutingProfiles `yaml:"routingProfiles,omitempty"`
	// AddonProfiles is a sparse override map keyed by addon type
	// (e.g. "redis", "postgres"). Entries here replace the org-level
	// addon profile of the same type for apps deployed to this
	// environment; types not present inherit the org default. Use
	// this for per-env provider swaps like "staging uses
	// valkey-operator, prod uses Crossplane ElastiCache".
	AddonProfiles domain.AddonProfiles `yaml:"addonProfiles,omitempty"`
}

// EffectiveAppNamespacePattern returns the per-env override that applies to
// apps with NamespaceScopeApp. Falls back to the legacy NamespacePattern for
// configs written before the field was split.
func (e OrgEnvironment) EffectiveAppNamespacePattern() string {
	if e.AppNamespacePattern != "" {
		return e.AppNamespacePattern
	}
	return e.NamespacePattern
}

// EffectiveClusterRef returns the cluster this environment currently deploys
// to. Mirrors domain.Environment.EffectiveClusterRef: ActiveClusterRef wins,
// else ClusterRefs[0], else "" (env unbound). Read through this everywhere
// instead of poking ClusterRefs / ActiveClusterRef directly.
func (e OrgEnvironment) EffectiveClusterRef() string {
	if e.ActiveClusterRef != "" {
		return e.ActiveClusterRef
	}
	if len(e.ClusterRefs) > 0 {
		return e.ClusterRefs[0]
	}
	return ""
}

// EffectiveProjectNamespacePattern returns the per-env override that applies
// to apps with NamespaceScopeProject. The legacy NamespacePattern is NOT used
// as a fallback because it commonly contains {app}, which doesn't render
// meaningfully into a shared project namespace; operators must opt in
// explicitly by setting ProjectNamespacePattern.
func (e OrgEnvironment) EffectiveProjectNamespacePattern() string {
	return e.ProjectNamespacePattern
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
	// ResourceNaming holds configurable naming patterns for K8s resources
	// and vault items. Allows vendor-neutral names outside suparship-system.
	ResourceNaming secrets.ResourceNaming `yaml:"resourceNaming,omitempty"`
	// Branding controls how the platform identifies itself in the GitOps
	// output (label domain + label/annotation values, commit author name).
	// Defaults — "suparship" / "suparship.io" — apply when fields are empty.
	// SRE contractors who white-label suparship can set their own platform
	// name without touching individual generators.
	Branding branding.Config `yaml:"branding,omitempty"`
	// RoutingProfiles maps ExposeMode names to the ingress class + cert-manager
	// ClusterIssuer the chart should use for components targeting that tier.
	// Per-env overrides live on OrgEnvironment.RoutingProfiles and replace
	// matching entries by name. When empty, the helmvalues mapper falls back
	// to a legacy Expose=true → nginx shim so existing AppSpecs without
	// ExposeMode keep rendering identically until they are migrated.
	RoutingProfiles domain.RoutingProfiles `yaml:"routingProfiles,omitempty"`
	// AddonProfiles maps addon types (redis, postgres, …) to the
	// wrapper chart + provider that materialises the addon at publish
	// time. App developers declare AppSpec.Addons[].Type only; ops
	// pin the implementation here. Per-env overrides on
	// OrgEnvironment.AddonProfiles replace entries by type sparsely.
	AddonProfiles domain.AddonProfiles `yaml:"addonProfiles,omitempty"`
	// Auth holds optional SSO configuration. When OIDC is enabled, developers
	// log in via the org IdP and their group claims drive RBAC (see
	// RoleBinding.Group). The local admin credential remains as break-glass.
	Auth AuthConfig `yaml:"auth,omitempty"`
}

// AuthConfig holds org-level authentication configuration.
type AuthConfig struct {
	// OIDC is the SSO config. Nil/disabled = local admin credential only.
	OIDC *OIDCConfig `yaml:"oidc,omitempty"`
}

// OIDCConfig configures OpenID Connect SSO login. The client secret is NOT
// stored here — it lives in a Kubernetes Secret (see SecretRef) so the org
// ConfigMap stays free of credentials.
type OIDCConfig struct {
	// Enabled turns the SSO login flow on. When false the struct is ignored.
	Enabled bool `yaml:"enabled"`
	// IssuerURL is the OIDC issuer (discovery happens at issuer/.well-known).
	IssuerURL string `yaml:"issuerURL"`
	// ClientID is the OAuth2 client ID registered with the IdP.
	ClientID string `yaml:"clientID"`
	// ClientSecretRef names the Kubernetes Secret + key holding the client
	// secret. Empty key defaults to "client-secret".
	ClientSecretRef SecretKeyRef `yaml:"clientSecretRef,omitempty"`
	// RedirectURL is suparShip's callback URL registered with the IdP, e.g.
	// https://suparship.example.com/api/v1/auth/oidc/callback.
	RedirectURL string `yaml:"redirectURL"`
	// Scopes requested at login. Defaults to openid, profile, email, groups.
	Scopes []string `yaml:"scopes,omitempty"`
	// UsernameClaim is the ID-token claim used as the suparShip username.
	// Defaults to "email".
	UsernameClaim string `yaml:"usernameClaim,omitempty"`
	// GroupsClaim is the ID-token claim carrying the user's IdP groups, matched
	// against RoleBinding.Group. Defaults to "groups".
	GroupsClaim string `yaml:"groupsClaim,omitempty"`
}

// SecretKeyRef references a key within a Kubernetes Secret in the suparship
// system namespace.
type SecretKeyRef struct {
	Name string `yaml:"name"`
	Key  string `yaml:"key,omitempty"`
}

// Defaulted returns a copy of the OIDC config with empty optional fields filled
// with their conventional defaults.
func (c OIDCConfig) Defaulted() OIDCConfig {
	if len(c.Scopes) == 0 {
		c.Scopes = []string{"openid", "profile", "email", "groups"}
	}
	if c.UsernameClaim == "" {
		c.UsernameClaim = "email"
	}
	if c.GroupsClaim == "" {
		c.GroupsClaim = "groups"
	}
	if c.ClientSecretRef.Key == "" {
		c.ClientSecretRef.Key = "client-secret"
	}
	return c
}

// Team represents a named group of users.
type Team struct {
	Name        string   `yaml:"name"`
	DisplayName string   `yaml:"displayName"`
	Members     []string `yaml:"members"`
}

// RoleBinding assigns a role for a project to a subject — either a local Team
// (matched by username membership) or an IdP Group (matched by a group claim
// from an SSO login). Exactly one of Team/Group is set. Project "*" matches all
// projects.
type RoleBinding struct {
	Project string `yaml:"project"`
	// Team matches users by membership in a local Team (username-based).
	Team string `yaml:"team,omitempty"`
	// Group matches SSO users by an IdP group claim value. Lets the platform
	// team grant access by IdP group without rostering individual users.
	Group string `yaml:"group,omitempty"`
	Role  Role   `yaml:"role"`
}

// NewDefaultOrg creates a minimal org with a single admins team containing
// the given admin user, bound to org_admin on all projects. Two default
// environments (staging → prod) are seeded so that apps can be created
// immediately after install without registering environments manually.
// Operators can add, rename, or remove environments via the API.
func NewDefaultOrg(orgName, displayName, adminUsername string) *Org {
	return &Org{
		Name:        orgName,
		DisplayName: displayName,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		Environments: []OrgEnvironment{
			{
				Name:             "staging",
				DisplayName:      "Staging",
				Order:            1,
				ClusterRefs:      []string{"in-cluster"},
				ActiveClusterRef: "in-cluster",
			},
			{
				Name:             "prod",
				DisplayName:      "Production",
				Order:            2,
				ClusterRefs:      []string{"in-cluster"},
				ActiveClusterRef: "in-cluster",
			},
		},
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
		if e.ActiveClusterRef != "" {
			found := false
			for _, c := range e.ClusterRefs {
				if c == e.ActiveClusterRef {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf(
					"environments[%s]: activeClusterRef %q must be present in clusterRefs %v",
					e.Name, e.ActiveClusterRef, e.ClusterRefs,
				)
			}
		}
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

	if err := validateRoutingProfiles("routingProfiles", o.RoutingProfiles); err != nil {
		return err
	}
	for _, e := range o.Environments {
		if err := validateRoutingProfiles(
			fmt.Sprintf("environments[%s].routingProfiles", e.Name),
			e.RoutingProfiles,
		); err != nil {
			return err
		}
	}

	return nil
}

// validateRoutingProfiles enforces:
//   - Profile names are recognised ExposeMode values (internal/external).
//     We deliberately reject "disabled" as a profile name — disabled is the
//     "no ingress" sentinel and never resolves to a profile lookup.
//   - Each profile carries a non-empty IngressClassName, the only required
//     field. ClusterIssuer and BaseDomain are optional.
//
// Called both for the org-level map and for every env-level override map.
// path is included in error messages so operators can locate the bad entry
// (e.g. "environments[prod].routingProfiles[external]: ...").
func validateRoutingProfiles(path string, profiles domain.RoutingProfiles) error {
	for name, p := range profiles {
		mode := domain.ExposeMode(name)
		if !mode.Valid() || mode == domain.ExposeDisabled {
			return fmt.Errorf("%s[%q]: must be one of %q or %q",
				path, name, domain.ExposeInternal, domain.ExposeExternal)
		}
		if p.IngressClassName == "" {
			return fmt.Errorf("%s[%q]: ingressClassName must not be empty", path, name)
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
