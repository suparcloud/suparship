package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListOrgRoutingProfiles_Empty(t *testing.T) {
	mux, ah := newTestRBACMux()
	req := httptest.NewRequest("GET", "/api/v1/org/routing-profiles", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		RoutingProfiles []RoutingProfileDTO `json:"routingProfiles"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.RoutingProfiles) != 0 {
		t.Errorf("expected empty list, got %d entries", len(resp.RoutingProfiles))
	}
}

func TestPutOrgRoutingProfile_Upsert(t *testing.T) {
	mux, ah := newTestRBACMux()

	body := `{"ingressClassName":"nginx","clusterIssuer":"letsencrypt-prod"}`
	req := httptest.NewRequest("PUT", "/api/v1/org/routing-profiles/external", strings.NewReader(body))
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Read back via GET.
	req = httptest.NewRequest("GET", "/api/v1/org/routing-profiles", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d", rec.Code)
	}
	var resp struct {
		RoutingProfiles []RoutingProfileDTO `json:"routingProfiles"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.RoutingProfiles) != 1 || resp.RoutingProfiles[0].Name != "external" {
		t.Fatalf("expected one external profile, got %+v", resp.RoutingProfiles)
	}
	if resp.RoutingProfiles[0].ClusterIssuer != "letsencrypt-prod" {
		t.Errorf("ClusterIssuer = %q, want letsencrypt-prod", resp.RoutingProfiles[0].ClusterIssuer)
	}
}

func TestPutOrgRoutingProfile_RejectsInvalidName(t *testing.T) {
	mux, ah := newTestRBACMux()

	body := `{"ingressClassName":"nginx"}`
	req := httptest.NewRequest("PUT", "/api/v1/org/routing-profiles/disabled", strings.NewReader(body))
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for name=disabled, got %d", rec.Code)
	}

	req = httptest.NewRequest("PUT", "/api/v1/org/routing-profiles/public", strings.NewReader(body))
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for name=public, got %d", rec.Code)
	}
}

func TestPutOrgRoutingProfile_RejectsEmptyClassName(t *testing.T) {
	mux, ah := newTestRBACMux()

	body := `{"clusterIssuer":"letsencrypt-prod"}`
	req := httptest.NewRequest("PUT", "/api/v1/org/routing-profiles/external", strings.NewReader(body))
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty ingressClassName, got %d (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestPutOrgRoutingProfile_RequiresOrgAdmin(t *testing.T) {
	mux, ah := newTestRBACMux()

	body := `{"ingressClassName":"nginx"}`
	req := httptest.NewRequest("PUT", "/api/v1/org/routing-profiles/external", strings.NewReader(body))
	req.AddCookie(sessionCookieFor(ah, "carol", "viewer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for viewer, got %d", rec.Code)
	}
}

func TestDeleteOrgRoutingProfile_Idempotent(t *testing.T) {
	mux, ah := newTestRBACMux()

	// Delete a non-existent profile — 204 (idempotent).
	req := httptest.NewRequest("DELETE", "/api/v1/org/routing-profiles/external", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for missing profile, got %d", rec.Code)
	}
}
