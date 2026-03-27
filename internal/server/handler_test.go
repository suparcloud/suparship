package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/suparcloud/suparship/internal/version"
)

func newTestMux() *http.ServeMux {
	mux := http.NewServeMux()
	registerRoutes(mux)
	return mux
}

func TestHandleHealthz(t *testing.T) {
	mux := newTestMux()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if body := rec.Body.String(); body != "ok" {
		t.Fatalf("expected body %q, got %q", "ok", body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("expected Content-Type text/plain, got %q", ct)
	}
}

func TestHandleReadyz(t *testing.T) {
	mux := newTestMux()

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if body := rec.Body.String(); body != "ok" {
		t.Fatalf("expected body %q, got %q", "ok", body)
	}
}

func TestHandleMeta(t *testing.T) {
	mux := newTestMux()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", ct)
	}

	var resp MetaResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if resp.App != "suparship" {
		t.Errorf("expected app %q, got %q", "suparship", resp.App)
	}
	if resp.Version != version.Version {
		t.Errorf("expected version %q, got %q", version.Version, resp.Version)
	}
	if resp.Commit != version.Commit {
		t.Errorf("expected commit %q, got %q", version.Commit, resp.Commit)
	}
	if resp.BuildDate != version.Date {
		t.Errorf("expected buildDate %q, got %q", version.Date, resp.BuildDate)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	mux := newTestMux()

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"POST /healthz", http.MethodPost, "/healthz"},
		{"PUT /readyz", http.MethodPut, "/readyz"},
		{"DELETE /api/v1/meta", http.MethodDelete, "/api/v1/meta"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code == http.StatusOK {
				t.Fatalf("expected non-200 for %s %s, got 200", tt.method, tt.path)
			}
		})
	}
}
