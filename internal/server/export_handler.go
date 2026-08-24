package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/gitops"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/registry"
	"github.com/suparcloud/suparship/internal/tpl"
)

// exportHandler serves GET /api/v1/org/export which assembles the current
// server configuration into a Helm-compatible values.yaml structure.
// Secret values are never included — only secret reference names.
type exportHandler struct {
	auth                  *authHandler
	orgProvider           rbac.OrgProvider
	clusterStore          domain.ClusterStore
	gitopsConfigStore     *gitops.ConfigStore
	registryStore         *registry.Store
	templateRegistryStore *tpl.RegistryStore
	logger                *slog.Logger
}

func (h *exportHandler) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/org/export", h.auth.requireAuth(h.handleExport))
}

// helmValues mirrors the onboarding-relevant sections of charts/suparship/values.yaml.
// Only safe-to-export fields are included — never secret values.
type helmValues struct {
	Org          helmOrg           `json:"org"`
	Environments []helmEnvironment `json:"environments,omitempty"`
	Clusters     []helmCluster     `json:"clusters,omitempty"`
	GitOps       *helmGitOps       `json:"gitops,omitempty"`
	Secrets      helmSecrets       `json:"secrets"`
	Registry     *helmRegistry     `json:"registry,omitempty"`
	Templates    *helmTemplates    `json:"templates,omitempty"`
	Auth         *helmAuth         `json:"auth,omitempty"`
	Teams        []helmTeam        `json:"teams,omitempty"`
	RoleBindings []helmRoleBinding `json:"roleBindings,omitempty"`
}

type helmOrg struct {
	Name            string `json:"name"`
	DisplayName     string `json:"displayName"`
	SecureEndpoints bool   `json:"secureEndpoints"`
}

type helmEnvironment struct {
	Name             string   `json:"name"`
	DisplayName      string   `json:"displayName,omitempty"`
	Order            int      `json:"order"`
	ClusterRefs      []string `json:"clusterRefs,omitempty"`
	ActiveClusterRef string   `json:"activeClusterRef,omitempty"`
	BaseDomain       string   `json:"baseDomain,omitempty"`
	NamespacePattern string   `json:"namespacePattern,omitempty"`
}

type helmCluster struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	APIServer   string `json:"apiServer,omitempty"`
	InCluster   bool   `json:"inCluster,omitempty"`
}

type helmGitOps struct {
	Provider       string           `json:"provider,omitempty"`
	RepoURL        string           `json:"repoURL,omitempty"`
	Branch         string           `json:"branch,omitempty"`
	SubPath        string           `json:"subPath,omitempty"`
	InitializeRepo bool             `json:"initializeRepo"`
	ExistingSecret string           `json:"existingSecret,omitempty"`
	ArgoCDRepoURL  string           `json:"argoCDRepoURL,omitempty"`
	KargoGitRepoURL string          `json:"kargoGitRepoURL,omitempty"`
	GitHub         *helmGitHub      `json:"github,omitempty"`
	Bitbucket      *helmBitbucket   `json:"bitbucket,omitempty"`
}

type helmGitHub struct {
	AppID          string `json:"appId,omitempty"`
	InstallationID string `json:"installationId,omitempty"`
}

type helmBitbucket struct {
	Workspace string `json:"workspace,omitempty"`
}

type helmSecrets struct {
	Backend    string                `json:"backend"`
	OnePassword *helmOnePassword     `json:"onePassword,omitempty"`
}

type helmOnePassword struct {
	GroupName string `json:"groupName,omitempty"`
}

type helmRegistry struct {
	Enabled             bool     `json:"enabled"`
	URL                 string   `json:"url,omitempty"`
	Username            string   `json:"username,omitempty"`
	ExistingSecret      string   `json:"existingSecret,omitempty"`
	CredentialExpiresAt string   `json:"credentialExpiresAt,omitempty"`
	Environments        []string `json:"environments,omitempty"`
}

