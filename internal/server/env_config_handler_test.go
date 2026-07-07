package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/envconfig"
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/session"
)

// ── Helpers ───────────────────────────────────────────────────────────────────

// envConfigOrg returns a test org that has both role bindings and canonical
// environments (staging, prod) needed by the environment-level endpoints.
func envConfigOrg() *rbac.Org {
	base := testRBACOrg()
	base.Environments = []rbac.OrgEnvironment{
		{Name: "staging", DisplayName: "Staging", Order: 1},
		{Name: "prod", DisplayName: "Production", Order: 2},
	}
	return base
}

// newEnvConfigMux builds a fully-wired mux that includes envConfigHandler.
// upperLevelWriter and publisher are nil (no k8s / no gitops in tests).
func newEnvConfigMux() (*http.ServeMux, *authHandler) {
	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
		cookieSecure:  false,
	}
	ah.registerRoutes(mux)

	projStore := newMemProjectStore()
	_ = projStore.Save(context.Background(), &project.Project{
		Metadata: project.ProjectMeta{Name: "api"},
		Spec:     project.ProjectSpec{DisplayName: "API Service"},
	})

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
	appStore.addEnv(&domain.AppEnvironment{
		AppName:     "backend",
		ProjectName: "api",
		EnvName:     "prod",
		EnvType:     domain.AppEnvProd,
		Namespace:   "api-backend-prod",
	})

	orgStore := &staticOrgProvider{org: envConfigOrg()}

	ech := &envConfigHandler{
		orgStore:     orgStore,
		projectStore: projStore,
		appStore:     appStore,
		logger:       slog.Default(),
	}

	rh := &rbacHandler{
		auth:             ah,
		orgStore:         orgStore,
		projectStore:     projStore,
		envConfigHandler: ech,
	}
	rh.registerRoutes(mux)

	return mux, ah
}

// envConfigBody encodes an EnvConfigDTO as JSON for request bodies.
func envConfigBody(vars map[string]string, refs []SecretRefDTO) *bytes.Buffer {
	dto := EnvConfigDTO{Vars: vars, SecretRefs: refs}
	b, _ := json.Marshal(dto)
	return bytes.NewBuffer(b)
}

// ── Config-variable catalog ───────────────────────────────────────────────────

func TestGetConfigVariables_ListsPlatformAndVars_NoSecrets(t *testing.T) {
	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	ah.registerRoutes(mux)

	org := envConfigOrg()
	org.EnvConfig = envconfig.EnvConfig{
		Vars:       map[string]string{"ORG_VAR": "x"},
		SecretRefs: []envconfig.SecretRef{{EnvKey: "DB_PASSWORD", Provider: "k8s", Name: "s", Key: "k"}},
	}
	org.Environments[0].EnvConfig = envconfig.EnvConfig{Vars: map[string]string{"ENV_VAR": "y"}}

	projStore := newMemProjectStore()
	_ = projStore.Save(context.Background(), &project.Project{
		Metadata: project.ProjectMeta{Name: "api"},
		Spec:     project.ProjectSpec{EnvConfig: envconfig.EnvConfig{Vars: map[string]string{"PROJ_VAR": "z"}}},
	})
	orgStore := &staticOrgProvider{org: org}
	ech := &envConfigHandler{orgStore: orgStore, projectStore: projStore, appStore: newMemAppStore(), logger: slog.Default()}
	rh := &rbacHandler{auth: ah, orgStore: orgStore, projectStore: projStore, envConfigHandler: ech}
	rh.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/projects/api/config-variables", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var resp ConfigVariablesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Platform) == 0 {
		t.Error("platform tokens missing")
	}
	names := map[string]string{}
	for _, v := range resp.Vars {
		names[v.Name] = v.Scope
		if v.Token != "((vars."+v.Name+"))" {
			t.Errorf("token format = %q", v.Token)
		}
	}
	for _, want := range []string{"ORG_VAR", "ENV_VAR", "PROJ_VAR"} {
		if _, ok := names[want]; !ok {
			t.Errorf("missing var %q in catalog", want)
		}
	}
	if _, leaked := names["DB_PASSWORD"]; leaked {
		t.Error("SECRET DB_PASSWORD leaked into the variable catalog")
	}
}

