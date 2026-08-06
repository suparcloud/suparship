package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/suparcloud/suparship/internal/envconfig"
	"github.com/suparcloud/suparship/internal/gitops"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/registry"
	"github.com/suparcloud/suparship/internal/secrets"
	"github.com/suparcloud/suparship/internal/session"
	"github.com/suparcloud/suparship/internal/tpl"
)

func newCredHealthMux(t *testing.T, org *rbac.Org, objs ...runtime.Object) (*http.ServeMux, *authHandler) {
	t.Helper()

	all := []runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: envconfig.SystemNamespace}},
	}
	all = append(all, objs...)
	client := kubefake.NewSimpleClientset(all...)

	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
		cookieSecure:  false,
	}
	ah.registerRoutes(mux)

	var orgProv rbac.OrgProvider
	if org != nil {
		orgProv = &staticOrgProvider{org: org}
	}

	chh := &credentialHealthHandler{
		auth:                  ah,
		kubeClient:            client,
		orgProvider:           orgProv,
		gitopsConfigStore:     gitops.NewConfigStore(client),
		registryStore:         registry.NewStore(client),
		templateRegistryStore: tpl.NewRegistryStore(client),
		logger:                slog.Default(),
	}
	chh.registerRoutes(mux)

	return mux, ah
}

func makeGitOpsCM(yamlContent string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "suparship-gitops-config",
			Namespace: envconfig.SystemNamespace,
			Labels:    map[string]string{"suparship.io/type": "gitops-config"},
		},
		Data: map[string]string{"gitops.yaml": yamlContent},
	}
}

func makeSecret(name string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: envconfig.SystemNamespace,
		},
		Data: map[string][]byte{"token": []byte("test-value")},
	}
}

func TestCredentialHealth_AllNotConfigured(t *testing.T) {
	mux, ah := newCredHealthMux(t, nil)
	cookie := sessionCookieFor(ah, "admin", "org_admin")

	req := httptest.NewRequest("GET", "/api/v1/credentials/health", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}

	var resp CredentialHealthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(resp.Credentials) != 5 {
		t.Fatalf("credentials count = %d, want 5 (gitops, registry, 1password, vault, templates); body = %s", len(resp.Credentials), w.Body.String())
	}
	for _, c := range resp.Credentials {
		if c.Status != credStatusNotConfigured {
			t.Errorf("%s: status = %q, want not_configured", c.Name, c.Status)
		}
	}
	if resp.OverallStatus != credStatusHealthy {
		t.Errorf("overall = %q, want healthy", resp.OverallStatus)
	}
}