type helmTemplates struct {
	BuiltIn  []string                   `json:"builtIn,omitempty"`
	External []helmExternalTemplateRepo `json:"external,omitempty"`
}

type helmExternalTemplateRepo struct {
	Name           string `json:"name"`
	RepoURL        string `json:"repoURL"`
	Ref            string `json:"ref"`
	Path           string `json:"path"`
	Provider       string `json:"provider,omitempty"`
	ExistingSecret string `json:"existingSecret,omitempty"`
}

// helmAuth / helmOIDC mirror the OIDC SSO config. The client secret value is
// never exported — only the name/key of the Secret that holds it.
type helmAuth struct {
	OIDC *helmOIDC `json:"oidc,omitempty"`
}

type helmOIDC struct {
	Enabled         bool             `json:"enabled"`
	IssuerURL       string           `json:"issuerURL,omitempty"`
	ClientID        string           `json:"clientID,omitempty"`
	ClientSecretRef helmSecretKeyRef `json:"clientSecretRef,omitempty"`
	RedirectURL     string           `json:"redirectURL,omitempty"`
	Scopes          []string         `json:"scopes,omitempty"`
	UsernameClaim   string           `json:"usernameClaim,omitempty"`
	GroupsClaim     string           `json:"groupsClaim,omitempty"`
}

type helmSecretKeyRef struct {
	Name string `json:"name,omitempty"`
	Key  string `json:"key,omitempty"`
}

