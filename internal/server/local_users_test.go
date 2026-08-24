package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/auth"
	"github.com/suparcloud/suparship/internal/localuser"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/session"
)

// newLocalUsersTestMux wires an authHandler with a MemStore-backed local-user
// store chained after the fake admin credential, plus the rbacHandler admin
// endpoints — the full invite flow end to end.
func newLocalUsersTestMux() (*http.ServeMux, *authHandler, *staticOrgProvider, *localuser.MemStore) {
	mux := http.NewServeMux()
	users := localuser.NewMemStore("admin")
	// ONE org provider shared by auth (display-role lookup) and rbac (team
	// writes) — mirrors production wiring.
	store := &staticOrgProvider{org: testRBACOrg()}
	ah := &authHandler{
		authenticator: auth.Chain{
			&fakeAuthenticator{username: "admin", password: "pass"},
			localuser.AsAuthenticator(users),
		},
		sessions:    session.NewStore(time.Hour),
		localUsers:  users,
		orgProvider: store,
	}
	ah.registerRoutes(mux)
	rh := &rbacHandler{auth: ah, orgStore: store}
	rh.registerRoutes(mux)
	return mux, ah, store, users
}

func doUserJSON(mux *http.ServeMux, method, path string, cookie *http.Cookie, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// The full lifecycle: admin creates a user with a team → invite is valid →
// redeem sets the password AND a session → login works → the link is dead.
func TestLocalUserInviteFlow(t *testing.T) {
	mux, ah, store, _ := newLocalUsersTestMux()
	adminCookie := sessionCookieFor(ah, "alice", "org_admin")

	// Create with team membership.
	rec := doUserJSON(mux, "POST", "/api/v1/org/users", adminCookie, `{"username":"jane","teams":["admins"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create user: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var inv localUserInviteResponse
	if err := json.NewDecoder(rec.Body).Decode(&inv); err != nil {
		t.Fatalf("decode invite: %v", err)
	}
	if !strings.HasPrefix(inv.InviteToken, localuser.InvitePrefix) {
		t.Fatalf("invite token %q lacks prefix", inv.InviteToken)
	}
	found := false
	for _, team := range store.org.Teams {
		if team.Name == "admins" {
			for _, m := range team.Members {
				if m == "jane" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("jane should have been added to the admins team")
	}

	// Public greeting.
	rec = doUserJSON(mux, "GET", "/api/v1/auth/invite/"+inv.InviteToken, nil, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"valid":true`) {
		t.Fatalf("invite info: %d %s", rec.Code, rec.Body.String())
	}

	// Redeem: password set + logged in (session cookie + real display role).
	rec = doUserJSON(mux, "POST", "/api/v1/auth/invite/accept", nil,
		`{"token":"`+inv.InviteToken+`","password":"jane-pass-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("accept: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(rec.Result().Cookies()) == 0 {
		t.Fatal("accept should set a session cookie")
	}
	var me userResponse
	_ = json.NewDecoder(rec.Body).Decode(&me)
	if me.Username != "jane" || me.Role != "org_admin" { // admins team → org_admin binding
		t.Errorf("accept response = %+v, want jane/org_admin via team binding", me)
	}

	// Password login now works through the chain.
	rec = doUserJSON(mux, "POST", "/api/v1/auth/login", nil, `{"username":"jane","password":"jane-pass-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login after invite: %d %s", rec.Code, rec.Body.String())
	}

	// SINGLE-USE: the link is dead.
	rec = doUserJSON(mux, "GET", "/api/v1/auth/invite/"+inv.InviteToken, nil, "")
	if !strings.Contains(rec.Body.String(), `"valid":false`) {
		t.Fatalf("spent invite should be invalid: %s", rec.Body.String())
	}
	rec = doUserJSON(mux, "POST", "/api/v1/auth/invite/accept", nil,
		`{"token":"`+inv.InviteToken+`","password":"other-pass-2"}`)
	if rec.Code != http.StatusGone {
		t.Fatalf("second accept: expected 410, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestLocalUserEndpointsRequireOrgAdmin(t *testing.T) {
	mux, ah, _, _ := newLocalUsersTestMux()
	devCookie := sessionCookieFor(ah, "bob", "developer")

	if rec := doUserJSON(mux, "POST", "/api/v1/org/users", devCookie, `{"username":"x"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("create as developer: expected 403, got %d", rec.Code)
	}
	if rec := doUserJSON(mux, "GET", "/api/v1/org/users", devCookie, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("list as developer: expected 403, got %d", rec.Code)
	}
}

func TestLocalUserDeleteStripsTeamsAndAuth(t *testing.T) {
	mux, ah, store, _ := newLocalUsersTestMux()
	adminCookie := sessionCookieFor(ah, "alice", "org_admin")

	rec := doUserJSON(mux, "POST", "/api/v1/org/users", adminCookie, `{"username":"jane","teams":["admins"]}`)
	var inv localUserInviteResponse
	_ = json.NewDecoder(rec.Body).Decode(&inv)
	_ = doUserJSON(mux, "POST", "/api/v1/auth/invite/accept", nil,
		`{"token":"`+inv.InviteToken+`","password":"jane-pass-1"}`)

	if rec := doUserJSON(mux, "DELETE", "/api/v1/org/users/jane", adminCookie, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	for _, team := range store.org.Teams {
		for _, m := range team.Members {
			if m == "jane" {
				t.Fatal("jane should be stripped from teams on delete")
			}
		}
	}
	if rec := doUserJSON(mux, "POST", "/api/v1/auth/login", nil, `{"username":"jane","password":"jane-pass-1"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("deleted user login: expected 401, got %d", rec.Code)
	}
}

// A local user in a developer-bound team must see their real display role —
// the old hardcoded org_admin would be a lie the middleware then contradicts.
func TestLocalLoginDisplayRoleFromTeams(t *testing.T) {
	mux, ah, store, users := newLocalUsersTestMux()
	_ = users // direct store use below via API instead
	adminCookie := sessionCookieFor(ah, "alice", "org_admin")

	// Bind a viewers team with the developer role and put the user in it.
	// testRBACOrg already binds team "devs" to developer on project "api";
	// add a wildcard developer binding so the display role resolves on "*".
	store.org.RoleBindings = append(store.org.RoleBindings,
		rbac.RoleBinding{Project: "*", Team: "devs", Role: rbac.RoleDeveloper})

	rec := doUserJSON(mux, "POST", "/api/v1/org/users", adminCookie, `{"username":"bob-dev","teams":["devs"]}`)
	var inv localUserInviteResponse
	_ = json.NewDecoder(rec.Body).Decode(&inv)
	rec = doUserJSON(mux, "POST", "/api/v1/auth/invite/accept", nil,
		`{"token":"`+inv.InviteToken+`","password":"bob-pass-99"}`)
	var me userResponse
	_ = json.NewDecoder(rec.Body).Decode(&me)
	if me.Role != "developer" {
		t.Fatalf("display role = %q, want developer (from team binding)", me.Role)
	}

	rec = doUserJSON(mux, "POST", "/api/v1/auth/login", nil, `{"username":"bob-dev","password":"bob-pass-99"}`)
	_ = json.NewDecoder(rec.Body).Decode(&me)
	if me.Role != "developer" {
		t.Fatalf("login display role = %q, want developer", me.Role)
	}
}
