package tpl

import (
	"errors"
	"fmt"
)

// ErrInvalidProvider is returned when ExternalTemplateRepo.Provider is
// set to a value the credentials handler doesn't know how to shape.
var ErrInvalidProvider = errors.New("invalid template repo provider")

// ErrInvalidSourceType is returned when ExternalTemplateRepo.Type is
// set to a value the fetcher dispatch doesn't recognise.
var ErrInvalidSourceType = errors.New("invalid template source type")

// ErrUnsupportedSourceType is returned when the fetcher dispatch
// recognises Type but no implementation has shipped yet (e.g.
// "chartmuseum" / "gittgz" before their fetchers land).
var ErrUnsupportedSourceType = errors.New("template source type not yet supported")

// Source-type values for ExternalTemplateRepo.Type. Empty == SourceTypeGit
// for back-compat — earlier sources have no Type field.
const (
	// SourceTypeGit clones a git templates repo. The repo can mix
	// inline-mode templates (chart/ sibling) and external-mode
	// templates (template.yaml only, engine.chart points at a registry).
	SourceTypeGit = "git"
	// SourceTypeGitCharts clones a plain Helm charts monorepo and imports
	// every standard chart found under a subpath (default "charts/") as a
	// template. Unlike SourceTypeGit it does not expect the suparship
	// templates-repo convention: charts without a bundled template.yaml are
	// imported as passthrough/BYO (the chart's own values.yaml is the base).
	SourceTypeGitCharts = "gitcharts"
	// SourceTypeOCI pulls a single chart from an OCI registry. The
	// chart's bundled template.yaml drives the template metadata when
	// present; chartimport's inferred fallback runs otherwise.
	SourceTypeOCI = "oci"
	// SourceTypeChartMuseum pulls from a classic Helm HTTP repo (not
	// yet implemented; reserved for a parallel fetcher).
	SourceTypeChartMuseum = "chartmuseum"
	// SourceTypeGitTgz reads a chart .tgz at a path inside a git repo
	// (not yet implemented; reserved for a parallel fetcher).
	SourceTypeGitTgz = "gittgz"
)

// TemplateSource describes where a template comes from and its sync state.
type TemplateSource struct {
	// Name is the template name (e.g. "web-service").
	Name string `json:"name" yaml:"name"`
	// Origin is "builtin" or "external".
	Origin string `json:"origin" yaml:"origin"`
	// Version is the current template version.
	Version string `json:"version" yaml:"version"`
	// ExternalRepo is the source repo URL for external templates.
	ExternalRepo string `json:"externalRepo,omitempty" yaml:"externalRepo,omitempty"`
	// ExternalRef is the pinned Git ref (tag/branch/commit) for external templates.
	ExternalRef string `json:"externalRef,omitempty" yaml:"externalRef,omitempty"`
	// ExternalPath is the path within the external repo.
	ExternalPath string `json:"externalPath,omitempty" yaml:"externalPath,omitempty"`
	// SyncedAt is the ISO 8601 timestamp of the last successful sync.
	SyncedAt string `json:"syncedAt,omitempty" yaml:"syncedAt,omitempty"`
}

