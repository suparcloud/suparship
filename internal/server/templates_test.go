package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/session"
	"github.com/suparcloud/suparship/internal/tpl"
)

func testTemplates() []*tpl.Template {
	min1, max10 := 1.0, 10.0
	return []*tpl.Template{
		{
			APIVersion: tpl.CurrentAPIVersion,
			Kind:       tpl.TemplateKind,
			Metadata:   tpl.Metadata{Name: "api-service", Version: "1.0.0"},
			Spec: tpl.TemplateSpec{
				Title:    "API Service",
				Category: "web",
				Engine:   tpl.Engine{Type: tpl.EngineHelm, Chart: "./chart"},
				Inputs: []tpl.Input{
					{Name: "image", Title: "Image", Type: tpl.InputTypeString, Required: true},
					{Name: "replicas", Title: "Replicas", Type: tpl.InputTypeNumber, Default: 2, Min: &min1, Max: &max10},
					{Name: "tier", Title: "Tier", Type: tpl.InputTypeEnum, Options: []string{"small", "large"}, Default: "small"},
				},
				AdvancedInputs: []tpl.Input{
					{Name: "debug", Title: "Debug", Type: tpl.InputTypeBoolean},
				},
				SecretInputs: []tpl.SecretInput{
					{Name: "db_url", Title: "Database URL", SecretRef: "db.url"},
				},
				Presets: []tpl.Preset{
					{Name: "starter", Title: "Starter", Values: map[string]any{"replicas": 1}},
				},
			},
		},
		{
			APIVersion: tpl.CurrentAPIVersion,
			Kind:       tpl.TemplateKind,
			Metadata:   tpl.Metadata{Name: "worker", Version: "2.0.0"},
			Spec: tpl.TemplateSpec{
				Title:    "Background Worker",
				Category: "worker",
				Engine:   tpl.Engine{Type: tpl.EngineHelm},
			},
		},
	}
}

func newTestTemplateMux() (*http.ServeMux, *authHandler) {
	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	ah.registerRoutes(mux)

	th := newTemplateHandler(ah, testTemplates())
	th.registerRoutes(mux)

	return mux, ah
}

// --- GET /api/v1/templates ---

func TestListTemplatesAuthenticated(t *testing.T) {
	mux, ah := newTestTemplateMux()

	req := httptest.NewRequest("GET", "/api/v1/templates", nil)
	req.AddCookie(sessionCookieFor(ah, "admin", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp TemplatesResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Templates) != 2 {
		t.Fatalf("expected 2 templates, got %d", len(resp.Templates))
	}
	if resp.Templates[0].Name != "api-service" {
		t.Fatalf("expected first template %q, got %q", "api-service", resp.Templates[0].Name)
	}
	if resp.Templates[0].Engine != "helm" {
		t.Fatalf("expected engine %q, got %q", "helm", resp.Templates[0].Engine)
	}
}

func TestListTemplatesUnauthenticated(t *testing.T) {
	mux, _ := newTestTemplateMux()

	req := httptest.NewRequest("GET", "/api/v1/templates", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestListTemplatesSummaryShape(t *testing.T) {
	mux, ah := newTestTemplateMux()

	req := httptest.NewRequest("GET", "/api/v1/templates", nil)
	req.AddCookie(sessionCookieFor(ah, "admin", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var resp TemplatesResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)

	for _, tmpl := range resp.Templates {
		if tmpl.Name == "" || tmpl.Title == "" || tmpl.Category == "" || tmpl.Engine == "" {
			t.Fatalf("summary missing required fields: %+v", tmpl)
		}
	}
}

// --- GET /api/v1/templates/{name} ---

func TestGetTemplateDetail(t *testing.T) {
	mux, ah := newTestTemplateMux()

	req := httptest.NewRequest("GET", "/api/v1/templates/api-service", nil)
	req.AddCookie(sessionCookieFor(ah, "admin", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp TemplateDetailDTO
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.Name != "api-service" {
		t.Fatalf("expected name %q, got %q", "api-service", resp.Name)
	}
	if resp.Version != "1.0.0" {
		t.Fatalf("expected version %q, got %q", "1.0.0", resp.Version)
	}
	if len(resp.Inputs) != 3 {
		t.Fatalf("expected 3 inputs, got %d", len(resp.Inputs))
	}
	if len(resp.AdvancedInputs) != 1 {
		t.Fatalf("expected 1 advanced input, got %d", len(resp.AdvancedInputs))
	}
	if len(resp.SecretInputs) != 1 {
		t.Fatalf("expected 1 secret input, got %d", len(resp.SecretInputs))
	}
	if len(resp.Presets) != 1 {
		t.Fatalf("expected 1 preset, got %d", len(resp.Presets))
	}
}

func TestGetTemplateDetailInputFields(t *testing.T) {
	mux, ah := newTestTemplateMux()

	req := httptest.NewRequest("GET", "/api/v1/templates/api-service", nil)
	req.AddCookie(sessionCookieFor(ah, "admin", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var resp TemplateDetailDTO
	_ = json.NewDecoder(rec.Body).Decode(&resp)

	imageInput := resp.Inputs[0]
	if imageInput.Name != "image" || imageInput.Type != "string" || !imageInput.Required {
		t.Fatalf("image input mismatch: %+v", imageInput)
	}

	replicasInput := resp.Inputs[1]
	if replicasInput.Min == nil || *replicasInput.Min != 1 {
		t.Fatalf("replicas min should be 1, got %v", replicasInput.Min)
	}
	if replicasInput.Max == nil || *replicasInput.Max != 10 {
		t.Fatalf("replicas max should be 10, got %v", replicasInput.Max)
	}

	tierInput := resp.Inputs[2]
	if tierInput.Type != "enum" || len(tierInput.Options) != 2 {
		t.Fatalf("tier input should be enum with 2 options: %+v", tierInput)
	}
}

func TestGetTemplateNotFound(t *testing.T) {
	mux, ah := newTestTemplateMux()

	req := httptest.NewRequest("GET", "/api/v1/templates/nonexistent", nil)
	req.AddCookie(sessionCookieFor(ah, "admin", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}

	var resp errorResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Error != "template not found" {
		t.Fatalf("expected 'template not found', got %q", resp.Error)
	}
}

func TestGetTemplateUnauthenticated(t *testing.T) {
	mux, _ := newTestTemplateMux()

	req := httptest.NewRequest("GET", "/api/v1/templates/api-service", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestGetTemplateEmptyArraysNotNull(t *testing.T) {
	mux, ah := newTestTemplateMux()

	req := httptest.NewRequest("GET", "/api/v1/templates/worker", nil)
	req.AddCookie(sessionCookieFor(ah, "admin", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var raw map[string]json.RawMessage
	_ = json.NewDecoder(rec.Body).Decode(&raw)

	for _, field := range []string{"inputs", "advancedInputs", "secretInputs", "presets"} {
		if string(raw[field]) == "null" {
			t.Fatalf("%s should be [] not null", field)
		}
	}
}
