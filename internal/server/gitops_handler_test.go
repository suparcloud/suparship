package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/suparcloud/suparship/internal/envconfig"
	"github.com/suparcloud/suparship/internal/gitops"
	"github.com/suparcloud/suparship/internal/session"
)

func newGitopsMux(t *testing.T) (*http.ServeMux, *authHandler) {
	t.Helper()
	client := kubefake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: envconfig.SystemNamespace}},
	)

	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
		cookieSecure:  false,
	}
	ah.registerRoutes(mux)

	gh := &gitopsHandler{
		store:  gitops.NewConfigStore(client),
		auth:   ah,
		logger: slog.Default(),
	}
	gh.registerRoutes(mux)

	return mux, ah
}

func TestGitopsHandler_GetConfig_NotConfigured(t *testing.T) {
	mux, ah := newGitopsMux(t)

	req := httptest.NewRequest("GET", "/api/v1/gitops/config", nil)
	req.AddCookie(sessionCookieFor(ah, "admin", "org_admin"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp gitopsConfigResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Configured {
		t.Error("expected configured=false for fresh store")
	}
	if resp.CredentialsSet {
		t.Error("expected credentialsSet=false for fresh store")
	}
}

func TestGitopsHandler_PutAndGet(t *testing.T) {
	mux, ah := newGitopsMux(t)
	cookie := sessionCookieFor(ah, "admin", "org_admin")

	body := `{"provider":"github","repoURL":"https://github.com/org/repo","branch":"main"}`
	putReq := httptest.NewRequest("PUT", "/api/v1/gitops/config", bytes.NewBufferString(body))
	putReq.Header.Set("Content-Type", "application/json")
	putReq.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, putReq)

	if w.Code != http.StatusOK {
		t.Fatalf("PUT expected 200, got %d: %s", w.Code, w.Body.String())
	}

	getReq := httptest.NewRequest("GET", "/api/v1/gitops/config", nil)
	getReq.AddCookie(cookie)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, getReq)

	if w2.Code != http.StatusOK {
		t.Fatalf("GET expected 200, got %d", w2.Code)
	}

	var resp gitopsConfigResponse
	if err := json.NewDecoder(w2.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Configured {
		t.Error("expected configured=true after PUT")
	}
	if resp.Config.Provider != "github" {
		t.Errorf("provider = %q, want github", resp.Config.Provider)
	}
	if resp.Config.RepoURL != "https://github.com/org/repo" {
		t.Errorf("repoURL = %q", resp.Config.RepoURL)
	}
}

func TestGitopsHandler_PutWithCredentials(t *testing.T) {
	mux, ah := newGitopsMux(t)
	cookie := sessionCookieFor(ah, "admin", "org_admin")

	body := `{
		"provider":"github",
		"repoURL":"https://github.com/org/repo",
		"branch":"main",
		"credentials":{"token":"ghp_testtoken"}
	}`
	putReq := httptest.NewRequest("PUT", "/api/v1/gitops/config", bytes.NewBufferString(body))
	putReq.Header.Set("Content-Type", "application/json")
	putReq.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, putReq)

	if w.Code != http.StatusOK {
		t.Fatalf("PUT expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var putResp gitopsConfigResponse
	if err := json.NewDecoder(w.Body).Decode(&putResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !putResp.CredentialsSet {
		t.Error("expected credentialsSet=true after PUT with token")
	}
	// authSecretRef should be set to the managed secret name automatically.
	if putResp.Config == nil {
		t.Fatal("expected config in response")
	}
	if putResp.Config.AuthSecretRef != gitops.ManagedCredentialSecretName {
		t.Errorf("authSecretRef = %q, want %q", putResp.Config.AuthSecretRef, gitops.ManagedCredentialSecretName)
	}

	// GET should now report credentialsSet=true.
	getReq := httptest.NewRequest("GET", "/api/v1/gitops/config", nil)
	getReq.AddCookie(cookie)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, getReq)

	var getResp gitopsConfigResponse
	if err := json.NewDecoder(w2.Body).Decode(&getResp); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if !getResp.CredentialsSet {
		t.Error("GET should report credentialsSet=true after credentials were stored")
	}
}

func TestGitopsHandler_PutWithBitbucketCredentials(t *testing.T) {
	mux, ah := newGitopsMux(t)
	cookie := sessionCookieFor(ah, "admin", "org_admin")

	body := `{
		"provider":"bitbucket",
		"repoURL":"https://bitbucket.org/org/repo.git",
		"branch":"main",
		"credentials":{"username":"myuser","password":"ATBBmyapppassword"}
	}`
	req := httptest.NewRequest("PUT", "/api/v1/gitops/config", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp gitopsConfigResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.CredentialsSet {
		t.Error("expected credentialsSet=true after saving bitbucket credentials")
	}
}

func TestGitopsHandler_PutNoCredentials_CredentialsSetFalse(t *testing.T) {
	mux, ah := newGitopsMux(t)
	cookie := sessionCookieFor(ah, "admin", "org_admin")

	body := `{"provider":"github","repoURL":"https://github.com/org/repo","branch":"main"}`
	req := httptest.NewRequest("PUT", "/api/v1/gitops/config", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp gitopsConfigResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.CredentialsSet {
		t.Error("expected credentialsSet=false when no credentials submitted")
	}
}

func TestGitopsHandler_PutValidation(t *testing.T) {
	mux, ah := newGitopsMux(t)

	body := `{"provider":"github"}`
	req := httptest.NewRequest("PUT", "/api/v1/gitops/config", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "admin", "org_admin"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGitopsHandler_Unauthenticated(t *testing.T) {
	mux, _ := newGitopsMux(t)

	req := httptest.NewRequest("GET", "/api/v1/gitops/config", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without cookie, got %d", w.Code)
	}
}

func TestInjectCredentials(t *testing.T) {
	tests := []struct {
		url, user, pass, want string
	}{
		{
			"https://github.com/org/repo.git", "x-access-token", "ghp_abc",
			"https://x-access-token:ghp_abc@github.com/org/repo.git",
		},
		{
			"http://notssl.com/repo", "user", "pass",
			"http://notssl.com/repo",
		},
	}
	for _, tt := range tests {
		got := injectCredentials(tt.url, tt.user, tt.pass)
		if got != tt.want {
			t.Errorf("injectCredentials(%q, %q, %q) = %q, want %q", tt.url, tt.user, tt.pass, got, tt.want)
		}
	}
}

func TestSanitizeGitError(t *testing.T) {
	input := "fatal: could not read from https://x-access-token:ghp_secret@github.com/org/repo.git"
	got := sanitizeGitError(input)
	if got == input {
		t.Error("expected credentials to be sanitized")
	}
}
