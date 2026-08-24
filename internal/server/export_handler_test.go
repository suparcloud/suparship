package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"sort"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	kubefake "k8s.io/client-go/kubernetes/fake"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/envconfig"
	"github.com/suparcloud/suparship/internal/gitops"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/registry"
	"github.com/suparcloud/suparship/internal/seal"
	"github.com/suparcloud/suparship/internal/secrets"
	"github.com/suparcloud/suparship/internal/session"
	"github.com/suparcloud/suparship/internal/tpl"
)

type fakeClusterStore struct {
	clusters []domain.Cluster
}

func (f *fakeClusterStore) ListClusters(_ context.Context) ([]domain.Cluster, error) {
	return f.clusters, nil
}

func (f *fakeClusterStore) GetCluster(_ context.Context, name string) (*domain.Cluster, error) {
	for _, c := range f.clusters {
		if c.Name == name {
			return &c, nil
		}
	}
	return nil, nil
}

func (f *fakeClusterStore) CreateCluster(_ context.Context, _ domain.Cluster, _ []byte) error {
	return nil
}

func (f *fakeClusterStore) DeleteCluster(_ context.Context, _ string) error {
	return nil
}

func newExportMux(t *testing.T) (*http.ServeMux, *authHandler) {
	t.Helper()

	client := kubefake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: envconfig.SystemNamespace}},
	)

	org := &rbac.Org{
		Name:        "testorg",
		DisplayName: "Test Organization",
		Environments: []rbac.OrgEnvironment{
			{Name: "staging", DisplayName: "Staging", Order: 1, ClusterRefs: []string{"in-cluster"}, ActiveClusterRef: "in-cluster", BaseDomain: "staging.local"},
			{Name: "prod", Order: 2},
		},
		Teams:        []rbac.Team{{Name: "admins", DisplayName: "Admins", Members: []string{"admin"}}},
		RoleBindings: []rbac.RoleBinding{{Project: "*", Team: "admins", Role: "org_admin"}},
		Auth: rbac.AuthConfig{OIDC: &rbac.OIDCConfig{
			Enabled:         true,
			IssuerURL:       "https://idp.example.com",
			ClientID:        "suparship",
			ClientSecretRef: rbac.SecretKeyRef{Name: "suparship-oidc", Key: "client-secret"},
			RedirectURL:     "https://suparship.example.com/api/v1/auth/oidc/callback",
		}},
		SecretBackend: secrets.BackendConfig{
			Type: secrets.Backend1Password,
			OnePassword: &secrets.OnePasswordConfig{
				GroupName: "Suparship",
			},
		},
	}

	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
		cookieSecure:  false,
	}
	ah.registerRoutes(mux)

	eh := &exportHandler{
		auth:                  ah,
		orgProvider:           &staticOrgProvider{org: org},
		clusterStore:          &fakeClusterStore{clusters: []domain.Cluster{{Name: "in-cluster", DisplayName: "Local", APIServer: "https://kubernetes.default.svc"}}},
		gitopsConfigStore:     gitops.NewConfigStore(client),
		registryStore:         registry.NewStore(client),
		templateRegistryStore: tpl.NewRegistryStore(client),
		logger:                slog.Default(),
	}
	eh.registerRoutes(mux)

	return mux, ah
}

// newSealedExportMux is newExportMux plus the deps the sealed export needs:
// the fake clientset (to seed credential Secrets) and the handler (to stub
// fetchCert with a self-generated cert).
func newSealedExportMux(t *testing.T) (*http.ServeMux, *authHandler, *kubefake.Clientset, *exportHandler) {
	t.Helper()

	client := kubefake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: envconfig.SystemNamespace}},
	)
	org := &rbac.Org{
		Name:        "testorg",
		DisplayName: "Test Organization",
		Teams:       []rbac.Team{{Name: "admins", DisplayName: "Admins", Members: []string{"admin"}}},
		RoleBindings: []rbac.RoleBinding{
			{Project: "*", Team: "admins", Role: "org_admin"},
		},
		Auth: rbac.AuthConfig{OIDC: &rbac.OIDCConfig{
			Enabled:         true,
			IssuerURL:       "https://idp.example.com",
			ClientID:        "suparship",
			ClientSecretRef: rbac.SecretKeyRef{Name: "custom-oidc-secret", Key: "client-secret"},
			RedirectURL:     "https://suparship.example.com/cb",
		}},
	}

	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
		orgProvider:   &staticOrgProvider{org: org},
	}
	ah.registerRoutes(mux)

	eh := &exportHandler{
		auth:                  ah,
		orgProvider:           &staticOrgProvider{org: org},
		clusterStore:          &fakeClusterStore{clusters: []domain.Cluster{{Name: "in-cluster"}}},
		gitopsConfigStore:     gitops.NewConfigStore(client),
		registryStore:         registry.NewStore(client),
		templateRegistryStore: tpl.NewRegistryStore(client),
		logger:                slog.Default(),
		kubeClient:            client,
	}
	eh.registerRoutes(mux)
	return mux, ah, client, eh
}

