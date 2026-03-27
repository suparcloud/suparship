package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func stubHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestCORSAllowedOrigin(t *testing.T) {
	h := corsMiddleware([]string{"http://localhost:5173"}, stubHandler())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("expected Allow-Origin %q, got %q", "http://localhost:5173", got)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Fatalf("expected Vary Origin, got %q", got)
	}
}

func TestCORSDisallowedOrigin(t *testing.T) {
	h := corsMiddleware([]string{"http://localhost:5173"}, stubHandler())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil)
	req.Header.Set("Origin", "http://evil.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected no Allow-Origin header, got %q", got)
	}
}

func TestCORSNoOriginHeader(t *testing.T) {
	h := corsMiddleware([]string{"http://localhost:5173"}, stubHandler())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected no Allow-Origin header, got %q", got)
	}
}

func TestCORSPreflight(t *testing.T) {
	h := corsMiddleware([]string{"http://localhost:5173"}, stubHandler())

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/meta", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Fatal("expected Allow-Methods header on preflight")
	}
}

func TestCORSPreflightDisallowedOrigin(t *testing.T) {
	h := corsMiddleware([]string{"http://localhost:5173"}, stubHandler())

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/meta", nil)
	req.Header.Set("Origin", "http://evil.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected no Allow-Origin for disallowed preflight, got %q", got)
	}
}