// ExternalTemplateRepo is the Helm values structure for an external template source.
type ExternalTemplateRepo struct {
	// Name identifies this external source.
	Name string `json:"name" yaml:"name"`
	// Type selects the fetcher: git (default), oci, chartmuseum, gittgz.
	// Empty == git for back-compat with sources persisted before this
	// field was introduced.
	Type string `json:"type,omitempty" yaml:"type,omitempty"`
	// RepoURL is the source URL. For git types this is the clone URL;
	// for oci/chartmuseum types it's the registry root (e.g.
	// "oci://ghcr.io/myorg/charts" or "https://charts.acme.io").
	RepoURL string `json:"repoURL" yaml:"repoURL"`
	// Ref is the pinned git ref (tag, branch, or commit SHA). Required
	// for git-type sources; ignored for oci/chartmuseum (use Version).
	Ref string `json:"ref,omitempty" yaml:"ref,omitempty"`
	// Path within the repo where templates live. Required for git-type
	// sources; ignored for oci/chartmuseum (the registry's root and
	// Chart name describe the location).
	Path string `json:"path,omitempty" yaml:"path,omitempty"`
	// Chart names a single chart to pull. Required for oci/chartmuseum/
	// gittgz; ignored for git (which discovers all charts under Path).
	Chart string `json:"chart,omitempty" yaml:"chart,omitempty"`
	// Version pins the chart to a specific release. Required for
	// oci/chartmuseum/gittgz; ignored for git (use Ref instead).
	Version string `json:"version,omitempty" yaml:"version,omitempty"`
	// Provider identifies the Git host: github, gitlab, gitea, bitbucket,
	// generic. Drives the Secret-key shape the UI-managed credentials flow
	// writes (single "token" for github/gitlab/gitea; "username"+"password"
	// for bitbucket/generic). Empty is treated as "generic". Only
	// meaningful for git-type sources.
	Provider string `json:"provider,omitempty" yaml:"provider,omitempty"`
	// ExistingSecret is the name of a K8s Secret for auth (optional).
	ExistingSecret string `json:"existingSecret,omitempty" yaml:"existingSecret,omitempty"`
}

// EffectiveType returns Type with the empty default normalized to "git".
// Callers branching on the source type should prefer this over
// inspecting Type directly.
func (r *ExternalTemplateRepo) EffectiveType() string {
	if r.Type == "" {
		return SourceTypeGit
	}
	return r.Type
}

// Validate returns an error if the repo definition is unusable.
func (r *ExternalTemplateRepo) Validate() error {
	if r.Name == "" {
		return errors.New("external template repo: name is required")
	}
	if r.RepoURL == "" {
		return errors.New("external template repo: repoURL is required")
	}
	switch r.Provider {
	case "github", "gitlab", "gitea", "bitbucket", "generic", "":
		// ok
	default:
		return fmt.Errorf("%w: %q", ErrInvalidProvider, r.Provider)
	}
	switch r.EffectiveType() {
	case SourceTypeGit, SourceTypeGitCharts:
		// Path defaults to repo root (charts/ for gitcharts); Ref defaults
		// to "main" — both optional. Nothing else to enforce.
	case SourceTypeOCI, SourceTypeChartMuseum:
		// Registry-pull sources name a single chart at a single version.
		if r.Chart == "" {
			return fmt.Errorf("external template repo: chart is required for type %q", r.EffectiveType())
		}
		if r.Version == "" {
			return fmt.Errorf("external template repo: version is required for type %q", r.EffectiveType())
		}
	case SourceTypeGitTgz:
		// gittgz reads a .tgz at a known path inside a git repo.
		if r.Path == "" {
			return fmt.Errorf("external template repo: path is required for type %q (point at the .tgz file inside the repo)", SourceTypeGitTgz)
		}
	default:
		return fmt.Errorf("%w: %q", ErrInvalidSourceType, r.Type)
	}
	return nil
}

// TemplateRegistry holds the full set of registered template sources.
type TemplateRegistry struct {
	// BuiltIn lists the names of built-in templates that have been synced.
	BuiltIn []string `json:"builtIn" yaml:"builtIn"`
	// External lists configured external template repositories.
	External []ExternalTemplateRepo `json:"external,omitempty" yaml:"external,omitempty"`
	// Sources is the resolved list of all template sources with sync state.
	Sources []TemplateSource `json:"sources" yaml:"sources"`
}

// FindSource returns the TemplateSource for the given name, or nil.
func (r *TemplateRegistry) FindSource(name string) *TemplateSource {
	for i := range r.Sources {
		if r.Sources[i].Name == name {
			return &r.Sources[i]
		}
	}
	return nil
}

// UpsertSource adds or updates a TemplateSource by name.
func (r *TemplateRegistry) UpsertSource(src TemplateSource) {
	for i := range r.Sources {
		if r.Sources[i].Name == src.Name {
			r.Sources[i] = src
			return
		}
	}
	r.Sources = append(r.Sources, src)
}
