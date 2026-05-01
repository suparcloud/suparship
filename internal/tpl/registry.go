package tpl

import (
	"errors"
	"fmt"
)

// ErrInvalidProvider is returned when ExternalTemplateRepo.Provider is
// set to a value the credentials handler doesn't know how to shape.
var ErrInvalidProvider = errors.New("invalid template repo provider")

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
	// RepoURL is the Git repository URL.
	RepoURL string `json:"repoURL" yaml:"repoURL"`
	// Ref is the pinned Git ref (tag, branch, or commit SHA).
	Ref string `json:"ref" yaml:"ref"`
	// Path within the repo where templates live.
	Path string `json:"path" yaml:"path"`
	// Provider identifies the Git host: github, gitlab, gitea, bitbucket,
	// generic. Drives the Secret-key shape the UI-managed credentials flow
	// writes (single "token" for github/gitlab/gitea; "username"+"password"
	// for bitbucket/generic). Empty is treated as "generic".
	Provider string `json:"provider,omitempty" yaml:"provider,omitempty"`
	// ExistingSecret is the name of a K8s Secret for auth (optional).
	ExistingSecret string `json:"existingSecret,omitempty" yaml:"existingSecret,omitempty"`
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
