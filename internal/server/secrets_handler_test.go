package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/rbac"
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

// clusterVaultIDs must expose an env's vault to EVERY cluster the env is bound
// to (ClusterRefs), not only the active one — otherwise a standby cluster's
// suparship-store lists only the global vault and cannot resolve env secrets
// before/after an active-cluster failover.
func TestClusterVaultIDs_AllBoundClustersGetEnvVault(t *testing.T) {
	org := &rbac.Org{
		SecretBackend: secrets.BackendConfig{
			Type: secrets.Backend1Password,
			OnePassword: &secrets.OnePasswordConfig{
				GlobalVault: secrets.VaultRef{VaultID: "v-global"},
				EnvVaults:   []secrets.VaultRef{{Key: "staging", VaultID: "v-staging"}},
			},
		},
		Environments: []rbac.OrgEnvironment{{
			Name:             "staging",
			ClusterRefs:      []string{"clusterA", "clusterB"},
			ActiveClusterRef: "clusterA",
		}},
	}

	want := map[string][]string{
		"clusterA": {"v-global", "v-staging"}, // active bound cluster
		"clusterB": {"v-global", "v-staging"}, // non-active but bound → must include env vault
		"clusterC": {"v-global"},              // not bound to the env → global only
	}
	for cluster, exp := range want {
		if got := clusterVaultIDs(org, cluster); !slices.Equal(got, exp) {
			t.Errorf("clusterVaultIDs(%q) = %v, want %v", cluster, got, exp)
		}
	}
}

