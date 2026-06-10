package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/session"
)

func TestClaimString(t *testing.T) {
	claims := map[string]any{"email": "a@b.com", "n": 1}
	if got := claimString(claims, "email"); got != "a@b.com" {
		t.Errorf("email = %q, want a@b.com", got)
	}
	if got := claimString(claims, "n"); got != "" {
		t.Errorf("non-string claim should yield empty, got %q", got)
	}
	if got := claimString(claims, "missing"); got != "" {
		t.Errorf("missing claim should yield empty, got %q", got)
	}
}

func TestClaimStrings(t *testing.T) {
	cases := []struct {
		name   string
		claims map[string]any
		want   []string
	}{
		{"array of strings", map[string]any{"groups": []any{"a", "b"}}, []string{"a", "b"}},
		{"single string", map[string]any{"groups": "a"}, []string{"a"}},
		{"native []string", map[string]any{"groups": []string{"x"}}, []string{"x"}},
		{"missing", map[string]any{}, nil},
		{"drops non-strings", map[string]any{"groups": []any{"a", 1, "b"}}, []string{"a", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := claimStrings(tc.claims, "groups")
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestGroupAwareRBAC proves the middleware authorizes by IdP group claim: a
// session carrying a group bound to org_admin passes an org-admin route, while
// a session with an unbound group is rejected. This is what makes SSO logins
// (which carry no team membership) able to use RBAC.
func TestGroupAwareRBAC(t *testing.T) {
	org := &rbac.Org{
		Name: "test",
		RoleBindings: []rbac.RoleBinding{
			{Project: "*", Group: "eng-admins", Role: rbac.RoleOrgAdmin},
		},
	}

	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	ah.registerRoutes(mux)
	rh := &rbacHandler{auth: ah, orgStore: &staticOrgProvider{org: org}}
	rh.registerRoutes(mux)

	cookieFor := func(groups []string) *http.Cookie {
		sess, _ := ah.sessions.CreateWithGroups("ext@idp", "", groups)
		return &http.Cookie{Name: sessionCookieName, Value: sess.ID}
	}

	// Group bound to org_admin → allowed (POST /api/v1/teams is org_admin-only).
	rec := doJSON(mux, cookieFor([]string{"eng-admins"}), "POST", "/api/v1/teams",
		upsertTeamRequest{Name: "platform"})
	if rec.Code != http.StatusCreated {
		t.Errorf("group-bound org_admin should be allowed, got %d: %s", rec.Code, rec.Body.String())
	}

	// Unbound group → forbidden.
	rec = doJSON(mux, cookieFor([]string{"randos"}), "POST", "/api/v1/teams",
		upsertTeamRequest{Name: "platform2"})
	if rec.Code != http.StatusForbidden {
		t.Errorf("unbound group must be forbidden, got %d: %s", rec.Code, rec.Body.String())
	}
}
