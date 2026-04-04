// Package app provides business logic for creating and managing apps.
//
// The primary entry point for the creation flow is Create, which reads the
// template, initialises components, validates inputs, builds the AppSpec, and
// generates Helm values — all as a pure function with no I/O.
//
// Build and DefaultComponentsFromTemplate are retained for callers that need
// finer-grained control (e.g. legacy HTTP handler paths).
package app

import (
	"fmt"
	"sort"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/helmvalues"
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/tpl"
)

// DefaultComponentsFromTemplate derives a minimal component list from a
// template's category field. It is used as the legacy fallback when the
// template does not declare an explicit components section.
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

// ComponentsFromTemplate initialises a deterministic []ComponentSpec from a
// template. When the template declares a spec.components section those entries
// are used; otherwise the function falls back to DefaultComponentsFromTemplate.
//
// Initialisation rules for spec.components entries:
//   - required: true  → Enabled=true, ignores any toggle from the caller
//   - required: false → Enabled follows IsDefaultEnabled(); the caller's
//     toggles map (component name → desired enabled state) can override it
//
// The returned slice is sorted by component name for deterministic output.
func ComponentsFromTemplate(tmpl *tpl.Template, toggles map[string]bool) []domain.ComponentSpec {
	if len(tmpl.Spec.Components) == 0 {
		// Legacy path: no explicit component declarations — derive from category.
		return DefaultComponentsFromTemplate(tmpl)
	}

	sorted := make([]tpl.TemplateComponent, len(tmpl.Spec.Components))
	copy(sorted, tmpl.Spec.Components)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	specs := make([]domain.ComponentSpec, 0, len(sorted))
	for _, tc := range sorted {
		specs = append(specs, domain.ComponentSpec{
			Name:           tc.Name,
			Type:           templateComponentTypeToDomain(tc.Type),
			Enabled:        resolveEnabled(tc, toggles),
			Expose:         tc.Exposed,
			PreviewEnabled: tc.PreviewEnabled,
		})
	}
	return specs
}

// resolveEnabled returns the enabled state for a single TemplateComponent.
// Required components are always enabled. For optional components the caller's
// toggle wins over IsDefaultEnabled when present.
func resolveEnabled(tc tpl.TemplateComponent, toggles map[string]bool) bool {
	if tc.Required {
		return true
	}
	if toggle, ok := toggles[tc.Name]; ok {
		return toggle
	}
	return tc.IsDefaultEnabled()
}

// templateComponentTypeToDomain converts a tpl.TemplateComponentType to the
// equivalent domain.ComponentType. Unknown values fall back to ComponentWeb.
func templateComponentTypeToDomain(t tpl.TemplateComponentType) domain.ComponentType {
	switch t {
	case tpl.TemplateComponentWorker:
		return domain.ComponentWorker
	case tpl.TemplateComponentCron:
		return domain.ComponentCron
	default:
		return domain.ComponentWeb
	}
}

// CreateRequest carries all inputs required to create a new app through the
// full creation pipeline (validation → component init → AppSpec → HelmValues).
type CreateRequest struct {
	ProjectName string
	AppName     string
	DisplayName string
	Description string
	// Template is the resolved template for this app. Must not be nil.
	Template *tpl.Template
	// Values holds the curated (non-secret) template input values.
	Values map[string]any
	// SecretRefs maps secret input names to Kubernetes Secret key references.
	SecretRefs []domain.AppSecretRef
	// ComponentToggles overrides the enabled state of optional (non-required)
	// components declared in Template.Spec.Components. Keys are component names.
	// Required components always remain enabled and ignore this map.
	// When Template.Spec.Components is empty this field has no effect (the
	// category-based fallback is used instead).
	ComponentToggles map[string]bool
	// ExplicitComponents, when non-empty, bypasses ComponentsFromTemplate
	// entirely and uses these specs directly. Intended for legacy callers that
	// supply fully-specified component lists.
	ExplicitComponents []domain.ComponentSpec
}

// CreateResult holds the pure-function outputs of Create.
type CreateResult struct {
	App          *domain.App
	Environments []*domain.AppEnvironment
	// HelmValues holds generated Helm values for each default environment,
	// keyed by environment name ("staging", "prod"). These are ready to be
	// serialised and committed to the GitOps repository.
	HelmValues map[string]helmvalues.HelmValues
}

// Create is the end-to-end app creation pipeline. It is a pure function that
// performs no I/O; the caller is responsible for persistence.
//
// Steps:
//  1. Validate template inputs (values + secret refs).
//  2. Initialise component specs from the template (or use ExplicitComponents).
//  3. Build the App and default staging+prod AppEnvironments.
//  4. Generate deterministic Helm values for each default environment.
//
// Returns an error if input validation fails or the request is structurally
// invalid (e.g. nil template). All other errors are programming mistakes.
func Create(req CreateRequest) (*CreateResult, error) {
	if req.Template == nil {
		return nil, fmt.Errorf("template must not be nil")
	}
	if err := domain.ValidateAppName(req.AppName); err != nil {
		return nil, err
	}

	// Convert domain secret refs to the project type required by the validator.
	secretRefsForValidation := make([]project.SecretRef, len(req.SecretRefs))
	for i, sr := range req.SecretRefs {
		secretRefsForValidation[i] = project.SecretRef{Name: sr.Name, SecretRef: sr.SecretRef}
	}
	if err := project.ValidateAppInputs(req.Values, secretRefsForValidation, req.Template); err != nil {
		return nil, fmt.Errorf("invalid template inputs: %w", err)
	}

	comps := req.ExplicitComponents
	if len(comps) == 0 {
		comps = ComponentsFromTemplate(req.Template, req.ComponentToggles)
	}

	app, envs := Build(
		req.ProjectName,
		req.AppName,
		req.DisplayName,
		req.Description,
		req.Template,
		req.Values,
		req.SecretRefs,
		comps,
	)

	// Generate Helm values for each default environment.
	hvMap := make(map[string]helmvalues.HelmValues, len(envs))
	for _, env := range envs {
		hvMap[env.EnvName] = helmvalues.MapToHelmValues(app, env.EnvName, env.EnvType)
	}

	return &CreateResult{
		App:          app,
		Environments: envs,
		HelmValues:   hvMap,
	}, nil
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
// When components is empty, ComponentsFromTemplate is called to derive the
// component list from the template's spec.components section (falling back to
// the category-based default for templates without an explicit components list).
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
		comps = ComponentsFromTemplate(tmpl, nil)
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
