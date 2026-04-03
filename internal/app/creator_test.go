package app

import (
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
	if !c.EnabledInPreview {
		t.Error("expected EnabledInPreview=true for web component")
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
	if c.EnabledInPreview {
		t.Error("expected EnabledInPreview=false for worker component")
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
	if c.EnabledInPreview {
		t.Error("expected EnabledInPreview=false for cron component")
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

	if byName["staging"].Namespace != "my-api-staging" {
		t.Errorf("expected namespace %q, got %q", "my-api-staging", byName["staging"].Namespace)
	}
	if byName["prod"].Namespace != "my-api-prod" {
		t.Errorf("expected namespace %q, got %q", "my-api-prod", byName["prod"].Namespace)
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
	explicit := []domain.Component{
		{Name: "web", Type: domain.ComponentWeb, EnabledInPreview: true},
		{Name: "worker", Type: domain.ComponentWorker, EnabledInPreview: false},
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
