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

	"github.com/suparcloud/suparship/internal/preview"
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/runtime"
	"github.com/suparcloud/suparship/internal/session"
)

// --- In-memory preview store for tests ---

type memPreviewStore struct {
	mu       sync.Mutex
	previews map[string]*preview.Preview
}

func newMemPreviewStore() *memPreviewStore {
	return &memPreviewStore{previews: make(map[string]*preview.Preview)}
}

func (m *memPreviewStore) List(_ context.Context) ([]*preview.Preview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*preview.Preview, 0, len(m.previews))
	for _, p := range m.previews {
		out = append(out, p)
	}
	return out, nil
}

func (m *memPreviewStore) Get(_ context.Context, name string) (*preview.Preview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.previews[name]
	if !ok {
		return nil, fmt.Errorf("preview %q not found", name)
	}
	return p, nil
}

func (m *memPreviewStore) Save(_ context.Context, p *preview.Preview) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.previews[p.Metadata.Name] = p
	return nil
}

func (m *memPreviewStore) Delete(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.previews, name)
	return nil
}

// --- Fake runtime provider ---

type fakePreviewRuntimeProvider struct {
	infos map[string]*runtime.RuntimeInfo
}

func (f *fakePreviewRuntimeProvider) GetServiceRuntime(_ context.Context, namespace, serviceName string) (*runtime.RuntimeInfo, error) {
	key := namespace + "/" + serviceName
	if info, ok := f.infos[key]; ok {
		return info, nil
	}
	return &runtime.RuntimeInfo{
		Status:      runtime.StatusNotDeployed,
		IngressURLs: []string{},
		Namespace:   namespace,
	}, nil
}

// --- Test setup ---

func previewTestProject() *project.Project {
	return &project.Project{
		APIVersion: project.CurrentAPIVersion,
		Kind:       project.ProjectKind,
		Metadata:   project.ProjectMeta{Name: "api"},
		Spec: project.ProjectSpec{
			Environments: []project.Environment{
				{Name: "dev", Order: 1},
			},
			Services: []project.Service{
				{Name: "backend", Template: project.TemplateRef{Name: "web-service"}},
			},
		},
	}
}

func newTestPreviewMux(rp runtime.Provider) (*http.ServeMux, *authHandler, *memPreviewStore, *memProjectStore) {
	mux := http.NewServeMux()

	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	ah.registerRoutes(mux)

	projStore := newMemProjectStore()
	prevStore := newMemPreviewStore()

	rh := &rbacHandler{
		auth:           ah,
		orgStore:    &staticOrgProvider{org: testRBACOrg()},
		projectStore:   projStore,
		previewHandler: newPreviewHandler(prevStore, projStore, rp, &staticOrgProvider{org: testRBACOrg()}),
	}
	rh.registerRoutes(mux)

	return mux, ah, prevStore, projStore
}

func postPreviewJSON(mux *http.ServeMux, cookie *http.Cookie, body any) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/previews", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// --- Tests: POST /api/v1/previews ---

