package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/session"
	"github.com/suparcloud/suparship/internal/token"
)

// newTokenMux wires a mux with the token handler and bearer auth, seeded with
// project "api" (matching testRBACOrg: alice=org_admin, bob=developer,
// carol=viewer). It returns the mux, the authHandler, and the token store so
// tests can mint tokens directly when needed.
func newTokenMux(t *testing.T) (*http.ServeMux, *authHandler, token.Store) {
	t.Helper()
	mux := http.NewServeMux()

	tokStore := token.NewMemStore()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
		tokenStore:    tokStore,
	}
	ah.registerRoutes(mux)

	projStore := newMemProjectStore()
	_ = projStore.Save(context.Background(), &project.Project{
		APIVersion: project.CurrentAPIVersion,
		Kind:       project.ProjectKind,
		Metadata:   project.ProjectMeta{Name: "api"},
	})

	orgProv := &staticOrgProvider{org: testRBACOrg()}
	rh := &rbacHandler{
		auth:         ah,
		orgStore:     orgProv,
		projectStore: projStore,
		tokenHandler: newTokenHandler(tokStore, projStore),
	}
	rh.registerRoutes(mux)

	return mux, ah, tokStore
}

func postTokenJSON(mux *http.ServeMux, cookie *http.Cookie, projectName string, body any) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/projects/"+projectName+"/tokens", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// --- Creation + validation ---

func TestCreateTokenDefaultsToDeveloper(t *testing.T) {
	mux, ah, _ := newTokenMux(t)
	rec := postTokenJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "api", map[string]any{"name": "ci"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	var resp createTokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Role != "developer" {
		t.Errorf("role = %q, want developer", resp.Role)
	}
	if resp.ExpiresAt != nil {
		t.Errorf("ExpiresAt = %v, want nil (never)", resp.ExpiresAt)
	}
	if len(resp.Token) == 0 || resp.Token[:6] != token.Prefix {
		t.Errorf("token %q missing %q prefix", resp.Token, token.Prefix)
	}
	if resp.CreatedBy != "alice" {
		t.Errorf("createdBy = %q, want alice", resp.CreatedBy)
	}
}

func TestCreateTokenWithExpiry(t *testing.T) {
	mux, ah, _ := newTokenMux(t)
	rec := postTokenJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "api",
		map[string]any{"name": "ci", "role": "viewer", "expiresInDays": 30})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	var resp createTokenResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Role != "viewer" {
		t.Errorf("role = %q, want viewer", resp.Role)
	}
	if resp.ExpiresAt == nil {
		t.Fatal("ExpiresAt = nil, want ~30 days out")
	}
}

func TestCreateTokenValidation(t *testing.T) {
	mux, ah, _ := newTokenMux(t)
	admin := func() *http.Cookie { return sessionCookieFor(ah, "alice", "org_admin") }

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"missing name", map[string]any{"role": "developer"}, http.StatusBadRequest},
		{"invalid role", map[string]any{"name": "x", "role": "superuser"}, http.StatusBadRequest},
		{"org_admin role rejected", map[string]any{"name": "x", "role": "org_admin"}, http.StatusBadRequest},
		{"negative expiry", map[string]any{"name": "x", "expiresInDays": -1}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postTokenJSON(mux, admin(), "api", tc.body)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d (%s)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestCreateTokenUnknownProject(t *testing.T) {
	mux, ah, _ := newTokenMux(t)
	// alice is org_admin (via "*"), so passes the project_admin gate; the handler
	// then 404s on the missing project.
	rec := postTokenJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "ghost", map[string]any{"name": "x"})
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}

// --- RBAC on the management routes (project_admin only) ---

func TestCreateTokenRequiresProjectAdmin(t *testing.T) {
	mux, ah, _ := newTokenMux(t)
	// bob is only a developer on "api".
	rec := postTokenJSON(mux, sessionCookieFor(ah, "bob", "developer"), "api", map[string]any{"name": "x"})
	if rec.Code != http.StatusForbidden {
		t.Errorf("developer minting token: status = %d, want 403", rec.Code)
	}
}

// --- List + delete ---

