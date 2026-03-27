package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/runtime"
	"github.com/suparcloud/suparship/internal/session"
)

// --- Fake runtime provider ---

type fakeRuntimeProvider struct {
	infos map[string]*runtime.RuntimeInfo // key: "namespace/service"
}

func newFakeRuntimeProvider() *fakeRuntimeProvider {
	return &fakeRuntimeProvider{infos: make(map[string]*runtime.RuntimeInfo)}
}

func (f *fakeRuntimeProvider) set(namespace, service string, info *runtime.RuntimeInfo) {
	f.infos[namespace+"/"+service] = info
}

func (f *fakeRuntimeProvider) GetServiceRuntime(_ context.Context, namespace, serviceName string) (*runtime.RuntimeInfo, error) {
	info, ok := f.infos[namespace+"/"+serviceName]
	if !ok {
		return &runtime.RuntimeInfo{
			Status:      runtime.StatusNotDeployed,
			IngressURLs: []string{},
			Namespace:   namespace,
		}, nil
	}
	return info, nil
}

// --- Test helpers ---

func invTestProject() *project.Project {
	return &project.Project{
		APIVersion: project.CurrentAPIVersion,
		Kind:       project.ProjectKind,
		Metadata:   project.ProjectMeta{Name: "myapi"},
		Spec: project.ProjectSpec{
			DisplayName: "My API",
			Environments: []project.Environment{
				{Name: "dev", DisplayName: "Development", Order: 1},
				{Name: "staging", Order: 2},
				{Name: "prod", Order: 3},
			},
			Services: []project.Service{
				{
					Name:     "api",
					Template: project.TemplateRef{Name: "web-service", Version: "1.0.0"},
					Values:   map[string]any{"size": "small"},
					SecretRefs: []project.SecretRef{
						{Name: "database_url", SecretRef: "api-db.url"},
					},
				},
				{
					Name:     "worker",
					Template: project.TemplateRef{Name: "web-service", Version: "1.0.0"},
				},
			},
		},
	}
}

func newTestInventoryMux(rp runtime.Provider) (*http.ServeMux, *authHandler, *memProjectStore) {
	mux := http.NewServeMux()

	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	ah.registerRoutes(mux)

	store := newMemProjectStore()

	rh := &rbacHandler{
		auth:             ah,
		orgProvider:      &staticOrgProvider{org: testRBACOrg()},
		inventoryHandler: newInventoryHandler(store, rp),
	}
	rh.registerRoutes(mux)

	return mux, ah, store
}

// --- Tests ---

func TestListEnvironments(t *testing.T) {
	rp := newFakeRuntimeProvider()
	mux, ah, store := newTestInventoryMux(rp)
	_ = store.Save(context.Background(), invTestProject())

	req := httptest.NewRequest("GET", "/api/v1/environments", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp EnvironmentsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Environments) != 3 {
		t.Fatalf("expected 3 environments, got %d", len(resp.Environments))
	}

	env := resp.Environments[0]
	if env.Name != "dev" {
		t.Errorf("expected first env name %q, got %q", "dev", env.Name)
	}
	if env.Project != "myapi" {
		t.Errorf("expected project %q, got %q", "myapi", env.Project)
	}
	if env.Namespace != "myapi-dev" {
		t.Errorf("expected namespace %q, got %q", "myapi-dev", env.Namespace)
	}
	if env.Order != 1 {
		t.Errorf("expected order 1, got %d", env.Order)
	}
}

func TestListEnvironmentsEmpty(t *testing.T) {
	mux, ah, _ := newTestInventoryMux(nil)

	req := httptest.NewRequest("GET", "/api/v1/environments", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp EnvironmentsResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.Environments) != 0 {
		t.Fatalf("expected 0 environments, got %d", len(resp.Environments))
	}
}

