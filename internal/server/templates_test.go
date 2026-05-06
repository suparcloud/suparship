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
				Engine:   tpl.Engine{Type: tpl.EngineHelm, Chart: tpl.ChartLocator{Path: "./chart"}},
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

	th := newTemplateHandler(ah, testTemplates(), nil, nil)
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

func TestGetTemplateDetail_ComponentsAndCapabilities(t *testing.T) {
	// Custom template with explicit component declarations + capability
	// overrides exercises both the components surface and the
	// resolved-capability serialization through the API.
	off := false
	tmpls := []*tpl.Template{{
		APIVersion: tpl.CurrentAPIVersion,
		Kind:       tpl.TemplateKind,
		Metadata:   tpl.Metadata{Name: "multi", Version: "1.0.0"},
		Spec: tpl.TemplateSpec{
			Title:    "Multi-component",
			Category: "web",
			Engine:   tpl.Engine{Type: tpl.EngineHelm},
			Components: []tpl.TemplateComponent{
				{
					Name: "web", Type: tpl.TemplateComponentWeb, Required: true,
					Exposed: true, Produces: []string{"Deployment", "Service"},
				},
				{
					Name: "stateful", Type: tpl.TemplateComponentWorker,
					Capabilities: tpl.ComponentCapabilities{
						Autoscaling: "none",
						PDB:         &off,
						Resources:   &off,
					},
				},
			},
		},
	}}
	mux := http.NewServeMux()
	ah := &authHandler{authenticator: &fakeAuthenticator{username: "admin", password: "pass"}, sessions: session.NewStore(time.Hour)}
	ah.registerRoutes(mux)
	th := newTemplateHandler(ah, tmpls, nil, nil)
	th.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/templates/multi", nil)
	req.AddCookie(sessionCookieFor(ah, "admin", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var got TemplateDetailDTO
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Components) != 2 {
		t.Fatalf("components = %d, want 2", len(got.Components))
	}

	// web component: defaults filled in, Produces echoed.
	web := got.Components[0]
	if web.Name != "web" || web.Type != "web" {
		t.Errorf("web meta wrong: %+v", web)
	}
	if !web.Capabilities.Expose || web.Capabilities.Routing != "ingress" || web.Capabilities.Autoscaling != "keda" {
		t.Errorf("web defaults not filled in: %+v", web.Capabilities)
	}
	if len(web.Produces) != 2 {
		t.Errorf("web produces = %v, want [Deployment Service]", web.Produces)
	}

	// stateful component: explicit overrides win.
	st := got.Components[1]
	if st.Capabilities.Autoscaling != "none" {
		t.Errorf("stateful autoscaling = %q, want none", st.Capabilities.Autoscaling)
	}
	if st.Capabilities.PDB {
		t.Error("stateful pdb = true, want explicit false")
	}
	if st.Capabilities.Resources {
		t.Error("stateful resources = true, want explicit false")
	}
}

func TestGetTemplateDetail_EmptyComponentsArrayNotNull(t *testing.T) {
	// Templates without a components section serialize as [] (not null)
	// so the UI's array iteration doesn't blow up.
	mux, ah := newTestTemplateMux()
	req := httptest.NewRequest("GET", "/api/v1/templates/worker", nil)
	req.AddCookie(sessionCookieFor(ah, "admin", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var raw map[string]json.RawMessage
	_ = json.NewDecoder(rec.Body).Decode(&raw)
	if string(raw["components"]) == "null" {
		t.Fatal("components should be [] not null when no components declared")
	}
}

// --- semverGreater (PR5.2 helper) ---

func TestSemverGreater(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"2.0.0", "1.9.9", true},
		{"1.10.0", "1.9.0", true},
		{"1.9.0", "1.10.0", false},
		{"1.0.0", "1.0.0", false},
		{"1.0.0-rc.1", "1.0.0-rc.0", false}, // pre-release stripped → equal → false
		{"v1.0.0", "1.0.0", true},          // non-numeric "v" prefix falls back to lex
		{"a.b.c", "1.0.0", true},           // unparseable falls back to lex
	}
	for _, tc := range cases {
		t.Run(tc.a+">"+tc.b, func(t *testing.T) {
			if got := semverGreater(tc.a, tc.b); got != tc.want {
				t.Errorf("semverGreater(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestParseSemVer(t *testing.T) {
	cases := []struct {
		in     string
		want   [3]int
		wantOK bool
	}{
		{"1.2.3", [3]int{1, 2, 3}, true},
		{"0.0.0", [3]int{0, 0, 0}, true},
		{"10.20.30", [3]int{10, 20, 30}, true},
		{"1.2.3-rc.1", [3]int{1, 2, 3}, true},
		{"1.2.3+build.5", [3]int{1, 2, 3}, true},
		{"1.2", [3]int{}, false},
		{"v1.2.3", [3]int{}, false},
		{"abc", [3]int{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := parseSemVer(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
