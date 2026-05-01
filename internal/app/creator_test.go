package app

import (
	"strings"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
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

// --- DefaultComponentsFromTemplate ---

func TestDefaultComponentsWebCategory(t *testing.T) {
	comps := DefaultComponentsFromTemplate(webTemplate())
	if len(comps) != 1 {
		t.Fatalf("expected 1 component, got %d", len(comps))
	}
	c := comps[0]
	if c.Name != "web" {
		t.Errorf("expected name %q, got %q", "web", c.Name)
	}
	if c.Type != domain.ComponentWeb {
		t.Errorf("expected type %q, got %q", domain.ComponentWeb, c.Type)
	}
	if !c.PreviewEnabled {
		t.Error("expected PreviewEnabled=true for web component")
	}
}

func TestDefaultComponentsWorkerCategory(t *testing.T) {
	comps := DefaultComponentsFromTemplate(workerTemplate())
	if len(comps) != 1 {
		t.Fatalf("expected 1 component, got %d", len(comps))
	}
	c := comps[0]
	if c.Name != "worker" {
		t.Errorf("expected name %q, got %q", "worker", c.Name)
	}
	if c.Type != domain.ComponentWorker {
		t.Errorf("expected type %q, got %q", domain.ComponentWorker, c.Type)
	}
	if c.PreviewEnabled {
		t.Error("expected PreviewEnabled=false for worker component")
	}
}

func TestDefaultComponentsCronCategory(t *testing.T) {
	comps := DefaultComponentsFromTemplate(cronTemplate())
	if len(comps) != 1 {
		t.Fatalf("expected 1 component, got %d", len(comps))
	}
	c := comps[0]
	if c.Name != "cron" {
		t.Errorf("expected name %q, got %q", "cron", c.Name)
	}
	if c.Type != domain.ComponentCron {
		t.Errorf("expected type %q, got %q", domain.ComponentCron, c.Type)
	}
	if c.PreviewEnabled {
		t.Error("expected PreviewEnabled=false for cron component")
	}
}

func TestDefaultComponentsUnknownCategoryFallsBackToWeb(t *testing.T) {
	tmpl := webTemplate()
	tmpl.Spec.Category = "something-new"
	comps := DefaultComponentsFromTemplate(tmpl)
	if len(comps) != 1 {
		t.Fatalf("expected 1 component, got %d", len(comps))
	}
	if comps[0].Type != domain.ComponentWeb {
		t.Errorf("expected fallback to ComponentWeb, got %q", comps[0].Type)
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

func TestBuildUsesDefaultComponentsWhenNoneProvided(t *testing.T) {
	a, _ := Build("demo", "myapp", "", "", webTemplate(), nil, nil, nil)
	if len(a.Spec.Components) != 1 {
		t.Fatalf("expected 1 component, got %d", len(a.Spec.Components))
	}
	if a.Spec.Components[0].Name != "web" {
		t.Errorf("expected default component name %q, got %q", "web", a.Spec.Components[0].Name)
	}
}

func TestBuildRespectsExplicitComponents(t *testing.T) {
	explicit := []domain.ComponentSpec{
		{Name: "web", Type: domain.ComponentWeb, Enabled: true, PreviewEnabled: true},
		{Name: "worker", Type: domain.ComponentWorker, Enabled: true, PreviewEnabled: false},
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

// --- PreviewEnabledComponents ---

func TestPreviewEnabledComponentsFiltersCorrectly(t *testing.T) {
	components := []domain.ComponentSpec{
		{Name: "web", Type: domain.ComponentWeb, PreviewEnabled: true},
		{Name: "worker", Type: domain.ComponentWorker, PreviewEnabled: false},
		{Name: "cron", Type: domain.ComponentCron, PreviewEnabled: false},
	}
	got := PreviewEnabledComponents(components)
	if len(got) != 1 {
		t.Fatalf("expected 1 preview-enabled component, got %d", len(got))
	}
	if got[0].Name != "web" {
		t.Errorf("expected component %q, got %q", "web", got[0].Name)
	}
}

func TestPreviewEnabledComponentsAllEnabled(t *testing.T) {
	components := []domain.ComponentSpec{
		{Name: "web", Type: domain.ComponentWeb, PreviewEnabled: true},
		{Name: "api", Type: domain.ComponentWeb, PreviewEnabled: true},
	}
	got := PreviewEnabledComponents(components)
	if len(got) != 2 {
		t.Fatalf("expected 2 preview-enabled components, got %d", len(got))
	}
}

func TestPreviewEnabledComponentsNoneEnabled(t *testing.T) {
	components := []domain.ComponentSpec{
		{Name: "worker", Type: domain.ComponentWorker, PreviewEnabled: false},
	}
	got := PreviewEnabledComponents(components)
	if len(got) != 0 {
		t.Fatalf("expected 0 preview-enabled components, got %d", len(got))
	}
}

func TestPreviewEnabledComponentsEmptyList(t *testing.T) {
	got := PreviewEnabledComponents(nil)
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
				{Name: "web", Type: domain.ComponentWeb, PreviewEnabled: true},
				{Name: "worker", Type: domain.ComponentWorker, PreviewEnabled: false},
			},
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

func TestNewPreviewEnvironmentNoPreviewComponents(t *testing.T) {
	a := &domain.App{
		Name:        "worker-app",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Components: []domain.ComponentSpec{
				{Name: "worker", Type: domain.ComponentWorker, PreviewEnabled: false},
			},
		},
	}

	_, err := NewPreviewEnvironment(a, "pr-42")
	if err == nil {
		t.Fatal("expected error when no preview-enabled components")
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
						{Name: "web", Type: domain.ComponentWeb, PreviewEnabled: true},
					},
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

// --- ComponentsFromTemplate ---

// templateWithComponents builds a template that has an explicit spec.components
// section (web + worker) for testing ComponentsFromTemplate.
func templateWithComponents() *tpl.Template {
	defaultEnabledFalse := false
	return &tpl.Template{
		APIVersion: tpl.CurrentAPIVersion,
		Kind:       tpl.TemplateKind,
		Metadata:   tpl.Metadata{Name: "full-stack", Version: "1.0.0"},
		Spec: tpl.TemplateSpec{
			Title:    "Full Stack",
			Category: "web",
			Engine:   tpl.Engine{Type: tpl.EngineHelm},
			Components: []tpl.TemplateComponent{
				{
					Name:           "web",
					Type:           tpl.TemplateComponentWeb,
					Required:       true,
					PreviewEnabled: true,
					Exposed:        true,
				},
				{
					Name:           "worker",
					Type:           tpl.TemplateComponentWorker,
					Required:       false,
					DefaultEnabled: &defaultEnabledFalse,
					PreviewEnabled: false,
					Exposed:        false,
				},
			},
		},
	}
}

func TestComponentsFromTemplate_FallbackWhenNoComponentsDefined(t *testing.T) {
	comps := ComponentsFromTemplate(webTemplate(), nil)
	if len(comps) != 1 {
		t.Fatalf("expected 1 component, got %d", len(comps))
	}
	if comps[0].Name != "web" || comps[0].Type != domain.ComponentWeb {
		t.Errorf("unexpected fallback component: %+v", comps[0])
	}
}

func TestComponentsFromTemplate_RequiredAlwaysEnabled(t *testing.T) {
	comps := ComponentsFromTemplate(templateWithComponents(), nil)
	var web *domain.ComponentSpec
	for i := range comps {
		if comps[i].Name == "web" {
			web = &comps[i]
		}
	}
	if web == nil {
		t.Fatal("expected web component")
	}
	if !web.Enabled {
		t.Error("required component 'web' must always be enabled")
	}
}

func TestComponentsFromTemplate_OptionalRespectsDefaultEnabled(t *testing.T) {
	comps := ComponentsFromTemplate(templateWithComponents(), nil)
	var worker *domain.ComponentSpec
	for i := range comps {
		if comps[i].Name == "worker" {
			worker = &comps[i]
		}
	}
	if worker == nil {
		t.Fatal("expected worker component")
	}
	if worker.Enabled {
		t.Error("optional component 'worker' with defaultEnabled=false should be disabled")
	}
}

func TestComponentsFromTemplate_UserToggleEnablesOptional(t *testing.T) {
	toggles := map[string]bool{"worker": true}
	comps := ComponentsFromTemplate(templateWithComponents(), toggles)
	for _, c := range comps {
		if c.Name == "worker" && !c.Enabled {
			t.Error("user toggle should enable the optional 'worker' component")
		}
	}
}

func TestComponentsFromTemplate_UserToggleCannotDisableRequired(t *testing.T) {
	toggles := map[string]bool{"web": false, "worker": false}
	comps := ComponentsFromTemplate(templateWithComponents(), toggles)
	for _, c := range comps {
		switch c.Name {
		case "web":
			if !c.Enabled {
				t.Error("required component 'web' must stay enabled regardless of toggle")
			}
		case "worker":
			if c.Enabled {
				t.Error("optional 'worker' with toggle=false should be disabled")
			}
		}
	}
}

func TestComponentsFromTemplate_FieldsFromTemplateComponent(t *testing.T) {
	comps := ComponentsFromTemplate(templateWithComponents(), nil)
	for _, c := range comps {
		if c.Name == "web" {
			if c.ExposeMode != domain.ExposeExternal {
				t.Errorf("web component ExposeMode = %q, want %q", c.ExposeMode, domain.ExposeExternal)
			}
			if !c.PreviewEnabled {
				t.Error("web component should have PreviewEnabled=true from template")
			}
			if c.Type != domain.ComponentWeb {
				t.Errorf("web component type = %q, want ComponentWeb", c.Type)
			}
		}
		if c.Name == "worker" {
			if c.ExposeMode != domain.ExposeDisabled {
				t.Errorf("worker component ExposeMode = %q, want %q", c.ExposeMode, domain.ExposeDisabled)
			}
			if c.PreviewEnabled {
				t.Error("worker component should have PreviewEnabled=false from template")
			}
			if c.Type != domain.ComponentWorker {
				t.Errorf("worker component type = %q, want ComponentWorker", c.Type)
			}
		}
	}
}

func TestComponentsFromTemplate_Deterministic(t *testing.T) {
	tmpl := templateWithComponents()
	got1 := ComponentsFromTemplate(tmpl, nil)
	got2 := ComponentsFromTemplate(tmpl, nil)
	if len(got1) != len(got2) {
		t.Fatalf("non-deterministic length: %d vs %d", len(got1), len(got2))
	}
	for i := range got1 {
		if got1[i].Name != got2[i].Name ||
			got1[i].Type != got2[i].Type ||
			got1[i].Enabled != got2[i].Enabled ||
			got1[i].ExposeMode != got2[i].ExposeMode ||
			got1[i].PreviewEnabled != got2[i].PreviewEnabled {
			t.Errorf("non-deterministic output at index %d: %+v vs %+v", i, got1[i], got2[i])
		}
	}
}

func TestComponentsFromTemplate_SortedByName(t *testing.T) {
	tmpl := templateWithComponents() // declares "web" first, "worker" second
	comps := ComponentsFromTemplate(tmpl, nil)
	if len(comps) < 2 {
		t.Fatalf("expected ≥2 components, got %d", len(comps))
	}
	// "web" < "worker" alphabetically
	if comps[0].Name > comps[1].Name {
		t.Errorf("components not sorted: %q > %q", comps[0].Name, comps[1].Name)
	}
}

// --- templateComponentTypeToDomain ---

func TestTemplateComponentTypeToDomain(t *testing.T) {
	tests := []struct {
		input tpl.TemplateComponentType
		want  domain.ComponentType
	}{
		{tpl.TemplateComponentWeb, domain.ComponentWeb},
		{tpl.TemplateComponentWorker, domain.ComponentWorker},
		{tpl.TemplateComponentCron, domain.ComponentCron},
		{"unknown", domain.ComponentWeb},
	}
	for _, tt := range tests {
		got := templateComponentTypeToDomain(tt.input)
		if got != tt.want {
			t.Errorf("templateComponentTypeToDomain(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// --- Create ---

// minimalTemplateWithComponents returns a template with a required web
// component and one required input, suitable for Create pipeline tests.
func minimalTemplateWithComponents() *tpl.Template {
	return &tpl.Template{
		APIVersion: tpl.CurrentAPIVersion,
		Kind:       tpl.TemplateKind,
		Metadata:   tpl.Metadata{Name: "web-service", Version: "1.0.0"},
		Spec: tpl.TemplateSpec{
			Title:    "Web Service",
			Category: "web",
			Engine:   tpl.Engine{Type: tpl.EngineHelm},
			Components: []tpl.TemplateComponent{
				{Name: "web", Type: tpl.TemplateComponentWeb, Required: true, PreviewEnabled: true, Exposed: true},
			},
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

func TestCreate_ComponentsInitialisedFromTemplate(t *testing.T) {
	result, err := Create(CreateRequest{
		ProjectName: "demo",
		AppName:     "my-app",
		Template:    minimalTemplateWithComponents(),
		Values:      map[string]any{"image": "ghcr.io/org/app:v1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.App.Spec.Components) != 1 {
		t.Fatalf("expected 1 component, got %d", len(result.App.Spec.Components))
	}
	c := result.App.Spec.Components[0]
	if c.Name != "web" || !c.Enabled || !c.PreviewEnabled || c.ExposeMode != domain.ExposeExternal {
		t.Errorf("web component fields unexpected: %+v", c)
	}
}

func TestCreate_GeneratesHelmValuesForAllEnvs(t *testing.T) {
	result, err := Create(CreateRequest{
		ProjectName: "demo",
		AppName:     "my-app",
		Template:    minimalTemplateWithComponents(),
		Values:      map[string]any{"image": "ghcr.io/org/app:v1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := result.HelmValues["staging"]; !ok {
		t.Error("expected HelmValues for 'staging'")
	}
	if _, ok := result.HelmValues["prod"]; !ok {
		t.Error("expected HelmValues for 'prod'")
	}
}

func TestCreate_HelmValuesAreEnvironmentSpecific(t *testing.T) {
	result, err := Create(CreateRequest{
		ProjectName: "demo",
		AppName:     "my-app",
		Template:    minimalTemplateWithComponents(),
		Values:      map[string]any{"image": "ghcr.io/org/app:v1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stagingHV := result.HelmValues["staging"]
	prodHV := result.HelmValues["prod"]

	if stagingHV.App.Env != "staging" {
		t.Errorf("staging HelmValues.App.Env = %q, want %q", stagingHV.App.Env, "staging")
	}
	if prodHV.App.Env != "prod" {
		t.Errorf("prod HelmValues.App.Env = %q, want %q", prodHV.App.Env, "prod")
	}
	if stagingHV.Routing.Host == prodHV.Routing.Host {
		t.Errorf("staging and prod hosts must differ; both = %q", stagingHV.Routing.Host)
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

func TestCreate_ValidationRejectsMissingRequiredInput(t *testing.T) {
	_, err := Create(CreateRequest{
		ProjectName: "demo",
		AppName:     "my-app",
		Template:    minimalTemplateWithComponents(),
		Values:      map[string]any{}, // "image" is required but absent
	})
	if err == nil {
		t.Fatal("expected error for missing required input")
	}
	if !strings.Contains(err.Error(), "image") {
		t.Errorf("error should mention missing input 'image', got: %v", err)
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

func TestCreate_ComponentTogglesApplied(t *testing.T) {
	defaultEnabledFalse := false
	tmpl := &tpl.Template{
		APIVersion: tpl.CurrentAPIVersion,
		Kind:       tpl.TemplateKind,
		Metadata:   tpl.Metadata{Name: "full-stack", Version: "1.0.0"},
		Spec: tpl.TemplateSpec{
			Title:    "Full Stack",
			Category: "web",
			Engine:   tpl.Engine{Type: tpl.EngineHelm},
			Components: []tpl.TemplateComponent{
				{Name: "web", Type: tpl.TemplateComponentWeb, Required: true, PreviewEnabled: true, Exposed: true},
				{Name: "worker", Type: tpl.TemplateComponentWorker, DefaultEnabled: &defaultEnabledFalse},
			},
		},
	}
	result, err := Create(CreateRequest{
		ProjectName:      "demo",
		AppName:          "my-app",
		Template:         tmpl,
		Values:           map[string]any{},
		ComponentToggles: map[string]bool{"worker": true},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, c := range result.App.Spec.Components {
		if c.Name == "worker" && !c.Enabled {
			t.Error("worker should be enabled via ComponentToggles")
		}
	}
}

func TestCreate_ExplicitComponentsBypassTemplate(t *testing.T) {
	explicit := []domain.ComponentSpec{
		{Name: "custom", Type: domain.ComponentWorker, Enabled: true, PreviewEnabled: false},
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
	hv1 := r1.HelmValues["staging"]
	hv2 := r2.HelmValues["staging"]
	if hv1.App != hv2.App || hv1.Routing != hv2.Routing {
		t.Error("non-deterministic HelmValues")
	}
}

// --- Addon claims ---

func TestCreate_AcceptsAddonClaims(t *testing.T) {
	res, err := Create(CreateRequest{
		ProjectName: "demo",
		AppName:     "hello",
		Template:    webTemplate(),
		Addons: []domain.AddonSpec{
			{Name: "cache", Type: "redis", Size: "small"},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(res.App.Spec.Addons) != 1 {
		t.Fatalf("App.Spec.Addons = %d, want 1", len(res.App.Spec.Addons))
	}
	if res.App.Spec.Addons[0].Type != "redis" {
		t.Errorf("addon type = %q, want redis", res.App.Spec.Addons[0].Type)
	}
}

func TestCreate_RejectsUnknownAddonType(t *testing.T) {
	_, err := Create(CreateRequest{
		ProjectName: "demo",
		AppName:     "hello",
		Template:    webTemplate(),
		Addons: []domain.AddonSpec{
			{Name: "queue", Type: "kafka"}, // not registered
		},
	})
	if err == nil || !strings.Contains(err.Error(), "kafka") {
		t.Fatalf("expected error mentioning unknown type, got: %v", err)
	}
}

func TestCreate_RejectsDuplicateAddonName(t *testing.T) {
	_, err := Create(CreateRequest{
		ProjectName: "demo",
		AppName:     "hello",
		Template:    webTemplate(),
		Addons: []domain.AddonSpec{
			{Name: "cache", Type: "redis"},
			{Name: "cache", Type: "redis"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate-name error, got: %v", err)
	}
}
