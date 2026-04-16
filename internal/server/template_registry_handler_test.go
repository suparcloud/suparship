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

	"github.com/suparcloud/suparship/internal/session"
	"github.com/suparcloud/suparship/internal/tpl"
)

func newTemplateRegistryMux(t *testing.T) (*http.ServeMux, *authHandler) {
	t.Helper()
	client := kubefake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "suparship-system"}},
	)

	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
		cookieSecure:  false,
	}
	ah.registerRoutes(mux)

	trh := &templateRegistryHandler{
		store:  tpl.NewRegistryStore(client),
		auth:   ah,
		logger: slog.Default(),
	}
	trh.registerRoutes(mux)

	return mux, ah
}

func TestTemplateRegistryHandler_GetEmpty(t *testing.T) {
	mux, ah := newTemplateRegistryMux(t)

	req := httptest.NewRequest("GET", "/api/v1/templates/registry", nil)
	req.AddCookie(sessionCookieFor(ah, "admin", "org_admin"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp templateRegistryResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Configured {
		t.Error("expected configured=false for fresh store")
	}
}

func TestTemplateRegistryHandler_PutAndGet(t *testing.T) {
	mux, ah := newTemplateRegistryMux(t)
	cookie := sessionCookieFor(ah, "admin", "org_admin")

	body := `{
		"builtIn": ["web-service", "color-app"],
		"sources": [
			{"name": "web-service", "origin": "builtin", "version": "1.0.0"},
			{"name": "color-app", "origin": "builtin", "version": "1.0.0"}
		]
	}`

	putReq := httptest.NewRequest("PUT", "/api/v1/templates/registry", bytes.NewBufferString(body))
	putReq.Header.Set("Content-Type", "application/json")
	putReq.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, putReq)

	if w.Code != http.StatusOK {
		t.Fatalf("PUT expected 200, got %d: %s", w.Code, w.Body.String())
	}

	getReq := httptest.NewRequest("GET", "/api/v1/templates/registry", nil)
	getReq.AddCookie(cookie)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, getReq)

	var resp templateRegistryResponse
	if err := json.NewDecoder(w2.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Configured {
		t.Error("expected configured=true after PUT")
	}
	if len(resp.Registry.Sources) != 2 {
		t.Errorf("sources count = %d, want 2", len(resp.Registry.Sources))
	}
}

func TestTemplateRegistryHandler_ListSources(t *testing.T) {
	mux, ah := newTemplateRegistryMux(t)
	cookie := sessionCookieFor(ah, "admin", "org_admin")

	body := `{
		"builtIn": ["web-service"],
		"sources": [
			{"name": "web-service", "origin": "builtin", "version": "1.0.0"}
		]
	}`
	putReq := httptest.NewRequest("PUT", "/api/v1/templates/registry", bytes.NewBufferString(body))
	putReq.Header.Set("Content-Type", "application/json")
	putReq.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, putReq)

	if w.Code != http.StatusOK {
		t.Fatalf("PUT setup failed: %d", w.Code)
	}

	getReq := httptest.NewRequest("GET", "/api/v1/templates/sources", nil)
	getReq.AddCookie(cookie)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, getReq)

	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w2.Code)
	}

	var resp templateSourcesResponse
	if err := json.NewDecoder(w2.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Sources) != 1 {
		t.Errorf("sources count = %d, want 1", len(resp.Sources))
	}
	if resp.Sources[0].Name != "web-service" {
		t.Errorf("source name = %q, want web-service", resp.Sources[0].Name)
	}
}

func TestTemplateRegistryHandler_Unauthenticated(t *testing.T) {
	mux, _ := newTemplateRegistryMux(t)

	req := httptest.NewRequest("GET", "/api/v1/templates/registry", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}
