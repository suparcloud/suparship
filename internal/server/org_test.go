package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
