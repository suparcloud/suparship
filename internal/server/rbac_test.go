package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/session"
)

// staticOrgProvider implements rbac.OrgStore with a fixed in-memory org.
// SaveOrg is a no-op for tests that don't need to observe mutations.
type staticOrgProvider struct {
	org *rbac.Org
}

func (s *staticOrgProvider) GetOrg(_ context.Context) (*rbac.Org, error) {
	return s.org, nil
}

func (s *staticOrgProvider) SaveOrg(_ context.Context, org *rbac.Org) error {
	s.org = org
	return nil
}

func testRBACOrg() *rbac.Org {
	return &rbac.Org{
		Name:        "test",
		DisplayName: "Test Org",
		CreatedAt:   "2026-01-01T00:00:00Z",
		Environments: []rbac.OrgEnvironment{
			{Name: "staging", DisplayName: "Staging", Order: 1, ClusterRef: "in-cluster"},
			{Name: "prod", DisplayName: "Production", Order: 2, ClusterRef: "in-cluster"},
		},
		Teams: []rbac.Team{
			{Name: "admins", DisplayName: "Admins", Members: []string{"alice"}},
			{Name: "devs", DisplayName: "Devs", Members: []string{"bob"}},
			{Name: "viewers", DisplayName: "Viewers", Members: []string{"carol"}},
		},
		RoleBindings: []rbac.RoleBinding{
			{Project: "*", Team: "admins", Role: rbac.RoleOrgAdmin},
			{Project: "api", Team: "devs", Role: rbac.RoleDeveloper},
			{Project: "*", Team: "viewers", Role: rbac.RoleViewer},
		},
	}
}

func newTestRBACMux() (*http.ServeMux, *authHandler) {
	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
		cookieSecure:  false,
	}
	ah.registerRoutes(mux)

	store := newMemProjectStore()

	rh := &rbacHandler{
		auth:           ah,
		orgStore:       &staticOrgProvider{org: testRBACOrg()},
		promoteHandler: newPromoteHandler(store),
	}
	rh.registerRoutes(mux)

	return mux, ah
}

func sessionCookieFor(ah *authHandler, username, role string) *http.Cookie {
	sess, _ := ah.sessions.Create(username, role)
	return &http.Cookie{Name: sessionCookieName, Value: sess.ID}
}

// --- org_admin access ---

func TestRBACOrgAdminCanViewProject(t *testing.T) {
	mux, ah := newTestRBACMux()

	req := httptest.NewRequest("GET", "/api/v1/projects/api", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRBACOrgAdminCanManageProject(t *testing.T) {
	mux, ah := newTestRBACMux()

	req := httptest.NewRequest("PUT", "/api/v1/projects/api", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRBACOrgAdminCanAccessAnyProject(t *testing.T) {
	mux, ah := newTestRBACMux()

	req := httptest.NewRequest("GET", "/api/v1/projects/unknown-project", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("org_admin should access any project, got %d", rec.Code)
	}
}

// --- developer access ---

func TestRBACDeveloperCanViewProject(t *testing.T) {
	mux, ah := newTestRBACMux()

	req := httptest.NewRequest("GET", "/api/v1/projects/api", nil)
	req.AddCookie(sessionCookieFor(ah, "bob", "developer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("developer should view project, got %d", rec.Code)
	}
}

func TestRBACDeveloperCannotPromote(t *testing.T) {
	mux, ah := newTestRBACMux()

	req := httptest.NewRequest("POST", "/api/v1/projects/api/services/backend/promote", nil)
	req.AddCookie(sessionCookieFor(ah, "bob", "developer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("developer should not promote (requires project_admin), got %d", rec.Code)
	}
}

func TestRBACDeveloperCannotManageProject(t *testing.T) {
	mux, ah := newTestRBACMux()

	req := httptest.NewRequest("PUT", "/api/v1/projects/api", nil)
	req.AddCookie(sessionCookieFor(ah, "bob", "developer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("developer should not manage project, got %d", rec.Code)
	}

	var resp errorResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Error != "insufficient permissions" {
		t.Fatalf("expected 'insufficient permissions' error, got %q", resp.Error)
	}
}

func TestRBACDeveloperCannotAccessOtherProject(t *testing.T) {
	mux, ah := newTestRBACMux()

	req := httptest.NewRequest("POST", "/api/v1/projects/web/services/svc/promote", nil)
	req.AddCookie(sessionCookieFor(ah, "bob", "developer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("developer should not access unbound project, got %d", rec.Code)
	}
}

// --- viewer access ---

func TestRBACViewerCanView(t *testing.T) {
	mux, ah := newTestRBACMux()

	req := httptest.NewRequest("GET", "/api/v1/projects/api", nil)
	req.AddCookie(sessionCookieFor(ah, "carol", "viewer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("viewer should view project, got %d", rec.Code)
	}
}

func TestRBACViewerCannotPromoteService(t *testing.T) {
	mux, ah := newTestRBACMux()

	req := httptest.NewRequest("POST", "/api/v1/projects/api/services/svc/promote", nil)
	req.AddCookie(sessionCookieFor(ah, "carol", "viewer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer should not promote service, got %d", rec.Code)
	}
}

// --- unauthenticated access ---

func TestRBACUnauthenticatedReturns401(t *testing.T) {
	mux, _ := newTestRBACMux()

	req := httptest.NewRequest("GET", "/api/v1/projects/api", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated, got %d", rec.Code)
	}

	var resp errorResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Error != "not authenticated" {
		t.Fatalf("expected 'not authenticated' error, got %q", resp.Error)
	}
}

func TestRBACInvalidSessionReturns401(t *testing.T) {
	mux, _ := newTestRBACMux()

	req := httptest.NewRequest("GET", "/api/v1/projects/api", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "bogus"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid session, got %d", rec.Code)
	}
}

// --- unknown user (authenticated but not in any team) ---

func TestRBACUnknownUserForbidden(t *testing.T) {
	mux, ah := newTestRBACMux()

	req := httptest.NewRequest("GET", "/api/v1/projects/api", nil)
	req.AddCookie(sessionCookieFor(ah, "stranger", "viewer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("unknown user should be forbidden, got %d", rec.Code)
	}
}
