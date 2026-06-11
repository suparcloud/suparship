package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/session"
)

// TestClusterHandlerRequiresOrgAdmin verifies the workload-cluster registry is
// org_admin-only for both reads and writes — a developer/viewer must not be
// able to list, register, or delete clusters (infra topology + credentials).
func TestClusterHandlerRequiresOrgAdmin(t *testing.T) {
	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	ah.registerRoutes(mux)
	ch := &clusterHandler{
		store:    &fakeClusterStore{clusters: nil},
		auth:     ah,
		orgStore: &staticOrgProvider{org: testRBACOrg()}, // alice=org_admin, carol=viewer
	}
	ch.registerRoutes(mux)

	do := func(method, path, user, role string) int {
		req := httptest.NewRequest(method, path, nil)
		if user != "" {
			req.AddCookie(sessionCookieFor(ah, user, role))
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := do("GET", "/api/v1/clusters", "carol", "viewer"); code != http.StatusForbidden {
		t.Errorf("viewer list clusters: got %d, want 403", code)
	}
	if code := do("DELETE", "/api/v1/clusters/x", "bob", "developer"); code != http.StatusForbidden {
		t.Errorf("developer delete cluster: got %d, want 403", code)
	}
	if code := do("GET", "/api/v1/clusters", "", ""); code != http.StatusUnauthorized {
		t.Errorf("unauthenticated list clusters: got %d, want 401", code)
	}
	if code := do("GET", "/api/v1/clusters", "alice", "org_admin"); code != http.StatusOK {
		t.Errorf("org_admin list clusters: got %d, want 200", code)
	}
}
