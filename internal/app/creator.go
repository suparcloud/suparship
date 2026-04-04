// Package app provides business logic for creating and managing apps.
//
// This package is intentionally decoupled from HTTP handlers and storage:
// it only transforms validated input into domain types. Validation of template
// inputs is handled by the project package (project.ValidateServiceInputs);
// persistence is the caller's responsibility via domain.AppStore.
package app

import (
	"fmt"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/tpl"
)

// DefaultComponentsFromTemplate derives a minimal component list from a
// template. The template's category field is mapped to the primary
// ComponentType and a sensible default component name.
//
// Mapping:
//   - "worker" → {Name: "worker", Type: ComponentWorker, Enabled: true, PreviewEnabled: false}
//   - "cron"   → {Name: "cron",   Type: ComponentCron,   Enabled: true, PreviewEnabled: false}
//   - any other → {Name: "web",  Type: ComponentWeb,    Enabled: true, Expose: true, PreviewEnabled: true}
func DefaultComponentsFromTemplate(tmpl *tpl.Template) []domain.ComponentSpec {
	switch tmpl.Spec.Category {
	case "worker":
		return []domain.ComponentSpec{
			{Name: "worker", Type: domain.ComponentWorker, Enabled: true, PreviewEnabled: false},
		}
	case "cron":
		return []domain.ComponentSpec{
			{Name: "cron", Type: domain.ComponentCron, Enabled: true, PreviewEnabled: false},
		}
	default:
		return []domain.ComponentSpec{
			{Name: "web", Type: domain.ComponentWeb, Enabled: true, Expose: true, PreviewEnabled: true},
		}
	}
}

// DefaultEnvironments returns the default staging and prod AppEnvironment
// instances for a newly-created app. Both start with no release and a
// StatusNotDeployed phase.
//
// Namespace convention: {appName}-{envName} (e.g. "myapp-staging").
func DefaultEnvironments(a *domain.App) []*domain.AppEnvironment {
	return []*domain.AppEnvironment{
		{
			AppName:     a.Name,
			ProjectName: a.ProjectName,
			EnvName:     "staging",
			EnvType:     domain.AppEnvStaging,
			Namespace:   a.Name + "-staging",
			URLs:        []string{},
			Status:      domain.AppRuntimeStatus{Phase: domain.StatusNotDeployed},
		},
		{
			AppName:     a.Name,
			ProjectName: a.ProjectName,
			EnvName:     "prod",
			EnvType:     domain.AppEnvProd,
			Namespace:   a.Name + "-prod",
			URLs:        []string{},
			Status:      domain.AppRuntimeStatus{Phase: domain.StatusNotDeployed},
		},
	}
}

// PreviewEnabledComponents returns the subset of components from the given
// list that should be deployed in preview environments (PreviewEnabled == true).
func PreviewEnabledComponents(components []domain.ComponentSpec) []domain.ComponentSpec {
	out := make([]domain.ComponentSpec, 0, len(components))
	for _, c := range components {
		if c.PreviewEnabled {
			out = append(out, c)
		}
	}
	return out
}

// NewPreviewEnvironment builds a preview AppEnvironment for the given app and
// sanitized preview name. It uses domain.AppPreviewNamespace for the namespace
// convention. Returns an error when the app has no preview-enabled components,
// since deploying a preview with zero active components is meaningless.
func NewPreviewEnvironment(a *domain.App, previewName string) (*domain.AppEnvironment, error) {
	if len(PreviewEnabledComponents(a.Spec.Components)) == 0 {
		return nil, fmt.Errorf("app %q has no preview-enabled components", a.Name)
	}
	return &domain.AppEnvironment{
		AppName:     a.Name,
		ProjectName: a.ProjectName,
		EnvName:     previewName,
		EnvType:     domain.AppEnvPreview,
		Namespace:   domain.AppPreviewNamespace(a.Name, previewName),
		URLs:        []string{},
		Status:      domain.AppRuntimeStatus{Phase: domain.StatusNotDeployed},
	}, nil
}

// Build constructs a new App and its default staging+prod AppEnvironment
// instances from validated inputs. It does not persist anything.
//
// When components is empty, DefaultComponentsFromTemplate is used to derive
// a single default component from the template category.
// When values is nil it is normalised to an empty map.
func Build(
	projectName string,
	name string,
	displayName string,
	description string,
	tmpl *tpl.Template,
	values map[string]any,
	secretRefs []domain.AppSecretRef,
	components []domain.ComponentSpec,
) (*domain.App, []*domain.AppEnvironment) {
	comps := components
	if len(comps) == 0 {
		comps = DefaultComponentsFromTemplate(tmpl)
	}

	if values == nil {
		values = map[string]any{}
	}

	a := &domain.App{
		Name:        name,
		ProjectName: projectName,
		Spec: domain.AppSpec{
			DisplayName: displayName,
			Description: description,
			Template: domain.AppTemplateRef{
				Name:    tmpl.Metadata.Name,
				Version: tmpl.Metadata.Version,
			},
			Values:     values,
			SecretRefs: secretRefs,
			Components: comps,
		},
	}

	envs := DefaultEnvironments(a)
	return a, envs
}
