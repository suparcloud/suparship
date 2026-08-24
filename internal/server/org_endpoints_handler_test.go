package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/session"
)

func newOrgEndpointsTestMux() (*http.ServeMux, *authHandler, *staticOrgProvider) {
	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	ah.registerRoutes(mux)
	store := &staticOrgProvider{org: testRBACOrg()}
	rh := &rbacHandler{auth: ah, orgStore: store}
	rh.registerRoutes(mux)
	return mux, ah, store
}

func getOrgEndpoints(t *testing.T, mux *http.ServeMux, cookie *http.Cookie) OrgEndpointsDTO {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/org/endpoints", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /org/endpoints: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var dto OrgEndpointsDTO
	if err := json.NewDecoder(rec.Body).Decode(&dto); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return dto
}

// An org that never set the flag reports the secure default: true.
func TestGetOrgEndpoints_DefaultsTrue(t *testing.T) {
	mux, ah, _ := newOrgEndpointsTestMux()
	dto := getOrgEndpoints(t, mux, sessionCookieFor(ah, "alice", "developer"))
	if !dto.SecureEndpoints {
		t.Error("secureEndpoints should default to true when the org never set it")
	}
}

func TestPutOrgEndpoints_PersistsAndRoundTrips(t *testing.T) {
	mux, ah, store := newOrgEndpointsTestMux()
	cookie := sessionCookieFor(ah, "alice", "org_admin")

	req := httptest.NewRequest("PUT", "/api/v1/org/endpoints",
		strings.NewReader(`{"secureEndpoints":false}`))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var dto OrgEndpointsDTO
	if err := json.NewDecoder(rec.Body).Decode(&dto); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if dto.SecureEndpoints {
		t.Error("PUT false should echo false")
	}
	if store.org.SecureEndpoints == nil || *store.org.SecureEndpoints {
		t.Error("saved org should carry an explicit false")
	}
	if got := getOrgEndpoints(t, mux, cookie); got.SecureEndpoints {
		t.Error("GET after PUT false should report false")
	}

	// Flip back on.
	req = httptest.NewRequest("PUT", "/api/v1/org/endpoints",
		strings.NewReader(`{"secureEndpoints":true}`))
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT true: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := getOrgEndpoints(t, mux, cookie); !got.SecureEndpoints {
		t.Error("GET after PUT true should report true")
	}
}

func TestPutOrgEndpoints_RequiresOrgAdmin(t *testing.T) {
	mux, ah, store := newOrgEndpointsTestMux()
	req := httptest.NewRequest("PUT", "/api/v1/org/endpoints",
		strings.NewReader(`{"secureEndpoints":false}`))
	req.AddCookie(sessionCookieFor(ah, "bob", "developer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.org.SecureEndpoints != nil {
		t.Error("org must be unchanged after a forbidden PUT")
	}
}
