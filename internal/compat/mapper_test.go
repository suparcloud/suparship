package compat_test

import (
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/compat"
	"github.com/suparcloud/suparship/internal/domain"
)

// ── MapServiceToApp ───────────────────────────────────────────────────────────

func TestMapServiceToApp_FieldsCarriedOver(t *testing.T) {
	svc := &domain.Service{
		Name:         "api",
		ProjectName:  "myproject",
		TemplateName: "web-service",
		DisplayName:  "My API",
		Description:  "Backend API service",
	}

	app := compat.MapServiceToApp(svc)

	if app.Name != svc.Name {
		t.Errorf("Name = %q, want %q", app.Name, svc.Name)
	}
	if app.ProjectName != svc.ProjectName {
		t.Errorf("ProjectName = %q, want %q", app.ProjectName, svc.ProjectName)
	}
	if app.Spec.DisplayName != svc.DisplayName {
		t.Errorf("Spec.DisplayName = %q, want %q", app.Spec.DisplayName, svc.DisplayName)
	}
	if app.Spec.Description != svc.Description {
		t.Errorf("Spec.Description = %q, want %q", app.Spec.Description, svc.Description)
	}
	if app.Spec.Template.Name != svc.TemplateName {
		t.Errorf("Spec.Template.Name = %q, want %q", app.Spec.Template.Name, svc.TemplateName)
	}
}

func TestMapServiceToApp_TemplateVersionLeftEmpty(t *testing.T) {
	// domain.Service carries no version; the mapped app must not invent one.
	app := compat.MapServiceToApp(&domain.Service{
		Name:         "svc",
		ProjectName:  "proj",
		TemplateName: "web-service",
	})
	if app.Spec.Template.Version != "" {
		t.Errorf("Spec.Template.Version should be empty for legacy service mapping, got %q", app.Spec.Template.Version)
	}
}

func TestMapServiceToApp_SingleWebComponent(t *testing.T) {
	app := compat.MapServiceToApp(&domain.Service{
		Name:         "svc",
		ProjectName:  "proj",
		TemplateName: "web-service",
	})

	if len(app.Spec.Components) != 1 {
		t.Fatalf("expected 1 component, got %d", len(app.Spec.Components))
	}
	c := app.Spec.Components[0]
	if c.Name != "web" {
		t.Errorf("Component.Name = %q, want %q", c.Name, "web")
	}
	if c.Type != domain.ComponentWeb {
		t.Errorf("Component.Type = %q, want %q", c.Type, domain.ComponentWeb)
	}
	if !app.Spec.PreviewsEnabled {
		t.Error("mapped app must have PreviewsEnabled=true")
	}
}

func TestMapServiceToApp_ValuesAndSecretRefsEmpty(t *testing.T) {
	// Values and SecretRefs live in project.Service, not in domain.Service;
	// the mapped app must leave both fields empty.
	app := compat.MapServiceToApp(&domain.Service{Name: "s", ProjectName: "p"})
	if app.Spec.Values != nil {
		t.Errorf("Spec.Values should be nil for legacy service mapping, got %v", app.Spec.Values)
	}
	if len(app.Spec.SecretRefs) != 0 {
		t.Errorf("Spec.SecretRefs should be empty, got %v", app.Spec.SecretRefs)
	}
}

func TestMapServiceToApp_EmptyDisplayName(t *testing.T) {
	app := compat.MapServiceToApp(&domain.Service{
		Name:        "bare",
		ProjectName: "proj",
	})
	if app.Spec.DisplayName != "" {
		t.Errorf("DisplayName = %q, want empty string when service has none", app.Spec.DisplayName)
	}
}

// ── MapServiceStatusToAppEnvironment ─────────────────────────────────────────

