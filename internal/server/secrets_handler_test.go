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

// ── Helpers ──────────────────────────────────────────────────────────────────

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

	memBE := secrets.NewMemBackend()

	sh := &secretsHandler{
		orgStore:    orgStore,
		appStore:    appStore,
		backend:     memBE,
		upperWriter: secrets.NewMemUpperLevelWriter(memBE),
		logger:      slog.Default(),
	}

	rh := &rbacHandler{
		auth:           ah,
		orgStore:       orgStore,
		secretsHandler: sh,
	}
	rh.registerRoutes(mux)

	return mux, ah
}

// ── Org backend config tests ────────────────────────────────────────────────

func TestGetSecretsBackend_Default(t *testing.T) {
	mux, ah := newSecretsMux()

	req := httptest.NewRequest("GET", "/api/v1/org/secrets-backend", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var dto SecretBackendDTO
	mustDecode(t, rec.Body.Bytes(), &dto)
	if dto.Type != "k8s" {
		t.Errorf("expected default type 'k8s', got %q", dto.Type)
	}
}

func TestPutSecretsBackend_OrgAdmin(t *testing.T) {
	mux, ah := newSecretsMux()

	body, _ := json.Marshal(SecretBackendDTO{Type: "k8s"})
	req := httptest.NewRequest("PUT", "/api/v1/org/secrets-backend", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPutSecretsBackend_InvalidType(t *testing.T) {
	mux, ah := newSecretsMux()

	body, _ := json.Marshal(SecretBackendDTO{Type: "invalid"})
	req := httptest.NewRequest("PUT", "/api/v1/org/secrets-backend", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for invalid type, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPutSecretsBackend_ForbiddenForDeveloper(t *testing.T) {
	mux, ah := newSecretsMux()

	body, _ := json.Marshal(SecretBackendDTO{Type: "k8s"})
	req := httptest.NewRequest("PUT", "/api/v1/org/secrets-backend", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "bob", "developer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("developer should not PUT secrets-backend, got %d", rec.Code)
	}
}

// ── Org-level secrets CRUD ──────────────────────────────────────────────────

func TestOrgSecrets_UpsertAndList(t *testing.T) {
	mux, ah := newSecretsMux()

	upsertBody, _ := json.Marshal(UpsertSecretsRequest{
		Entries: map[string]string{"ORG_KEY": "org-value"},
	})
	req := httptest.NewRequest("POST", "/api/v1/org/secrets", bytes.NewBuffer(upsertBody))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upsert org: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	req2 := httptest.NewRequest("GET", "/api/v1/org/secrets", nil)
	req2.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("list org: expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var resp SecretKeysResponse
	mustDecode(t, rec2.Body.Bytes(), &resp)
	if len(resp.Keys) != 1 || resp.Keys[0].Key != "ORG_KEY" {
		t.Errorf("unexpected org keys: %+v", resp.Keys)
	}
}

func TestOrgSecrets_Delete(t *testing.T) {
	mux, ah := newSecretsMux()

	upsertBody, _ := json.Marshal(UpsertSecretsRequest{
		Entries: map[string]string{"DEL": "val"},
	})
	req := httptest.NewRequest("POST", "/api/v1/org/secrets", bytes.NewBuffer(upsertBody))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	req2 := httptest.NewRequest("DELETE", "/api/v1/org/secrets/DEL", nil)
	req2.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("delete org key: expected 200, got %d", rec2.Code)
	}
}

func TestOrgSecrets_ForbiddenForDeveloper(t *testing.T) {
	mux, ah := newSecretsMux()

	body, _ := json.Marshal(UpsertSecretsRequest{Entries: map[string]string{"K": "v"}})
	req := httptest.NewRequest("POST", "/api/v1/org/secrets", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "bob", "developer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("developer should not upsert org secrets, got %d", rec.Code)
	}
}

func TestOrgSecrets_ReadAllowedForAnyAuth(t *testing.T) {
	mux, ah := newSecretsMux()

	req := httptest.NewRequest("GET", "/api/v1/org/secrets", nil)
	req.AddCookie(sessionCookieFor(ah, "bob", "developer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("developer should read org secrets, got %d", rec.Code)
	}
}

// ── Env-type-level secrets ──────────────────────────────────────────────────

func TestEnvTypeSecrets_UpsertAndList(t *testing.T) {
	mux, ah := newSecretsMux()

	upsertBody, _ := json.Marshal(UpsertSecretsRequest{
		Entries: map[string]string{"STAGING_DB": "staging-conn"},
	})
	req := httptest.NewRequest("POST", "/api/v1/org/secrets/envtype/staging", bytes.NewBuffer(upsertBody))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upsert envtype: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	req2 := httptest.NewRequest("GET", "/api/v1/org/secrets/envtype/staging", nil)
	req2.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("list envtype: expected 200, got %d", rec2.Code)
	}
	var resp SecretKeysResponse
	mustDecode(t, rec2.Body.Bytes(), &resp)
	if len(resp.Keys) != 1 || resp.Keys[0].Key != "STAGING_DB" {
		t.Errorf("unexpected envtype keys: %+v", resp.Keys)
	}
}

// ── Project-level secrets ───────────────────────────────────────────────────

func TestProjectSecrets_UpsertAndList(t *testing.T) {
	mux, ah := newSecretsMux()

	upsertBody, _ := json.Marshal(UpsertSecretsRequest{
		Entries: map[string]string{"PROJ_SECRET": "proj-val"},
	})
	req := httptest.NewRequest("POST", "/api/v1/projects/api/secrets", bytes.NewBuffer(upsertBody))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upsert project: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	req2 := httptest.NewRequest("GET", "/api/v1/projects/api/secrets", nil)
	req2.AddCookie(sessionCookieFor(ah, "bob", "developer"))
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("list project: expected 200, got %d", rec2.Code)
	}
	var resp SecretKeysResponse
	mustDecode(t, rec2.Body.Bytes(), &resp)
	if len(resp.Keys) != 1 || resp.Keys[0].Key != "PROJ_SECRET" {
		t.Errorf("unexpected project keys: %+v", resp.Keys)
	}
}

func TestProjectSecrets_ForbiddenForViewer(t *testing.T) {
	mux, ah := newSecretsMux()

	body, _ := json.Marshal(UpsertSecretsRequest{Entries: map[string]string{"K": "v"}})
	req := httptest.NewRequest("POST", "/api/v1/projects/api/secrets", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "carol", "viewer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer should not upsert project secrets, got %d", rec.Code)
	}
}

// ── App-env secrets CRUD (existing) ─────────────────────────────────────────

func TestUpsertAndListSecrets(t *testing.T) {
	mux, ah := newSecretsMux()

	upsertBody, _ := json.Marshal(UpsertSecretsRequest{
		Entries: map[string]string{
			"DB_URL":  "postgres://localhost",
			"API_KEY": "sk-secret",
		},
	})
	req := httptest.NewRequest("POST", "/api/v1/projects/api/apps/backend/envs/staging/secrets", bytes.NewBuffer(upsertBody))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "bob", "developer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("upsert: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	req2 := httptest.NewRequest("GET", "/api/v1/projects/api/apps/backend/envs/staging/secrets", nil)
	req2.AddCookie(sessionCookieFor(ah, "bob", "developer"))
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var listResp SecretKeysResponse
	mustDecode(t, rec2.Body.Bytes(), &listResp)

	if len(listResp.Keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(listResp.Keys))
	}
	if listResp.SecretName != "suparship-secrets-api-backend-staging" {
		t.Errorf("secretName = %q, want %q", listResp.SecretName, "suparship-secrets-api-backend-staging")
	}
}

func TestDeleteSecret(t *testing.T) {
	mux, ah := newSecretsMux()

	upsertBody, _ := json.Marshal(UpsertSecretsRequest{
		Entries: map[string]string{"TO_DELETE": "value"},
	})
	req := httptest.NewRequest("POST", "/api/v1/projects/api/apps/backend/envs/staging/secrets", bytes.NewBuffer(upsertBody))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "bob", "developer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upsert: got %d", rec.Code)
	}

	req2 := httptest.NewRequest("DELETE", "/api/v1/projects/api/apps/backend/envs/staging/secrets/TO_DELETE", nil)
	req2.AddCookie(sessionCookieFor(ah, "bob", "developer"))
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}

	req3 := httptest.NewRequest("GET", "/api/v1/projects/api/apps/backend/envs/staging/secrets", nil)
	req3.AddCookie(sessionCookieFor(ah, "bob", "developer"))
	rec3 := httptest.NewRecorder()
	mux.ServeHTTP(rec3, req3)

	var listResp SecretKeysResponse
	mustDecode(t, rec3.Body.Bytes(), &listResp)
	if len(listResp.Keys) != 0 {
		t.Errorf("expected 0 keys after delete, got %d", len(listResp.Keys))
	}
}

