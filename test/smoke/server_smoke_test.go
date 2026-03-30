// Package smoke contains lightweight end-to-end smoke tests for the suparship
// server. They exercise the fully assembled server backed by
// fake.NewDevServerDeps() — the same configuration used by `task dev`.
//
// No running cluster or external service is required; net/http/httptest is used
// throughout.
//
// Run all smoke tests:
//
//	go test ./test/smoke/...
//	make test-smoke
//
// Run a specific test:
//
//	go test ./test/smoke/... -run TestSmoke_Login -v
package smoke

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/suparcloud/suparship/internal/fake"
	"github.com/suparcloud/suparship/internal/server"
)

// sessionCookieName mirrors the constant in internal/server/auth.go.
// It is part of the public HTTP contract so the literal is intentional.
const sessionCookieName = "suparship_session"

// newTestServer returns an httptest.Server wired with fake.NewDevServerDeps(),
// the same configuration `task dev` uses. The server is closed automatically
// when the test ends.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	deps := fake.NewDevServerDeps()
	srv := server.New(server.Config{
		Addr:            ":0",
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		Authenticator:   deps.Authenticator,
		OrgProvider:     deps.OrgProvider,
		ProjectStore:    deps.ProjectStore,
		PreviewStore:    deps.PreviewStore,
		RuntimeProvider: deps.RuntimeProvider,
		LogsProvider:    deps.LogsProvider,
		CookieSecure:    false,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// sessionLogin posts the default fake credentials and returns the session
// cookie. It fails the test immediately if the login request is unsuccessful.
func sessionLogin(t *testing.T, ts *httptest.Server, username, password string) *http.Cookie {
	t.Helper()
	body := `{"username":"` + username + `","password":"` + password + `"}`
	resp, err := ts.Client().Post(
		ts.URL+"/api/v1/auth/login",
		"application/json",
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatalf("login request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: want 200, got %d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}
	t.Fatal("login: no session cookie in response")
	return nil
}

// TestSmoke_Login verifies the fully assembled server accepts the default dev
// credentials, returns the authenticated user in the body, and sets a valid
// HttpOnly session cookie.
func TestSmoke_Login(t *testing.T) {
	ts := newTestServer(t)

	body := `{"username":"` + fake.FakeAdminUsername + `","password":"` + fake.FakeAdminPassword + `"}`
	resp, err := ts.Client().Post(
		ts.URL+"/api/v1/auth/login",
		"application/json",
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	var me struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if me.Username != fake.FakeAdminUsername {
		t.Errorf("username: want %q, got %q", fake.FakeAdminUsername, me.Username)
	}

	var found bool
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName {
			found = true
			if !c.HttpOnly {
				t.Error("session cookie must be HttpOnly")
			}
		}
	}
	if !found {
		t.Fatal("no session cookie in login response")
	}
}

// TestSmoke_DashboardData verifies that after a successful login,
// GET /api/v1/projects returns the seeded demo project that the dashboard
// displays. This exercises the full RBAC middleware → project store path.
func TestSmoke_DashboardData(t *testing.T) {
	ts := newTestServer(t)
	cookie := sessionLogin(t, ts, fake.FakeAdminUsername, fake.FakeAdminPassword)

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/projects", nil)
	if err != nil {
		t.Fatalf("creating request: %v", err)
	}
	req.AddCookie(cookie)

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	var result struct {
		Projects []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"projects"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if len(result.Projects) == 0 {
		t.Fatal("want at least one seeded project, got none")
	}

	var found bool
	for _, p := range result.Projects {
		if p.Name == "demo" {
			found = true
			if p.DisplayName != "Demo Project" {
				t.Errorf("displayName: want %q, got %q", "Demo Project", p.DisplayName)
			}
			break
		}
	}
	if !found {
		t.Fatalf("seeded 'demo' project not found in response; got %+v", result.Projects)
	}
}
