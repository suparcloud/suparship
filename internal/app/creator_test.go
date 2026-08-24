package app

import (
	"strings"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/envconfig"
	"github.com/suparcloud/suparship/internal/tpl"
)

// --- helpers ---

func webTemplate() *tpl.Template {
	return &tpl.Template{
		APIVersion: tpl.CurrentAPIVersion,
		Kind:       tpl.TemplateKind,
		Metadata:   tpl.Metadata{Name: "web-service", Version: "1.0.0"},
		Spec: tpl.TemplateSpec{
			Title:    "Web Service",
			Category: "web",
			Engine:   tpl.Engine{Type: tpl.EngineHelm},
		},
	}
}

func workerTemplate() *tpl.Template {
	return &tpl.Template{
		APIVersion: tpl.CurrentAPIVersion,
		Kind:       tpl.TemplateKind,
		Metadata:   tpl.Metadata{Name: "worker", Version: "1.0.0"},
		Spec: tpl.TemplateSpec{
			Title:    "Worker",
			Category: "worker",
			Engine:   tpl.Engine{Type: tpl.EngineHelm},
		},
	}
}

func cronTemplate() *tpl.Template {
	return &tpl.Template{
		APIVersion: tpl.CurrentAPIVersion,
		Kind:       tpl.TemplateKind,
		Metadata:   tpl.Metadata{Name: "cron-job", Version: "1.0.0"},
		Spec: tpl.TemplateSpec{
			Title:    "Cron Job",
			Category: "cron",
			Engine:   tpl.Engine{Type: tpl.EngineHelm},
		},
	}
}

// --- DefaultEnvironments ---

func TestDefaultEnvironmentsProducesStaginAndProd(t *testing.T) {
	a := &domain.App{Name: "myapp", ProjectName: "demo"}
	envs := DefaultEnvironments(a)

	if len(envs) != 2 {
		t.Fatalf("expected 2 environments, got %d", len(envs))
	}

	names := map[string]bool{}
	for _, e := range envs {
		names[e.EnvName] = true
		if e.AppName != "myapp" {
			t.Errorf("env %q: expected AppName %q, got %q", e.EnvName, "myapp", e.AppName)
		}
		if e.ProjectName != "demo" {
			t.Errorf("env %q: expected ProjectName %q, got %q", e.EnvName, "demo", e.ProjectName)
		}
		if e.Status.Phase != domain.StatusNotDeployed {
			t.Errorf("env %q: expected phase %q, got %q", e.EnvName, domain.StatusNotDeployed, e.Status.Phase)
		}
	}

	if !names["staging"] {
		t.Error("expected staging environment")
	}
	if !names["prod"] {
		t.Error("expected prod environment")
	}
}

func TestDefaultEnvironmentsNamespaceConvention(t *testing.T) {
	a := &domain.App{Name: "my-api", ProjectName: "demo"}
	envs := DefaultEnvironments(a)

	byName := map[string]*domain.AppEnvironment{}
	for _, e := range envs {
		byName[e.EnvName] = e
	}

	if byName["staging"].Namespace != "demo-my-api-staging" {
		t.Errorf("expected namespace %q, got %q", "demo-my-api-staging", byName["staging"].Namespace)
	}
	if byName["prod"].Namespace != "demo-my-api-prod" {
		t.Errorf("expected namespace %q, got %q", "demo-my-api-prod", byName["prod"].Namespace)
	}
}

func TestDefaultEnvironmentsTypes(t *testing.T) {
	a := &domain.App{Name: "myapp", ProjectName: "demo"}
	envs := DefaultEnvironments(a)
	byName := map[string]*domain.AppEnvironment{}
	for _, e := range envs {
		byName[e.EnvName] = e
	}
	if byName["staging"].EnvType != domain.AppEnvStaging {
		t.Errorf("expected EnvType staging, got %q", byName["staging"].EnvType)
	}
	if byName["prod"].EnvType != domain.AppEnvProd {
		t.Errorf("expected EnvType prod, got %q", byName["prod"].EnvType)
	}
}