func TestUpsertSecrets_EmptyEntries(t *testing.T) {
	mux, ah := newSecretsMux()

	body, _ := json.Marshal(UpsertSecretsRequest{Entries: map[string]string{}})
	req := httptest.NewRequest("POST", "/api/v1/projects/api/apps/backend/envs/staging/secrets", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "bob", "developer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty entries, got %d", rec.Code)
	}
}

func TestListSecrets_Unauthenticated(t *testing.T) {
	mux, _ := newSecretsMux()

	req := httptest.NewRequest("GET", "/api/v1/projects/api/apps/backend/envs/staging/secrets", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestListSecrets_ViewerCanRead(t *testing.T) {
	mux, ah := newSecretsMux()

	req := httptest.NewRequest("GET", "/api/v1/projects/api/apps/backend/envs/staging/secrets", nil)
	req.AddCookie(sessionCookieFor(ah, "carol", "viewer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("viewer should be able to list secret keys, got %d", rec.Code)
	}
}

func TestUpsertSecrets_ForbiddenForViewer(t *testing.T) {
	mux, ah := newSecretsMux()

	body, _ := json.Marshal(UpsertSecretsRequest{Entries: map[string]string{"K": "v"}})
	req := httptest.NewRequest("POST", "/api/v1/projects/api/apps/backend/envs/staging/secrets", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "carol", "viewer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer should not upsert secrets, got %d", rec.Code)
	}
}

// ── Resolved secrets ────────────────────────────────────────────────────────

func TestResolvedSecrets(t *testing.T) {
	mux, ah := newSecretsMux()

	// Add org secret.
	body1, _ := json.Marshal(UpsertSecretsRequest{Entries: map[string]string{"SHARED": "org", "ORG_ONLY": "o"}})
	req1 := httptest.NewRequest("POST", "/api/v1/org/secrets", bytes.NewBuffer(body1))
	req1.Header.Set("Content-Type", "application/json")
	req1.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec1 := httptest.NewRecorder()
	mux.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("upsert org: %d", rec1.Code)
	}

	// Add app-env secret that overrides SHARED.
	body2, _ := json.Marshal(UpsertSecretsRequest{Entries: map[string]string{"SHARED": "env", "ENV_ONLY": "e"}})
	req2 := httptest.NewRequest("POST", "/api/v1/projects/api/apps/backend/envs/staging/secrets", bytes.NewBuffer(body2))
	req2.Header.Set("Content-Type", "application/json")
	req2.AddCookie(sessionCookieFor(ah, "bob", "developer"))
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("upsert app-env: %d", rec2.Code)
	}

	// Resolve.
	req3 := httptest.NewRequest("GET", "/api/v1/projects/api/apps/backend/envs/staging/secrets/resolved", nil)
	req3.AddCookie(sessionCookieFor(ah, "bob", "developer"))
	rec3 := httptest.NewRecorder()
	mux.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("resolved: expected 200, got %d: %s", rec3.Code, rec3.Body.String())
	}

	var resp ResolvedSecretsResponse
	mustDecode(t, rec3.Body.Bytes(), &resp)

	if len(resp.Secrets) != 3 {
		t.Fatalf("expected 3 resolved secrets, got %d", len(resp.Secrets))
	}

	byKey := make(map[string]ResolvedSecretDTO)
	for _, s := range resp.Secrets {
		byKey[s.Key] = s
	}
	if byKey["SHARED"].Source != "app-environment" {
		t.Errorf("SHARED source = %q, want app-environment", byKey["SHARED"].Source)
	}
	if byKey["ORG_ONLY"].Source != "org" {
		t.Errorf("ORG_ONLY source = %q, want org", byKey["ORG_ONLY"].Source)
	}
	if byKey["ENV_ONLY"].Source != "app-environment" {
		t.Errorf("ENV_ONLY source = %q, want app-environment", byKey["ENV_ONLY"].Source)
	}
}