// testSealingCertPEM generates a throwaway self-signed RSA cert kubeseal can
// encrypt against.
func testSealingCertPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "sealed-secrets-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func seedSecret(t *testing.T, client *kubefake.Clientset, name string, data map[string][]byte) {
	t.Helper()
	_, err := client.CoreV1().Secrets(envconfig.SystemNamespace).Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: envconfig.SystemNamespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       data,
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
}

func TestExportHandler_SealedSecrets(t *testing.T) {
	if _, err := exec.LookPath("kubeseal"); err != nil {
		t.Skip("kubeseal not on PATH")
	}
	mux, ah, client, eh := newSealedExportMux(t)
	certPEM := testSealingCertPEM(t)
	eh.fetchCert = func(_ context.Context, _ kubernetes.Interface) ([]byte, error) { return certPEM, nil }

	seedSecret(t, client, gitops.ManagedCredentialSecretName, map[string][]byte{"token": []byte("git-cred")})
	seedSecret(t, client, secrets.VaultTokenSecretName, map[string][]byte{"token": []byte("vault-root")})
	seedSecret(t, client, "custom-oidc-secret", map[string][]byte{"client-secret": []byte("oidc-secret")})

	cookie := sessionCookieFor(ah, "admin", "org_admin")
	req := httptest.NewRequest("GET", "/api/v1/org/export?includeSecrets=1", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var vals helmValues
	if err := json.Unmarshal(w.Body.Bytes(), &vals); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(vals.ExtraObjects) != 3 {
		t.Fatalf("extraObjects = %d, want 3 (the seeded secrets; missing ones skipped)", len(vals.ExtraObjects))
	}
	var names []string
	for _, obj := range vals.ExtraObjects {
		if kind, _ := obj["kind"].(string); kind != "SealedSecret" {
			t.Errorf("kind = %v, want SealedSecret", obj["kind"])
		}
		meta, _ := obj["metadata"].(map[string]any)
		names = append(names, meta["name"].(string))
		spec, _ := obj["spec"].(map[string]any)
		if enc, _ := spec["encryptedData"].(map[string]any); len(enc) == 0 {
			t.Errorf("object %v has no encryptedData", meta["name"])
		}
	}
	want := []string{"custom-oidc-secret", gitops.ManagedCredentialSecretName, secrets.VaultTokenSecretName}
	sort.Strings(names)
	for i, n := range want {
		if names[i] != n {
			t.Errorf("sealed names = %v, want %v", names, want)
			break
		}
	}

	// YAML format renders a parseable document containing the same objects.
	req = httptest.NewRequest("GET", "/api/v1/org/export?includeSecrets=1&format=yaml", nil)
	req.AddCookie(cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("yaml status = %d: %s", w.Code, w.Body.String())
	}
	var doc map[string]any
	if err := sigyaml.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("exported YAML does not parse: %v\n%s", err, w.Body.String())
	}
	objs, _ := doc["extraObjects"].([]any)
	if len(objs) != 3 {
		t.Fatalf("yaml extraObjects = %d, want 3", len(objs))
	}

	// Without the param: no extraObjects, banner keeps the no-secrets wording.
	req = httptest.NewRequest("GET", "/api/v1/org/export?format=yaml", nil)
	req.AddCookie(cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if strings.Contains(w.Body.String(), "extraObjects:") {
		t.Error("plain export must not include extraObjects")
	}
	if !strings.Contains(w.Body.String(), "Secret values are NOT included") {
		t.Error("plain export should keep its no-secrets banner")
	}
}

func TestExportHandler_SealedSecretsControllerMissing(t *testing.T) {
	mux, ah, _, eh := newSealedExportMux(t)
	eh.fetchCert = func(_ context.Context, _ kubernetes.Interface) ([]byte, error) {
		return nil, seal.ErrControllerNotFound
	}
	cookie := sessionCookieFor(ah, "admin", "org_admin")
	req := httptest.NewRequest("GET", "/api/v1/org/export?includeSecrets=1", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want 412: %s", w.Code, w.Body.String())
	}
}

// The export (plain AND sealed) is org_admin-gated now — it reveals the full
// platform topology and, sealed, the credential bundle.
func TestExportHandler_RequiresOrgAdmin(t *testing.T) {
	mux, ah, _, _ := newSealedExportMux(t)
	cookie := sessionCookieFor(ah, "mallory", "viewer")
	for _, path := range []string{"/api/v1/org/export", "/api/v1/org/export?includeSecrets=1"} {
		req := httptest.NewRequest("GET", path, nil)
		req.AddCookie(cookie)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", path, w.Code)
		}
	}
}

