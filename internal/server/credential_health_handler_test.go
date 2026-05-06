package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

	if len(resp.Credentials) != 4 {
		t.Fatalf("credentials count = %d, want 4 (gitops, registry, 1password, templates); body = %s", len(resp.Credentials), w.Body.String())
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
