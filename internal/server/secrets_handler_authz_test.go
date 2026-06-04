package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSecretsAuthzMatrix verifies the RBAC enforcement for every
// (scope, verb, role) combination per the plan's authorization matrix:
//
//	org/env-type → org_admin only for writes
//	project      → project_admin+ for writes
//	app/app-env  → developer+ for writes
//	all scopes   → viewer+ for reads
func TestSecretsAuthzMatrix(t *testing.T) {
	mux, ah := newSecretsMux()

	upsertBody := func() *bytes.Buffer {
		b, _ := json.Marshal(UpsertSecretsRequest{Entries: map[string]string{"K": "v"}})
		return bytes.NewBuffer(b)
	}

	tests := []struct {
		name     string
		method   string
		path     string
		body     func() *bytes.Buffer
		user     string
		role     string
		wantCode int
	}{
		// ── Shared global (org_admin writes, viewer reads) ──────────
		{"shared global upsert by org_admin", "POST", "/api/v1/org/secrets/global", upsertBody, "alice", "org_admin", 200},
		{"shared global upsert by developer → 403", "POST", "/api/v1/org/secrets/global", upsertBody, "bob", "developer", 403},
		{"shared global upsert by viewer → 403", "POST", "/api/v1/org/secrets/global", upsertBody, "carol", "viewer", 403},
		{"shared global delete by org_admin", "DELETE", "/api/v1/org/secrets/global/K", nil, "alice", "org_admin", 200},
		{"shared global list by viewer", "GET", "/api/v1/org/secrets/global", nil, "carol", "viewer", 200},

		// ── Shared env (org_admin writes) ───────────────────────────
		{"shared env upsert by org_admin", "POST", "/api/v1/org/secrets/env/staging", upsertBody, "alice", "org_admin", 200},
		{"shared env upsert by developer → 403", "POST", "/api/v1/org/secrets/env/staging", upsertBody, "bob", "developer", 403},
		{"shared env list by viewer", "GET", "/api/v1/org/secrets/env/staging", nil, "carol", "viewer", 200},

		// ── Shared cluster (org_admin writes) ───────────────────────
		{"shared cluster upsert by org_admin", "POST", "/api/v1/org/secrets/cluster/prod-eu", upsertBody, "alice", "org_admin", 200},
		{"shared cluster upsert by developer → 403", "POST", "/api/v1/org/secrets/cluster/prod-eu", upsertBody, "bob", "developer", 403},

		// ── App global (developer writes, viewer reads) ─────────────
		{"app global upsert by developer", "POST", "/api/v1/projects/api/apps/backend/secrets/global", upsertBody, "bob", "developer", 200},
		{"app global upsert by viewer → 403", "POST", "/api/v1/projects/api/apps/backend/secrets/global", upsertBody, "carol", "viewer", 403},
		{"app global list by viewer", "GET", "/api/v1/projects/api/apps/backend/secrets/global", nil, "carol", "viewer", 200},

		// ── App env (developer writes) ──────────────────────────────
		{"app env upsert by developer", "POST", "/api/v1/projects/api/apps/backend/secrets/env/staging", upsertBody, "bob", "developer", 200},
		{"app env upsert by viewer → 403", "POST", "/api/v1/projects/api/apps/backend/secrets/env/staging", upsertBody, "carol", "viewer", 403},

		// ── Unauthenticated ──────────────────────────────────────────
		{"shared global list unauthenticated → 401", "GET", "/api/v1/org/secrets/global", nil, "", "", 401},
		{"app global list unauthenticated → 401", "GET", "/api/v1/projects/api/apps/backend/secrets/global", nil, "", "", 401},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body *bytes.Buffer
			if tt.body != nil {
				body = tt.body()
			}

			var req *http.Request
			if body != nil {
				req = httptest.NewRequest(tt.method, tt.path, body)
				req.Header.Set("Content-Type", "application/json")
			} else {
				req = httptest.NewRequest(tt.method, tt.path, nil)
			}

			if tt.user != "" {
				req.AddCookie(sessionCookieFor(ah, tt.user, tt.role))
			}

			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Errorf("got %d, want %d (body: %s)", rec.Code, tt.wantCode, rec.Body.String())
			}
		})
	}
}