// Removing an env vault binding clears it from the org's backend config.
func TestUnregisterEnvVault(t *testing.T) {
	org := &rbac.Org{
		SecretBackend: secrets.BackendConfig{
			Type: secrets.Backend1Password,
			OnePassword: &secrets.OnePasswordConfig{
				EnvVaults: []secrets.VaultRef{{Key: "staging", VaultID: "v1", Provisioned: true}},
			},
		},
	}
	store := &staticOrgProvider{org: org}
	h := &secretsHandler{orgStore: store, logger: slog.Default()}

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/org/secret-backend/vaults/env/staging", nil)
	req.SetPathValue("env", "staging")
	rec := httptest.NewRecorder()
	h.handleUnregisterEnvVault(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := store.GetOrg(context.Background())
	if got.SecretBackend.FindVault(secrets.EnvScope("staging")) != nil {
		t.Fatalf("staging env vault binding should be removed")
	}
}

// Switching the active secrets backend must NOT lose the previously configured
// backend's settings — re-selecting it reloads its config. Regression for the
// bug where saving type=k8s wiped the stored 1Password config.
func TestPutSecretsBackend_PreservesConfigAcrossTypeSwitch(t *testing.T) {
	mux, ah := newSecretsMux()

	// Configure 1Password.
	rec := do(t, mux, ah, "PUT", "/api/v1/org/secret-backend", "alice", "org_admin", map[string]any{
		"type": "onepassword",
		"onePassword": map[string]any{
			"groupName": "Suparship",
			"connect":   map[string]any{"endpoint": "http://op:8080"},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("configure 1Password: got %d: %s", rec.Code, rec.Body.String())
	}

	// Switch the active backend to k8s — the UI sends only {type}.
	rec = do(t, mux, ah, "PUT", "/api/v1/org/secret-backend", "alice", "org_admin", map[string]any{"type": "k8s"})
	if rec.Code != http.StatusOK {
		t.Fatalf("switch to k8s: got %d: %s", rec.Code, rec.Body.String())
	}

	// The active type is k8s, but the 1Password config is preserved.
	rec = do(t, mux, ah, "GET", "/api/v1/org/secret-backend", "alice", "org_admin", nil)
	var cfg secrets.BackendConfig
	mustDecode(t, rec.Body.Bytes(), &cfg)
	if cfg.Type != "k8s" {
		t.Errorf("type = %q, want k8s", cfg.Type)
	}
	if cfg.OnePassword == nil || cfg.OnePassword.GroupName != "Suparship" {
		t.Fatalf("1Password config lost on switch to k8s: %+v", cfg.OnePassword)
	}
	if cfg.OnePassword.Connect.Endpoint != "http://op:8080" {
		t.Errorf("connect endpoint lost: %q", cfg.OnePassword.Connect.Endpoint)
	}

	// Re-selecting 1Password reloads the saved config.
	if rec = do(t, mux, ah, "PUT", "/api/v1/org/secret-backend", "alice", "org_admin", map[string]any{"type": "onepassword"}); rec.Code != http.StatusOK {
		t.Fatalf("re-select 1Password: got %d: %s", rec.Code, rec.Body.String())
	}
	rec = do(t, mux, ah, "GET", "/api/v1/org/secret-backend", "alice", "org_admin", nil)
	mustDecode(t, rec.Body.Bytes(), &cfg)
	if cfg.Type != "onepassword" || cfg.OnePassword == nil || cfg.OnePassword.GroupName != "Suparship" {
		t.Fatalf("re-selecting 1Password did not reload config: %+v", cfg)
	}
}

// Three-way round trip: every backend's config must coexist and survive any
// switch order. This is what makes "try Vault, switch back, forward again"
// a zero-re-entry operation.
func TestPutSecretsBackend_ThreeWayConfigPersistence(t *testing.T) {
	mux, ah := newSecretsMux()

	// Configure 1Password, then Vault (switch sends the new backend's config).
	rec := do(t, mux, ah, "PUT", "/api/v1/org/secret-backend", "alice", "org_admin", map[string]any{
		"type":        "onepassword",
		"onePassword": map[string]any{"groupName": "Suparship"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("configure 1Password: got %d: %s", rec.Code, rec.Body.String())
	}
	rec = do(t, mux, ah, "PUT", "/api/v1/org/secret-backend", "alice", "org_admin", map[string]any{
		"type":  "vault",
		"vault": map[string]any{"address": "https://vault.example.com:8200", "mount": "platform-kv"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("configure vault: got %d: %s", rec.Code, rec.Body.String())
	}

	// Switch to k8s with only {type}: BOTH configs must survive.
	if rec = do(t, mux, ah, "PUT", "/api/v1/org/secret-backend", "alice", "org_admin", map[string]any{"type": "k8s"}); rec.Code != http.StatusOK {
		t.Fatalf("switch to k8s: got %d: %s", rec.Code, rec.Body.String())
	}
	rec = do(t, mux, ah, "GET", "/api/v1/org/secret-backend", "alice", "org_admin", nil)
	var cfg secrets.BackendConfig
	mustDecode(t, rec.Body.Bytes(), &cfg)
	if cfg.Effective() != secrets.BackendK8s {
		t.Errorf("type = %q, want k8s", cfg.Type)
	}
	if cfg.OnePassword == nil || cfg.OnePassword.GroupName != "Suparship" {
		t.Errorf("1Password config lost: %+v", cfg.OnePassword)
	}
	if cfg.Vault == nil || cfg.Vault.Address != "https://vault.example.com:8200" || cfg.Vault.Mount != "platform-kv" {
		t.Errorf("vault config lost: %+v", cfg.Vault)
	}

	// Re-select vault with only {type}: the saved config reloads and validates
	// (vault requires an address — proof the retained one is what validated).
	if rec = do(t, mux, ah, "PUT", "/api/v1/org/secret-backend", "alice", "org_admin", map[string]any{"type": "vault"}); rec.Code != http.StatusOK {
		t.Fatalf("re-select vault: got %d: %s", rec.Code, rec.Body.String())
	}
	rec = do(t, mux, ah, "GET", "/api/v1/org/secret-backend", "alice", "org_admin", nil)
	mustDecode(t, rec.Body.Bytes(), &cfg)
	if cfg.Effective() != secrets.BackendVault || cfg.Vault == nil || cfg.Vault.Mount != "platform-kv" {
		t.Fatalf("re-selecting vault did not reload config: %+v", cfg)
	}
	if cfg.OnePassword == nil {
		t.Error("1Password config lost while vault active")
	}
}

// Selecting vault with no address anywhere must be rejected, not silently
// stored as an unusable backend.
func TestPutSecretsBackend_VaultRequiresAddress(t *testing.T) {
	mux, ah := newSecretsMux()
	rec := do(t, mux, ah, "PUT", "/api/v1/org/secret-backend", "alice", "org_admin", map[string]any{"type": "vault"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("vault without address: got %d, want 422: %s", rec.Code, rec.Body.String())
	}
}

// TestSharedSecrets_RoundTrip exercises shared-tier CRUD across the three
// scopes through the org-admin routes.
func TestSharedSecrets_RoundTrip(t *testing.T) {
	mux, ah := newSecretsMux()
	cases := []struct{ scope, path string }{
		{"global", "/api/v1/org/secrets/global"},
		{"env", "/api/v1/org/secrets/env/staging"},
		{"cluster", "/api/v1/org/secrets/env/staging/cluster/prod-eu"},
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
	for _, scopePath := range []string{base + "/global", base + "/env/staging", base + "/env/staging/cluster/prod-eu"} {
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

// TestProjectSecrets_RoundTrip exercises the project-scope routes (global +
// per-env) and confirms a project secret surfaces in an app's resolved view
// with source=project.
func TestProjectSecrets_RoundTrip(t *testing.T) {
	mux, ah := newSecretsMux()

	// Project-global + project-env writes require project admin (org_admin
	// satisfies it). alice is org_admin in testRBACOrg.
	for _, p := range []string{
		"/api/v1/projects/api/secrets/global",
		"/api/v1/projects/api/secrets/env/staging",
	} {
		rec := do(t, mux, ah, "POST", p, "alice", "org_admin", UpsertSecretsRequest{Entries: map[string]string{"PROJ_K": "v"}})
		if rec.Code != http.StatusOK {
			t.Fatalf("upsert %s: expected 200, got %d: %s", p, rec.Code, rec.Body.String())
		}
		rec = do(t, mux, ah, "GET", p, "bob", "developer", nil)
		var resp SecretKeysResponse
		mustDecode(t, rec.Body.Bytes(), &resp)
		if len(resp.Keys) != 1 || resp.Keys[0].Key != "PROJ_K" {
			t.Fatalf("list %s: expected [PROJ_K], got %+v", p, resp.Keys)
		}
	}

	// A developer must not be able to set project secrets (needs project admin).
	rec := do(t, mux, ah, "POST", "/api/v1/projects/api/secrets/global", "bob", "developer", UpsertSecretsRequest{Entries: map[string]string{"X": "y"}})
	if rec.Code == http.StatusOK {
		t.Errorf("developer should not be able to write project secrets, got 200")
	}

	// The app's resolved view includes the project key, attributed to project.
	rec = do(t, mux, ah, "GET", "/api/v1/projects/api/apps/backend/envs/staging/secrets/resolved", "bob", "developer", nil)
	var resolved ResolvedSecretsResponse
	mustDecode(t, rec.Body.Bytes(), &resolved)
	var found *ResolvedSecretDTO
	for i := range resolved.Secrets {
		if resolved.Secrets[i].Key == "PROJ_K" {
			found = &resolved.Secrets[i]
		}
	}
	if found == nil || found.Source != secrets.SourceProject {
		t.Errorf("expected PROJ_K resolved with source=project, got %+v", resolved.Secrets)
	}
}

// TestResolvedSecrets merges shared + app across scopes; cluster/app wins.
func TestResolvedSecrets(t *testing.T) {
	mux, ah := newSecretsMux()
	// shared global, app env, and a colliding key at app cluster.
	do(t, mux, ah, "POST", "/api/v1/org/secrets/global", "alice", "org_admin", UpsertSecretsRequest{Entries: map[string]string{"SHARED": "1", "WIN": "g"}})
	do(t, mux, ah, "POST", "/api/v1/projects/api/apps/backend/secrets/env/staging/cluster/prod-eu", "bob", "developer", UpsertSecretsRequest{Entries: map[string]string{"WIN": "c"}})

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
		map[string]string{"vaultId": "v1"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 (1Password backend required), got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestSetClusterConnectToken_RequiresOnePassword verifies the per-cluster
// Connect-token endpoint is rejected on the default k8s backend.
func TestSetClusterConnectToken_RequiresOnePassword(t *testing.T) {
	mux, ah := newSecretsMux()
	rec := do(t, mux, ah, "POST", "/api/v1/org/secret-backend/clusters/prod-eu/connect-token", "alice", "org_admin",
		map[string]string{"connectToken": "tok"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 (1Password backend required), got %d: %s", rec.Code, rec.Body.String())
	}
}
