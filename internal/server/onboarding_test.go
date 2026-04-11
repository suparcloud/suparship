package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/session"
)

// --- ComputeStatus unit tests ---

func TestOnboardingNothingConfigured(t *testing.T) {
	s := ComputeStatus(context.Background(), false, nil, nil)
	if s.ClusterConnected {
		t.Error("expected ClusterConnected=false")
	}
	if s.AuthConfigured {
		t.Error("expected AuthConfigured=false")
	}
	if s.OrgExists {
		t.Error("expected OrgExists=false")
	}
	if s.HasProjects {
		t.Error("expected HasProjects=false")
	}
	if s.Complete {
		t.Error("expected Complete=false")
	}
}

func TestOnboardingAuthOnly(t *testing.T) {
	s := ComputeStatus(context.Background(), true, nil, nil)
	if !s.AuthConfigured {
		t.Error("expected AuthConfigured=true")
	}
	if s.ClusterConnected {
		t.Error("expected ClusterConnected=false when providers nil")
	}
	if s.Complete {
		t.Error("expected Complete=false")
	}
}

func TestOnboardingClusterNoProjects(t *testing.T) {
	org := &staticOrgProvider{org: &rbac.Org{Name: "default", DisplayName: "Default"}}
	store := newMemProjectStore()

	s := ComputeStatus(context.Background(), true, org, store)
	if !s.ClusterConnected {
		t.Error("expected ClusterConnected=true")
	}
	if !s.AuthConfigured {
		t.Error("expected AuthConfigured=true")
	}
	if !s.OrgExists {
		t.Error("expected OrgExists=true")
	}
	if s.HasProjects {
		t.Error("expected HasProjects=false")
	}
	if s.HasEnvironments {
		t.Error("expected HasEnvironments=false")
	}
	if s.HasServices {
		t.Error("expected HasServices=false")
	}
	if s.Complete {
		t.Error("expected Complete=false")
	}
}

func TestOnboardingProjectNoEnvsOrServices(t *testing.T) {
	org := &staticOrgProvider{org: &rbac.Org{Name: "default"}}
	store := newMemProjectStore()
	_ = store.Save(context.Background(), &project.Project{
		APIVersion: project.CurrentAPIVersion,
		Kind:       project.ProjectKind,
		Metadata:   project.ProjectMeta{Name: "myapi"},
		Spec: project.ProjectSpec{
			Environments: []project.Environment{},
		},
	})

	s := ComputeStatus(context.Background(), true, org, store)
	if !s.HasProjects {
		t.Error("expected HasProjects=true")
	}
	if s.HasEnvironments {
		t.Error("expected HasEnvironments=false with empty environments")
	}
	if s.HasServices {
		t.Error("expected HasServices=false")
	}
	if s.Complete {
		t.Error("expected Complete=false")
	}
}

func TestOnboardingProjectWithEnvsNoServices(t *testing.T) {
	org := &staticOrgProvider{org: &rbac.Org{Name: "default"}}
	store := newMemProjectStore()
	_ = store.Save(context.Background(), &project.Project{
		APIVersion: project.CurrentAPIVersion,
		Kind:       project.ProjectKind,
		Metadata:   project.ProjectMeta{Name: "myapi"},
		Spec: project.ProjectSpec{
			Environments: []project.Environment{
				{Name: "dev", Order: 1},
			},
		},
	})

	s := ComputeStatus(context.Background(), true, org, store)
	if !s.HasEnvironments {
		t.Error("expected HasEnvironments=true")
	}
	if s.HasServices {
		t.Error("expected HasServices=false")
	}
	if s.Complete {
		t.Error("expected Complete=false")
	}
}

func TestOnboardingFullyComplete(t *testing.T) {
	org := &staticOrgProvider{org: &rbac.Org{Name: "default", DisplayName: "Default"}}
	store := newMemProjectStore()
	_ = store.Save(context.Background(), &project.Project{
		APIVersion: project.CurrentAPIVersion,
		Kind:       project.ProjectKind,
		Metadata:   project.ProjectMeta{Name: "myapi"},
		Spec: project.ProjectSpec{
			Environments: []project.Environment{
				{Name: "dev", Order: 1},
				{Name: "prod", Order: 2},
			},
			Services: []project.Service{
				{Name: "api", Template: project.TemplateRef{Name: "web-service"}},
			},
		},
	})

	s := ComputeStatus(context.Background(), true, org, store)
	if !s.ClusterConnected {
		t.Error("expected ClusterConnected=true")
	}
	if !s.AuthConfigured {
		t.Error("expected AuthConfigured=true")
	}
	if !s.OrgExists {
		t.Error("expected OrgExists=true")
	}
	if !s.HasProjects {
		t.Error("expected HasProjects=true")
	}
	if !s.HasEnvironments {
		t.Error("expected HasEnvironments=true")
	}
	if !s.HasServices {
		t.Error("expected HasServices=true")
	}
	if !s.Complete {
		t.Error("expected Complete=true")
	}
}