// --- Build ---

func TestBuildSetsAppFields(t *testing.T) {
	tmpl := webTemplate()
	values := map[string]any{"image": "ghcr.io/org/app:v1"}
	secretRefs := []domain.AppSecretRef{{Name: "db", SecretRef: "app-db.url"}}

	a, envs := Build("demo", "my-app", "My App", "A test app", tmpl, values, secretRefs, nil)

	if a.Name != "my-app" {
		t.Errorf("expected name %q, got %q", "my-app", a.Name)
	}
	if a.ProjectName != "demo" {
		t.Errorf("expected ProjectName %q, got %q", "demo", a.ProjectName)
	}
	if a.Spec.DisplayName != "My App" {
		t.Errorf("expected DisplayName %q, got %q", "My App", a.Spec.DisplayName)
	}
	if a.Spec.Description != "A test app" {
		t.Errorf("expected Description %q, got %q", "A test app", a.Spec.Description)
	}
	if a.Spec.Template.Name != "web-service" {
		t.Errorf("expected template name %q, got %q", "web-service", a.Spec.Template.Name)
	}
	if a.Spec.Template.Version != "1.0.0" {
		t.Errorf("expected template version %q, got %q", "1.0.0", a.Spec.Template.Version)
	}
	if len(a.Spec.SecretRefs) != 1 || a.Spec.SecretRefs[0].SecretRef != "app-db.url" {
		t.Errorf("unexpected secretRefs: %v", a.Spec.SecretRefs)
	}
	if len(envs) != 2 {
		t.Errorf("expected 2 default environments, got %d", len(envs))
	}
}

func TestBuildKeepsComponentsEmptyWhenNoneProvided(t *testing.T) {
	// Templates carry no component list: a plain single-chart app has zero
	// components (the chart defines its own workloads).
	a, _ := Build("demo", "myapp", "", "", webTemplate(), nil, nil, nil)
	if len(a.Spec.Components) != 0 {
		t.Fatalf("expected 0 components, got %d", len(a.Spec.Components))
	}
}

func TestBuildRespectsExplicitComponents(t *testing.T) {
	explicit := []domain.ComponentSpec{
		{Name: "web", Type: domain.ComponentWeb, Enabled: true},
		{Name: "worker", Type: domain.ComponentWorker, Enabled: true},
	}
	a, _ := Build("demo", "myapp", "", "", webTemplate(), nil, nil, explicit)
	if len(a.Spec.Components) != 2 {
		t.Fatalf("expected 2 components, got %d", len(a.Spec.Components))
	}
}

func TestBuildNilValuesNormalisedToEmptyMap(t *testing.T) {
	a, _ := Build("demo", "myapp", "", "", webTemplate(), nil, nil, nil)
	if a.Spec.Values == nil {
		t.Error("expected non-nil values map after normalisation")
	}
}

// --- EnabledComponents ---

func TestEnabledComponentsFiltersCorrectly(t *testing.T) {
	components := []domain.ComponentSpec{
		{Name: "web", Type: domain.ComponentWeb, Enabled: true},
		{Name: "worker", Type: domain.ComponentWorker, Enabled: false},
		{Name: "cron", Type: domain.ComponentCron, Enabled: false},
	}
	got := EnabledComponents(components)
	if len(got) != 1 {
		t.Fatalf("expected 1 enabled component, got %d", len(got))
	}
	if got[0].Name != "web" {
		t.Errorf("expected component %q, got %q", "web", got[0].Name)
	}
}

func TestEnabledComponentsAllEnabled(t *testing.T) {
	components := []domain.ComponentSpec{
		{Name: "web", Type: domain.ComponentWeb, Enabled: true},
		{Name: "api", Type: domain.ComponentWeb, Enabled: true},
	}
	got := EnabledComponents(components)
	if len(got) != 2 {
		t.Fatalf("expected 2 enabled components, got %d", len(got))
	}
}

