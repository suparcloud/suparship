package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/session"
)

// fixedAuthz is a stub Authorizer returning a constant decision/error,
// standing in for an enterprise policy engine.
type fixedAuthz struct {
	allow bool
	err   error
}

func (f fixedAuthz) Authorize(context.Context, rbac.Identity, string, rbac.Role) (bool, error) {
	return f.allow, f.err
}

// errOrgProvider always fails to load the org, exercising the 500 path.
type errOrgProvider struct{}

func (errOrgProvider) GetOrg(context.Context) (*rbac.Org, error) {
	return nil, fmt.Errorf("org unavailable")
}
func (errOrgProvider) SaveOrg(context.Context, *rbac.Org) error { return nil }

// authzTestMux registers a single project_admin-protected route behind
// requireRole so the authorization decision can be observed in isolation.
func authzTestMux(authz rbac.Authorizer, org rbac.OrgStore) (*http.ServeMux, *authHandler) {
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	rh := &rbacHandler{auth: ah, orgStore: org, authz: authz}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /t/{project}", rh.requireRole(rbac.RoleProjectAdmin, ProjectFromPathValue("project"))(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
	))
	return mux, ah
}

func getProtected(mux *http.ServeMux, ah *authHandler, user, role string) int {
	req := httptest.NewRequest("GET", "/t/api", nil)
	req.AddCookie(sessionCookieFor(ah, user, role))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code
}

// Default authorizer (authz == nil) must behave exactly as the prior inline
// org permission check: org_admin passes a project_admin gate, a viewer is
// denied.
func TestAuthorizerDefaultPreservesBehavior(t *testing.T) {
	mux, ah := authzTestMux(nil, &staticOrgProvider{org: testRBACOrg()})

	if code := getProtected(mux, ah, "alice", "org_admin"); code != http.StatusOK {
		t.Errorf("org_admin alice: expected 200, got %d", code)
	}
	if code := getProtected(mux, ah, "carol", "viewer"); code != http.StatusForbidden {
		t.Errorf("viewer carol: expected 403, got %d", code)
	}
}

// A custom Authorizer that denies everything must override the org policy:
// even org_admin alice is forbidden.
func TestAuthorizerOverrideDenies(t *testing.T) {
	mux, ah := authzTestMux(fixedAuthz{allow: false}, &staticOrgProvider{org: testRBACOrg()})

	if code := getProtected(mux, ah, "alice", "org_admin"); code != http.StatusForbidden {
		t.Errorf("deny-all authorizer: expected 403 for alice, got %d", code)
	}
}

// A custom Authorizer that allows everything must grant access to a user the
// org policy would otherwise reject (carol is only a viewer).
func TestAuthorizerOverrideAllows(t *testing.T) {
	mux, ah := authzTestMux(fixedAuthz{allow: true}, &staticOrgProvider{org: testRBACOrg()})

	if code := getProtected(mux, ah, "carol", "viewer"); code != http.StatusOK {
		t.Errorf("allow-all authorizer: expected 200 for carol, got %d", code)
	}
}

// An org-load failure must surface as 500, not a silent deny.
func TestAuthorizerOrgLoadErrorIs500(t *testing.T) {
	mux, ah := authzTestMux(nil, errOrgProvider{})

	if code := getProtected(mux, ah, "alice", "org_admin"); code != http.StatusInternalServerError {
		t.Errorf("org load error: expected 500, got %d", code)
	}
}

// The accessor falls back to a non-nil OrgAuthorizer when none is injected,
// and returns the injected one otherwise.
func TestRBACAuthorizerAccessor(t *testing.T) {
	rh := &rbacHandler{orgStore: &staticOrgProvider{org: testRBACOrg()}}
	if _, ok := rh.authorizer().(*rbac.OrgAuthorizer); !ok {
		t.Errorf("nil authz should fall back to *rbac.OrgAuthorizer, got %T", rh.authorizer())
	}

	custom := fixedAuthz{allow: true}
	rh.authz = custom
	if got := rh.authorizer(); got != rbac.Authorizer(custom) {
		t.Errorf("injected authz should be returned, got %T", got)
	}
}