// Project per-environment variables round-trip through the new endpoints and
// surface in the variable catalog scoped to that env.
func TestProjectEnvEnvConfig_RoundTrip(t *testing.T) {
	mux, ah := newEnvConfigMux()
	cookie := sessionCookieFor(ah, "alice", "org_admin")

	body, _ := json.Marshal(EnvConfigDTO{Vars: map[string]string{"PE_VAR": "1"}})
	put := httptest.NewRequest("PUT", "/api/v1/projects/api/envconfig/env/staging", bytes.NewReader(body))
	put.AddCookie(cookie)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, put)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	get := httptest.NewRequest("GET", "/api/v1/projects/api/envconfig/env/staging", nil)
	get.AddCookie(cookie)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, get)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", rec.Code)
	}
	var dto EnvConfigDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if dto.Vars["PE_VAR"] != "1" {
		t.Fatalf("project-env var not persisted: %+v", dto.Vars)
	}

	// It must NOT bleed into the project-global (all-envs) variables.
	getGlobal := httptest.NewRequest("GET", "/api/v1/projects/api/envconfig", nil)
	getGlobal.AddCookie(cookie)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, getGlobal)
	var gdto EnvConfigDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &gdto)
	if _, leaked := gdto.Vars["PE_VAR"]; leaked {
		t.Errorf("project-env var leaked into project-global variables")
	}

	// The catalog lists it scoped to project:staging.
	cat := httptest.NewRequest("GET", "/api/v1/projects/api/config-variables", nil)
	cat.AddCookie(cookie)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, cat)
	var cresp ConfigVariablesResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &cresp)
	found := false
	for _, v := range cresp.Vars {
		if v.Name == "PE_VAR" {
			found = true
		}
	}
	if !found {
		t.Errorf("PE_VAR missing from variable catalog")
	}
}

func TestGetPlatformConfigVariables_OmitsProjectScope(t *testing.T) {
	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	ah.registerRoutes(mux)

	org := envConfigOrg()
	org.EnvConfig = envconfig.EnvConfig{Vars: map[string]string{"ORG_VAR": "x"}}
	org.Environments[0].EnvConfig = envconfig.EnvConfig{Vars: map[string]string{"ENV_VAR": "y"}}

	projStore := newMemProjectStore()
	_ = projStore.Save(context.Background(), &project.Project{
		Metadata: project.ProjectMeta{Name: "api"},
		Spec:     project.ProjectSpec{EnvConfig: envconfig.EnvConfig{Vars: map[string]string{"PROJ_VAR": "z"}}},
	})
	orgStore := &staticOrgProvider{org: org}
	ech := &envConfigHandler{orgStore: orgStore, projectStore: projStore, appStore: newMemAppStore(), logger: slog.Default()}
	rh := &rbacHandler{auth: ah, orgStore: orgStore, projectStore: projStore, envConfigHandler: ech}
	rh.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/platform/config-variables", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var resp ConfigVariablesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Platform) == 0 {
		t.Error("platform tokens missing")
	}
	names := map[string]bool{}
	for _, v := range resp.Vars {
		names[v.Name] = true
	}
	for _, want := range []string{"ORG_VAR", "ENV_VAR"} {
		if !names[want] {
			t.Errorf("missing org/env var %q in platform catalog", want)
		}
	}
	if names["PROJ_VAR"] {
		t.Error("project-scoped var PROJ_VAR must not appear in the project-agnostic catalog")
	}
}

func TestGetPlatformConfigVariables_Unauthenticated(t *testing.T) {
	mux, _ := newEnvConfigMux()
	req := httptest.NewRequest("GET", "/api/v1/platform/config-variables", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated = %d, want 401", rec.Code)
	}
}

func TestGetConfigVariables_RequiresProjectView(t *testing.T) {
	mux, _ := newEnvConfigMux()
	req := httptest.NewRequest("GET", "/api/v1/projects/api/config-variables", nil)
	// no cookie → unauthenticated
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated = %d, want 401", rec.Code)
	}
}