func TestEnabledComponentsNoneEnabled(t *testing.T) {
	components := []domain.ComponentSpec{
		{Name: "worker", Type: domain.ComponentWorker, Enabled: false},
	}
	got := EnabledComponents(components)
	if len(got) != 0 {
		t.Fatalf("expected 0 enabled components, got %d", len(got))
	}
}

func TestEnabledComponentsEmptyList(t *testing.T) {
	got := EnabledComponents(nil)
	if len(got) != 0 {
		t.Fatalf("expected 0 components for nil input, got %d", len(got))
	}
}

// --- NewPreviewEnvironment ---

func TestNewPreviewEnvironmentSuccess(t *testing.T) {
	a := &domain.App{
		Name:        "my-app",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Components: []domain.ComponentSpec{
				{Name: "web", Type: domain.ComponentWeb, Enabled: true},
				{Name: "worker", Type: domain.ComponentWorker, Enabled: false},
			},
			PreviewsEnabled: true,
		},
	}

	env, err := NewPreviewEnvironment(a, "pr-42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if env.AppName != "my-app" {
		t.Errorf("AppName = %q, want %q", env.AppName, "my-app")
	}
	if env.ProjectName != "demo" {
		t.Errorf("ProjectName = %q, want %q", env.ProjectName, "demo")
	}
	if env.EnvName != "pr-42" {
		t.Errorf("EnvName = %q, want %q", env.EnvName, "pr-42")
	}
	if env.EnvType != domain.AppEnvPreview {
		t.Errorf("EnvType = %q, want %q", env.EnvType, domain.AppEnvPreview)
	}
	if env.Namespace != "my-app-pr-42" {
		t.Errorf("Namespace = %q, want %q", env.Namespace, "my-app-pr-42")
	}
	if env.Status.Phase != domain.StatusNotDeployed {
		t.Errorf("Status.Phase = %q, want %q", env.Status.Phase, domain.StatusNotDeployed)
	}
	if env.URLs == nil {
		t.Error("URLs should be non-nil (empty slice)")
	}
}

func TestNewPreviewEnvironmentNoEnabledComponents(t *testing.T) {
	// Previews are gated only by PreviewsEnabled (app level), not by component
	// enablement — so an app with no enabled components still previews.
	a := &domain.App{
		Name:        "worker-app",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Components: []domain.ComponentSpec{
				{Name: "worker", Type: domain.ComponentWorker, Enabled: false},
			},
			PreviewsEnabled: true,
		},
	}

	env, err := NewPreviewEnvironment(a, "pr-42")
	if err != nil {
		t.Fatalf("preview should succeed when previews are enabled: %v", err)
	}
	if env == nil || env.EnvType != domain.AppEnvPreview {
		t.Fatal("expected a preview AppEnvironment")
	}
}

func TestNewPreviewEnvironmentPreviewsDisabled(t *testing.T) {
	a := &domain.App{
		Name:        "web-app",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Components: []domain.ComponentSpec{
				{Name: "web", Type: domain.ComponentWeb, Enabled: true},
			},
			PreviewsEnabled: false,
		},
	}

	_, err := NewPreviewEnvironment(a, "pr-42")
	if err == nil {
		t.Fatal("expected error when previews are disabled")
	}
}

