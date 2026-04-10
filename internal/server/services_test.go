package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/session"
	"github.com/suparcloud/suparship/internal/tpl"
)

// --- In-memory project store for tests ---

type memProjectStore struct {
	mu       sync.Mutex
	projects map[string]*project.Project
}

func newMemProjectStore() *memProjectStore {
	return &memProjectStore{projects: make(map[string]*project.Project)}
}

func (m *memProjectStore) List(_ context.Context) ([]*project.Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*project.Project
	for _, p := range m.projects {
		out = append(out, p)
	}
	return out, nil
}

func (m *memProjectStore) Get(_ context.Context, name string) (*project.Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.projects[name]
	if !ok {
		return nil, fmt.Errorf("project %q not found", name)
	}
	return p, nil
}

func (m *memProjectStore) Save(_ context.Context, p *project.Project) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.projects[p.Metadata.Name] = p
	return nil
}

func (m *memProjectStore) Delete(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.projects, name)
	return nil
}

// --- Test template and project ---

func svcTestTemplate() *tpl.Template {
	min1 := 1.0
	return &tpl.Template{
		APIVersion: tpl.CurrentAPIVersion,
		Kind:       tpl.TemplateKind,
		Metadata:   tpl.Metadata{Name: "web-service", Version: "1.0.0"},
		Spec: tpl.TemplateSpec{
			Title:    "Web Service",
			Category: "web",
			Engine:   tpl.Engine{Type: tpl.EngineHelm},
			Inputs: []tpl.Input{
				{Name: "service_name", Title: "Service Name", Type: tpl.InputTypeString, Required: true},
				{Name: "size", Title: "Size", Type: tpl.InputTypeEnum, Options: []string{"small", "large"}, Default: "small"},
				{Name: "port", Title: "Port", Type: tpl.InputTypeNumber, Default: 8080, Min: &min1},
			},
			SecretInputs: []tpl.SecretInput{
				{Name: "database_url", Title: "Database URL", SecretRef: "db.url"},
			},
			Mappings: map[string]string{
				"fullnameOverride": "{{ .inputs.service_name }}",
				"size":             "{{ .inputs.size }}",
			},
		},
	}
}

func svcTestProject() *project.Project {
	return &project.Project{
		APIVersion: project.CurrentAPIVersion,
		Kind:       project.ProjectKind,
		Metadata:   project.ProjectMeta{Name: "myapi"},
		Spec: project.ProjectSpec{
			Environments: []project.Environment{
				{Name: "dev", Order: 1},
			},
		},
	}
}

func newTestServiceMux() (*http.ServeMux, *authHandler, *memProjectStore) {
	mux := http.NewServeMux()

	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	ah.registerRoutes(mux)

	store := newMemProjectStore()

	rh := &rbacHandler{
		auth:        ah,
		orgStore: &staticOrgProvider{org: testRBACOrg()},
		serviceHandler: newServiceHandler(store, []*tpl.Template{svcTestTemplate()}),
	}
	rh.registerRoutes(mux)

	return mux, ah, store
}

func postServiceJSON(mux *http.ServeMux, cookie *http.Cookie, projectName string, body any) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/projects/"+projectName+"/services", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// --- Tests ---

func TestCreateServiceValid(t *testing.T) {
	mux, ah, store := newTestServiceMux()
	_ = store.Save(context.Background(), svcTestProject())

	rec := postServiceJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "myapi", createServiceRequest{
		Name:     "api",
		Template: "web-service",
		Values:   map[string]any{"service_name": "my-api", "size": "large"},
		SecretRefs: []secretRefRequest{
			{Name: "database_url", SecretRef: "api-db.url"},
		},
	})

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp createServiceResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Service.Name != "api" {
		t.Fatalf("expected service name %q, got %q", "api", resp.Service.Name)
	}
	if resp.Service.Template.Name != "web-service" {
		t.Fatalf("expected template %q, got %q", "web-service", resp.Service.Template.Name)
	}
	if resp.Service.Template.Version != "1.0.0" {
		t.Fatalf("expected version %q, got %q", "1.0.0", resp.Service.Template.Version)
	}
	if resp.HelmValues["fullnameOverride"] != "my-api" {
		t.Fatalf("expected fullnameOverride %q, got %v", "my-api", resp.HelmValues["fullnameOverride"])
	}
	if resp.HelmValues["size"] != "large" {
		t.Fatalf("expected size %q, got %v", "large", resp.HelmValues["size"])
	}

	proj, _ := store.Get(context.Background(), "myapi")
	if len(proj.Spec.Services) != 1 {
		t.Fatalf("expected 1 service persisted, got %d", len(proj.Spec.Services))
	}
}