func TestListAndDeleteToken(t *testing.T) {
	mux, ah, _ := newTokenMux(t)
	admin := sessionCookieFor(ah, "alice", "org_admin")

	create := postTokenJSON(mux, admin, "api", map[string]any{"name": "ci"})
	var created createTokenResponse
	_ = json.Unmarshal(create.Body.Bytes(), &created)

	// List shows it (without the secret).
	listReq := httptest.NewRequest("GET", "/api/v1/projects/api/tokens", nil)
	listReq.AddCookie(admin)
	listRec := httptest.NewRecorder()
	mux.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", listRec.Code)
	}
	var list tokensResponse
	_ = json.Unmarshal(listRec.Body.Bytes(), &list)
	if len(list.Tokens) != 1 || list.Tokens[0].ID != created.ID {
		t.Fatalf("list = %+v, want one token id %s", list.Tokens, created.ID)
	}

	// Delete it.
	delReq := httptest.NewRequest("DELETE", "/api/v1/projects/api/tokens/"+created.ID, nil)
	delReq.AddCookie(admin)
	delRec := httptest.NewRecorder()
	mux.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204 (%s)", delRec.Code, delRec.Body.String())
	}

	// Second delete → 404.
	delReq2 := httptest.NewRequest("DELETE", "/api/v1/projects/api/tokens/"+created.ID, nil)
	delReq2.AddCookie(admin)
	delRec2 := httptest.NewRecorder()
	mux.ServeHTTP(delRec2, delReq2)
	if delRec2.Code != http.StatusNotFound {
		t.Errorf("second delete status = %d, want 404", delRec2.Code)
	}
}

// --- Bearer authentication + authorization ---

// bearerReq issues a request authenticated by an API token.
func bearerReq(mux *http.ServeMux, method, path, rawToken string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+rawToken)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestBearerTokenAuthorizesAtItsRole(t *testing.T) {
	mux, _, store := newTokenMux(t)
	// A project_admin token on "api" can hit the project_admin-gated list route.
	raw, _, err := store.Issue(context.Background(), token.Metadata{
		Name: "admin-tok", Project: "api", Role: "project_admin", CreatedBy: "alice",
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	rec := bearerReq(mux, "GET", "/api/v1/projects/api/tokens", raw)
	if rec.Code != http.StatusOK {
		t.Errorf("project_admin token listing: status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
}

func TestBearerTokenBelowRequiredRoleDenied(t *testing.T) {
	mux, _, store := newTokenMux(t)
	// A developer token cannot reach the project_admin-gated list route.
	raw, _, _ := store.Issue(context.Background(), token.Metadata{
		Name: "dev-tok", Project: "api", Role: "developer", CreatedBy: "alice",
	})
	rec := bearerReq(mux, "GET", "/api/v1/projects/api/tokens", raw)
	if rec.Code != http.StatusForbidden {
		t.Errorf("developer token on admin route: status = %d, want 403", rec.Code)
	}
}

func TestBearerTokenWrongProjectDenied(t *testing.T) {
	mux, _, store := newTokenMux(t)
	// A token scoped to "other" must not authorize "api" routes even at admin role.
	raw, _, _ := store.Issue(context.Background(), token.Metadata{
		Name: "x", Project: "other", Role: "project_admin", CreatedBy: "alice",
	})
	rec := bearerReq(mux, "GET", "/api/v1/projects/api/tokens", raw)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-project token: status = %d, want 403", rec.Code)
	}
}

func TestBearerTokenNeverOrgAdmin(t *testing.T) {
	mux, _, store := newTokenMux(t)
	// Token minted by alice, who IS an org admin — but a project token must never
	// inherit that. The org-admin-gated teams route must reject it.
	raw, _, _ := store.Issue(context.Background(), token.Metadata{
		Name: "x", Project: "api", Role: "project_admin", CreatedBy: "alice",
	})
	rec := bearerReq(mux, "GET", "/api/v1/teams", raw)
	if rec.Code != http.StatusForbidden {
		t.Errorf("project token on org-admin route: status = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
}

func TestBearerTokenInvalidRejected(t *testing.T) {
	mux, _, _ := newTokenMux(t)
	rec := bearerReq(mux, "GET", "/api/v1/projects/api/tokens", "supat_garbage")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("invalid bearer: status = %d, want 401", rec.Code)
	}
}

func TestBearerTokenExpiredRejected(t *testing.T) {
	mux, _, store := newTokenMux(t)
	past := time.Now().Add(-time.Hour)
	raw, _, _ := store.Issue(context.Background(), token.Metadata{
		Name: "old", Project: "api", Role: "project_admin", CreatedBy: "alice", ExpiresAt: &past,
	})
	rec := bearerReq(mux, "GET", "/api/v1/projects/api/tokens", raw)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expired bearer: status = %d, want 401", rec.Code)
	}
}