func TestNewPreviewEnvironmentNamespaceConvention(t *testing.T) {
	tests := []struct {
		appName     string
		previewName string
		wantNS      string
	}{
		{"hello", "pr-42", "hello-pr-42"},
		{"my-api", "feature-branch", "my-api-feature-branch"},
		{"svc", "pr-182", "svc-pr-182"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.appName+"/"+tt.previewName, func(t *testing.T) {
			a := &domain.App{
				Name:        tt.appName,
				ProjectName: "demo",
				Spec: domain.AppSpec{
					Components: []domain.ComponentSpec{
						{Name: "web", Type: domain.ComponentWeb, Enabled: true},
					},
					PreviewsEnabled: true,
				},
			}
			env, err := NewPreviewEnvironment(a, tt.previewName)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if env.Namespace != tt.wantNS {
				t.Errorf("Namespace = %q, want %q", env.Namespace, tt.wantNS)
			}
		})
	}
}

// --- DefaultEnvironments ---

func TestDefaultEnvironmentsUsesGenerateNamespace(t *testing.T) {
	a := &domain.App{Name: "my-app", ProjectName: "demo"}
	envs := DefaultEnvironments(a)
	if len(envs) != 2 {
		t.Fatalf("expected 2 environments, got %d", len(envs))
	}
	for _, env := range envs {
		want := domain.GenerateProjectNamespace(a.ProjectName, env.AppName, env.EnvName)
		if env.Namespace != want {
			t.Errorf("env %q: Namespace = %q, want %q (from GenerateProjectNamespace)", env.EnvName, env.Namespace, want)
		}
	}
}

func TestDefaultEnvironmentsNamespacePatterns(t *testing.T) {
	tests := []struct {
		appName string
		wantNSs []string
	}{
		{"hello", []string{"demo-hello-staging", "demo-hello-prod"}},
		{"api-gateway", []string{"demo-api-gateway-staging", "demo-api-gateway-prod"}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.appName, func(t *testing.T) {
			a := &domain.App{Name: tt.appName, ProjectName: "demo"}
			envs := DefaultEnvironments(a)
			got := make([]string, len(envs))
			for i, e := range envs {
				got[i] = e.Namespace
			}
			for i, want := range tt.wantNSs {
				if got[i] != want {
					t.Errorf("env[%d] Namespace = %q, want %q", i, got[i], want)
				}
			}
		})
	}
}

// --- Create ---

// minimalTemplateWithComponents returns a minimal template with one input,
// suitable for Create pipeline tests (templates declare no components).
func minimalTemplateWithComponents() *tpl.Template {
	return &tpl.Template{
		APIVersion: tpl.CurrentAPIVersion,
		Kind:       tpl.TemplateKind,
		Metadata:   tpl.Metadata{Name: "web-service", Version: "1.0.0"},
		Spec: tpl.TemplateSpec{
			Title:    "Web Service",
			Category: "web",
			Engine:   tpl.Engine{Type: tpl.EngineHelm},
			Inputs: []tpl.Input{
				{Name: "image", Title: "Image", Type: tpl.InputTypeString, Required: true},
			},
		},
	}
}