func TestCreateServiceDuplicate(t *testing.T) {
	mux, ah, store := newTestServiceMux()
	p := svcTestProject()
	p.Spec.Services = []project.Service{
		{Name: "api", Template: project.TemplateRef{Name: "web-service"}},
	}
	_ = store.Save(context.Background(), p)

	rec := postServiceJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "myapi", createServiceRequest{
		Name:     "api",
		Template: "web-service",
		Values:   map[string]any{"service_name": "api"},
	})

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp errorResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Error == "" || !contains(resp.Error, "already exists") {
		t.Fatalf("expected 'already exists' error, got %q", resp.Error)
	}
}

func TestCreateServiceMissingName(t *testing.T) {
	mux, ah, store := newTestServiceMux()
	_ = store.Save(context.Background(), svcTestProject())

	rec := postServiceJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "myapi", createServiceRequest{
		Template: "web-service",
	})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateServiceMissingTemplate(t *testing.T) {
	mux, ah, store := newTestServiceMux()
	_ = store.Save(context.Background(), svcTestProject())

	rec := postServiceJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "myapi", createServiceRequest{
		Name: "api",
	})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateServiceUnknownTemplate(t *testing.T) {
	mux, ah, store := newTestServiceMux()
	_ = store.Save(context.Background(), svcTestProject())

	rec := postServiceJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "myapi", createServiceRequest{
		Name:     "api",
		Template: "nonexistent",
	})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateServiceValidationFailure(t *testing.T) {
	mux, ah, store := newTestServiceMux()
	_ = store.Save(context.Background(), svcTestProject())

	rec := postServiceJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "myapi", createServiceRequest{
		Name:     "api",
		Template: "web-service",
		Values:   map[string]any{"size": "xlarge"},
	})

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateServiceSecretAsPlaintext(t *testing.T) {
	mux, ah, store := newTestServiceMux()
	_ = store.Save(context.Background(), svcTestProject())

	rec := postServiceJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "myapi", createServiceRequest{
		Name:     "api",
		Template: "web-service",
		Values:   map[string]any{"service_name": "api", "database_url": "postgres://..."},
	})

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateServiceProjectNotFound(t *testing.T) {
	mux, ah, _ := newTestServiceMux()

	rec := postServiceJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "nonexistent", createServiceRequest{
		Name:     "api",
		Template: "web-service",
		Values:   map[string]any{"service_name": "api"},
	})

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateServiceUnauthenticated(t *testing.T) {
	mux, _, store := newTestServiceMux()
	_ = store.Save(context.Background(), svcTestProject())

	rec := postServiceJSON(mux, nil, "myapi", createServiceRequest{
		Name:     "api",
		Template: "web-service",
		Values:   map[string]any{"service_name": "api"},
	})

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestCreateServiceInsufficientPermissions(t *testing.T) {
	mux, ah, store := newTestServiceMux()
	_ = store.Save(context.Background(), svcTestProject())

	rec := postServiceJSON(mux, sessionCookieFor(ah, "carol", "viewer"), "myapi", createServiceRequest{
		Name:     "api",
		Template: "web-service",
		Values:   map[string]any{"service_name": "api"},
	})

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