func TestMapServiceStatusToAppEnvironment_StagingEnv(t *testing.T) {
	svc := &domain.Service{Name: "api", ProjectName: "proj"}
	status := &domain.ServiceStatus{
		Status:       domain.StatusHealthy,
		Environment:  "staging",
		Replicas:     2,
		Available:    2,
		Image:        "ghcr.io/org/api:v1.0.0",
		IngressURLs:  []string{"http://api.staging.localhost"},
		Namespace:    "proj-staging",
		LastDeployed: "2026-01-01T00:00:00Z",
	}

	env := compat.MapServiceStatusToAppEnvironment(svc, "staging", status)

	if env.AppName != "api" {
		t.Errorf("AppName = %q, want %q", env.AppName, "api")
	}
	if env.ProjectName != "proj" {
		t.Errorf("ProjectName = %q, want %q", env.ProjectName, "proj")
	}
	if env.EnvName != "staging" {
		t.Errorf("EnvName = %q, want %q", env.EnvName, "staging")
	}
	if env.EnvType != domain.AppEnvStaging {
		t.Errorf("EnvType = %q, want %q", env.EnvType, domain.AppEnvStaging)
	}
	if env.Namespace != "proj-staging" {
		t.Errorf("Namespace = %q, want %q", env.Namespace, "proj-staging")
	}
	if env.Release == nil {
		t.Fatal("Release must not be nil when image is set")
	}
	if env.Release.Image != status.Image {
		t.Errorf("Release.Image = %q, want %q", env.Release.Image, status.Image)
	}
	if len(env.URLs) != 1 || env.URLs[0] != status.IngressURLs[0] {
		t.Errorf("URLs = %v, want %v", env.URLs, status.IngressURLs)
	}
	if env.Status.Phase != domain.StatusHealthy {
		t.Errorf("Status.Phase = %q, want %q", env.Status.Phase, domain.StatusHealthy)
	}
	if env.Status.Replicas != 2 {
		t.Errorf("Status.Replicas = %d, want 2", env.Status.Replicas)
	}
	if env.Status.Available != 2 {
		t.Errorf("Status.Available = %d, want 2", env.Status.Available)
	}
	if env.Status.LastDeployed != "2026-01-01T00:00:00Z" {
		t.Errorf("Status.LastDeployed = %q, want %q", env.Status.LastDeployed, "2026-01-01T00:00:00Z")
	}
}

func TestMapServiceStatusToAppEnvironment_ProdEnv(t *testing.T) {
	svc := &domain.Service{Name: "api", ProjectName: "proj"}
	status := &domain.ServiceStatus{Namespace: "proj-prod"}
	env := compat.MapServiceStatusToAppEnvironment(svc, "prod", status)
	if env.EnvType != domain.AppEnvProd {
		t.Errorf("EnvType = %q, want %q", env.EnvType, domain.AppEnvProd)
	}
}

func TestMapServiceStatusToAppEnvironment_UnknownEnvDefaultsToStaging(t *testing.T) {
	// Custom or unrecognised environment names should default to AppEnvStaging
	// rather than being dropped entirely.
	svc := &domain.Service{Name: "api", ProjectName: "proj"}
	status := &domain.ServiceStatus{Namespace: "proj-dev"}
	env := compat.MapServiceStatusToAppEnvironment(svc, "dev", status)
	if env.EnvType != domain.AppEnvStaging {
		t.Errorf("EnvType = %q, want %q for unknown env name", env.EnvType, domain.AppEnvStaging)
	}
	if env.EnvName != "dev" {
		t.Errorf("EnvName = %q, want %q (original name must be preserved)", env.EnvName, "dev")
	}
}

func TestMapServiceStatusToAppEnvironment_NoImageNoRelease(t *testing.T) {
	// When image is empty, Release must be nil to indicate nothing deployed.
	svc := &domain.Service{Name: "api", ProjectName: "proj"}
	status := &domain.ServiceStatus{Status: domain.StatusNotDeployed}
	env := compat.MapServiceStatusToAppEnvironment(svc, "staging", status)
	if env.Release != nil {
		t.Errorf("Release should be nil when image is empty, got %+v", env.Release)
	}
}

