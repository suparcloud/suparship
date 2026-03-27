package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/auth"
	"github.com/suparcloud/suparship/internal/session"
)

type fakeAuthenticator struct {
	username string
	password string
}

func (f *fakeAuthenticator) Authenticate(_ context.Context, username, password string) (*auth.Credentials, error) {
	if username == f.username && password == f.password {
		return &auth.Credentials{Username: username, PasswordHash: "fake-hash"}, nil
	}
	return nil, auth.ErrInvalidCredentials
}

func newTestAuthMux() (*http.ServeMux, *authHandler) {
	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "correct-pass"},
		sessions:      session.NewStore(time.Hour),
		cookieSecure:  false,
	}
	ah.registerRoutes(mux)
	return mux, ah
}

func TestLoginSuccess(t *testing.T) {
	mux, _ := newTestAuthMux()

	body := `{"username":"admin","password":"correct-pass"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp userResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Username != "admin" {
		t.Fatalf("expected username %q, got %q", "admin", resp.Username)
	}
	if resp.Role != roleOrgAdmin {
		t.Fatalf("expected role %q, got %q", roleOrgAdmin, resp.Role)
	}

	cookies := rec.Result().Cookies()
	var found bool
	for _, c := range cookies {
		if c.Name == sessionCookieName {
			found = true
			if !c.HttpOnly {
				t.Error("session cookie must be HttpOnly")
			}
			if c.SameSite != http.SameSiteLaxMode {
				t.Error("session cookie must be SameSite=Lax")
			}
		}
	}
	if !found {
		t.Fatal("session cookie not set on login")
	}
}

func TestLoginWrongPassword(t *testing.T) {
	mux, _ := newTestAuthMux()

	body := `{"username":"admin","password":"wrong"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestLoginWrongUsername(t *testing.T) {
	mux, _ := newTestAuthMux()

	body := `{"username":"nobody","password":"correct-pass"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestLoginMissingFields(t *testing.T) {
	mux, _ := newTestAuthMux()

	tests := []struct {
		name string
		body string
	}{
		{"empty body", `{}`},
		{"missing password", `{"username":"admin"}`},
		{"missing username", `{"password":"pass"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", rec.Code)
			}
		})
	}
}

func TestMeAuthenticated(t *testing.T) {
	mux, ah := newTestAuthMux()

	sess, err := ah.sessions.Create("admin", roleOrgAdmin)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp userResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Username != "admin" {
		t.Fatalf("expected username %q, got %q", "admin", resp.Username)
	}
	if resp.Role != roleOrgAdmin {
		t.Fatalf("expected role %q, got %q", roleOrgAdmin, resp.Role)
	}
}

func TestMeUnauthenticated(t *testing.T) {
	mux, _ := newTestAuthMux()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestMeInvalidSession(t *testing.T) {
	mux, _ := newTestAuthMux()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "bogus-session-id"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestLogout(t *testing.T) {
	mux, ah := newTestAuthMux()

	sess, err := ah.sessions.Create("admin", roleOrgAdmin)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}

	// Session should be deleted.
	if _, ok := ah.sessions.Get(sess.ID); ok {
		t.Fatal("session should be deleted after logout")
	}

	// Cookie should be expired.
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName && c.MaxAge >= 0 {
			t.Fatal("session cookie should have negative MaxAge after logout")
		}
	}
}

func TestLogoutWithoutSession(t *testing.T) {
	mux, _ := newTestAuthMux()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 even without session, got %d", rec.Code)
	}
}

func TestLoginThenMe(t *testing.T) {
	mux, _ := newTestAuthMux()

	loginBody := `{"username":"admin","password":"correct-pass"}`
	loginReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(loginBody))
	loginRec := httptest.NewRecorder()
	mux.ServeHTTP(loginRec, loginReq)

	if loginRec.Code != http.StatusOK {
		t.Fatalf("login: expected 200, got %d", loginRec.Code)
	}

	// Extract session cookie from login response.
	var sessionCookie *http.Cookie
	for _, c := range loginRec.Result().Cookies() {
		if c.Name == sessionCookieName {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatal("no session cookie after login")
	}

	// Use cookie to call /me.
	meReq := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	meReq.AddCookie(sessionCookie)
	meRec := httptest.NewRecorder()
	mux.ServeHTTP(meRec, meReq)

	if meRec.Code != http.StatusOK {
		t.Fatalf("me: expected 200, got %d: %s", meRec.Code, meRec.Body.String())
	}

	var resp userResponse
	if err := json.NewDecoder(meRec.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding me response: %v", err)
	}
	if resp.Username != "admin" {
		t.Fatalf("expected username %q, got %q", "admin", resp.Username)
	}
}
