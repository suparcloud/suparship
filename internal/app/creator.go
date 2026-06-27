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
//   - "worker" → {Name: "worker", Type: ComponentWorker, Enabled: true}
//   - "cron"   → {Name: "cron",   Type: ComponentCron,   Enabled: true}
//   - any other → {Name: "web",  Type: ComponentWeb,    Enabled: true, ExposeMode: ExposeExternal}
func DefaultComponentsFromTemplate(tmpl *tpl.Template) []domain.ComponentSpec {
	switch tmpl.Spec.Category {
	case "worker":
		return []domain.ComponentSpec{
			{Name: "worker", Type: domain.ComponentWorker, Enabled: true},
		}
	case "cron":
		return []domain.ComponentSpec{
			{Name: "cron", Type: domain.ComponentCron, Enabled: true},
		}
	default:
		return []domain.ComponentSpec{
			{Name: "web", Type: domain.ComponentWeb, Enabled: true, ExposeMode: domain.ExposeExternal},
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
		// BYO/passthrough templates opt out of the canonical schema entirely; the
		// chart defines its own workloads, so synthesizing a phantom "web"
		// component (which maps to nothing the chart renders) would be misleading.
		if !tmpl.Spec.CanonicalValues() {
			return nil
		}
		// Legacy path: no explicit component declarations — derive from category.
		return DefaultComponentsFromTemplate(tmpl)
	}

	sorted := make([]tpl.TemplateComponent, len(tmpl.Spec.Components))
	copy(sorted, tmpl.Spec.Components)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	specs := make([]domain.ComponentSpec, 0, len(sorted))
	for _, tc := range sorted {
		spec := domain.ComponentSpec{
			Name:       tc.Name,
			Type:       templateComponentTypeToDomain(tc.Type),
			Enabled:    resolveEnabled(tc, toggles),
			ExposeMode: templateExposedToMode(tc.Exposed),
		}
		applyComponentDefaults(&spec, tc.Defaults)
		specs = append(specs, spec)
	}
	return specs
}

// applyComponentConfig writes per-component config (resources, envFrom, scaling,
// env) onto a ComponentSpec, overriding template defaults. Only set fields apply;
// Env replaces the component's Config.
func applyComponentConfig(spec *domain.ComponentSpec, cfg domain.ComponentConfig) {
	if cfg.Resources != nil {
		spec.Resources = cfg.Resources
	}
	if cfg.EnvFromSecrets != nil {
		spec.EnvFromSecrets = cfg.EnvFromSecrets
	}
	if cfg.EnvFromConfigMaps != nil {
		spec.EnvFromConfigMaps = cfg.EnvFromConfigMaps
	}
	if cfg.Scaling != nil {
		spec.Scaling = cfg.Scaling
	}
	if cfg.Env != nil {
		spec.Config = cfg.Env
	}
}

// applyComponentDefaults seeds a template's per-component Defaults block into
// the ComponentSpec (raw resources, envFrom, KEDA scaling, env). No-op when nil.
func applyComponentDefaults(spec *domain.ComponentSpec, d *tpl.ComponentDefaults) {
	if d == nil {
		return
	}
	if d.Resources != nil {
		spec.Resources = &domain.ComponentResources{
			Requests: d.Resources.Requests,
			Limits:   d.Resources.Limits,
		}
	}
	spec.EnvFromSecrets = d.EnvFromSecrets
	spec.EnvFromConfigMaps = d.EnvFromConfigMaps
	if d.Scaling != nil {
		triggers := make([]domain.KEDATrigger, len(d.Scaling.Triggers))
		for i, t := range d.Scaling.Triggers {
			triggers[i] = domain.KEDATrigger{Type: t.Type, MetricType: t.MetricType, Metadata: t.Metadata}
		}
		spec.Scaling = &domain.ComponentScaling{
			Triggers:    triggers,
			MinReplicas: d.Scaling.MinReplicas,
			MaxReplicas: d.Scaling.MaxReplicas,
		}
	}
	if len(d.Env) > 0 {
		if spec.Config == nil {
			spec.Config = map[string]string{}
		}
		for k, v := range d.Env {
			spec.Config[k] = v
		}
	}
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

// templateExposedToMode maps the template's boolean Exposed flag to the
// domain ExposeMode. Templates haven't been updated to declare
// internal-vs-external; until they are, "exposed" defaults to external
// (matching the prior Expose=true behaviour). Apps that want internal
// routing override the field explicitly via the API after creation.
func templateExposedToMode(exposed bool) domain.ExposeMode {
	if exposed {
		return domain.ExposeExternal
	}
	return domain.ExposeDisabled
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
	// Addons lists managed-dependency claims (databases, caches, queues).
	// Validated at create time via domain.ValidateAddons (DNS-label name,
	// known type, no duplicates within an app). The publisher resolves
	// each claim against the org/env AddonProfile catalog at publish time;
	// claims without a configured profile are logged + skipped, not blocked.
	Addons []domain.AddonSpec
	// NamespaceScope controls whether the app deploys into a dedicated
	// namespace ("app", default) or the shared project namespace ("project").
	NamespaceScope domain.NamespaceScope
	// NamespacePattern overrides org/project defaults for app namespace naming.
	// Only applies when NamespaceScope is "app".
	NamespacePattern string
	// RawValues is an optional freeform Helm values overlay (escape hatch),
	// deep-merged onto the generated chart values at publish. May reference
	// {platform.*}/{vars.*} tokens. No secrets.
	RawValues map[string]any
	// ComponentConfigs holds per-component config (resources, envFrom, scaling,
	// env) keyed by component name, applied over the template defaults. Optional.
	ComponentConfigs map[string]domain.ComponentConfig
	// EnvComponents holds per-(env, component) overrides keyed env → component,
	// folded into the app's EnvironmentDefaults at creation. Optional.
	EnvComponents map[string]map[string]domain.ComponentConfig
	// CD configures external-CD (Kargo) ownership of the deployed image tag.
	// Zero value disables it (the platform owns the tag). Optional.
	CD domain.CDConfig
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
	// Template inputs are retired in the values-editor-first flow: the form sends
	// no `values` (developers configure via the values editor). Only validate when
	// values are actually provided (legacy API clients), so a template declaring a
	// required, default-less input no longer blocks creation.
	if len(req.Values) > 0 {
		if err := project.ValidateAppInputs(req.Values, secretRefsForValidation, req.Template); err != nil {
			return nil, fmt.Errorf("invalid template inputs: %w", err)
		}
	}
	if err := domain.ValidateAddons(req.Addons); err != nil {
		return nil, fmt.Errorf("invalid addon claims: %w", err)
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

	// Apply namespace scope and pattern overrides from the request.
	if req.NamespaceScope != "" {
		app.Spec.NamespaceScope = req.NamespaceScope
	}
	if req.NamespacePattern != "" {
		app.Spec.NamespacePattern = req.NamespacePattern
	}
	app.Spec.Addons = req.Addons
	app.Spec.RawValues = req.RawValues
	app.Spec.CD = req.CD

	// Apply per-component config (app level) over the template defaults.
	for i := range app.Spec.Components {
		if cc, ok := req.ComponentConfigs[app.Spec.Components[i].Name]; ok {
			applyComponentConfig(&app.Spec.Components[i], cc)
		}
	}
	// Fold per-(env, component) overrides into EnvironmentDefaults.
	for envName, byComp := range req.EnvComponents {
		if len(byComp) == 0 {
			continue
		}
		ov := app.Spec.EnvironmentDefaults[envName]
		ov.Components = byComp
		if app.Spec.EnvironmentDefaults == nil {
			app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{}
		}
		app.Spec.EnvironmentDefaults[envName] = ov
	}

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
			Values:          values,
			SecretRefs:      secretRefs,
			Components:      comps,
			PreviewsEnabled: true,
		},
	}

	envs := DefaultEnvironments(a)
	return a, envs
}