func TestCreate_ReturnsAppAndEnvironments(t *testing.T) {
	result, err := Create(CreateRequest{
		ProjectName: "demo",
		AppName:     "my-app",
		DisplayName: "My App",
		Description: "test",
		Template:    minimalTemplateWithComponents(),
		Values:      map[string]any{"image": "ghcr.io/org/app:v1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.App == nil {
		t.Fatal("App must not be nil")
	}
	if result.App.Name != "my-app" {
		t.Errorf("App.Name = %q, want %q", result.App.Name, "my-app")
	}
	if result.App.ProjectName != "demo" {
		t.Errorf("App.ProjectName = %q, want %q", result.App.ProjectName, "demo")
	}
	if len(result.Environments) != 2 {
		t.Errorf("expected 2 environments, got %d", len(result.Environments))
	}
}

// TestCreate_EnvConfigSetAtCreation verifies the create wizard's non-secret env
// vars land on the app spec: app-global on AppSpec.EnvConfig and per-env on
// EnvironmentDefaults[env].EnvConfig, so the initial publish commits them.
func TestCreate_EnvConfigSetAtCreation(t *testing.T) {
	result, err := Create(CreateRequest{
		ProjectName: "demo",
		AppName:     "my-app",
		Template:    minimalTemplateWithComponents(),
		Values:      map[string]any{"image": "ghcr.io/org/app:v1"},
		EnvConfig:   envconfig.EnvConfig{Vars: map[string]string{"LOG_LEVEL": "info"}},
		EnvConfigByEnv: map[string]envconfig.EnvConfig{
			"prod":  {Vars: map[string]string{"LOG_LEVEL": "warn"}},
			"empty": {}, // dropped
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := result.App.Spec.EnvConfig.Vars["LOG_LEVEL"]; got != "info" {
		t.Errorf("app-global LOG_LEVEL = %q, want info", got)
	}
	prod, ok := result.App.Spec.EnvironmentDefaults["prod"]
	if !ok {
		t.Fatal("expected a prod EnvironmentDefaults override")
	}
	if got := prod.EnvConfig.Vars["LOG_LEVEL"]; got != "warn" {
		t.Errorf("prod LOG_LEVEL = %q, want warn (per-env override)", got)
	}
	if _, exists := result.App.Spec.EnvironmentDefaults["empty"]; exists {
		t.Error("empty per-env config must not create an EnvironmentDefaults entry")
	}
}

// TestCreate_ComposedMultiTemplate verifies the create/ingest half of composed
// apps: explicit components each carrying their own Template (+ a per-component
// Values overlay) produce a composed AppSpec (IsComposed true), with each
// ComponentSpec keeping its template and values, and AppSpec.Template set to the
// app-level "primary" the handler passes.
func TestCreate_ComposedMultiTemplate(t *testing.T) {
	result, err := Create(CreateRequest{
		ProjectName: "demo",
		AppName:     "bigly",
		// Handler passes the first component's template as the app "primary".
		Template: webTemplate(),
		ExplicitComponents: []domain.ComponentSpec{
			{Name: "api", Type: domain.ComponentWeb, Enabled: true,
				Template: &domain.AppTemplateRef{Name: "web-service", Version: "1.0.0"},
				Values:   map[string]any{"components": map[string]any{"web": map[string]any{"image": map[string]any{"tag": "v1"}}}}},
			{Name: "worker", Type: domain.ComponentWorker, Enabled: true,
				Template: &domain.AppTemplateRef{Name: "worker", Version: "1.0.0"}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	app := result.App
	if !app.Spec.IsComposed() {
		t.Fatal("expected app to be composed (every component carries a Template)")
	}
	if len(app.Spec.Components) != 2 {
		t.Fatalf("Components = %d, want 2", len(app.Spec.Components))
	}
	for _, c := range app.Spec.Components {
		if c.Template == nil || c.Template.Name == "" {
			t.Errorf("component %q missing per-component Template", c.Name)
		}
	}
	if app.Spec.Components[0].Values == nil {
		t.Error("component api lost its per-component Values overlay")
	}
	if app.Spec.Template.Name != "web-service" {
		t.Errorf("AppSpec.Template = %q, want primary web-service", app.Spec.Template.Name)
	}
}

// TestCreate_MixedTemplatesRejected verifies the composed all-or-nothing rule is
// enforced inside Create: some-but-not-all components carrying a Template fails.
func TestCreate_MixedTemplatesRejected(t *testing.T) {
	_, err := Create(CreateRequest{
		ProjectName: "demo",
		AppName:     "bigly",
		Template:    webTemplate(),
		ExplicitComponents: []domain.ComponentSpec{
			{Name: "api", Type: domain.ComponentWeb, Enabled: true,
				Template: &domain.AppTemplateRef{Name: "web-service", Version: "1.0.0"}},
			{Name: "worker", Type: domain.ComponentWorker, Enabled: true}, // no template
		},
	})
	if err == nil {
		t.Fatal("expected mixed templated/inherited components to be rejected")
	}
	if !strings.Contains(err.Error(), "mix") {
		t.Errorf("error = %q, want it to mention the templated/inherited mix", err)
	}
}

func TestCreate_NoComponentsWithoutExplicitList(t *testing.T) {
	// Templates carry no component list — an app created without explicit
	// components has none (EffectiveComponents synthesizes the display row).
	result, err := Create(CreateRequest{
		ProjectName: "demo",
		AppName:     "my-app",
		Template:    minimalTemplateWithComponents(),
		Values:      map[string]any{"image": "ghcr.io/org/app:v1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.App.Spec.Components) != 0 {
		t.Fatalf("expected 0 components, got %d", len(result.App.Spec.Components))
	}
	if got := result.App.EffectiveComponents(); len(got) != 1 || got[0].Name != "my-app" {
		t.Errorf("EffectiveComponents = %+v, want the synthesized display row", got)
	}
}

func TestCreate_ValidationRejectsInvalidAppName(t *testing.T) {
	_, err := Create(CreateRequest{
		ProjectName: "demo",
		AppName:     "INVALID_NAME!",
		Template:    minimalTemplateWithComponents(),
		Values:      map[string]any{"image": "ghcr.io/org/app:v1"},
	})
	if err == nil {
		t.Fatal("expected error for invalid app name")
	}
}

func TestCreate_EmptyValuesSkipsInputValidation(t *testing.T) {
	// Values-editor-first: with no `values`, template inputs are not enforced —
	// a required input ("image") absent must NOT fail creation. Developers
	// configure via the values editor (rawValues) instead.
	_, err := Create(CreateRequest{
		ProjectName: "demo",
		AppName:     "my-app",
		Template:    minimalTemplateWithComponents(),
		Values:      map[string]any{}, // "image" is required but absent — allowed now
	})
	if err != nil {
		t.Fatalf("expected no error when values are omitted, got: %v", err)
	}
}

func TestCreate_StillValidatesProvidedValues(t *testing.T) {
	// When values ARE provided, validation still runs.
	_, err := Create(CreateRequest{
		ProjectName: "demo",
		AppName:     "my-app",
		Template:    minimalTemplateWithComponents(),
		Values:      map[string]any{"unknown_input": "x"},
	})
	if err == nil {
		t.Fatal("expected error for unknown provided input")
	}
}

func TestCreate_ValidationRejectsNilTemplate(t *testing.T) {
	_, err := Create(CreateRequest{
		ProjectName: "demo",
		AppName:     "my-app",
		Template:    nil,
	})
	if err == nil {
		t.Fatal("expected error for nil template")
	}
}

func TestCreate_ExplicitComponentsBypassTemplate(t *testing.T) {
	explicit := []domain.ComponentSpec{
		{Name: "custom", Type: domain.ComponentWorker, Enabled: true},
	}
	result, err := Create(CreateRequest{
		ProjectName:        "demo",
		AppName:            "my-app",
		Template:           minimalTemplateWithComponents(),
		Values:             map[string]any{"image": "ghcr.io/org/app:v1"},
		ExplicitComponents: explicit,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.App.Spec.Components) != 1 || result.App.Spec.Components[0].Name != "custom" {
		t.Errorf("expected explicit 'custom' component, got: %+v", result.App.Spec.Components)
	}
}

func TestCreate_Deterministic(t *testing.T) {
	req := CreateRequest{
		ProjectName: "demo",
		AppName:     "my-app",
		Template:    minimalTemplateWithComponents(),
		Values:      map[string]any{"image": "ghcr.io/org/app:v1"},
	}
	r1, _ := Create(req)
	r2, _ := Create(req)

	if r1.App.Name != r2.App.Name {
		t.Error("non-deterministic App.Name")
	}
	if len(r1.App.Spec.Components) != len(r2.App.Spec.Components) {
		t.Error("non-deterministic component count")
	}
}

// --- Addon claims ---
