package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/session"
)

// --- GET /api/v1/org ---

func TestGetOrgAuthenticated(t *testing.T) {
	mux, ah := newTestRBACMux()

	req := httptest.NewRequest("GET", "/api/v1/org", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp OrgResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Name != "test" {
		t.Fatalf("expected org name %q, got %q", "test", resp.Name)
	}
	if resp.DisplayName != "Test Org" {
		t.Fatalf("expected displayName %q, got %q", "Test Org", resp.DisplayName)
	}
	if resp.CreatedAt != "2026-01-01T00:00:00Z" {
		t.Fatalf("expected createdAt %q, got %q", "2026-01-01T00:00:00Z", resp.CreatedAt)
	}
}

func TestGetOrgUnauthenticated(t *testing.T) {
	mux, _ := newTestRBACMux()

	req := httptest.NewRequest("GET", "/api/v1/org", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestGetOrgAnyAuthenticatedUser(t *testing.T) {
	mux, ah := newTestRBACMux()

	req := httptest.NewRequest("GET", "/api/v1/org", nil)
	req.AddCookie(sessionCookieFor(ah, "carol", "viewer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("any authenticated user should access /org, got %d", rec.Code)
	}
}

// --- GET /api/v1/teams ---

func TestGetTeamsAuthenticated(t *testing.T) {
	mux, ah := newTestRBACMux()

	req := httptest.NewRequest("GET", "/api/v1/teams", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp TeamsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(resp.Teams) != 3 {
		t.Fatalf("expected 3 teams, got %d", len(resp.Teams))
	}

	teamNames := make(map[string]bool)
	for _, team := range resp.Teams {
		teamNames[team.Name] = true
		if team.Members == nil {
			t.Fatalf("team %q members should not be null", team.Name)
		}
	}
	for _, expected := range []string{"admins", "devs", "viewers"} {
		if !teamNames[expected] {
			t.Fatalf("expected team %q in response", expected)
		}
	}
}

func TestGetTeamsUnauthenticated(t *testing.T) {
	mux, _ := newTestRBACMux()

	req := httptest.NewRequest("GET", "/api/v1/teams", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

// --- GET /api/v1/projects ---

func TestGetProjectsAuthenticated(t *testing.T) {
	mux, ah := newTestRBACMux()

	req := httptest.NewRequest("GET", "/api/v1/projects", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp ProjectsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	// testRBACOrg has one non-wildcard project: "api"
	if len(resp.Projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(resp.Projects))
	}
	if resp.Projects[0].Name != "api" {
		t.Fatalf("expected project %q, got %q", "api", resp.Projects[0].Name)
	}
}

func TestGetProjectsExcludesWildcard(t *testing.T) {
	mux, ah := newTestRBACMux()

	req := httptest.NewRequest("GET", "/api/v1/projects", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var resp ProjectsResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	for _, p := range resp.Projects {
		if p.Name == "*" {
			t.Fatal("wildcard should not appear as a project")
		}
	}
}

func TestGetProjectsMergesProjectStore(t *testing.T) {
	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
		cookieSecure:  false,
	}
	ah.registerRoutes(mux)

	store := newMemProjectStore()
	_ = store.Save(context.Background(), &project.Project{
		Metadata: project.ProjectMeta{Name: "api"},
	})
	_ = store.Save(context.Background(), &project.Project{
		Metadata: project.ProjectMeta{Name: "web"},
	})
	_ = store.Save(context.Background(), &project.Project{
		Metadata: project.ProjectMeta{Name: "billing"},
	})

	rh := &rbacHandler{
		auth:         ah,
		orgStore:  &staticOrgProvider{org: testRBACOrg()},
		projectStore: store,
	}
	rh.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/projects", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp ProjectsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	want := []string{"api", "billing", "web"}
	if len(resp.Projects) != len(want) {
		t.Fatalf("expected %d projects, got %d: %+v", len(want), len(resp.Projects), resp.Projects)
	}
	for i, p := range resp.Projects {
		if p.Name != want[i] {
			t.Fatalf("project[%d]: expected %q, got %q", i, want[i], p.Name)
		}
	}
}

func TestGetProjectsUnauthenticated(t *testing.T) {
	mux, _ := newTestRBACMux()

	req := httptest.NewRequest("GET", "/api/v1/projects", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

// --- GET /api/v1/projects/{project}/rbac ---

func TestGetProjectRBACOrgAdmin(t *testing.T) {
	mux, ah := newTestRBACMux()

	req := httptest.NewRequest("GET", "/api/v1/projects/api/rbac", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp ProjectRBACResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Project != "api" {
		t.Fatalf("expected project %q, got %q", "api", resp.Project)
	}
	// "api" should match: wildcard admins (org_admin), devs (developer), wildcard viewers (viewer)
	if len(resp.RoleBindings) != 3 {
		t.Fatalf("expected 3 bindings for project api, got %d: %+v", len(resp.RoleBindings), resp.RoleBindings)
	}
}

func TestGetProjectRBACDeveloper(t *testing.T) {
	mux, ah := newTestRBACMux()

	req := httptest.NewRequest("GET", "/api/v1/projects/api/rbac", nil)
	req.AddCookie(sessionCookieFor(ah, "bob", "developer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("developer with access should see rbac, got %d", rec.Code)
	}
}

func TestGetProjectRBACForbidden(t *testing.T) {
	mux, ah := newTestRBACMux()

	// bob is developer on "api" only, not on "web"
	req := httptest.NewRequest("GET", "/api/v1/projects/web/rbac", nil)
	req.AddCookie(sessionCookieFor(ah, "bob", "developer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for unbound project, got %d", rec.Code)
	}
}

func TestGetProjectRBACUnauthenticated(t *testing.T) {
	mux, _ := newTestRBACMux()

	req := httptest.NewRequest("GET", "/api/v1/projects/api/rbac", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestGetProjectRBACIncludesWildcardBindings(t *testing.T) {
	mux, ah := newTestRBACMux()

	req := httptest.NewRequest("GET", "/api/v1/projects/api/rbac", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var resp ProjectRBACResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)

	var hasWildcard bool
	for _, rb := range resp.RoleBindings {
		if rb.Project == "*" {
			hasWildcard = true
			break
		}
	}
	if !hasWildcard {
		t.Fatal("project rbac response should include wildcard bindings")
	}
}

// --- GET /api/v1/projects/{project} ---

// newProjectDetailMux builds a mux with a seeded project store for testing
// the handleGetProject handler.
func newProjectDetailMux() (*http.ServeMux, *authHandler) {
	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
		cookieSecure:  false,
	}
	ah.registerRoutes(mux)

	store := newMemProjectStore()
	_ = store.Save(context.Background(), &project.Project{
		APIVersion: project.CurrentAPIVersion,
		Kind:       project.ProjectKind,
		Metadata:   project.ProjectMeta{Name: "api"},
		Spec: project.ProjectSpec{
			DisplayName: "API Service",
			Description: "Core API.",
			Environments: []project.Environment{
				{Name: "staging", DisplayName: "Staging", Order: 1},
				{Name: "prod", DisplayName: "Production", Order: 2},
			},
			Services: []project.Service{
				{Name: "backend", Template: project.TemplateRef{Name: "web-service"}},
				{Name: "worker", Template: project.TemplateRef{Name: "web-service"}},
			},
		},
	})

	rh := &rbacHandler{
		auth:         ah,
		orgStore:  &staticOrgProvider{org: testRBACOrg()},
		projectStore: store,
	}
	rh.registerRoutes(mux)

	return mux, ah
}

func TestGetProjectDetail_FullData(t *testing.T) {
	mux, ah := newProjectDetailMux()

	req := httptest.NewRequest("GET", "/api/v1/projects/api", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp ProjectDetailResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.Name != "api" {
		t.Errorf("Name = %q, want %q", resp.Name, "api")
	}
	if resp.DisplayName != "API Service" {
		t.Errorf("DisplayName = %q, want %q", resp.DisplayName, "API Service")
	}
	if resp.Description != "Core API." {
		t.Errorf("Description = %q, want %q", resp.Description, "Core API.")
	}
	if len(resp.Environments) != 2 {
		t.Fatalf("expected 2 environments, got %d", len(resp.Environments))
	}
	if resp.Environments[0].Name != "staging" {
		t.Errorf("Environments[0].Name = %q, want %q", resp.Environments[0].Name, "staging")
	}
	if resp.Environments[0].Namespace == "" {
		t.Error("environment Namespace must not be empty")
	}
	if len(resp.Services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(resp.Services))
	}
}

func TestGetProjectDetail_NotFound(t *testing.T) {
	mux, ah := newProjectDetailMux()

	req := httptest.NewRequest("GET", "/api/v1/projects/unknown", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGetProjectDetail_Unauthenticated(t *testing.T) {
	mux, _ := newProjectDetailMux()

	req := httptest.NewRequest("GET", "/api/v1/projects/api", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestGetProjectDetail_Forbidden(t *testing.T) {
	mux, ah := newProjectDetailMux()

	// bob is developer on "api" only; carol is viewer on all (*) but tries nothing special.
	// Use a user not in any team for the project to test 403.
	req := httptest.NewRequest("GET", "/api/v1/projects/api", nil)
	req.AddCookie(sessionCookieFor(ah, "stranger", "viewer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for unknown user, got %d", rec.Code)
	}
}

func TestGetProjectDetail_NoStore_ReturnsMinimal(t *testing.T) {
	// newTestRBACMux has no projectStore on rbacHandler (only on promoteHandler).
	mux, ah := newTestRBACMux()

	req := httptest.NewRequest("GET", "/api/v1/projects/api", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 minimal response when no store, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp ProjectDetailResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Name != "api" {
		t.Errorf("Name = %q, want %q", resp.Name, "api")
	}
	if resp.Environments == nil {
		t.Error("Environments must not be null")
	}
	if resp.Services == nil {
		t.Error("Services must not be null")
	}
}

// --- GET /api/v1/projects (displayName enrichment) ---

func TestGetProjectsIncludesDisplayName(t *testing.T) {
	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
		cookieSecure:  false,
	}
	ah.registerRoutes(mux)

	store := newMemProjectStore()
	_ = store.Save(context.Background(), &project.Project{
		Metadata: project.ProjectMeta{Name: "api"},
		Spec: project.ProjectSpec{
			DisplayName: "API Service",
			Description: "Core API.",
		},
	})

	rh := &rbacHandler{
		auth:         ah,
		orgStore:  &staticOrgProvider{org: testRBACOrg()},
		projectStore: store,
	}
	rh.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/projects", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp ProjectsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	var found *ProjectDTO
	for i := range resp.Projects {
		if resp.Projects[i].Name == "api" {
			found = &resp.Projects[i]
			break
		}
	}
	if found == nil {
		t.Fatal("project 'api' not found in response")
	}
	if found.DisplayName != "API Service" {
		t.Errorf("DisplayName = %q, want %q", found.DisplayName, "API Service")
	}
	if found.Description != "Core API." {
		t.Errorf("Description = %q, want %q", found.Description, "Core API.")
	}
}
