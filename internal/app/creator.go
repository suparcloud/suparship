// Package app provides business logic for creating and managing apps.
//
// The primary entry point for the creation flow is Create, which reads the
// template, initialises components, validates inputs, builds the AppSpec, and
// generates Helm values — all as a pure function with no I/O.
//
// Build is retained for callers that need finer-grained control (e.g. legacy
// HTTP handler paths).
package app

import (
	"fmt"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/envconfig"
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/tpl"
)

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
	// ExplicitComponents is the app's user-declared component list. Empty for a
	// plain single-chart app (the chart defines its own workloads); a composed
	// app declares its components here, each with its own template ref.
	ExplicitComponents []domain.ComponentSpec
	// NamespaceScope controls whether the app deploys into a dedicated
	// namespace ("app", default) or the shared project namespace ("project").
	NamespaceScope domain.NamespaceScope
	// NamespacePattern overrides org/project defaults for app namespace naming.
	// Only applies when NamespaceScope is "app".
	NamespacePattern string
	// RawValues is an optional freeform Helm values overlay (escape hatch),
	// deep-merged onto the generated chart values at publish. May reference
	// ((platform.*))/((vars.*)) tokens. No secrets.
	RawValues map[string]any
	// EnvConfig holds app-level (all-environments) non-secret env vars + secret
	// refs, set at creation — the create wizard's "Environment variables" section.
	// Committed to Git (the app's ConfigMap). Optional.
	EnvConfig envconfig.EnvConfig
	// EnvConfigByEnv holds per-environment config overrides keyed by env name,
	// folded into EnvironmentDefaults at creation (wins over EnvConfig). Optional.
	EnvConfigByEnv map[string]envconfig.EnvConfig
	// CD configures external-CD (Kargo) ownership of the deployed image tag.
	// Zero value disables it (the platform owns the tag). Optional.
	CD domain.CDConfig
	// Images binds the template's image slots to concrete repositories for this
	// app, keyed by slot Name. Optional; empty means no CD-managed images (no
	// Warehouse) until a binding is selected.
	Images []domain.AppImageBinding
	// DeliveryMode controls how the app deploys across envs ("pipeline" default,
	// or "direct" for off-the-shelf apps with no Kargo/promotion). Empty =
	// pipeline. Resolved by the caller from the template default + request.
	DeliveryMode domain.DeliveryMode
}

// CreateResult holds the pure-function outputs of Create.
type CreateResult struct {
	App          *domain.App
	Environments []*domain.AppEnvironment
}

// Create is the end-to-end app creation pipeline. It is a pure function that
// performs no I/O; the caller is responsible for persistence.
//
// Steps:
//  1. Validate template inputs (values + secret refs).
//  2. Take the user-declared component specs (ExplicitComponents) as-is.
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
	// Template inputs are retired in the values-editor-first flow: the form sends
	// no `values` (developers configure via the values editor). Only validate when
	// values are actually provided (legacy API clients), so a template declaring a
	// required, default-less input no longer blocks creation.
	if len(req.Values) > 0 {
		if err := project.ValidateAppInputs(req.Values, secretRefsForValidation, req.Template); err != nil {
			return nil, fmt.Errorf("invalid template inputs: %w", err)
		}
	}
	// Components are user-declared only: templates carry no component list.
	// A plain single-chart app has none (the chart defines its own workloads;
	// EffectiveComponents synthesizes the display row); a composed app declares
	// its components explicitly, each with its own template ref.
	comps := req.ExplicitComponents
	// Enforce component invariants — including the composed all-or-nothing rule
	// (either no component carries its own Template, or every one does).
	if err := domain.ValidateComponents(comps); err != nil {
		return nil, fmt.Errorf("invalid components: %w", err)
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

	// Apply namespace scope and pattern overrides from the request.
	if req.NamespaceScope != "" {
		app.Spec.NamespaceScope = req.NamespaceScope
	}
	if req.NamespacePattern != "" {
		app.Spec.NamespacePattern = req.NamespacePattern
	}
	app.Spec.RawValues = req.RawValues
	app.Spec.CD = req.CD
	app.Spec.Images = req.Images
	app.Spec.DeliveryMode = req.DeliveryMode


	// App-level (all-env) non-secret env vars set at creation.
	if len(req.EnvConfig.Vars) > 0 || len(req.EnvConfig.SecretRefs) > 0 {
		app.Spec.EnvConfig = req.EnvConfig
	}
	// Per-env config overrides fold into EnvironmentDefaults[env].EnvConfig
	// (alongside any Components override set above for the same env).
	for envName, cfg := range req.EnvConfigByEnv {
		if len(cfg.Vars) == 0 && len(cfg.SecretRefs) == 0 {
			continue
		}
		if app.Spec.EnvironmentDefaults == nil {
			app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{}
		}
		ov := app.Spec.EnvironmentDefaults[envName]
		ov.EnvConfig = cfg
		app.Spec.EnvironmentDefaults[envName] = ov
	}

	return &CreateResult{
		App:          app,
		Environments: envs,
	}, nil
}

