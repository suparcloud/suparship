package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestListOrgAddonProfiles_Empty(t *testing.T) {
	mux, ah := newTestRBACMux()
	req := httptest.NewRequest("GET", "/api/v1/org/addon-profiles", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		AddonProfiles  []AddonProfileDTO `json:"addonProfiles"`
		AvailableTypes []string          `json:"availableTypes"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.AddonProfiles) != 0 {
		t.Errorf("expected empty list, got %d entries", len(resp.AddonProfiles))
	}
	// availableTypes documents the closed set of registered contracts.
	for _, want := range []string{"redis", "postgres"} {
		if !slices.Contains(resp.AvailableTypes, want) {
			t.Errorf("availableTypes missing %q (got %v)", want, resp.AvailableTypes)
		}
	}
}

func TestPutOrgAddonProfile_Upsert(t *testing.T) {
	mux, ah := newTestRBACMux()

	body := `{"provider":"valkey-operator","chart":"valkey","defaults":{"persistence":true}}`
	req := httptest.NewRequest("PUT", "/api/v1/org/addon-profiles/redis", strings.NewReader(body))
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Read back via GET.
	req = httptest.NewRequest("GET", "/api/v1/org/addon-profiles", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d", rec.Code)
	}
	var resp struct {
		AddonProfiles []AddonProfileDTO `json:"addonProfiles"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.AddonProfiles) != 1 {
		t.Fatalf("expected one profile, got %+v", resp.AddonProfiles)
	}
	got := resp.AddonProfiles[0]
	if got.Type != "redis" || got.Provider != "valkey-operator" || got.Chart != "valkey" {
		t.Errorf("DTO = %+v, want type=redis provider=valkey-operator chart=valkey", got)
	}
	if got.Defaults["persistence"] != true {
		t.Errorf("Defaults round-trip lost persistence=true: %+v", got.Defaults)
	}
}

func TestPutOrgAddonProfile_RejectsUnknownType(t *testing.T) {
	mux, ah := newTestRBACMux()

	body := `{"provider":"strimzi","chart":"kafka"}`
	req := httptest.NewRequest("PUT", "/api/v1/org/addon-profiles/kafka", strings.NewReader(body))
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unregistered type, got %d (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestPutOrgAddonProfile_RejectsEmptyProvider(t *testing.T) {
	mux, ah := newTestRBACMux()

	body := `{"chart":"valkey"}`
	req := httptest.NewRequest("PUT", "/api/v1/org/addon-profiles/redis", strings.NewReader(body))
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty provider, got %d", rec.Code)
	}
}

func TestPutOrgAddonProfile_RejectsEmptyChart(t *testing.T) {
	mux, ah := newTestRBACMux()

	body := `{"provider":"valkey-operator"}`
	req := httptest.NewRequest("PUT", "/api/v1/org/addon-profiles/redis", strings.NewReader(body))
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty chart, got %d", rec.Code)
	}
}

func TestPutOrgAddonProfile_RequiresOrgAdmin(t *testing.T) {
	mux, ah := newTestRBACMux()

	body := `{"provider":"valkey-operator","chart":"valkey"}`
	req := httptest.NewRequest("PUT", "/api/v1/org/addon-profiles/redis", strings.NewReader(body))
	req.AddCookie(sessionCookieFor(ah, "carol", "viewer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for viewer, got %d", rec.Code)
	}
}

func TestDeleteOrgAddonProfile_Idempotent(t *testing.T) {
	mux, ah := newTestRBACMux()

	req := httptest.NewRequest("DELETE", "/api/v1/org/addon-profiles/redis", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for missing profile, got %d", rec.Code)
	}
}