func TestExportHandler_JSON(t *testing.T) {
	mux, ah := newExportMux(t)
	cookie := sessionCookieFor(ah, "admin", "org_admin")

	req := httptest.NewRequest("GET", "/api/v1/org/export", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("content-type = %q, want application/json", ct)
	}

	var vals helmValues
	if err := json.Unmarshal(w.Body.Bytes(), &vals); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if vals.Org.Name != "testorg" {
		t.Errorf("org name = %q, want testorg", vals.Org.Name)
	}
	if len(vals.Environments) != 2 {
		t.Errorf("environments = %d, want 2", len(vals.Environments))
	}
	if len(vals.Clusters) != 1 {
		t.Errorf("clusters = %d, want 1", len(vals.Clusters))
	}
	if vals.Secrets.Backend != "onepassword" {
		t.Errorf("secrets backend = %q, want onepassword", vals.Secrets.Backend)
	}
	if vals.Secrets.OnePassword == nil {
		t.Fatal("expected onePassword config")
	}
	if vals.Secrets.OnePassword.GroupName != "Suparship" {
		t.Errorf("1password groupName = %q, want Suparship", vals.Secrets.OnePassword.GroupName)
	}
}

func TestExportHandler_YAML(t *testing.T) {
	mux, ah := newExportMux(t)
	cookie := sessionCookieFor(ah, "admin", "org_admin")

	req := httptest.NewRequest("GET", "/api/v1/org/export?format=yaml", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/yaml") {
		t.Errorf("content-type = %q, want text/yaml", ct)
	}

	body := w.Body.String()
	checks := []string{
		"org:", "name: testorg",
		"environments:", "name: staging", "name: prod",
		"clusters:", "name: in-cluster",
		"secrets:", "backend: onepassword",
		"onePassword:", "groupName: Suparship",
	}
	for _, c := range checks {
		if !strings.Contains(body, c) {
			t.Errorf("YAML missing %q", c)
		}
	}

	disp := w.Header().Get("Content-Disposition")
	if !strings.Contains(disp, "values.yaml") {
		t.Errorf("Content-Disposition = %q, want attachment with values.yaml", disp)
	}
}

