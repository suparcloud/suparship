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

	"github.com/suparcloud/suparship/internal/registry"
	"github.com/suparcloud/suparship/internal/session"
)

func newRegistryMux(t *testing.T) (*http.ServeMux, *authHandler) {
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

	rgh := &registryHandler{
		store:  registry.NewStore(client),
		auth:   ah,
		logger: slog.Default(),
	}
	rgh.registerRoutes(mux)

	return mux, ah
}

func TestRegistryHandler_GetNotConfigured(t *testing.T) {
	mux, ah := newRegistryMux(t)

	req := httptest.NewRequest("GET", "/api/v1/registry/config", nil)
	req.AddCookie(sessionCookieFor(ah, "admin", "org_admin"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp registryConfigResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Configured {
		t.Error("expected configured=false")
	}
	if resp.Config.Enabled {
		t.Error("expected enabled=false")
	}
}

func TestRegistryHandler_PutAndGet(t *testing.T) {
	mux, ah := newRegistryMux(t)
	cookie := sessionCookieFor(ah, "admin", "org_admin")

	body := `{"enabled":true,"url":"ghcr.io","username":"robot","authSecretRef":"reg-creds","environments":["staging","prod"]}`
	putReq := httptest.NewRequest("PUT", "/api/v1/registry/config", bytes.NewBufferString(body))
	putReq.Header.Set("Content-Type", "application/json")
	putReq.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, putReq)

	if w.Code != http.StatusOK {
		t.Fatalf("PUT expected 200, got %d: %s", w.Code, w.Body.String())
	}

	getReq := httptest.NewRequest("GET", "/api/v1/registry/config", nil)
	getReq.AddCookie(cookie)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, getReq)

	var resp registryConfigResponse
	if err := json.NewDecoder(w2.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Configured {
		t.Error("expected configured=true")
	}
	if resp.Config.URL != "ghcr.io" {
		t.Errorf("url = %q, want ghcr.io", resp.Config.URL)
	}
	if len(resp.Config.Environments) != 2 {
		t.Errorf("environments = %d, want 2", len(resp.Config.Environments))
	}
}

func TestRegistryHandler_PutValidation(t *testing.T) {
	mux, ah := newRegistryMux(t)

	body := `{"enabled":true}`
	req := httptest.NewRequest("PUT", "/api/v1/registry/config", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "admin", "org_admin"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRegistryHandler_Unauthenticated(t *testing.T) {
	mux, _ := newRegistryMux(t)

	req := httptest.NewRequest("GET", "/api/v1/registry/config", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}