// ── Level 1 — Org ─────────────────────────────────────────────────────────────

func TestGetOrgEnvConfig_ReturnsEmpty(t *testing.T) {
	mux, ah := newEnvConfigMux()

	req := httptest.NewRequest("GET", "/api/v1/org/envconfig", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var dto EnvConfigDTO
	mustDecode(t, rec.Body.Bytes(), &dto)
	if len(dto.Vars) != 0 || len(dto.SecretRefs) != 0 {
		t.Fatalf("expected empty EnvConfig, got %+v", dto)
	}
}

func TestGetOrgEnvConfig_Unauthenticated(t *testing.T) {
	mux, _ := newEnvConfigMux()

	req := httptest.NewRequest("GET", "/api/v1/org/envconfig", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestPutOrgEnvConfig_OrgAdmin(t *testing.T) {
	mux, ah := newEnvConfigMux()

	body := envConfigBody(map[string]string{"LOG_LEVEL": "info", "REGION": "us-east-1"}, nil)
	req := httptest.NewRequest("PUT", "/api/v1/org/envconfig", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var dto EnvConfigDTO
	mustDecode(t, rec.Body.Bytes(), &dto)
	if dto.Vars["LOG_LEVEL"] != "info" {
		t.Errorf("LOG_LEVEL = %q, want %q", dto.Vars["LOG_LEVEL"], "info")
	}
	if dto.Vars["REGION"] != "us-east-1" {
		t.Errorf("REGION = %q, want %q", dto.Vars["REGION"], "us-east-1")
	}

	// Verify persisted by reading it back.
	req2 := httptest.NewRequest("GET", "/api/v1/org/envconfig", nil)
	req2.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	var dto2 EnvConfigDTO
	mustDecode(t, rec2.Body.Bytes(), &dto2)
	if dto2.Vars["LOG_LEVEL"] != "info" {
		t.Errorf("persisted LOG_LEVEL = %q, want %q", dto2.Vars["LOG_LEVEL"], "info")
	}
}

func TestPutOrgEnvConfig_ForbiddenForNonAdmin(t *testing.T) {
	mux, ah := newEnvConfigMux()

	body := envConfigBody(map[string]string{"KEY": "val"}, nil)
	req := httptest.NewRequest("PUT", "/api/v1/org/envconfig", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "bob", "developer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("developer should not PUT org envconfig, got %d", rec.Code)
	}
}

func TestPutOrgEnvConfig_InvalidVarKey(t *testing.T) {
	mux, ah := newEnvConfigMux()

	body := envConfigBody(map[string]string{"invalid-key": "val"}, nil)
	req := httptest.NewRequest("PUT", "/api/v1/org/envconfig", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for invalid key, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPutOrgEnvConfig_WithSecretRef(t *testing.T) {
	mux, ah := newEnvConfigMux()

	body := envConfigBody(nil, []SecretRefDTO{
		{Provider: "k8s", Name: "my-secret", Key: "db_pass", EnvKey: "DB_PASS"},
	})
	req := httptest.NewRequest("PUT", "/api/v1/org/envconfig", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var dto EnvConfigDTO
	mustDecode(t, rec.Body.Bytes(), &dto)
	if len(dto.SecretRefs) != 1 {
		t.Fatalf("expected 1 secret ref, got %d", len(dto.SecretRefs))
	}
	if dto.SecretRefs[0].EnvKey != "DB_PASS" {
		t.Errorf("EnvKey = %q, want %q", dto.SecretRefs[0].EnvKey, "DB_PASS")
	}
}

// ── Level 2 — Environment ─────────────────────────────────────────────────────

func TestGetEnvTypeEnvConfig_NotFound(t *testing.T) {
	mux, ah := newEnvConfigMux()

	req := httptest.NewRequest("GET", "/api/v1/org/envconfig/unknown", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown env type, got %d", rec.Code)
	}
}

func TestGetEnvTypeEnvConfig_Staging(t *testing.T) {
	mux, ah := newEnvConfigMux()

	req := httptest.NewRequest("GET", "/api/v1/org/envconfig/staging", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPutEnvTypeEnvConfig_Staging(t *testing.T) {
	mux, ah := newEnvConfigMux()

	body := envConfigBody(map[string]string{"ENV_NAME": "staging", "DEBUG": "true"}, nil)
	req := httptest.NewRequest("PUT", "/api/v1/org/envconfig/staging", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var dto EnvConfigDTO
	mustDecode(t, rec.Body.Bytes(), &dto)
	if dto.Vars["ENV_NAME"] != "staging" {
		t.Errorf("ENV_NAME = %q, want %q", dto.Vars["ENV_NAME"], "staging")
	}
}

func TestPutEnvTypeEnvConfig_ForbiddenForDeveloper(t *testing.T) {
	mux, ah := newEnvConfigMux()

	body := envConfigBody(map[string]string{"K": "v"}, nil)
	req := httptest.NewRequest("PUT", "/api/v1/org/envconfig/staging", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "bob", "developer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("developer should not update env envconfig, got %d", rec.Code)
	}
}

func TestPutEnvTypeEnvConfig_NotFoundEnvType(t *testing.T) {
	mux, ah := newEnvConfigMux()

	body := envConfigBody(map[string]string{"K": "v"}, nil)
	req := httptest.NewRequest("PUT", "/api/v1/org/envconfig/canary", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing env type, got %d", rec.Code)
	}
}

// ── Level 3 — Project ─────────────────────────────────────────────────────────

func TestGetProjectEnvConfig_Returns200(t *testing.T) {
	mux, ah := newEnvConfigMux()

	req := httptest.NewRequest("GET", "/api/v1/projects/api/envconfig", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGetProjectEnvConfig_Unauthenticated(t *testing.T) {
	mux, _ := newEnvConfigMux()

	req := httptest.NewRequest("GET", "/api/v1/projects/api/envconfig", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestPutProjectEnvConfig_ProjectAdmin(t *testing.T) {
	mux, ah := newEnvConfigMux()

	body := envConfigBody(map[string]string{"PROJECT_ID": "api-001", "TEAM": "platform"}, nil)
	req := httptest.NewRequest("PUT", "/api/v1/projects/api/envconfig", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var dto EnvConfigDTO
	mustDecode(t, rec.Body.Bytes(), &dto)
	if dto.Vars["PROJECT_ID"] != "api-001" {
		t.Errorf("PROJECT_ID = %q, want %q", dto.Vars["PROJECT_ID"], "api-001")
	}
}

func TestPutProjectEnvConfig_ForbiddenForViewer(t *testing.T) {
	mux, ah := newEnvConfigMux()

	body := envConfigBody(map[string]string{"K": "v"}, nil)
	req := httptest.NewRequest("PUT", "/api/v1/projects/api/envconfig", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "carol", "viewer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer should not PUT project envconfig, got %d", rec.Code)
	}
}

func TestPutProjectEnvConfig_NotFoundProject(t *testing.T) {
	mux, ah := newEnvConfigMux()

	body := envConfigBody(map[string]string{"K": "v"}, nil)
	req := httptest.NewRequest("PUT", "/api/v1/projects/unknown/envconfig", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown project, got %d", rec.Code)
	}
}

// ── Level 4 — App ─────────────────────────────────────────────────────────────

func TestGetAppEnvConfig_Returns200(t *testing.T) {
	mux, ah := newEnvConfigMux()

	req := httptest.NewRequest("GET", "/api/v1/projects/api/apps/backend/envconfig", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGetAppEnvConfig_NotFoundApp(t *testing.T) {
	mux, ah := newEnvConfigMux()

	req := httptest.NewRequest("GET", "/api/v1/projects/api/apps/noapp/envconfig", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown app, got %d", rec.Code)
	}
}

func TestPutAppEnvConfig_Developer_Returns202(t *testing.T) {
	mux, ah := newEnvConfigMux()

	body := envConfigBody(map[string]string{"APP_PORT": "8080", "APP_NAME": "backend"}, nil)
	req := httptest.NewRequest("PUT", "/api/v1/projects/api/apps/backend/envconfig", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "bob", "developer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify persisted by reading back.
	req2 := httptest.NewRequest("GET", "/api/v1/projects/api/apps/backend/envconfig", nil)
	req2.AddCookie(sessionCookieFor(ah, "bob", "developer"))
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("readback: expected 200, got %d", rec2.Code)
	}
	var dto EnvConfigDTO
	mustDecode(t, rec2.Body.Bytes(), &dto)
	if dto.Vars["APP_PORT"] != "8080" {
		t.Errorf("APP_PORT = %q, want %q", dto.Vars["APP_PORT"], "8080")
	}
}

func TestPutAppEnvConfig_ForbiddenForViewer(t *testing.T) {
	mux, ah := newEnvConfigMux()

	body := envConfigBody(map[string]string{"K": "v"}, nil)
	req := httptest.NewRequest("PUT", "/api/v1/projects/api/apps/backend/envconfig", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "carol", "viewer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer should not PUT app envconfig, got %d", rec.Code)
	}
}

func TestPutAppEnvConfig_InvalidBody(t *testing.T) {
	mux, ah := newEnvConfigMux()

	req := httptest.NewRequest("PUT", "/api/v1/projects/api/apps/backend/envconfig",
		bytes.NewBufferString("not json"))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad body, got %d", rec.Code)
	}
}

func TestPutAppEnvConfig_InvalidVarKey(t *testing.T) {
	mux, ah := newEnvConfigMux()

	body := envConfigBody(map[string]string{"1INVALID": "v"}, nil)
	req := httptest.NewRequest("PUT", "/api/v1/projects/api/apps/backend/envconfig", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for invalid key, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ── Level 5 — App-Env ─────────────────────────────────────────────────────────

func TestGetAppEnvEnvConfig_Returns200(t *testing.T) {
	mux, ah := newEnvConfigMux()

	req := httptest.NewRequest("GET", "/api/v1/projects/api/apps/backend/envs/staging/envconfig", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPutAppEnvEnvConfig_Developer_Returns202(t *testing.T) {
	mux, ah := newEnvConfigMux()

	body := envConfigBody(map[string]string{"REPLICA_COUNT": "2"}, nil)
	req := httptest.NewRequest("PUT", "/api/v1/projects/api/apps/backend/envs/staging/envconfig", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "bob", "developer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify persisted.
	req2 := httptest.NewRequest("GET", "/api/v1/projects/api/apps/backend/envs/staging/envconfig", nil)
	req2.AddCookie(sessionCookieFor(ah, "bob", "developer"))
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	var dto EnvConfigDTO
	mustDecode(t, rec2.Body.Bytes(), &dto)
	if dto.Vars["REPLICA_COUNT"] != "2" {
		t.Errorf("REPLICA_COUNT = %q, want %q", dto.Vars["REPLICA_COUNT"], "2")
	}
}

func TestPutAppEnvEnvConfig_ForbiddenForViewer(t *testing.T) {
	mux, ah := newEnvConfigMux()

	body := envConfigBody(map[string]string{"K": "v"}, nil)
	req := httptest.NewRequest("PUT", "/api/v1/projects/api/apps/backend/envs/staging/envconfig", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "carol", "viewer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer should not PUT app-env envconfig, got %d", rec.Code)
	}
}

// ── Resolved endpoint ─────────────────────────────────────────────────────────

func TestGetResolvedEnvConfig_EmptyLevels(t *testing.T) {
	mux, ah := newEnvConfigMux()

	req := httptest.NewRequest("GET", "/api/v1/projects/api/apps/backend/envs/staging/envconfig/resolved", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp ResolvedEnvConfigResponse
	mustDecode(t, rec.Body.Bytes(), &resp)
	if len(resp.Vars) != 0 {
		t.Fatalf("expected 0 vars with all-empty levels, got %d", len(resp.Vars))
	}
}

func TestGetResolvedEnvConfig_MergesLevels(t *testing.T) {
	mux, ah := newEnvConfigMux()

	// Set Org level: LOG_LEVEL=warn, REGION=eu-west-1
	doEnvConfigPut(t, mux, ah, "PUT", "/api/v1/org/envconfig",
		"alice", map[string]string{"LOG_LEVEL": "warn", "REGION": "eu-west-1"})

	// Set App level: LOG_LEVEL=debug (overrides Org), APP_PORT=8080
	doEnvConfigPut(t, mux, ah, "PUT", "/api/v1/projects/api/apps/backend/envconfig",
		"alice", map[string]string{"LOG_LEVEL": "debug", "APP_PORT": "8080"})

	// Set App-Env level: APP_PORT=9090 (overrides App)
	doEnvConfigPut(t, mux, ah, "PUT", "/api/v1/projects/api/apps/backend/envs/staging/envconfig",
		"alice", map[string]string{"APP_PORT": "9090"})

	req := httptest.NewRequest("GET", "/api/v1/projects/api/apps/backend/envs/staging/envconfig/resolved", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp ResolvedEnvConfigResponse
	mustDecode(t, rec.Body.Bytes(), &resp)

	// Build a map for easy lookup.
	byKey := make(map[string]ResolvedEnvVarDTO)
	for _, v := range resp.Vars {
		byKey[v.Key] = v
	}

	// REGION comes from org (no override).
	if region, ok := byKey["REGION"]; !ok {
		t.Error("REGION missing from resolved vars")
	} else {
		if region.Source != envconfig.LevelOrg {
			t.Errorf("REGION.Source = %q, want %q", region.Source, envconfig.LevelOrg)
		}
		if region.Value != "eu-west-1" {
			t.Errorf("REGION.Value = %q, want %q", region.Value, "eu-west-1")
		}
	}

	// LOG_LEVEL: org sets warn, app overrides with debug → source = app.
	if ll, ok := byKey["LOG_LEVEL"]; !ok {
		t.Error("LOG_LEVEL missing from resolved vars")
	} else {
		if ll.Source != envconfig.LevelApp {
			t.Errorf("LOG_LEVEL.Source = %q, want %q", ll.Source, envconfig.LevelApp)
		}
		if ll.Value != "debug" {
			t.Errorf("LOG_LEVEL.Value = %q, want %q", ll.Value, "debug")
		}
	}

	// APP_PORT: app sets 8080, app-env overrides with 9090 → source = app-environment.
	if port, ok := byKey["APP_PORT"]; !ok {
		t.Error("APP_PORT missing from resolved vars")
	} else {
		if port.Source != envconfig.LevelAppEnv {
			t.Errorf("APP_PORT.Source = %q, want %q", port.Source, envconfig.LevelAppEnv)
		}
		if port.Value != "9090" {
			t.Errorf("APP_PORT.Value = %q, want %q", port.Value, "9090")
		}
	}
}

func TestGetResolvedEnvConfig_SecretRefNotExposed(t *testing.T) {
	mux, ah := newEnvConfigMux()

	// Set a secret ref at app level.
	dto := EnvConfigDTO{
		SecretRefs: []SecretRefDTO{
			{Provider: "k8s", Name: "my-secret", Key: "api_key", EnvKey: "API_KEY"},
		},
	}
	b, _ := json.Marshal(dto)
	req := httptest.NewRequest("PUT", "/api/v1/projects/api/apps/backend/envconfig", bytes.NewBuffer(b))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("setup: expected 202, got %d", rec.Code)
	}

	// Resolve.
	req2 := httptest.NewRequest("GET", "/api/v1/projects/api/apps/backend/envs/staging/envconfig/resolved", nil)
	req2.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}

	var resp ResolvedEnvConfigResponse
	mustDecode(t, rec2.Body.Bytes(), &resp)

	for _, v := range resp.Vars {
		if v.Key == "API_KEY" {
			if v.Value != "" {
				t.Errorf("secret value must not be exposed in resolved response, got %q", v.Value)
			}
			if !v.IsSecret {
				t.Error("API_KEY should have isSecret=true")
			}
			return
		}
	}
	t.Error("API_KEY not found in resolved vars")
}

func TestGetResolvedEnvConfig_KeysAreSorted(t *testing.T) {
	mux, ah := newEnvConfigMux()

	// Set several vars at org level in no particular order.
	doEnvConfigPut(t, mux, ah, "PUT", "/api/v1/org/envconfig",
		"alice", map[string]string{"ZEBRA": "z", "ALPHA": "a", "MIDDLE": "m"})

	req := httptest.NewRequest("GET", "/api/v1/projects/api/apps/backend/envs/staging/envconfig/resolved", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var resp ResolvedEnvConfigResponse
	mustDecode(t, rec.Body.Bytes(), &resp)

	keys := make([]string, len(resp.Vars))
	for i, v := range resp.Vars {
		keys[i] = v.Key
	}
	for i := 1; i < len(keys); i++ {
		if keys[i] < keys[i-1] {
			t.Errorf("resolved vars not sorted: %v", keys)
		}
	}
}

func TestGetResolvedEnvConfig_Unauthenticated(t *testing.T) {
	mux, _ := newEnvConfigMux()

	req := httptest.NewRequest("GET", "/api/v1/projects/api/apps/backend/envs/staging/envconfig/resolved", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

// ── Internal helper tests ─────────────────────────────────────────────────────

func TestAppEnvConfig_RoundTrip(t *testing.T) {
	app := &domain.App{
		Name:        "myapp",
		ProjectName: "proj",
	}
	cfg := envconfig.EnvConfig{
		Vars: map[string]string{"KEY": "value"},
	}

	setAppEnvConfig(app, "staging", cfg)

	got := appEnvConfig(app, "staging")
	if got.Vars["KEY"] != "value" {
		t.Errorf("expected KEY=value, got %q", got.Vars["KEY"])
	}

	// Non-existent env returns empty.
	empty := appEnvConfig(app, "prod")
	if !empty.IsEmpty() {
		t.Errorf("expected empty config for absent env, got %+v", empty)
	}
}

func TestEnvCfgForEnvironment_DirectMatch(t *testing.T) {
	envs := []rbac.OrgEnvironment{
		{Name: "staging", EnvConfig: envconfig.EnvConfig{Vars: map[string]string{"ENV": "staging"}}},
		{Name: "prod", EnvConfig: envconfig.EnvConfig{Vars: map[string]string{"ENV": "prod"}}},
	}

	cfg := envCfgForEnvironment(envs, "staging")
	if cfg.Vars["ENV"] != "staging" {
		t.Errorf("expected ENV=staging, got %q", cfg.Vars["ENV"])
	}
}

func TestEnvCfgForEnvironment_PreviewFallback(t *testing.T) {
	envs := []rbac.OrgEnvironment{
		{Name: "staging"},
		{Name: "preview", EnvConfig: envconfig.EnvConfig{Vars: map[string]string{"PREVIEW": "true"}}},
	}

	cfg := envCfgForEnvironment(envs, "pr-42")
	if cfg.Vars["PREVIEW"] != "true" {
		t.Errorf("expected PREVIEW fallback to preview entry, got %+v", cfg)
	}
}

func TestEnvCfgForEnvironment_NoMatch(t *testing.T) {
	envs := []rbac.OrgEnvironment{
		{Name: "staging"},
		{Name: "prod"},
	}

	cfg := envCfgForEnvironment(envs, "canary")
	if !cfg.IsEmpty() {
		t.Errorf("expected empty config for no match, got %+v", cfg)
	}
}

// ── Test utilities ────────────────────────────────────────────────────────────

func mustDecode(t *testing.T, data []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("json decode: %v — body: %s", err, string(data))
	}
}

// doEnvConfigPut is a convenience wrapper for PUT requests in tests.
func doEnvConfigPut(t *testing.T, mux *http.ServeMux, ah *authHandler, method, path, user string, vars map[string]string) {
	t.Helper()
	body := envConfigBody(vars, nil)
	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, user, "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code >= 400 {
		t.Fatalf("%s %s: unexpected error %d: %s", method, path, rec.Code, rec.Body.String())
	}
}
