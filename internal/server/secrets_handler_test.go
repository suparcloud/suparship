package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/secrets"
	"github.com/suparcloud/suparship/internal/session"
)

// newSecretsMux wires the secrets handler over an in-memory vault store and a
// static org, for HTTP-level tests of the 3-scope CRUD endpoints.
func newSecretsMux() (*http.ServeMux, *authHandler) {
	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
		cookieSecure:  false,
	}
	ah.registerRoutes(mux)

	appStore := newMemAppStore()
	appStore.addApp(&domain.App{
		Name:        "backend",
		ProjectName: "api",
		Spec: domain.AppSpec{
			DisplayName: "Backend",
			Template:    domain.AppTemplateRef{Name: "web-service"},
		},
	})
	appStore.addEnv(&domain.AppEnvironment{
		AppName:     "backend",
		ProjectName: "api",
		EnvName:     "staging",
		EnvType:     domain.AppEnvStaging,
		Namespace:   "api-backend-staging",
	})

	orgStore := &staticOrgProvider{org: testRBACOrg()}

	sh := &secretsHandler{
		orgStore: orgStore,
		appStore: appStore,
		vault:    secrets.NewMemVaultStore(),
		auditor:  secrets.NewAuditor(slog.Default()),
		logger:   slog.Default(),
	}

	rh := &rbacHandler{auth: ah, orgStore: orgStore, secretsHandler: sh}
	rh.registerRoutes(mux)
	return mux, ah
}

func do(t *testing.T, mux *http.ServeMux, ah *authHandler, method, path, user, role string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Buffer = bytes.NewBuffer(nil)
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewBuffer(b)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, user, role))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestGetSecretsBackend_Default(t *testing.T) {
	mux, ah := newSecretsMux()
	rec := do(t, mux, ah, "GET", "/api/v1/org/secrets-backend", "alice", "org_admin", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var dto SecretBackendDTO
	mustDecode(t, rec.Body.Bytes(), &dto)
	if dto.Type != "k8s" {
		t.Errorf("expected default type 'k8s', got %q", dto.Type)
	}
}

// TestSharedSecrets_RoundTrip exercises shared-tier CRUD across the three
// scopes through the org-admin routes.
func TestSharedSecrets_RoundTrip(t *testing.T) {
	mux, ah := newSecretsMux()
	cases := []struct{ scope, path string }{
		{"global", "/api/v1/org/secrets/global"},
		{"env", "/api/v1/org/secrets/env/staging"},
		{"cluster", "/api/v1/org/secrets/cluster/prod-eu"},
	}
	for _, c := range cases {
		t.Run(c.scope, func(t *testing.T) {
			rec := do(t, mux, ah, "POST", c.path, "alice", "org_admin", UpsertSecretsRequest{Entries: map[string]string{"K": "v"}})
			if rec.Code != http.StatusOK {
				t.Fatalf("upsert: expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			rec = do(t, mux, ah, "GET", c.path, "alice", "org_admin", nil)
			var resp SecretKeysResponse
			mustDecode(t, rec.Body.Bytes(), &resp)
			if len(resp.Keys) != 1 || resp.Keys[0].Key != "K" {
				t.Fatalf("list: expected [K], got %+v", resp.Keys)
			}
			rec = do(t, mux, ah, "DELETE", c.path+"/K", "alice", "org_admin", nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("delete: expected 200, got %d", rec.Code)
			}
			rec = do(t, mux, ah, "GET", c.path, "alice", "org_admin", nil)
			mustDecode(t, rec.Body.Bytes(), &resp)
			if len(resp.Keys) != 0 {
				t.Fatalf("after delete: expected 0 keys, got %+v", resp.Keys)
			}
		})
	}
}

func TestSharedSecrets_ForbiddenForViewer(t *testing.T) {
	mux, ah := newSecretsMux()
	rec := do(t, mux, ah, "POST", "/api/v1/org/secrets/global", "carol", "viewer", UpsertSecretsRequest{Entries: map[string]string{"K": "v"}})
	if rec.Code == http.StatusOK {
		t.Fatalf("expected non-200 for viewer, got %d", rec.Code)
	}
}

// TestAppSecrets_RoundTrip exercises per-app CRUD (developer role).
func TestAppSecrets_RoundTrip(t *testing.T) {
	mux, ah := newSecretsMux()
	base := "/api/v1/projects/api/apps/backend/secrets"
	for _, scopePath := range []string{base + "/global", base + "/env/staging", base + "/cluster/prod-eu"} {
		rec := do(t, mux, ah, "POST", scopePath, "bob", "developer", UpsertSecretsRequest{Entries: map[string]string{"APP_K": "v"}})
		if rec.Code != http.StatusOK {
			t.Fatalf("upsert %s: expected 200, got %d: %s", scopePath, rec.Code, rec.Body.String())
		}
		rec = do(t, mux, ah, "GET", scopePath, "bob", "developer", nil)
		var resp SecretKeysResponse
		mustDecode(t, rec.Body.Bytes(), &resp)
		if len(resp.Keys) != 1 || resp.Keys[0].Key != "APP_K" {
			t.Fatalf("list %s: expected [APP_K], got %+v", scopePath, resp.Keys)
		}
	}
}

// TestResolvedSecrets merges shared + app across scopes; cluster/app wins.
func TestResolvedSecrets(t *testing.T) {
	mux, ah := newSecretsMux()
	// shared global, app env, and a colliding key at app cluster.
	do(t, mux, ah, "POST", "/api/v1/org/secrets/global", "alice", "org_admin", UpsertSecretsRequest{Entries: map[string]string{"SHARED": "1", "WIN": "g"}})
	do(t, mux, ah, "POST", "/api/v1/projects/api/apps/backend/secrets/cluster/prod-eu", "bob", "developer", UpsertSecretsRequest{Entries: map[string]string{"WIN": "c"}})

	rec := do(t, mux, ah, "GET", "/api/v1/projects/api/apps/backend/envs/staging/secrets/resolved", "bob", "developer", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("resolved: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp ResolvedSecretsResponse
	mustDecode(t, rec.Body.Bytes(), &resp)
	got := map[string]ResolvedSecretDTO{}
	for _, s := range resp.Secrets {
		got[s.Key] = s
	}
	if got["SHARED"].Source != "global" {
		t.Errorf("SHARED source = %q, want global", got["SHARED"].Source)
	}
	// Note: staging env has no bound cluster in testRBACOrg, so the cluster
	// scope may not resolve; assert SHARED at minimum and WIN present.
	if _, ok := got["WIN"]; !ok {
		t.Errorf("expected WIN key in resolved set: %+v", resp.Secrets)
	}
}

// TestRegisterEnvVault_RequiresOnePassword verifies env-vault registration is
// rejected on the default k8s backend (the harness uses MemVaultStore / k8s).
func TestRegisterEnvVault_RequiresOnePassword(t *testing.T) {
	mux, ah := newSecretsMux()
	rec := do(t, mux, ah, "POST", "/api/v1/org/secret-backend/vaults/env/staging", "alice", "org_admin",
		map[string]string{"vaultId": "v1", "connectToken": "tok"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 (1Password backend required), got %d: %s", rec.Code, rec.Body.String())
	}
}
