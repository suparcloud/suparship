package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	client_runtime "k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/gitops"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/seal"
	"github.com/suparcloud/suparship/internal/secrets"
	"github.com/suparcloud/suparship/internal/secrets/onepassword"
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
	// App / app-env handlers use k8sUpperWriter against suparship-system; tests
	// give it a fake K8s client so writes don't depend on the per-env namespace
	// existing.
	kc := k8sfake.NewSimpleClientset()

	sh := &secretsHandler{
		orgStore:       orgStore,
		appStore:       appStore,
		backend:        memBE,
		upperWriter:    secrets.NewMemUpperLevelWriter(memBE),
		k8sUpperWriter: secrets.NewUpperLevelSecretWriter(kc),
		auditor:        secrets.NewAuditor(slog.Default()),
		logger:         slog.Default(),
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

// ── Fakes for binding tests ─────────────────────────────────────────────────

type stubSealPublisher struct {
	published []gitops.SealedReadTokenPublishParams
	refreshed []gitops.RefreshSecretStoreParams
}

func (s *stubSealPublisher) PublishSealedReadToken(_ context.Context, params gitops.SealedReadTokenPublishParams) error {
	s.published = append(s.published, params)
	return nil
}

func (s *stubSealPublisher) DeleteSealedReadToken(_ context.Context, _ gitops.DeleteSealedReadTokenParams) error {
	return nil
}

func (s *stubSealPublisher) RefreshSecretStore(_ context.Context, params gitops.RefreshSecretStoreParams) error {
	s.refreshed = append(s.refreshed, params)
	return nil
}

type errClusterStore struct {
	clusters map[string]domain.Cluster
}

func (e *errClusterStore) GetCluster(_ context.Context, name string) (*domain.Cluster, error) {
	c, ok := e.clusters[name]
	if !ok {
		return nil, fmt.Errorf("cluster %q not found", name)
	}
	return &c, nil
}
func (e *errClusterStore) ListClusters(_ context.Context) ([]domain.Cluster, error) { return nil, nil }
func (e *errClusterStore) CreateCluster(_ context.Context, _ domain.Cluster, _ []byte) error {
	return nil
}
func (e *errClusterStore) DeleteCluster(_ context.Context, _ string) error { return nil }

func testCertPEM(t *testing.T) []byte {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "sealed-secrets"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func onePasswordOrg(clusterRef string) *rbac.Org {
	org := testRBACOrg()
	org.Environments = []rbac.OrgEnvironment{
		{Name: "staging", DisplayName: "Staging", Order: 1, ClusterRefs: []string{clusterRef}, ActiveClusterRef: clusterRef},
	}
	org.SecretBackend = secrets.BackendConfig{
		Type:        secrets.Backend1Password,
		OnePassword: &secrets.OnePasswordConfig{},
	}
	return org
}

func newBindingTestMux(t *testing.T, org *rbac.Org, cs domain.ClusterStore, certPEM []byte) (*http.ServeMux, *authHandler) {
	t.Helper()
	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
		cookieSecure:  false,
	}
	ah.registerRoutes(mux)

	appStore := newMemAppStore()
	appStore.addApp(&domain.App{
		Name: "backend", ProjectName: "api",
		Spec: domain.AppSpec{DisplayName: "Backend", Template: domain.AppTemplateRef{Name: "web-service"}},
	})
	appStore.addEnv(&domain.AppEnvironment{
		AppName: "backend", ProjectName: "api", EnvName: "staging",
		EnvType: domain.AppEnvStaging, Namespace: "api-backend-staging",
	})

	certCache := seal.NewMemCertCache()
	if certPEM != nil {
		_ = certCache.Put(context.Background(), org.Environments[0].EffectiveClusterRef(), certPEM)
	}

	orgProvider := &staticOrgProvider{org: org}
	sh := &secretsHandler{
		orgStore:      orgProvider,
		appStore:      appStore,
		backend:       secrets.NewMemBackend(),
		upperWriter:   secrets.NewMemUpperLevelWriter(secrets.NewMemBackend()),
		auditor:       secrets.NewAuditor(slog.Default()),
		logger:        slog.Default(),
		clusterStore:  cs,
		certCache:     certCache,
		sealPublisher: &stubSealPublisher{},
	}

	rh := &rbacHandler{
		auth:           ah,
		orgStore:       orgProvider,
		secretsHandler: sh,
	}
	rh.registerRoutes(mux)
	return mux, ah
}

// ── Binding cluster-resolution tests ────────────────────────────────────────

func TestAddBinding_ClusterNotFound(t *testing.T) {
	cs := &errClusterStore{clusters: map[string]domain.Cluster{}}
	org := onePasswordOrg("nonexistent-cluster")
	certPEM := testCertPEM(t)
	mux, ah := newBindingTestMux(t, org, cs, certPEM)

	body, _ := json.Marshal(AddBindingRequest{
		Env:          "staging",
		VaultID:      "vault-123",
		ConnectToken: "fake-token-value",
	})
	req := httptest.NewRequest("POST", "/api/v1/org/secret-backend/bindings", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for missing cluster, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp errorResponse
	mustDecode(t, rec.Body.Bytes(), &resp)
	if resp.Error == "" {
		t.Fatal("expected non-empty error message")
	}
}

func TestAddBinding_ClusterEmptyAPIServer(t *testing.T) {
	cs := &errClusterStore{clusters: map[string]domain.Cluster{
		"staging-cluster": {Name: "staging-cluster", APIServer: ""},
	}}
	org := onePasswordOrg("staging-cluster")
	certPEM := testCertPEM(t)
	mux, ah := newBindingTestMux(t, org, cs, certPEM)

	body, _ := json.Marshal(AddBindingRequest{
		Env:          "staging",
		VaultID:      "vault-123",
		ConnectToken: "fake-token-value",
	})
	req := httptest.NewRequest("POST", "/api/v1/org/secret-backend/bindings", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for empty apiServer, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ── Platform vault picker ──────────────────────────────────────────────────

func TestSetPlatformVault_PersistsOperatorPick(t *testing.T) {
	// Operator picks an existing 1Password vault that the SA token can see.
	fakeOP := onepassword.NewFakeClient()
	chosen, _ := fakeOP.CreateVault(context.Background(), "company-shared", "")

	org := &rbac.Org{
		Name:          "default",
		SecretBackend: secrets.BackendConfig{Type: secrets.Backend1Password},
	}
	store := &staticOrgProvider{org: org}
	sh := &secretsHandler{
		orgStore:        store,
		logger:          slog.Default(),
		saTokenStore:    &memSATokenStore{token: "fake"},
		saClientFactory: func(_ context.Context, _ string) (onepassword.SAClient, error) { return fakeOP, nil },
	}

	body, _ := json.Marshal(SetPlatformVaultRequest{VaultID: chosen.ID, VaultName: chosen.Title})
	req := httptest.NewRequest("PUT", "/api/v1/org/secret-backend/platform-vault", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	sh.handleSetPlatformVault(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := store.org.SecretBackend.OnePassword.PlatformVaultID; got != chosen.ID {
		t.Errorf("PlatformVaultID = %q, want %q", got, chosen.ID)
	}
	if got := store.org.SecretBackend.OnePassword.PlatformVaultName; got != chosen.Title {
		t.Errorf("PlatformVaultName = %q, want %q", got, chosen.Title)
	}
}

// Regression test: when the operator picks a new platform vault, every
// existing binding's per-cluster ClusterSecretStore in Git must be rewritten
// so the deployed store sees both vaults. Without this, the workload
// cluster's ESO can't resolve org/project items.
func TestSetPlatformVault_RefreshesEachBoundEnvStore(t *testing.T) {
	fakeOP := onepassword.NewFakeClient()
	platform, _ := fakeOP.CreateVault(context.Background(), "company-shared", "")
	org := &rbac.Org{
		Name: "default",
		SecretBackend: secrets.BackendConfig{
			Type: secrets.Backend1Password,
			OnePassword: &secrets.OnePasswordConfig{
				Bindings: []secrets.EnvBinding{
					{Env: "staging", VaultID: "v-stg", Provisioned: true, ConnectEndpoint: "http://op-stg:8080"},
					{Env: "prod", VaultID: "v-prd", Provisioned: true},
					{Env: "preview", VaultID: "v-prev", Provisioned: false}, // unprovisioned — should be skipped
				},
			},
		},
	}
	store := &staticOrgProvider{org: org}
	seal := &stubSealPublisher{}
	sh := &secretsHandler{
		orgStore:        store,
		logger:          slog.Default(),
		saTokenStore:    &memSATokenStore{token: "fake"},
		saClientFactory: func(_ context.Context, _ string) (onepassword.SAClient, error) { return fakeOP, nil },
		sealPublisher:   seal,
	}

	body, _ := json.Marshal(SetPlatformVaultRequest{VaultID: platform.ID})
	req := httptest.NewRequest("PUT", "/api/v1/org/secret-backend/platform-vault", bytes.NewBuffer(body))
	rec := httptest.NewRecorder()
	sh.handleSetPlatformVault(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(seal.refreshed) != 2 {
		t.Fatalf("expected 2 refresh calls (one per provisioned binding), got %d: %+v", len(seal.refreshed), seal.refreshed)
	}
	got := map[string]gitops.RefreshSecretStoreParams{}
	for _, p := range seal.refreshed {
		got[p.Env] = p
	}
	if got["staging"].PlatformVaultID != platform.ID {
		t.Errorf("staging refresh PlatformVaultID = %q, want %q", got["staging"].PlatformVaultID, platform.ID)
	}
	if got["staging"].VaultID != "v-stg" {
		t.Errorf("staging refresh VaultID = %q, want %q", got["staging"].VaultID, "v-stg")
	}
	if got["staging"].ConnectEndpoint != "http://op-stg:8080" {
		t.Errorf("staging refresh ConnectEndpoint = %q, want per-binding override", got["staging"].ConnectEndpoint)
	}
	if got["prod"].PlatformVaultID != platform.ID {
		t.Errorf("prod refresh PlatformVaultID = %q, want %q", got["prod"].PlatformVaultID, platform.ID)
	}
	if _, ok := got["preview"]; ok {
		t.Errorf("unprovisioned binding 'preview' should not have triggered a refresh, got %+v", got["preview"])
	}
}

// Regression test for the hot-swap: before this fix, the cached
// upper-level writer kept PlatformVaultID="" from startup, so any
// subsequent org write through h.upperWriter failed with "platform vault
// not provisioned" until the server was restarted.
func TestSetPlatformVault_RebuildsUpperWriterSoOrgWritesSucceed(t *testing.T) {
	fakeOP := onepassword.NewFakeClient()
	platform, _ := fakeOP.CreateVault(context.Background(), "company-shared", "")

	org := &rbac.Org{
		Name:          "default",
		SecretBackend: secrets.BackendConfig{Type: secrets.Backend1Password},
	}
	store := &staticOrgProvider{org: org}

	// Mimic startup: upper-level writer was built when PlatformVaultID="".
	stalePlatformID := ""
	staleWriter := onepassword.NewSAUpperLevelWriter(onepassword.SAUpperLevelWriterConfig{
		Client:          fakeOP,
		PlatformVaultID: stalePlatformID,
		OrgName:         "default",
	})
	sh := &secretsHandler{
		orgStore:        store,
		logger:          slog.Default(),
		saTokenStore:    &memSATokenStore{token: "fake"},
		saClientFactory: func(_ context.Context, _ string) (onepassword.SAClient, error) { return fakeOP, nil },
		upperWriter:     staleWriter,
	}

	// Sanity: stale writer would refuse the write.
	if err := sh.currentUpperWriter().WriteOrgSecrets(context.Background(), map[string][]byte{"K": []byte("v")}); err == nil {
		t.Fatal("stale writer should fail without PlatformVaultID")
	}

	body, _ := json.Marshal(SetPlatformVaultRequest{VaultID: platform.ID})
	req := httptest.NewRequest("PUT", "/api/v1/org/secret-backend/platform-vault", bytes.NewBuffer(body))
	rec := httptest.NewRecorder()
	sh.handleSetPlatformVault(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// After the picker save, an org write must succeed because the writer
	// was rebuilt with the new PlatformVaultID.
	if err := sh.currentUpperWriter().WriteOrgSecrets(context.Background(), map[string][]byte{"K": []byte("v")}); err != nil {
		t.Fatalf("WriteOrgSecrets after picker save: %v", err)
	}
	items, _ := fakeOP.ListItems(context.Background(), platform.ID)
	if len(items) != 1 {
		t.Errorf("expected 1 item in platform vault after write, got %d", len(items))
	}
}

func TestSetPlatformVault_RejectsUnknownVault(t *testing.T) {
	// Operator submits a vault ID that the SA token can't see — should 422
	// rather than persist a dangling reference.
	fakeOP := onepassword.NewFakeClient()
	org := &rbac.Org{
		Name:          "default",
		SecretBackend: secrets.BackendConfig{Type: secrets.Backend1Password},
	}
	store := &staticOrgProvider{org: org}
	sh := &secretsHandler{
		orgStore:        store,
		logger:          slog.Default(),
		saTokenStore:    &memSATokenStore{token: "fake"},
		saClientFactory: func(_ context.Context, _ string) (onepassword.SAClient, error) { return fakeOP, nil },
	}

	body, _ := json.Marshal(SetPlatformVaultRequest{VaultID: "no-such-vault"})
	req := httptest.NewRequest("PUT", "/api/v1/org/secret-backend/platform-vault", bytes.NewBuffer(body))
	rec := httptest.NewRecorder()
	sh.handleSetPlatformVault(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for unknown vault, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.org.SecretBackend.OnePassword != nil &&
		store.org.SecretBackend.OnePassword.PlatformVaultID != "" {
		t.Error("PlatformVaultID should not be persisted on validation failure")
	}
}

func TestSetPlatformVault_RejectsK8sBackend(t *testing.T) {
	org := &rbac.Org{
		Name:          "default",
		SecretBackend: secrets.BackendConfig{Type: secrets.BackendK8s},
	}
	store := &staticOrgProvider{org: org}
	sh := &secretsHandler{
		orgStore:        store,
		logger:          slog.Default(),
		saTokenStore:    &memSATokenStore{token: "fake"},
		saClientFactory: func(_ context.Context, _ string) (onepassword.SAClient, error) { return onepassword.NewFakeClient(), nil },
	}

	body, _ := json.Marshal(SetPlatformVaultRequest{VaultID: "v1"})
	req := httptest.NewRequest("PUT", "/api/v1/org/secret-backend/platform-vault", bytes.NewBuffer(body))
	rec := httptest.NewRecorder()
	sh.handleSetPlatformVault(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for k8s backend, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSetPlatformVault_MissingVaultIDIs422(t *testing.T) {
	org := &rbac.Org{
		Name:          "default",
		SecretBackend: secrets.BackendConfig{Type: secrets.Backend1Password},
	}
	store := &staticOrgProvider{org: org}
	sh := &secretsHandler{
		orgStore:        store,
		logger:          slog.Default(),
		saTokenStore:    &memSATokenStore{token: "fake"},
		saClientFactory: func(_ context.Context, _ string) (onepassword.SAClient, error) { return onepassword.NewFakeClient(), nil },
	}

	body, _ := json.Marshal(SetPlatformVaultRequest{})
	req := httptest.NewRequest("PUT", "/api/v1/org/secret-backend/platform-vault", bytes.NewBuffer(body))
	rec := httptest.NewRecorder()
	sh.handleSetPlatformVault(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for missing vaultId, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ── Migration to 1Password ──────────────────────────────────────────────────

func TestMigrateToOnePassword_CopiesK8sSecretsIntoVaults(t *testing.T) {
	// Pre-populate K8s suparship-system with upper-level Secrets.
	preload := []*corev1.Secret{
		{
			ObjectMeta: metav1.ObjectMeta{Name: secrets.OrgSecretName(), Namespace: secrets.SystemNamespace},
			Data:       map[string][]byte{"GLOBAL_KEY": []byte("g")},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: secrets.EnvTypeSecretName("staging"), Namespace: secrets.SystemNamespace},
			Data:       map[string][]byte{"DB_URL": []byte("staging-db")},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: secrets.ProjectSecretName("demo"), Namespace: secrets.SystemNamespace},
			Data:       map[string][]byte{"PROJ": []byte("p")},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: secrets.ClusterSecretName("kind-staging"), Namespace: secrets.SystemNamespace},
			Data:       map[string][]byte{"FEATURE_FLAG": []byte("off")},
		},
	}
	objs := make([]client_runtime.Object, 0, len(preload))
	for _, s := range preload {
		objs = append(objs, s)
	}
	kc := k8sfake.NewSimpleClientset(objs...)

	// Org with 1Password backend already provisioned (platform vault + binding).
	fakeOP := onepassword.NewFakeClient()
	platformVault, _ := fakeOP.CreateVault(context.Background(), secrets.PlatformVaultName("default"), "")
	envVault, _ := fakeOP.CreateVault(context.Background(), secrets.VaultName("default", "staging"), "")
	org := &rbac.Org{
		Name: "default",
		Environments: []rbac.OrgEnvironment{
			{Name: "staging", ClusterRefs: []string{"kind-staging"}, ActiveClusterRef: "kind-staging"},
		},
		SecretBackend: secrets.BackendConfig{
			Type: secrets.Backend1Password,
			OnePassword: &secrets.OnePasswordConfig{
				PlatformVaultID: platformVault.ID,
				Bindings: []secrets.EnvBinding{
					{Env: "staging", VaultID: envVault.ID, Provisioned: true},
				},
			},
		},
	}
	store := &staticOrgProvider{org: org}

	sh := &secretsHandler{
		orgStore:        store,
		logger:          slog.Default(),
		k8sUpperWriter:  secrets.NewUpperLevelSecretWriter(kc),
		saTokenStore:    &memSATokenStore{token: "fake-token"},
		saClientFactory: func(_ context.Context, _ string) (onepassword.SAClient, error) { return fakeOP, nil },
	}

	body, _ := json.Marshal(MigrateToOnePasswordRequest{
		EnvTypes: []string{"staging"},
		Projects: []string{"demo"},
		Clusters: []string{"kind-staging"},
	})
	req := httptest.NewRequest("POST", "/api/v1/org/secret-backend/migrate-to-onepassword", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	sh.handleMigrateToOnePassword(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp MigrateToOnePasswordResponse
	mustDecode(t, rec.Body.Bytes(), &resp)
	if resp.OrgKeys != 1 {
		t.Errorf("OrgKeys = %d, want 1", resp.OrgKeys)
	}
	if resp.EnvTypeKeys["staging"] != 1 {
		t.Errorf("EnvTypeKeys[staging] = %d, want 1", resp.EnvTypeKeys["staging"])
	}
	if resp.ProjectKeys["demo"] != 1 {
		t.Errorf("ProjectKeys[demo] = %d, want 1", resp.ProjectKeys["demo"])
	}
	if resp.ClusterKeys["kind-staging"] != 1 {
		t.Errorf("ClusterKeys[kind-staging] = %d, want 1", resp.ClusterKeys["kind-staging"])
	}

	// Confirm vault items were actually created.
	platformItems, _ := fakeOP.ListItems(context.Background(), platformVault.ID)
	if len(platformItems) != 2 { // org + project
		t.Errorf("expected 2 items in platform vault (org + project), got %+v", platformItems)
	}
	envItems, _ := fakeOP.ListItems(context.Background(), envVault.ID)
	if len(envItems) != 2 { // env-type + cluster
		t.Errorf("expected 2 items in env vault (env-type + cluster), got %+v", envItems)
	}
}

func TestMigrateToOnePassword_RejectsWhenBackendStillK8s(t *testing.T) {
	org := &rbac.Org{
		Name:          "default",
		SecretBackend: secrets.BackendConfig{Type: secrets.BackendK8s},
	}
	store := &staticOrgProvider{org: org}

	sh := &secretsHandler{
		orgStore:        store,
		logger:          slog.Default(),
		k8sUpperWriter:  secrets.NewUpperLevelSecretWriter(k8sfake.NewSimpleClientset()),
		saTokenStore:    &memSATokenStore{token: "fake"},
		saClientFactory: func(_ context.Context, _ string) (onepassword.SAClient, error) { return onepassword.NewFakeClient(), nil },
	}

	body, _ := json.Marshal(MigrateToOnePasswordRequest{})
	req := httptest.NewRequest("POST", "/api/v1/org/secret-backend/migrate-to-onepassword", bytes.NewBuffer(body))
	rec := httptest.NewRecorder()
	sh.handleMigrateToOnePassword(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 when backend is still k8s, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMigrateToOnePassword_RejectsWhenPlatformVaultMissing(t *testing.T) {
	org := &rbac.Org{
		Name: "default",
		SecretBackend: secrets.BackendConfig{
			Type:        secrets.Backend1Password,
			OnePassword: &secrets.OnePasswordConfig{}, // no PlatformVaultID
		},
	}
	store := &staticOrgProvider{org: org}

	sh := &secretsHandler{
		orgStore:        store,
		logger:          slog.Default(),
		k8sUpperWriter:  secrets.NewUpperLevelSecretWriter(k8sfake.NewSimpleClientset()),
		saTokenStore:    &memSATokenStore{token: "fake"},
		saClientFactory: func(_ context.Context, _ string) (onepassword.SAClient, error) { return onepassword.NewFakeClient(), nil },
	}

	body, _ := json.Marshal(MigrateToOnePasswordRequest{})
	req := httptest.NewRequest("POST", "/api/v1/org/secret-backend/migrate-to-onepassword", bytes.NewBuffer(body))
	rec := httptest.NewRecorder()
	sh.handleMigrateToOnePassword(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 when platform vault missing, got %d: %s", rec.Code, rec.Body.String())
	}
}

// memSATokenStore is a minimal in-memory SATokenStore for handler tests.
type memSATokenStore struct {
	token string
}

func (m *memSATokenStore) SaveToken(_ context.Context, t string) error { m.token = t; return nil }
func (m *memSATokenStore) LoadToken(_ context.Context) (string, error) { return m.token, nil }

