package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/session"
	"github.com/suparcloud/suparship/internal/tpl"
)

// TestLegacyServiceRoutes_DeprecationHeader verifies that every legacy
// service-oriented route emits the "Deprecation: true" response header defined
// by RFC 8594. The body/status code of each response is not the focus here;
// the header must be present regardless of the outcome.
func TestLegacyServiceRoutes_DeprecationHeader(t *testing.T) {
	mux, cookie := newCompatTestMux(t)

	cases := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{
			name:   "POST /projects/{project}/services",
			method: http.MethodPost,
			path:   "/api/v1/projects/myapi/services",
			body:   createServiceRequest{Name: "svc", Template: "web-service", Values: map[string]any{"service_name": "svc"}},
		},
		{
			name:   "GET /environments",
			method: http.MethodGet,
			path:   "/api/v1/environments",
		},
		{
			name:   "GET /projects/{project}/services",
			method: http.MethodGet,
			path:   "/api/v1/projects/myapi/services",
		},
		{
			name:   "GET /projects/{project}/services/{service}",
			method: http.MethodGet,
			path:   "/api/v1/projects/myapi/services/hello",
		},
		{
			name:   "GET /previews",
			method: http.MethodGet,
			path:   "/api/v1/previews",
		},
		{
			name:   "POST /previews",
			method: http.MethodPost,
			path:   "/api/v1/previews",
			body:   CreatePreviewRequest{Name: "pr-1", Project: "myapi", Service: "notes-web"},
		},
		{
			name:   "GET /projects/{project}/services/{service}/previews",
			method: http.MethodGet,
			path:   "/api/v1/projects/myapi/services/hello/previews",
		},
		{
			name:   "POST /projects/{project}/services/{service}/promote",
			method: http.MethodPost,
			path:   "/api/v1/projects/myapi/services/hello/promote",
			body:   PromoteRequest{TargetEnvironment: "prod"},
		},
		{
			name:   "GET /projects/{project}/services/{service}/logs",
			method: http.MethodGet,
			path:   "/api/v1/projects/myapi/services/hello/logs?environment=staging",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var bodyReader *bytes.Reader
			if tc.body != nil {
				data, err := json.Marshal(tc.body)
				if err != nil {
					t.Fatalf("marshal body: %v", err)
				}
				bodyReader = bytes.NewReader(data)
			} else {
				bodyReader = bytes.NewReader(nil)
			}

			req := httptest.NewRequest(tc.method, tc.path, bodyReader)
			req.Header.Set("Content-Type", "application/json")
			req.AddCookie(cookie)

			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			dep := rec.Header().Get("Deprecation")
			if dep != "true" {
				t.Errorf("%s %s: expected Deprecation: true, got %q (status %d)",
					tc.method, tc.path, dep, rec.Code)
			}
		})
	}
}

// TestNonLegacyRoutes_NoDeprecationHeader verifies that non-legacy (app-
// oriented and infrastructure) routes do NOT emit a Deprecation header.
func TestNonLegacyRoutes_NoDeprecationHeader(t *testing.T) {
	mux, cookie := newCompatTestMux(t)

	cases := []struct {
		name   string
		method string
		path   string
	}{
		{"GET /healthz", http.MethodGet, "/healthz"},
		{"GET /api/v1/meta", http.MethodGet, "/api/v1/meta"},
		{"GET /api/v1/org", http.MethodGet, "/api/v1/org"},
		{"GET /api/v1/projects", http.MethodGet, "/api/v1/projects"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.AddCookie(cookie)

			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if dep := rec.Header().Get("Deprecation"); dep != "" {
				t.Errorf("%s %s: unexpected Deprecation header %q", tc.method, tc.path, dep)
			}
		})
	}
}

// TestLegacyServiceRoute_DeprecationHeaderOnError verifies that
// legacyServiceRoute sets the header even when the underlying handler writes
// an error response (i.e. the header is not conditional on success).
func TestLegacyServiceRoute_DeprecationHeaderOnError(t *testing.T) {
	mux, cookie := newCompatTestMux(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/does-not-exist/services", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
	if dep := rec.Header().Get("Deprecation"); dep != "true" {
		t.Errorf("expected Deprecation: true on error response, got %q", dep)
	}
}

// compatTestOrg returns an rbac.Org where "admin" is an org_admin on all
// projects, matching what newCompatTestMux uses.
func compatTestOrg() *rbac.Org {
	return &rbac.Org{
		Name:        "default",
		DisplayName: "Default Org",
		Teams: []rbac.Team{
			{Name: "admins", DisplayName: "Admins", Members: []string{"admin"}},
		},
		RoleBindings: []rbac.RoleBinding{
			{Project: "*", Team: "admins", Role: rbac.RoleOrgAdmin},
		},
	}
}

// newCompatTestMux builds a fully wired test mux with all legacy service
// handlers registered, plus an authenticated session cookie for "admin".
func newCompatTestMux(t *testing.T) (*http.ServeMux, *http.Cookie) {
	t.Helper()

	mux := http.NewServeMux()
	registerRoutes(mux, nil, nil)

	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	ah.registerRoutes(mux)

	// --- project store with a seeded project ---
	store := newMemProjectStore()
	proj := &project.Project{
		APIVersion: project.CurrentAPIVersion,
		Kind:       project.ProjectKind,
		Metadata:   project.ProjectMeta{Name: "myapi"},
		Spec: project.ProjectSpec{
			Environments: []project.Environment{
				{Name: "staging", Order: 1},
				{Name: "prod", Order: 2},
			},
			Services: []project.Service{
				{
					Name:     "notes-web",
					Template: project.TemplateRef{Name: "web-service"},
				},
			},
		},
	}
	if err := store.Save(context.Background(), proj); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	// --- template index ---
	tmpl := &tpl.Template{
		APIVersion: tpl.CurrentAPIVersion,
		Kind:       tpl.TemplateKind,
		Metadata:   tpl.Metadata{Name: "web-service", Version: "1.0.0"},
		Spec: tpl.TemplateSpec{
			Title:    "Web Service",
			Category: "web",
			Engine:   tpl.Engine{Type: tpl.EngineHelm},
			Inputs: []tpl.Input{
				{Name: "service_name", Title: "Service Name", Type: tpl.InputTypeString, Required: true},
			},
		},
	}

	org := &staticOrgProvider{org: compatTestOrg()}
	ps := newMemPreviewStore()
	lp := newFakeLogsProvider()

	rh := &rbacHandler{
		auth:             ah,
		orgStore:         org,
		projectStore:     store,
		serviceHandler:   newServiceHandler(store, []*tpl.Template{tmpl}, nil),
		inventoryHandler: newInventoryHandler(store, nil),
		previewHandler:   newPreviewHandler(ps, store, nil, org),
		promoteHandler:   newPromoteHandler(store),
		logsHandler:      newLogsHandler(store, lp),
	}
	rh.registerRoutes(mux)

	cookie := sessionCookieFor(ah, "admin", "org_admin")
	return mux, cookie
}
