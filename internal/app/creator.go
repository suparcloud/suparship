// Package app provides business logic for creating and managing apps.
//
// This package is intentionally decoupled from HTTP handlers and storage:
// it only transforms validated input into domain types. Validation of template
// inputs is handled by the project package (project.ValidateServiceInputs);
// persistence is the caller's responsibility via domain.AppStore.
package app

import (
	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/tpl"
)

// DefaultComponentsFromTemplate derives a minimal component list from a
// template. The template's category field is mapped to the primary
// ComponentType and a sensible default component name.
//
// Mapping:
//   - "worker" → {Name: "worker", Type: ComponentWorker, EnabledInPreview: false}
//   - "cron"   → {Name: "cron",   Type: ComponentCron,   EnabledInPreview: false}
//   - any other → {Name: "web",  Type: ComponentWeb,    EnabledInPreview: true}
func DefaultComponentsFromTemplate(tmpl *tpl.Template) []domain.Component {
	switch tmpl.Spec.Category {
	case "worker":
		return []domain.Component{
			{Name: "worker", Type: domain.ComponentWorker, EnabledInPreview: false},
		}
	case "cron":
		return []domain.Component{
			{Name: "cron", Type: domain.ComponentCron, EnabledInPreview: false},
		}
	default:
		return []domain.Component{
			{Name: "web", Type: domain.ComponentWeb, EnabledInPreview: true},
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
	components []domain.Component,
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