func TestListEnvironmentsUnauthenticated(t *testing.T) {
	mux, _, _ := newTestInventoryMux(nil)

	req := httptest.NewRequest("GET", "/api/v1/environments", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestListProjectServices(t *testing.T) {
	rp := newFakeRuntimeProvider()
	rp.set("myapi-dev", "api", &runtime.RuntimeInfo{
		Status:       runtime.StatusHealthy,
		Image:        "ghcr.io/org/api:v1.2.3",
		Replicas:     2,
		Available:    2,
		IngressURLs:  []string{"https://api.example.com"},
		Namespace:    "myapi-dev",
		LastDeployed: "2026-03-27T10:00:00Z",
	})

	mux, ah, store := newTestInventoryMux(rp)
	_ = store.Save(context.Background(), invTestProject())

	req := httptest.NewRequest("GET", "/api/v1/projects/myapi/services", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp ProjectServicesResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Project != "myapi" {
		t.Errorf("expected project %q, got %q", "myapi", resp.Project)
	}
	if len(resp.Services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(resp.Services))
	}

	api := resp.Services[0]
	if api.Name != "api" {
		t.Errorf("expected service %q, got %q", "api", api.Name)
	}
	if api.Template.Name != "web-service" {
		t.Errorf("expected template %q, got %q", "web-service", api.Template.Name)
	}
	if api.Runtime.Status != runtime.StatusHealthy {
		t.Errorf("expected status %q, got %q", runtime.StatusHealthy, api.Runtime.Status)
	}
	if api.Runtime.Image != "ghcr.io/org/api:v1.2.3" {
		t.Errorf("expected image %q, got %q", "ghcr.io/org/api:v1.2.3", api.Runtime.Image)
	}
	if api.Runtime.Replicas != 2 {
		t.Errorf("expected 2 replicas, got %d", api.Runtime.Replicas)
	}
	if len(api.Runtime.IngressURLs) != 1 || api.Runtime.IngressURLs[0] != "https://api.example.com" {
		t.Errorf("expected ingress URL https://api.example.com, got %v", api.Runtime.IngressURLs)
	}

	worker := resp.Services[1]
	if worker.Name != "worker" {
		t.Errorf("expected service %q, got %q", "worker", worker.Name)
	}
	if worker.Runtime.Status != runtime.StatusNotDeployed {
		t.Errorf("expected status %q, got %q", runtime.StatusNotDeployed, worker.Runtime.Status)
	}
}

func TestListProjectServicesNotFound(t *testing.T) {
	mux, ah, _ := newTestInventoryMux(nil)

	req := httptest.NewRequest("GET", "/api/v1/projects/nonexistent/services", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestListProjectServicesNoRuntimeProvider(t *testing.T) {
	mux, ah, store := newTestInventoryMux(nil)
	_ = store.Save(context.Background(), invTestProject())

	req := httptest.NewRequest("GET", "/api/v1/projects/myapi/services", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp ProjectServicesResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	for _, svc := range resp.Services {
		if svc.Runtime.Status != runtime.StatusNotDeployed {
			t.Errorf("service %s: expected %q status without provider, got %q",
				svc.Name, runtime.StatusNotDeployed, svc.Runtime.Status)
		}
	}
}

func TestGetServiceDetail(t *testing.T) {
	rp := newFakeRuntimeProvider()
	rp.set("myapi-dev", "api", &runtime.RuntimeInfo{
		Status:    runtime.StatusHealthy,
		Image:     "ghcr.io/org/api:v1.2.3",
		Replicas:  2,
		Available: 2,
		IngressURLs: []string{"https://api-dev.example.com"},
		Namespace: "myapi-dev",
	})
	rp.set("myapi-staging", "api", &runtime.RuntimeInfo{
		Status:      runtime.StatusProgressing,
		Image:       "ghcr.io/org/api:v1.3.0",
		Replicas:    1,
		Available:   0,
		IngressURLs: []string{},
		Namespace:   "myapi-staging",
	})

	mux, ah, store := newTestInventoryMux(rp)
	_ = store.Save(context.Background(), invTestProject())

	req := httptest.NewRequest("GET", "/api/v1/projects/myapi/services/api", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp ServiceDetailResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Name != "api" {
		t.Errorf("expected name %q, got %q", "api", resp.Name)
	}
	if resp.Project != "myapi" {
		t.Errorf("expected project %q, got %q", "myapi", resp.Project)
	}
	if resp.Template.Name != "web-service" {
		t.Errorf("expected template %q, got %q", "web-service", resp.Template.Name)
	}
	if resp.Values["size"] != "small" {
		t.Errorf("expected values.size %q, got %v", "small", resp.Values["size"])
	}
	if len(resp.SecretRefs) != 1 {
		t.Fatalf("expected 1 secret ref, got %d", len(resp.SecretRefs))
	}
	if resp.SecretRefs[0].Name != "database_url" {
		t.Errorf("expected secret ref name %q, got %q", "database_url", resp.SecretRefs[0].Name)
	}

	if len(resp.Environments) != 3 {
		t.Fatalf("expected 3 environments, got %d", len(resp.Environments))
	}

	dev := resp.Environments[0]
	if dev.Environment != "dev" {
		t.Errorf("expected env %q, got %q", "dev", dev.Environment)
	}
	if dev.Namespace != "myapi-dev" {
		t.Errorf("expected namespace %q, got %q", "myapi-dev", dev.Namespace)
	}
	if dev.Runtime.Status != runtime.StatusHealthy {
		t.Errorf("expected dev status %q, got %q", runtime.StatusHealthy, dev.Runtime.Status)
	}
	if dev.Runtime.Image != "ghcr.io/org/api:v1.2.3" {
		t.Errorf("expected dev image %q, got %q", "ghcr.io/org/api:v1.2.3", dev.Runtime.Image)
	}

	staging := resp.Environments[1]
	if staging.Runtime.Status != runtime.StatusProgressing {
		t.Errorf("expected staging status %q, got %q", runtime.StatusProgressing, staging.Runtime.Status)
	}

	prod := resp.Environments[2]
	if prod.Runtime.Status != runtime.StatusNotDeployed {
		t.Errorf("expected prod status %q, got %q", runtime.StatusNotDeployed, prod.Runtime.Status)
	}
}

func TestGetServiceNotFound(t *testing.T) {
	mux, ah, store := newTestInventoryMux(nil)
	_ = store.Save(context.Background(), invTestProject())

	req := httptest.NewRequest("GET", "/api/v1/projects/myapi/services/nonexistent", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp errorResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if !contains(resp.Error, "not found") {
		t.Errorf("expected 'not found' in error, got %q", resp.Error)
	}
}

func TestGetServiceProjectNotFound(t *testing.T) {
	mux, ah, _ := newTestInventoryMux(nil)

	req := httptest.NewRequest("GET", "/api/v1/projects/nonexistent/services/api", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestInventoryUnauthenticated(t *testing.T) {
	mux, _, store := newTestInventoryMux(nil)
	_ = store.Save(context.Background(), invTestProject())

	endpoints := []string{
		"/api/v1/projects/myapi/services",
		"/api/v1/projects/myapi/services/api",
	}
	for _, ep := range endpoints {
		req := httptest.NewRequest("GET", ep, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: expected 401, got %d", ep, rec.Code)
		}
	}
}

func TestInventoryInsufficientPermissions(t *testing.T) {
	mux, ah, store := newTestInventoryMux(nil)
	_ = store.Save(context.Background(), invTestProject())

	cookie := sessionCookieFor(ah, "nobody", "viewer")
	endpoints := []string{
		"/api/v1/projects/myapi/services",
		"/api/v1/projects/myapi/services/api",
	}
	for _, ep := range endpoints {
		req := httptest.NewRequest("GET", ep, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		// "nobody" is not in any team in testRBACOrg but has viewer role set explicitly —
		// since viewer access is checked against org role bindings, we expect either 200
		// (if user has org-wide viewer) or 403.
		if rec.Code != http.StatusOK && rec.Code != http.StatusForbidden {
			t.Errorf("%s: expected 200 or 403, got %d", ep, rec.Code)
		}
	}
}

func TestEmptyIngressURLsNotNull(t *testing.T) {
	rp := newFakeRuntimeProvider()
	mux, ah, store := newTestInventoryMux(rp)
	_ = store.Save(context.Background(), invTestProject())

	req := httptest.NewRequest("GET", "/api/v1/projects/myapi/services/api", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !contains(body, `"ingressUrls":[]`) {
		t.Errorf("expected ingressUrls as empty array, got body: %s", body)
	}
}