func TestMapServiceStatusToAppEnvironment_NilURLsNormalised(t *testing.T) {
	// A nil IngressURLs slice must be normalised to an empty non-nil slice so
	// callers and JSON serialisation never receive null.
	svc := &domain.Service{Name: "api", ProjectName: "proj"}
	status := &domain.ServiceStatus{IngressURLs: nil}
	env := compat.MapServiceStatusToAppEnvironment(svc, "staging", status)
	if env.URLs == nil {
		t.Error("URLs must not be nil; expected empty slice")
	}
}

// ── MapPreviewToAppEnvironment ────────────────────────────────────────────────

func TestMapPreviewToAppEnvironment_FieldsCarriedOver(t *testing.T) {
	p := &domain.Preview{
		Name:        "pr-42",
		ProjectName: "proj",
		ServiceName: "api",
		Namespace:   "proj-preview-pr-42",
		Status:      domain.StatusHealthy,
		URL:         "http://pr-42.api.preview.localhost",
		CreatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	env := compat.MapPreviewToAppEnvironment(p)

	if env.AppName != "api" {
		t.Errorf("AppName = %q, want %q", env.AppName, "api")
	}
	if env.ProjectName != "proj" {
		t.Errorf("ProjectName = %q, want %q", env.ProjectName, "proj")
	}
	if env.EnvName != "pr-42" {
		t.Errorf("EnvName = %q, want %q", env.EnvName, "pr-42")
	}
	if env.EnvType != domain.AppEnvPreview {
		t.Errorf("EnvType = %q, want %q", env.EnvType, domain.AppEnvPreview)
	}
	if env.Namespace != p.Namespace {
		t.Errorf("Namespace = %q, want %q", env.Namespace, p.Namespace)
	}
	if len(env.URLs) != 1 || env.URLs[0] != p.URL {
		t.Errorf("URLs = %v, want [%q]", env.URLs, p.URL)
	}
	if env.Status.Phase != domain.StatusHealthy {
		t.Errorf("Status.Phase = %q, want %q", env.Status.Phase, domain.StatusHealthy)
	}
}

func TestMapPreviewToAppEnvironment_NoRelease(t *testing.T) {
	// Legacy Preview carries no image information; Release must always be nil.
	p := &domain.Preview{Name: "pr-1", ProjectName: "p", ServiceName: "s"}
	env := compat.MapPreviewToAppEnvironment(p)
	if env.Release != nil {
		t.Errorf("Release should be nil for preview (legacy Preview has no image info), got %+v", env.Release)
	}
}

func TestMapPreviewToAppEnvironment_EmptyURLGivesEmptySlice(t *testing.T) {
	p := &domain.Preview{
		Name:        "pr-99",
		ProjectName: "proj",
		ServiceName: "api",
		URL:         "",
	}
	env := compat.MapPreviewToAppEnvironment(p)
	if env.URLs == nil {
		t.Error("URLs must not be nil; expected empty slice")
	}
	if len(env.URLs) != 0 {
		t.Errorf("URLs = %v, want empty slice when URL is empty", env.URLs)
	}
}

func TestMapPreviewToAppEnvironment_AlwaysPreviewType(t *testing.T) {
	// EnvType must be preview regardless of the preview name or any other field.
	p := &domain.Preview{Name: "staging", ProjectName: "p", ServiceName: "s"}
	env := compat.MapPreviewToAppEnvironment(p)
	if env.EnvType != domain.AppEnvPreview {
		t.Errorf("EnvType = %q, must always be %q", env.EnvType, domain.AppEnvPreview)
	}
}

func TestMapPreviewToAppEnvironment_ServiceNameBecomesAppName(t *testing.T) {
	// During the transition ServiceName and AppName share the same identifier.
	p := &domain.Preview{Name: "pr-7", ProjectName: "proj", ServiceName: "hello"}
	env := compat.MapPreviewToAppEnvironment(p)
	if env.AppName != "hello" {
		t.Errorf("AppName = %q, want %q (ServiceName should become AppName)", env.AppName, "hello")
	}
}