func TestOnboardingCompleteMultipleProjects(t *testing.T) {
	org := &staticOrgProvider{org: &rbac.Org{Name: "default"}}
	store := newMemProjectStore()

	_ = store.Save(context.Background(), &project.Project{
		APIVersion: project.CurrentAPIVersion,
		Kind:       project.ProjectKind,
		Metadata:   project.ProjectMeta{Name: "frontend"},
		Spec: project.ProjectSpec{
			Environments: []project.Environment{{Name: "dev", Order: 1}},
		},
	})
	_ = store.Save(context.Background(), &project.Project{
		APIVersion: project.CurrentAPIVersion,
		Kind:       project.ProjectKind,
		Metadata:   project.ProjectMeta{Name: "backend"},
		Spec: project.ProjectSpec{
			Services: []project.Service{
				{Name: "api", Template: project.TemplateRef{Name: "web-service"}},
			},
		},
	})

	s := ComputeStatus(context.Background(), true, org, store)
	if !s.HasProjects {
		t.Error("expected HasProjects=true")
	}
	if !s.HasEnvironments {
		t.Error("expected HasEnvironments=true from 'frontend' project")
	}
	if !s.HasServices {
		t.Error("expected HasServices=true from 'backend' project")
	}
	if !s.Complete {
		t.Error("expected Complete=true")
	}
}

func TestOnboardingNoAuth(t *testing.T) {
	org := &staticOrgProvider{org: &rbac.Org{Name: "default"}}
	store := newMemProjectStore()
	_ = store.Save(context.Background(), &project.Project{
		APIVersion: project.CurrentAPIVersion,
		Kind:       project.ProjectKind,
		Metadata:   project.ProjectMeta{Name: "myapi"},
		Spec: project.ProjectSpec{
			Environments: []project.Environment{{Name: "dev", Order: 1}},
			Services: []project.Service{
				{Name: "api", Template: project.TemplateRef{Name: "web-service"}},
			},
		},
	})

	s := ComputeStatus(context.Background(), false, org, store)
	if !s.HasProjects {
		t.Error("expected HasProjects=true")
	}
	if s.Complete {
		t.Error("expected Complete=false when auth not configured")
	}
}

// --- HTTP handler test ---

func newTestOnboardingMux(
	authEnabled bool,
	orgProvider rbac.OrgProvider,
	projectStore project.Store,
) *http.ServeMux {
	mux := http.NewServeMux()
	registerRoutes(mux, nil)

	if authEnabled {
		ah := &authHandler{
			authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
			sessions:      session.NewStore(time.Hour),
		}
		ah.registerRoutes(mux)
	}

	oh := &onboardingHandler{
		orgProvider:  orgProvider,
		projectStore: projectStore,
		authEnabled:  authEnabled,
	}
	mux.HandleFunc("GET /api/v1/onboarding/status", oh.handleStatus)

	return mux
}

func TestOnboardingEndpointNoAuth(t *testing.T) {
	mux := newTestOnboardingMux(false, nil, nil)

	req := httptest.NewRequest("GET", "/api/v1/onboarding/status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp OnboardingStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ClusterConnected {
		t.Error("expected ClusterConnected=false")
	}
	if resp.AuthConfigured {
		t.Error("expected AuthConfigured=false")
	}
	if resp.Complete {
		t.Error("expected Complete=false")
	}
}

func TestOnboardingEndpointComplete(t *testing.T) {
	org := &staticOrgProvider{org: &rbac.Org{Name: "default"}}
	store := newMemProjectStore()
	_ = store.Save(context.Background(), &project.Project{
		APIVersion: project.CurrentAPIVersion,
		Kind:       project.ProjectKind,
		Metadata:   project.ProjectMeta{Name: "myapi"},
		Spec: project.ProjectSpec{
			Environments: []project.Environment{{Name: "dev", Order: 1}},
			Services: []project.Service{
				{Name: "api", Template: project.TemplateRef{Name: "web-service"}},
			},
		},
	})

	mux := newTestOnboardingMux(true, org, store)

	req := httptest.NewRequest("GET", "/api/v1/onboarding/status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp OnboardingStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Complete {
		t.Errorf("expected Complete=true, got status: %+v", resp)
	}
}

func TestOnboardingEndpointUnauthenticatedAccess(t *testing.T) {
	org := &staticOrgProvider{org: &rbac.Org{Name: "default"}}
	store := newMemProjectStore()
	mux := newTestOnboardingMux(true, org, store)

	req := httptest.NewRequest("GET", "/api/v1/onboarding/status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("onboarding status should be accessible without auth, got %d", rec.Code)
	}
}