func TestCreatePreviewValid(t *testing.T) {
	mux, ah, prevStore, projStore := newTestPreviewMux(nil)
	_ = projStore.Save(context.Background(), previewTestProject())

	rec := postPreviewJSON(mux, sessionCookieFor(ah, "bob", "developer"), CreatePreviewRequest{
		Name:    "pr-42",
		Project: "api",
		Service: "backend",
	})

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var dto PreviewDTO
	if err := json.NewDecoder(rec.Body).Decode(&dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dto.Name != "pr-42" {
		t.Fatalf("expected name %q, got %q", "pr-42", dto.Name)
	}
	if dto.Project != "api" {
		t.Fatalf("expected project %q, got %q", "api", dto.Project)
	}
	if dto.Service != "backend" {
		t.Fatalf("expected service %q, got %q", "backend", dto.Service)
	}
	if dto.Namespace != "api-preview-pr-42" {
		t.Fatalf("expected namespace %q, got %q", "api-preview-pr-42", dto.Namespace)
	}
	if dto.Status != runtime.StatusNotDeployed {
		t.Fatalf("expected status %q, got %q", runtime.StatusNotDeployed, dto.Status)
	}
	if dto.CreatedAt == "" {
		t.Fatal("expected createdAt to be set")
	}

	stored, err := prevStore.Get(context.Background(), "pr-42")
	if err != nil {
		t.Fatalf("preview not persisted: %v", err)
	}
	if stored.Spec.Project != "api" {
		t.Fatalf("stored project: want %q, got %q", "api", stored.Spec.Project)
	}
}

func TestCreatePreviewOrgAdmin(t *testing.T) {
	mux, ah, _, projStore := newTestPreviewMux(nil)
	_ = projStore.Save(context.Background(), previewTestProject())

	rec := postPreviewJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), CreatePreviewRequest{
		Name:    "pr-99",
		Project: "api",
		Service: "backend",
	})

	if rec.Code != http.StatusCreated {
		t.Fatalf("org_admin should create preview, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreatePreviewDuplicate(t *testing.T) {
	mux, ah, prevStore, projStore := newTestPreviewMux(nil)
	_ = projStore.Save(context.Background(), previewTestProject())
	_ = prevStore.Save(context.Background(), preview.New("pr-42", "api", "backend"))

	rec := postPreviewJSON(mux, sessionCookieFor(ah, "bob", "developer"), CreatePreviewRequest{
		Name:    "pr-42",
		Project: "api",
		Service: "backend",
	})

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreatePreviewMissingName(t *testing.T) {
	mux, ah, _, projStore := newTestPreviewMux(nil)
	_ = projStore.Save(context.Background(), previewTestProject())

	rec := postPreviewJSON(mux, sessionCookieFor(ah, "bob", "developer"), CreatePreviewRequest{
		Project: "api",
		Service: "backend",
	})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreatePreviewMissingProject(t *testing.T) {
	mux, ah, _, _ := newTestPreviewMux(nil)

	rec := postPreviewJSON(mux, sessionCookieFor(ah, "bob", "developer"), CreatePreviewRequest{
		Name:    "pr-42",
		Service: "backend",
	})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreatePreviewMissingService(t *testing.T) {
	mux, ah, _, _ := newTestPreviewMux(nil)

	rec := postPreviewJSON(mux, sessionCookieFor(ah, "bob", "developer"), CreatePreviewRequest{
		Name:    "pr-42",
		Project: "api",
	})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreatePreviewProjectNotFound(t *testing.T) {
	mux, ah, _, _ := newTestPreviewMux(nil)

	rec := postPreviewJSON(mux, sessionCookieFor(ah, "bob", "developer"), CreatePreviewRequest{
		Name:    "pr-42",
		Project: "nonexistent",
		Service: "api",
	})

	// bob is developer on "api" project only, "nonexistent" → 403
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreatePreviewServiceNotFound(t *testing.T) {
	mux, ah, _, projStore := newTestPreviewMux(nil)
	_ = projStore.Save(context.Background(), previewTestProject())

	rec := postPreviewJSON(mux, sessionCookieFor(ah, "bob", "developer"), CreatePreviewRequest{
		Name:    "pr-42",
		Project: "api",
		Service: "nonexistent",
	})

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreatePreviewUnauthenticated(t *testing.T) {
	mux, _, _, projStore := newTestPreviewMux(nil)
	_ = projStore.Save(context.Background(), previewTestProject())

	rec := postPreviewJSON(mux, nil, CreatePreviewRequest{
		Name:    "pr-42",
		Project: "api",
		Service: "backend",
	})

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestCreatePreviewViewerForbidden(t *testing.T) {
	mux, ah, _, projStore := newTestPreviewMux(nil)
	_ = projStore.Save(context.Background(), previewTestProject())

	rec := postPreviewJSON(mux, sessionCookieFor(ah, "carol", "viewer"), CreatePreviewRequest{
		Name:    "pr-42",
		Project: "api",
		Service: "backend",
	})

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for viewer, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- Tests: GET /api/v1/previews ---

func TestListPreviewsEmpty(t *testing.T) {
	mux, ah, _, _ := newTestPreviewMux(nil)

	req := httptest.NewRequest("GET", "/api/v1/previews", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp PreviewsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Previews) != 0 {
		t.Fatalf("expected 0 previews, got %d", len(resp.Previews))
	}
}

func TestListPreviewsPopulated(t *testing.T) {
	mux, ah, prevStore, _ := newTestPreviewMux(nil)
	_ = prevStore.Save(context.Background(), preview.New("pr-42", "api", "backend"))
	_ = prevStore.Save(context.Background(), preview.New("pr-99", "api", "frontend"))

	req := httptest.NewRequest("GET", "/api/v1/previews", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp PreviewsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Previews) != 2 {
		t.Fatalf("expected 2 previews, got %d", len(resp.Previews))
	}
}

func TestListPreviewsWithRuntime(t *testing.T) {
	rp := &fakePreviewRuntimeProvider{
		infos: map[string]*runtime.RuntimeInfo{
			"api-preview-pr-42/backend": {
				Status:      runtime.StatusHealthy,
				IngressURLs: []string{"https://pr-42.preview.local"},
				Namespace:   "api-preview-pr-42",
			},
		},
	}

	mux, ah, prevStore, _ := newTestPreviewMux(rp)
	_ = prevStore.Save(context.Background(), preview.New("pr-42", "api", "backend"))

	req := httptest.NewRequest("GET", "/api/v1/previews", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp PreviewsResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.Previews) != 1 {
		t.Fatalf("expected 1 preview, got %d", len(resp.Previews))
	}
	if resp.Previews[0].Status != runtime.StatusHealthy {
		t.Fatalf("expected status %q, got %q", runtime.StatusHealthy, resp.Previews[0].Status)
	}
	if resp.Previews[0].URL != "https://pr-42.preview.local" {
		t.Fatalf("expected URL %q, got %q", "https://pr-42.preview.local", resp.Previews[0].URL)
	}
}

func TestListPreviewsUnauthenticated(t *testing.T) {
	mux, _, _, _ := newTestPreviewMux(nil)

	req := httptest.NewRequest("GET", "/api/v1/previews", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

// --- Tests: DELETE /api/v1/previews/{name} ---

func TestDeletePreviewValid(t *testing.T) {
	mux, ah, prevStore, _ := newTestPreviewMux(nil)
	_ = prevStore.Save(context.Background(), preview.New("pr-42", "api", "backend"))

	req := httptest.NewRequest("DELETE", "/api/v1/previews/pr-42", nil)
	req.AddCookie(sessionCookieFor(ah, "bob", "developer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	if _, err := prevStore.Get(context.Background(), "pr-42"); err == nil {
		t.Fatal("preview should have been deleted")
	}
}

func TestDeletePreviewOrgAdmin(t *testing.T) {
	mux, ah, prevStore, _ := newTestPreviewMux(nil)
	_ = prevStore.Save(context.Background(), preview.New("pr-42", "api", "backend"))

	req := httptest.NewRequest("DELETE", "/api/v1/previews/pr-42", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("org_admin should delete preview, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDeletePreviewNotFound(t *testing.T) {
	mux, ah, _, _ := newTestPreviewMux(nil)

	req := httptest.NewRequest("DELETE", "/api/v1/previews/nonexistent", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDeletePreviewViewerForbidden(t *testing.T) {
	mux, ah, prevStore, _ := newTestPreviewMux(nil)
	_ = prevStore.Save(context.Background(), preview.New("pr-42", "api", "backend"))

	req := httptest.NewRequest("DELETE", "/api/v1/previews/pr-42", nil)
	req.AddCookie(sessionCookieFor(ah, "carol", "viewer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for viewer, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDeletePreviewUnauthenticated(t *testing.T) {
	mux, _, prevStore, _ := newTestPreviewMux(nil)
	_ = prevStore.Save(context.Background(), preview.New("pr-42", "api", "backend"))

	req := httptest.NewRequest("DELETE", "/api/v1/previews/pr-42", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

// --- Tests: GET /api/v1/projects/{project}/services/{service}/previews ---

func TestListServicePreviews_Empty(t *testing.T) {
	mux, ah, _, projStore := newTestPreviewMux(nil)
	_ = projStore.Save(context.Background(), previewTestProject())

	req := httptest.NewRequest("GET", "/api/v1/projects/api/services/backend/previews", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp PreviewsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Previews) != 0 {
		t.Fatalf("expected 0 previews, got %d", len(resp.Previews))
	}
}

func TestListServicePreviews_Filtered(t *testing.T) {
	mux, ah, prevStore, projStore := newTestPreviewMux(nil)
	_ = projStore.Save(context.Background(), previewTestProject())

	// Two previews for the target service, one for a different service.
	_ = prevStore.Save(context.Background(), preview.New("pr-10", "api", "backend"))
	_ = prevStore.Save(context.Background(), preview.New("pr-11", "api", "backend"))
	_ = prevStore.Save(context.Background(), preview.New("pr-20", "api", "frontend")) // different service

	req := httptest.NewRequest("GET", "/api/v1/projects/api/services/backend/previews", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp PreviewsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Previews) != 2 {
		t.Fatalf("expected 2 filtered previews, got %d: %+v", len(resp.Previews), resp.Previews)
	}
	for _, p := range resp.Previews {
		if p.Service != "backend" {
			t.Errorf("expected service 'backend', got %q in preview %q", p.Service, p.Name)
		}
		if p.Project != "api" {
			t.Errorf("expected project 'api', got %q in preview %q", p.Project, p.Name)
		}
	}
}

func TestListServicePreviews_WithRuntime(t *testing.T) {
	rp := &fakePreviewRuntimeProvider{
		infos: map[string]*runtime.RuntimeInfo{
			"api-preview-pr-10/backend": {
				Status:      runtime.StatusHealthy,
				IngressURLs: []string{"https://pr-10.preview.local"},
				Namespace:   "api-preview-pr-10",
			},
		},
	}

	mux, ah, prevStore, projStore := newTestPreviewMux(rp)
	_ = projStore.Save(context.Background(), previewTestProject())
	_ = prevStore.Save(context.Background(), preview.New("pr-10", "api", "backend"))

	req := httptest.NewRequest("GET", "/api/v1/projects/api/services/backend/previews", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp PreviewsResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.Previews) != 1 {
		t.Fatalf("expected 1 preview, got %d", len(resp.Previews))
	}
	if resp.Previews[0].Status != runtime.StatusHealthy {
		t.Errorf("Status = %q, want %q", resp.Previews[0].Status, runtime.StatusHealthy)
	}
	if resp.Previews[0].URL != "https://pr-10.preview.local" {
		t.Errorf("URL = %q, want %q", resp.Previews[0].URL, "https://pr-10.preview.local")
	}
}

func TestListServicePreviews_Unauthenticated(t *testing.T) {
	mux, _, _, _ := newTestPreviewMux(nil)

	req := httptest.NewRequest("GET", "/api/v1/projects/api/services/backend/previews", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestListServicePreviews_Forbidden(t *testing.T) {
	mux, ah, _, _ := newTestPreviewMux(nil)

	// "stranger" is not in any team for project "api".
	req := httptest.NewRequest("GET", "/api/v1/projects/api/services/backend/previews", nil)
	req.AddCookie(sessionCookieFor(ah, "stranger", "viewer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}