type helmTeam struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"displayName,omitempty"`
	Members     []string `json:"members,omitempty"`
}

type helmRoleBinding struct {
	Project string `json:"project"`
	Team    string `json:"team,omitempty"`
	Group   string `json:"group,omitempty"`
	Role    string `json:"role"`
}

func (h *exportHandler) handleExport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	vals := helmValues{
		Secrets: helmSecrets{Backend: "k8s"},
	}

	h.collectOrg(ctx, &vals)
	h.collectClusters(ctx, &vals)
	h.collectGitOps(ctx, &vals)
	h.collectSecrets(ctx, &vals)
	h.collectRegistry(ctx, &vals)
	h.collectTemplates(ctx, &vals)

	format := r.URL.Query().Get("format")
	if format == "yaml" {
		w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=values.yaml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(toYAML(vals)))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(vals)
}

func (h *exportHandler) collectOrg(ctx context.Context, vals *helmValues) {
	if h.orgProvider == nil {
		return
	}
	org, err := h.orgProvider.GetOrg(ctx)
	if err != nil {
		h.logger.Warn("export: could not read org", "error", err)
		return
	}
	vals.Org = helmOrg{
		Name:            org.Name,
		DisplayName:     org.DisplayName,
		SecureEndpoints: org.EffectiveSecureEndpoints(),
	}

	for _, env := range org.Environments {
		vals.Environments = append(vals.Environments, helmEnvironment{
			Name:             env.Name,
			DisplayName:      env.DisplayName,
			Order:            env.Order,
			ClusterRefs:      env.ClusterRefs,
			ActiveClusterRef: env.ActiveClusterRef,
			BaseDomain:       env.BaseDomain,
			NamespacePattern: env.NamespacePattern,
		})
	}

	if org.SecretBackend.Type != "" {
		vals.Secrets.Backend = string(org.SecretBackend.Type)
		if org.SecretBackend.OnePassword != nil {
			vals.Secrets.OnePassword = &helmOnePassword{
				GroupName: org.SecretBackend.OnePassword.GroupName,
			}
		}
	}

	// Teams + role bindings (the RBAC config configured in the UI).
	for _, t := range org.Teams {
		vals.Teams = append(vals.Teams, helmTeam{
			Name: t.Name, DisplayName: t.DisplayName, Members: t.Members,
		})
	}
	for _, rb := range org.RoleBindings {
		vals.RoleBindings = append(vals.RoleBindings, helmRoleBinding{
			Project: rb.Project, Team: rb.Team, Group: rb.Group, Role: string(rb.Role),
		})
	}

	// OIDC SSO config — secret ref only, never the secret value.
	if org.Auth.OIDC != nil {
		c := org.Auth.OIDC.Defaulted()
		vals.Auth = &helmAuth{OIDC: &helmOIDC{
			Enabled:         c.Enabled,
			IssuerURL:       c.IssuerURL,
			ClientID:        c.ClientID,
			ClientSecretRef: helmSecretKeyRef{Name: c.ClientSecretRef.Name, Key: c.ClientSecretRef.Key},
			RedirectURL:     c.RedirectURL,
			Scopes:          c.Scopes,
			UsernameClaim:   c.UsernameClaim,
			GroupsClaim:     c.GroupsClaim,
		}}
	}
}

func (h *exportHandler) collectClusters(ctx context.Context, vals *helmValues) {
	if h.clusterStore == nil {
		return
	}
	clusters, err := h.clusterStore.ListClusters(ctx)
	if err != nil {
		h.logger.Warn("export: could not list clusters", "error", err)
		return
	}
	for _, c := range clusters {
		hc := helmCluster{
			Name:        c.Name,
			DisplayName: c.DisplayName,
			APIServer:   c.APIServer,
		}
		if c.APIServer == "https://kubernetes.default.svc" {
			hc.InCluster = true
		}
		vals.Clusters = append(vals.Clusters, hc)
	}
}

func (h *exportHandler) collectGitOps(ctx context.Context, vals *helmValues) {
	if h.gitopsConfigStore == nil {
		return
	}
	cfg, err := h.gitopsConfigStore.Get(ctx)
	if err != nil {
		if err != gitops.ErrConfigNotFound {
			h.logger.Warn("export: could not read gitops config", "error", err)
		}
		return
	}
	g := &helmGitOps{
		Provider:        cfg.Provider,
		RepoURL:         cfg.RepoURL,
		Branch:          cfg.Branch,
		SubPath:         cfg.SubPath,
		InitializeRepo:  cfg.InitializeRepo,
		ExistingSecret:  cfg.AuthSecretRef,
		ArgoCDRepoURL:   cfg.ArgoCDRepoURL,
		KargoGitRepoURL: cfg.KargoGitRepoURL,
	}
	if cfg.GitHub != nil && (cfg.GitHub.AppID != "" || cfg.GitHub.InstallationID != "") {
		g.GitHub = &helmGitHub{
			AppID:          cfg.GitHub.AppID,
			InstallationID: cfg.GitHub.InstallationID,
		}
	}
	if cfg.Bitbucket != nil && cfg.Bitbucket.Workspace != "" {
		g.Bitbucket = &helmBitbucket{
			Workspace: cfg.Bitbucket.Workspace,
		}
	}
	vals.GitOps = g
}

func (h *exportHandler) collectSecrets(_ context.Context, vals *helmValues) {
	// Already collected from org in collectOrg; this is a no-op placeholder
	// for future backends that store config outside the org ConfigMap.
}

func (h *exportHandler) collectRegistry(ctx context.Context, vals *helmValues) {
	if h.registryStore == nil {
		return
	}
	cfg, err := h.registryStore.Get(ctx)
	if err != nil {
		if err != registry.ErrConfigNotFound {
			h.logger.Warn("export: could not read registry config", "error", err)
		}
		return
	}
	vals.Registry = &helmRegistry{
		Enabled:             cfg.Enabled,
		URL:                 cfg.URL,
		Username:            cfg.Username,
		ExistingSecret:      cfg.AuthSecretRef,
		CredentialExpiresAt: cfg.CredentialExpiresAt,
		Environments:        cfg.Environments,
	}
}

func (h *exportHandler) collectTemplates(ctx context.Context, vals *helmValues) {
	if h.templateRegistryStore == nil {
		return
	}
	reg, err := h.templateRegistryStore.Get(ctx)
	if err != nil {
		return
	}
	t := &helmTemplates{
		BuiltIn: reg.BuiltIn,
	}
	for _, ext := range reg.External {
		t.External = append(t.External, helmExternalTemplateRepo{
			Name:           ext.Name,
			RepoURL:        ext.RepoURL,
			Ref:            ext.Ref,
			Path:           ext.Path,
			Provider:       ext.Provider,
			ExistingSecret: ext.ExistingSecret,
		})
	}
	vals.Templates = t
}

// toYAML produces a minimal, readable YAML representation of helmValues.
// We hand-build this instead of using a YAML library to match the Helm
// values.yaml structure exactly and keep output stable.
func toYAML(v helmValues) string {
	var b strings.Builder

	b.WriteString("# suparship Helm values — exported from live config\n")
	b.WriteString("# Secret values are NOT included. Reference existing Secrets by name.\n\n")

	b.WriteString("org:\n")
	b.WriteString(fmt.Sprintf("  name: %s\n", yamlQ(v.Org.Name)))
	b.WriteString(fmt.Sprintf("  displayName: %s\n", yamlQ(v.Org.DisplayName)))

	if len(v.Environments) > 0 {
		b.WriteString("\nenvironments:\n")
		for _, e := range v.Environments {
			b.WriteString(fmt.Sprintf("  - name: %s\n", yamlQ(e.Name)))
			if e.DisplayName != "" {
				b.WriteString(fmt.Sprintf("    displayName: %s\n", yamlQ(e.DisplayName)))
			}
			b.WriteString(fmt.Sprintf("    order: %d\n", e.Order))
			if len(e.ClusterRefs) > 0 {
				b.WriteString("    clusterRefs:\n")
				for _, c := range e.ClusterRefs {
					b.WriteString(fmt.Sprintf("      - %s\n", yamlQ(c)))
				}
			}
			if e.ActiveClusterRef != "" {
				b.WriteString(fmt.Sprintf("    activeClusterRef: %s\n", yamlQ(e.ActiveClusterRef)))
			}
			if e.BaseDomain != "" {
				b.WriteString(fmt.Sprintf("    baseDomain: %s\n", yamlQ(e.BaseDomain)))
			}
			if e.NamespacePattern != "" {
				b.WriteString(fmt.Sprintf("    namespacePattern: %s\n", yamlQ(e.NamespacePattern)))
			}
		}
	}

	if len(v.Clusters) > 0 {
		b.WriteString("\nclusters:\n")
		for _, c := range v.Clusters {
			b.WriteString(fmt.Sprintf("  - name: %s\n", yamlQ(c.Name)))
			if c.DisplayName != "" {
				b.WriteString(fmt.Sprintf("    displayName: %s\n", yamlQ(c.DisplayName)))
			}
			if c.APIServer != "" {
				b.WriteString(fmt.Sprintf("    apiServer: %s\n", yamlQ(c.APIServer)))
			}
			if c.InCluster {
				b.WriteString("    inCluster: true\n")
			}
		}
	}

	if v.GitOps != nil {
		b.WriteString("\ngitops:\n")
		if v.GitOps.Provider != "" {
			b.WriteString(fmt.Sprintf("  provider: %s\n", yamlQ(v.GitOps.Provider)))
		}
		if v.GitOps.RepoURL != "" {
			b.WriteString(fmt.Sprintf("  repoURL: %s\n", yamlQ(v.GitOps.RepoURL)))
		}
		if v.GitOps.Branch != "" {
			b.WriteString(fmt.Sprintf("  branch: %s\n", yamlQ(v.GitOps.Branch)))
		}
		if v.GitOps.SubPath != "" {
			b.WriteString(fmt.Sprintf("  subPath: %s\n", yamlQ(v.GitOps.SubPath)))
		}
		b.WriteString(fmt.Sprintf("  initializeRepo: %t\n", v.GitOps.InitializeRepo))
		if v.GitOps.ExistingSecret != "" {
			b.WriteString(fmt.Sprintf("  existingSecret: %s\n", yamlQ(v.GitOps.ExistingSecret)))
		}
		if v.GitOps.ArgoCDRepoURL != "" {
			b.WriteString(fmt.Sprintf("  argoCDRepoURL: %s\n", yamlQ(v.GitOps.ArgoCDRepoURL)))
		}
		if v.GitOps.KargoGitRepoURL != "" {
			b.WriteString(fmt.Sprintf("  kargoGitRepoURL: %s\n", yamlQ(v.GitOps.KargoGitRepoURL)))
		}
		if v.GitOps.GitHub != nil {
			b.WriteString("  github:\n")
			if v.GitOps.GitHub.AppID != "" {
				b.WriteString(fmt.Sprintf("    appId: %s\n", yamlQ(v.GitOps.GitHub.AppID)))
			}
			if v.GitOps.GitHub.InstallationID != "" {
				b.WriteString(fmt.Sprintf("    installationId: %s\n", yamlQ(v.GitOps.GitHub.InstallationID)))
			}
		}
		if v.GitOps.Bitbucket != nil {
			b.WriteString("  bitbucket:\n")
			if v.GitOps.Bitbucket.Workspace != "" {
				b.WriteString(fmt.Sprintf("    workspace: %s\n", yamlQ(v.GitOps.Bitbucket.Workspace)))
			}
		}
	}

	b.WriteString("\nsecrets:\n")
	b.WriteString(fmt.Sprintf("  backend: %s\n", yamlQ(v.Secrets.Backend)))
	if v.Secrets.OnePassword != nil {
		b.WriteString("  onePassword:\n")
		if v.Secrets.OnePassword.GroupName != "" {
			b.WriteString(fmt.Sprintf("    groupName: %s\n", yamlQ(v.Secrets.OnePassword.GroupName)))
		}
	}

	if v.Registry != nil {
		b.WriteString("\nregistry:\n")
		b.WriteString(fmt.Sprintf("  enabled: %t\n", v.Registry.Enabled))
		if v.Registry.URL != "" {
			b.WriteString(fmt.Sprintf("  url: %s\n", yamlQ(v.Registry.URL)))
		}
		if v.Registry.Username != "" {
			b.WriteString(fmt.Sprintf("  username: %s\n", yamlQ(v.Registry.Username)))
		}
		if v.Registry.ExistingSecret != "" {
			b.WriteString(fmt.Sprintf("  existingSecret: %s\n", yamlQ(v.Registry.ExistingSecret)))
		}
		if len(v.Registry.Environments) > 0 {
			b.WriteString("  environments:\n")
			for _, e := range v.Registry.Environments {
				b.WriteString(fmt.Sprintf("    - %s\n", yamlQ(e)))
			}
		}
	}

	if v.Templates != nil {
		b.WriteString("\ntemplates:\n")
		if len(v.Templates.BuiltIn) > 0 {
			b.WriteString("  builtIn:\n")
			for _, t := range v.Templates.BuiltIn {
				b.WriteString(fmt.Sprintf("    - %s\n", t))
			}
		}
		if len(v.Templates.External) > 0 {
			b.WriteString("  external:\n")
			for _, ext := range v.Templates.External {
				b.WriteString(fmt.Sprintf("    - name: %s\n", yamlQ(ext.Name)))
				b.WriteString(fmt.Sprintf("      repoURL: %s\n", yamlQ(ext.RepoURL)))
				b.WriteString(fmt.Sprintf("      ref: %s\n", yamlQ(ext.Ref)))
				b.WriteString(fmt.Sprintf("      path: %s\n", yamlQ(ext.Path)))
				if ext.Provider != "" {
					b.WriteString(fmt.Sprintf("      provider: %s\n", yamlQ(ext.Provider)))
				}
				if ext.ExistingSecret != "" {
					b.WriteString(fmt.Sprintf("      existingSecret: %s\n", yamlQ(ext.ExistingSecret)))
				}
			}
		}
	}

	if len(v.Teams) > 0 {
		b.WriteString("\nteams:\n")
		for _, t := range v.Teams {
			b.WriteString(fmt.Sprintf("  - name: %s\n", yamlQ(t.Name)))
			if t.DisplayName != "" {
				b.WriteString(fmt.Sprintf("    displayName: %s\n", yamlQ(t.DisplayName)))
			}
			if len(t.Members) > 0 {
				b.WriteString("    members:\n")
				for _, m := range t.Members {
					b.WriteString(fmt.Sprintf("      - %s\n", yamlQ(m)))
				}
			}
		}
	}

	if len(v.RoleBindings) > 0 {
		b.WriteString("\nroleBindings:\n")
		for _, rb := range v.RoleBindings {
			b.WriteString(fmt.Sprintf("  - project: %s\n", yamlQ(rb.Project)))
			if rb.Team != "" {
				b.WriteString(fmt.Sprintf("    team: %s\n", yamlQ(rb.Team)))
			}
			if rb.Group != "" {
				b.WriteString(fmt.Sprintf("    group: %s\n", yamlQ(rb.Group)))
			}
			b.WriteString(fmt.Sprintf("    role: %s\n", yamlQ(rb.Role)))
		}
	}

	if v.Auth != nil && v.Auth.OIDC != nil {
		o := v.Auth.OIDC
		b.WriteString("\nauth:\n  oidc:\n")
		b.WriteString(fmt.Sprintf("    enabled: %t\n", o.Enabled))
		if o.IssuerURL != "" {
			b.WriteString(fmt.Sprintf("    issuerURL: %s\n", yamlQ(o.IssuerURL)))
		}
		if o.ClientID != "" {
			b.WriteString(fmt.Sprintf("    clientID: %s\n", yamlQ(o.ClientID)))
		}
		if o.RedirectURL != "" {
			b.WriteString(fmt.Sprintf("    redirectURL: %s\n", yamlQ(o.RedirectURL)))
		}
		if o.ClientSecretRef.Name != "" {
			b.WriteString("    clientSecretRef:\n")
			b.WriteString(fmt.Sprintf("      name: %s\n", yamlQ(o.ClientSecretRef.Name)))
			if o.ClientSecretRef.Key != "" {
				b.WriteString(fmt.Sprintf("      key: %s\n", yamlQ(o.ClientSecretRef.Key)))
			}
		}
		if len(o.Scopes) > 0 {
			b.WriteString("    scopes:\n")
			for _, s := range o.Scopes {
				b.WriteString(fmt.Sprintf("      - %s\n", yamlQ(s)))
			}
		}
		if o.UsernameClaim != "" {
			b.WriteString(fmt.Sprintf("    usernameClaim: %s\n", yamlQ(o.UsernameClaim)))
		}
		if o.GroupsClaim != "" {
			b.WriteString(fmt.Sprintf("    groupsClaim: %s\n", yamlQ(o.GroupsClaim)))
		}
	}

	return b.String()
}

// yamlQ quotes a string for YAML output. Uses bare form for simple values,
// double-quoted form otherwise.
func yamlQ(s string) string {
	if s == "" {
		return `""`
	}
	for _, c := range s {
		if c == ':' || c == '#' || c == '"' || c == '\'' || c == '{' || c == '}' ||
			c == '[' || c == ']' || c == ',' || c == '&' || c == '*' || c == '!' ||
			c == '|' || c == '>' || c == '%' || c == '@' || c == '`' || c == '\n' {
			return fmt.Sprintf("%q", s)
		}
	}
	if s == "true" || s == "false" || s == "null" || s == "yes" || s == "no" {
		return fmt.Sprintf("%q", s)
	}
	return s
}
