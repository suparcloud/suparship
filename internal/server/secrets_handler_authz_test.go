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
		// ── Org-level writes ─────────────────────────────────────────
		{"org upsert by org_admin", "POST", "/api/v1/org/secrets", upsertBody, "alice", "org_admin", 200},
		{"org upsert by developer → 403", "POST", "/api/v1/org/secrets", upsertBody, "bob", "developer", 403},
		{"org upsert by viewer → 403", "POST", "/api/v1/org/secrets", upsertBody, "carol", "viewer", 403},
		{"org delete by org_admin", "DELETE", "/api/v1/org/secrets/K", nil, "alice", "org_admin", 200},
		{"org delete by developer → 403", "DELETE", "/api/v1/org/secrets/K", nil, "bob", "developer", 403},

		// ── Org-level reads ──────────────────────────────────────────
		{"org list by org_admin", "GET", "/api/v1/org/secrets", nil, "alice", "org_admin", 200},
		{"org list by developer", "GET", "/api/v1/org/secrets", nil, "bob", "developer", 200},
		{"org list by viewer", "GET", "/api/v1/org/secrets", nil, "carol", "viewer", 200},

		// ── Env-type-level writes ────────────────────────────────────
		{"envtype upsert by org_admin", "POST", "/api/v1/org/secrets/envtype/staging", upsertBody, "alice", "org_admin", 200},
		{"envtype upsert by developer → 403", "POST", "/api/v1/org/secrets/envtype/staging", upsertBody, "bob", "developer", 403},
		{"envtype upsert by viewer → 403", "POST", "/api/v1/org/secrets/envtype/staging", upsertBody, "carol", "viewer", 403},

		// ── Env-type-level reads ─────────────────────────────────────
		{"envtype list by developer", "GET", "/api/v1/org/secrets/envtype/staging", nil, "bob", "developer", 200},
		{"envtype list by viewer", "GET", "/api/v1/org/secrets/envtype/staging", nil, "carol", "viewer", 200},

		// ── Project-level writes ─────────────────────────────────────
		{"project upsert by org_admin", "POST", "/api/v1/projects/api/secrets", upsertBody, "alice", "org_admin", 200},
		{"project upsert by developer → 403", "POST", "/api/v1/projects/api/secrets", upsertBody, "bob", "developer", 403},
		{"project upsert by viewer → 403", "POST", "/api/v1/projects/api/secrets", upsertBody, "carol", "viewer", 403},

		// ── Project-level reads ──────────────────────────────────────
		{"project list by developer", "GET", "/api/v1/projects/api/secrets", nil, "bob", "developer", 200},
		{"project list by viewer", "GET", "/api/v1/projects/api/secrets", nil, "carol", "viewer", 200},

		// ── App-level writes ─────────────────────────────────────────
		{"app upsert by org_admin", "POST", "/api/v1/projects/api/apps/backend/secrets", upsertBody, "alice", "org_admin", 200},
		{"app upsert by developer", "POST", "/api/v1/projects/api/apps/backend/secrets", upsertBody, "bob", "developer", 200},
		{"app upsert by viewer → 403", "POST", "/api/v1/projects/api/apps/backend/secrets", upsertBody, "carol", "viewer", 403},

		// ── App-level reads ──────────────────────────────────────────
		{"app list by viewer", "GET", "/api/v1/projects/api/apps/backend/secrets", nil, "carol", "viewer", 200},

		// ── App-env-level writes ─────────────────────────────────────
		{"appenv upsert by developer", "POST", "/api/v1/projects/api/apps/backend/envs/staging/secrets", upsertBody, "bob", "developer", 200},
		{"appenv upsert by viewer → 403", "POST", "/api/v1/projects/api/apps/backend/envs/staging/secrets", upsertBody, "carol", "viewer", 403},

		// ── App-env-level reads ──────────────────────────────────────
		{"appenv list by viewer", "GET", "/api/v1/projects/api/apps/backend/envs/staging/secrets", nil, "carol", "viewer", 200},

		// ── Unauthenticated ──────────────────────────────────────────
		{"org list unauthenticated → 401", "GET", "/api/v1/org/secrets", nil, "", "", 401},
		{"appenv list unauthenticated → 401", "GET", "/api/v1/projects/api/apps/backend/envs/staging/secrets", nil, "", "", 401},
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