func TestCredentialHealth_GitOpsHealthy(t *testing.T) {
	cm := makeGitOpsCM(`provider: "github"
repoURL: "https://github.com/org/repo"
branch: "main"
authSecretRef: "gitops-creds"
`)
	secret := makeSecret("gitops-creds")

	mux, ah := newCredHealthMux(t, nil, cm, secret)
	cookie := sessionCookieFor(ah, "admin", "org_admin")

	req := httptest.NewRequest("GET", "/api/v1/credentials/health", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp CredentialHealthResponse
	json.Unmarshal(w.Body.Bytes(), &resp)

	for _, c := range resp.Credentials {
		if c.Name == "gitops" {
			if c.Status != credStatusHealthy {
				t.Errorf("gitops status = %q, want healthy", c.Status)
			}
			if c.SecretRef != "gitops-creds" {
				t.Errorf("secretRef = %q, want gitops-creds", c.SecretRef)
			}
			return
		}
	}
	t.Fatal("missing gitops in response")
}

func TestCredentialHealth_GitOpsMissing(t *testing.T) {
	cm := makeGitOpsCM(`provider: "github"
repoURL: "https://github.com/org/repo"
authSecretRef: "missing-secret"
`)

	mux, ah := newCredHealthMux(t, nil, cm)
	cookie := sessionCookieFor(ah, "admin", "org_admin")

	req := httptest.NewRequest("GET", "/api/v1/credentials/health", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp CredentialHealthResponse
	json.Unmarshal(w.Body.Bytes(), &resp)

	for _, c := range resp.Credentials {
		if c.Name == "gitops" {
			if c.Status != credStatusMissing {
				t.Errorf("gitops status = %q, want missing", c.Status)
			}
			return
		}
	}
	t.Fatal("missing gitops in response")
}

func TestCredentialHealth_Expiring(t *testing.T) {
	expiry := time.Now().Add(15 * 24 * time.Hour).Format(time.RFC3339)
	cm := makeGitOpsCM(`provider: "github"
repoURL: "https://github.com/org/repo"
authSecretRef: "gitops-creds"
credentialExpiresAt: "` + expiry + `"
`)
	secret := makeSecret("gitops-creds")

	mux, ah := newCredHealthMux(t, nil, cm, secret)
	cookie := sessionCookieFor(ah, "admin", "org_admin")

	req := httptest.NewRequest("GET", "/api/v1/credentials/health", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp CredentialHealthResponse
	json.Unmarshal(w.Body.Bytes(), &resp)

	for _, c := range resp.Credentials {
		if c.Name == "gitops" {
			if c.Status != credStatusWarning {
				t.Errorf("gitops status = %q, want warning", c.Status)
			}
			if c.DaysUntilExpiry == nil || *c.DaysUntilExpiry > 16 {
				t.Errorf("daysUntilExpiry = %v, want ~15", c.DaysUntilExpiry)
			}
			return
		}
	}
	t.Fatal("missing gitops in response")
}

func TestCredentialHealth_Expired(t *testing.T) {
	expiry := time.Now().Add(-1 * 24 * time.Hour).Format(time.RFC3339)
	cm := makeGitOpsCM(`provider: "github"
repoURL: "https://github.com/org/repo"
authSecretRef: "gitops-creds"
credentialExpiresAt: "` + expiry + `"
`)
	secret := makeSecret("gitops-creds")

	mux, ah := newCredHealthMux(t, nil, cm, secret)
	cookie := sessionCookieFor(ah, "admin", "org_admin")

	req := httptest.NewRequest("GET", "/api/v1/credentials/health", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp CredentialHealthResponse
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp.OverallStatus != credStatusExpired {
		t.Errorf("overall = %q, want expired", resp.OverallStatus)
	}
}

func TestCredentialHealth_OnePasswordHealthy(t *testing.T) {
	org := &rbac.Org{
		Name:        "test",
		DisplayName: "Test",
		Teams:       []rbac.Team{{Name: "admins", DisplayName: "Admins", Members: []string{"admin"}}},
		RoleBindings: []rbac.RoleBinding{{Project: "*", Team: "admins", Role: "org_admin"}},
		SecretBackend: secrets.BackendConfig{
			Type: secrets.Backend1Password,
			OnePassword: &secrets.OnePasswordConfig{
				GroupName: "Suparship",
			},
		},
	}
	secret := makeSecret(secrets.SATokenSecretName)

	mux, ah := newCredHealthMux(t, org, secret)
	cookie := sessionCookieFor(ah, "admin", "org_admin")

	req := httptest.NewRequest("GET", "/api/v1/credentials/health", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp CredentialHealthResponse
	json.Unmarshal(w.Body.Bytes(), &resp)

	for _, c := range resp.Credentials {
		if c.Name == "1password" {
			if c.Status != credStatusHealthy {
				t.Errorf("1password status = %q, want healthy", c.Status)
			}
			return
		}
	}
	t.Fatal("missing 1password in response")
}

func makeTemplateRegistryCM(jsonContent string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "suparship-template-registry",
			Namespace: envconfig.SystemNamespace,
			Labels:    map[string]string{"suparship.io/type": "template-registry"},
		},
		Data: map[string]string{"registry.json": jsonContent},
	}
}

func TestCredentialHealth_TemplatesNoExternalSources(t *testing.T) {
	cm := makeTemplateRegistryCM(`{"builtIn":[],"external":[],"sources":[]}`)
	mux, ah := newCredHealthMux(t, nil, cm)
	cookie := sessionCookieFor(ah, "admin", "org_admin")

	req := httptest.NewRequest("GET", "/api/v1/credentials/health", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp CredentialHealthResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	for _, c := range resp.Credentials {
		if c.Name == "templates" {
			if c.Status != credStatusNotConfigured {
				t.Errorf("templates status = %q, want not_configured", c.Status)
			}
			return
		}
	}
	t.Fatalf("missing templates entry; got %+v", resp.Credentials)
}

func TestCredentialHealth_TemplatesPerSource(t *testing.T) {
	// Two sources: one with a present Secret (healthy), one whose Secret
	// is missing from the cluster (the operator deleted it out-of-band
	// or the SealedSecret never decrypted).
	cm := makeTemplateRegistryCM(`{
        "builtIn":[],
        "external":[
          {"name":"acme","repoURL":"https://example.com/r","ref":"main","path":"templates","existingSecret":"suparship-tpl-credentials-acme"},
          {"name":"public","repoURL":"https://example.com/pub","ref":"main","path":"templates"},
          {"name":"orphan","repoURL":"https://example.com/o","ref":"main","path":"templates","existingSecret":"suparship-tpl-credentials-orphan"}
        ],
        "sources":[]
    }`)

	mux, ah := newCredHealthMux(t, nil, cm, makeSecret("suparship-tpl-credentials-acme"))
	cookie := sessionCookieFor(ah, "admin", "org_admin")

	req := httptest.NewRequest("GET", "/api/v1/credentials/health", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp CredentialHealthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	want := map[string]string{
		"templates/acme":   credStatusHealthy,
		"templates/public": credStatusNotConfigured,
		"templates/orphan": credStatusMissing,
	}
	got := map[string]string{}
	for _, c := range resp.Credentials {
		if _, ok := want[c.Name]; ok {
			got[c.Name] = c.Status
		}
	}
	for name, wantStatus := range want {
		if got[name] != wantStatus {
			t.Errorf("%s: status = %q, want %q", name, got[name], wantStatus)
		}
	}

	// One missing source taints overall.
	if resp.OverallStatus != credStatusExpired {
		t.Errorf("overall = %q, want %q (missing template Secret should taint overall)", resp.OverallStatus, credStatusExpired)
	}
}

func TestCredentialHealth_Unauthenticated(t *testing.T) {
	mux, _ := newCredHealthMux(t, nil)

	req := httptest.NewRequest("GET", "/api/v1/credentials/health", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

// ── Vault backend health ────────────────────────────────────────────────────
// These cover the failure that used to be invisible: Vault selected but unusable,
// so writes were silently redirected to the Kubernetes store. Writes now refuse
// (dynamicVaultStore.resolve) and this check is where an operator sees why.

func vaultHealthOrg(cfg *secrets.HCVaultConfig) *rbac.Org {
	return &rbac.Org{
		Name:         "test",
		DisplayName:  "Test",
		Teams:        []rbac.Team{{Name: "admins", DisplayName: "Admins", Members: []string{"admin"}}},
		RoleBindings: []rbac.RoleBinding{{Project: "*", Team: "admins", Role: "org_admin"}},
		SecretBackend: secrets.BackendConfig{
			Type:  secrets.BackendVault,
			Vault: cfg,
		},
	}
}

func vaultCredStatus(t *testing.T, org *rbac.Org, objs ...runtime.Object) CredentialStatus {
	t.Helper()
	mux, ah := newCredHealthMux(t, org, objs...)
	req := httptest.NewRequest("GET", "/api/v1/credentials/health", nil)
	req.AddCookie(sessionCookieFor(ah, "admin", "org_admin"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp CredentialHealthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, c := range resp.Credentials {
		if c.Name == "vault" {
			return c
		}
	}
	t.Fatalf("no vault entry in %s", w.Body.String())
	return CredentialStatus{}
}

// Write token present and every bound cluster sealed → healthy.
func TestCredentialHealth_VaultHealthy(t *testing.T) {
	org := vaultHealthOrg(&secrets.HCVaultConfig{
		Address:       "https://vault.example.com:8200",
		ClusterTokens: []secrets.ClusterTokenRef{{Cluster: "eu-1", Sealed: true}},
	})
	org.Environments = []rbac.OrgEnvironment{{Name: "prod", ClusterRefs: []string{"eu-1"}}}

	got := vaultCredStatus(t, org, makeSecret(secrets.VaultTokenSecretName))
	if got.Status != credStatusHealthy {
		t.Errorf("status = %q (%s), want healthy", got.Status, got.Message)
	}
	if got.SecretRef != secrets.VaultTokenSecretName {
		t.Errorf("secretRef = %q", got.SecretRef)
	}
}

// The exact condition behind "secrets not getting saved in vault": no write
// token. It must report missing and say writes are refused.
func TestCredentialHealth_VaultMissingWriteToken(t *testing.T) {
	org := vaultHealthOrg(&secrets.HCVaultConfig{Address: "https://vault.example.com:8200"})

	got := vaultCredStatus(t, org) // no token Secret seeded
	if got.Status != credStatusMissing {
		t.Errorf("status = %q, want missing", got.Status)
	}
	if !strings.Contains(got.Message, secrets.VaultTokenSecretName) {
		t.Errorf("message should name the missing Secret: %q", got.Message)
	}
	if !strings.Contains(got.Message, "refused") {
		t.Errorf("message should say writes are refused: %q", got.Message)
	}
}

func TestCredentialHealth_VaultMissingAddress(t *testing.T) {
	got := vaultCredStatus(t, vaultHealthOrg(&secrets.HCVaultConfig{}),
		makeSecret(secrets.VaultTokenSecretName))
	if got.Status != credStatusMissing {
		t.Errorf("status = %q, want missing", got.Status)
	}
	if !strings.Contains(got.Message, "address") {
		t.Errorf("message should name the missing address: %q", got.Message)
	}
}

// A nil Vault config with the backend selected is the same unusable state.
func TestCredentialHealth_VaultNilConfig(t *testing.T) {
	got := vaultCredStatus(t, vaultHealthOrg(nil), makeSecret(secrets.VaultTokenSecretName))
	if got.Status != credStatusMissing {
		t.Errorf("status = %q, want missing", got.Status)
	}
}

// Write token fine but a bound cluster has no sealed read token: the control
// plane works, the data plane doesn't. Warning, not missing — and it must name
// the cluster so the operator knows where to paste.
func TestCredentialHealth_VaultUnsealedClusterWarns(t *testing.T) {
	org := vaultHealthOrg(&secrets.HCVaultConfig{
		Address:       "https://vault.example.com:8200",
		ClusterTokens: []secrets.ClusterTokenRef{{Cluster: "eu-1", Sealed: true}},
	})
	org.Environments = []rbac.OrgEnvironment{
		{Name: "prod", ClusterRefs: []string{"eu-1"}},
		{Name: "staging", ClusterRefs: []string{"stg-1"}}, // never sealed
	}

	got := vaultCredStatus(t, org, makeSecret(secrets.VaultTokenSecretName))
	if got.Status != credStatusWarning {
		t.Errorf("status = %q (%s), want warning", got.Status, got.Message)
	}
	if !strings.Contains(got.Message, "stg-1") {
		t.Errorf("message should name the unsealed cluster: %q", got.Message)
	}
	if strings.Contains(got.Message, "eu-1") {
		t.Errorf("message should not list the sealed cluster: %q", got.Message)
	}
}

// A cluster bound to no environment resolves no secrets, so a missing token
// there is not yet a problem.
func TestCredentialHealth_VaultIgnoresUnboundClusters(t *testing.T) {
	org := vaultHealthOrg(&secrets.HCVaultConfig{Address: "https://vault.example.com:8200"})
	org.Environments = nil // nothing bound anywhere

	got := vaultCredStatus(t, org, makeSecret(secrets.VaultTokenSecretName))
	if got.Status != credStatusHealthy {
		t.Errorf("status = %q (%s), want healthy", got.Status, got.Message)
	}
}

// On another backend the entry is informational, not a failure.
func TestCredentialHealth_VaultNotSelected(t *testing.T) {
	org := vaultHealthOrg(&secrets.HCVaultConfig{Address: "https://vault.example.com:8200"})
	org.SecretBackend.Type = secrets.Backend1Password
	org.SecretBackend.OnePassword = &secrets.OnePasswordConfig{}

	got := vaultCredStatus(t, org)
	if got.Status != credStatusNotConfigured {
		t.Errorf("status = %q, want not_configured", got.Status)
	}
}