func TestExportHandler_AuthTeamsRBAC(t *testing.T) {
	mux, ah := newExportMux(t)
	cookie := sessionCookieFor(ah, "admin", "org_admin")

	// JSON: structured assertions, and no secret value present.
	req := httptest.NewRequest("GET", "/api/v1/org/export", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var vals helmValues
	if err := json.Unmarshal(w.Body.Bytes(), &vals); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(vals.Teams) != 1 || vals.Teams[0].Name != "admins" {
		t.Errorf("teams not exported: %+v", vals.Teams)
	}
	if len(vals.RoleBindings) != 1 || vals.RoleBindings[0].Role != "org_admin" {
		t.Errorf("roleBindings not exported: %+v", vals.RoleBindings)
	}
	if vals.Auth == nil || vals.Auth.OIDC == nil || !vals.Auth.OIDC.Enabled {
		t.Fatalf("auth.oidc not exported: %+v", vals.Auth)
	}
	if vals.Auth.OIDC.ClientSecretRef.Name != "suparship-oidc" {
		t.Errorf("clientSecretRef not exported: %+v", vals.Auth.OIDC.ClientSecretRef)
	}

	// YAML: sections present, secret value never leaks.
	yreq := httptest.NewRequest("GET", "/api/v1/org/export?format=yaml", nil)
	yreq.AddCookie(cookie)
	yw := httptest.NewRecorder()
	mux.ServeHTTP(yw, yreq)
	body := yw.Body.String()
	for _, c := range []string{
		"teams:", "name: admins",
		"roleBindings:", "role: org_admin",
		"auth:", "oidc:", "issuerURL:", "clientSecretRef:", "name: suparship-oidc",
	} {
		if !strings.Contains(body, c) {
			t.Errorf("YAML missing %q", c)
		}
	}
}

func TestExportHandler_Unauthenticated(t *testing.T) {
	mux, _ := newExportMux(t)

	req := httptest.NewRequest("GET", "/api/v1/org/export", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestYamlQ(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", `""`},
		{"simple", "simple"},
		{"has:colon", `"has:colon"`},
		{"has space", "has space"},
		{"true", `"true"`},
		{"false", `"false"`},
		{"null", `"null"`},
		{"https://github.com/org/repo", `"https://github.com/org/repo"`},
	}
	for _, tt := range tests {
		got := yamlQ(tt.in)
		if got != tt.want {
			t.Errorf("yamlQ(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestToYAML_FullConfig(t *testing.T) {
	vals := helmValues{
		Org: helmOrg{Name: "myorg", DisplayName: "My Org"},
		Environments: []helmEnvironment{
			{Name: "staging", DisplayName: "Staging", Order: 1, ClusterRefs: []string{"in-cluster"}, ActiveClusterRef: "in-cluster", BaseDomain: "staging.example.com"},
			{Name: "prod", Order: 2},
		},
		Clusters: []helmCluster{
			{Name: "in-cluster", DisplayName: "Local", APIServer: "https://kubernetes.default.svc", InCluster: true},
		},
		GitOps: &helmGitOps{
			Provider:       "github",
			RepoURL:        "https://github.com/org/repo",
			Branch:         "main",
			InitializeRepo: true,
			ExistingSecret: "gitops-creds",
			GitHub:         &helmGitHub{AppID: "12345"},
		},
		Secrets: helmSecrets{
			Backend: "onepassword",
			OnePassword: &helmOnePassword{
				GroupName: "suparship-secrets",
			},
		},
		Registry: &helmRegistry{
			Enabled:        true,
			URL:            "ghcr.io",
			Username:       "robot",
			ExistingSecret: "reg-creds",
			Environments:   []string{"staging", "prod"},
		},
		Templates: &helmTemplates{
			BuiltIn: []string{"web-service", "color-app"},
			External: []helmExternalTemplateRepo{
				{Name: "corp-tpl", RepoURL: "https://github.com/corp/tpl.git", Ref: "v1.0.0", Path: "templates/"},
			},
		},
	}

	yaml := toYAML(vals)

	checks := []string{
		"org:", "name: myorg",
		"environments:", "name: staging", "name: prod",
		"clusters:", "inCluster: true",
		"gitops:", "provider: github",
		"secrets:", "backend: onepassword",
		"onePassword:", "groupName: suparship-secrets",
		"registry:", "enabled: true",
		"templates:", "builtIn:", "external:",
	}
	for _, c := range checks {
		if !strings.Contains(yaml, c) {
			t.Errorf("YAML missing %q", c)
		}
	}
}

func TestSecretBackendTypes_MatchHelmValues(t *testing.T) {
	helmTypes := map[secrets.BackendType]bool{
		"k8s":         true,
		"onepassword": true,
		"vault":       true,
	}
	for bt := range secrets.ValidBackendTypes {
		if !helmTypes[bt] {
			t.Errorf("backend type %q not in expected Helm values set", bt)
		}
	}
}