// DefaultEnvironments returns the default staging and prod AppEnvironment
// instances for a newly-created app. Both start with no release and a
// StatusNotDeployed phase. This is used as a fallback for the sync path
// when no org environments have been registered (e.g. legacy apps).
//
// Namespace convention: {projectName}-{appName}-{envName} via GenerateProjectNamespace.
func DefaultEnvironments(a *domain.App) []*domain.AppEnvironment {
	return []*domain.AppEnvironment{
		{
			AppName:     a.Name,
			ProjectName: a.ProjectName,
			EnvName:     "staging",
			EnvType:     domain.AppEnvStaging,
			Order:       1,
			Namespace:   domain.GenerateProjectNamespace(a.ProjectName, a.Name, "staging"),
			URLs:        []string{},
			Status:      domain.AppRuntimeStatus{Phase: domain.StatusNotDeployed},
		},
		{
			AppName:     a.Name,
			ProjectName: a.ProjectName,
			EnvName:     "prod",
			EnvType:     domain.AppEnvProd,
			Order:       2,
			Namespace:   domain.GenerateProjectNamespace(a.ProjectName, a.Name, "prod"),
			URLs:        []string{},
			Status:      domain.AppRuntimeStatus{Phase: domain.StatusNotDeployed},
		},
	}
}

// AppHasIngressRoute reports whether the app exposes an HTTP route — i.e. some
// component requests external or internal ingress (ExposeMode). Apps with no
// exposed component (e.g. a worker or agent) have no reachable URL, so callers
// should not synthesise one for them.
func AppHasIngressRoute(a *domain.App) bool {
	if a == nil {
		return false
	}
	for _, c := range a.Spec.Components {
		if c.ExposeMode == domain.ExposeExternal || c.ExposeMode == domain.ExposeInternal {
			return true
		}
	}
	return false
}

// EnabledComponents returns the subset of components from the given list that
// are enabled (Enabled == true). A preview deploys all of an app's enabled
// components — the same set its base env runs.
func EnabledComponents(components []domain.ComponentSpec) []domain.ComponentSpec {
	out := make([]domain.ComponentSpec, 0, len(components))
	for _, c := range components {
		if c.Enabled {
			out = append(out, c)
		}
	}
	return out
}

// NewPreviewEnvironment builds a preview AppEnvironment for the given app and
// sanitized preview name. It uses domain.AppPreviewNamespace for the namespace
// convention. Previews are gated only by the app's PreviewsEnabled opt-in (an
// app-level concept) — not by component enablement — so an app previews as a
// whole, mirroring its base env.
func NewPreviewEnvironment(a *domain.App, previewName string) (*domain.AppEnvironment, error) {
	if !a.Spec.PreviewsEnabled {
		return nil, fmt.Errorf("app %q has previews disabled", a.Name)
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
// Components are user-declared only (templates carry no component list); an
// empty components slice stays empty. When values is nil it is normalised to
// an empty map.
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
			Values:          values,
			SecretRefs:      secretRefs,
			Components:      comps,
			PreviewsEnabled: true,
		},
	}

	envs := DefaultEnvironments(a)
	return a, envs
}
