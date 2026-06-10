package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/suparcloud/suparship/internal/secrets"
	"github.com/suparcloud/suparship/internal/session"
)

// newRBACWriteMux builds a mux with the team/role-binding/OIDC write routes,
// a mutable in-memory org store, and a fake Kubernetes client (for the OIDC
// client-secret Secret). Returns the store + kube client so tests can observe
// the persisted effects.
func newRBACWriteMux() (*http.ServeMux, *authHandler, *staticOrgProvider, *fake.Clientset) {
	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
		cookieSecure:  false,
	}
	ah.registerRoutes(mux)

	store := &staticOrgProvider{org: testRBACOrg()}
	kube := fake.NewSimpleClientset()
	rh := &rbacHandler{auth: ah, orgStore: store, kubeClient: kube}
	rh.registerRoutes(mux)
	return mux, ah, store, kube
}

func doJSON(mux *http.ServeMux, cookie *http.Cookie, method, path string, body any) *httptest.ResponseRecorder {
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.AddCookie(cookie)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

// --- Teams ---

func TestCreateTeam(t *testing.T) {
	mux, ah, store, _ := newRBACWriteMux()
	cookie := sessionCookieFor(ah, "alice", "org_admin")

	rec := doJSON(mux, cookie, "POST", "/api/v1/teams",
		upsertTeamRequest{Name: "platform", DisplayName: "Platform", Members: []string{"dave"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	found := false
	for _, team := range store.org.Teams {
		if team.Name == "platform" && len(team.Members) == 1 && team.Members[0] == "dave" {
			found = true
		}
	}
	if !found {
		t.Errorf("team not persisted: %+v", store.org.Teams)
	}
}

func TestCreateTeam_DuplicateConflict(t *testing.T) {
	mux, ah, _, _ := newRBACWriteMux()
	rec := doJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "POST", "/api/v1/teams",
		upsertTeamRequest{Name: "devs"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for duplicate team, got %d", rec.Code)
	}
}

func TestDeleteTeam_RejectedWhenReferencedByBinding(t *testing.T) {
	mux, ah, _, _ := newRBACWriteMux()
	// "devs" is referenced by the api/developer binding in testRBACOrg.
	rec := doJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "DELETE", "/api/v1/teams/devs", nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 deleting a referenced team, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteTeam_OK(t *testing.T) {
	mux, ah, store, _ := newRBACWriteMux()
	cookie := sessionCookieFor(ah, "alice", "org_admin")
	// Create an unreferenced team, then delete it.
	if rec := doJSON(mux, cookie, "POST", "/api/v1/teams", upsertTeamRequest{Name: "temp"}); rec.Code != http.StatusCreated {
		t.Fatalf("setup create failed: %d %s", rec.Code, rec.Body.String())
	}
	rec := doJSON(mux, cookie, "DELETE", "/api/v1/teams/temp", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	for _, team := range store.org.Teams {
		if team.Name == "temp" {
			t.Error("team not removed")
		}
	}
}

// --- Role bindings ---

func TestCreateRoleBinding(t *testing.T) {
	mux, ah, store, _ := newRBACWriteMux()
	rec := doJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "POST", "/api/v1/role-bindings",
		RoleBindingDTO{Project: "api", Team: "viewers", Role: "viewer"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	found := false
	for _, rb := range store.org.RoleBindings {
		if rb.Project == "api" && rb.Team == "viewers" && string(rb.Role) == "viewer" {
			found = true
		}
	}
	if !found {
		t.Error("binding not persisted")
	}
}

func TestCreateRoleBinding_GroupBased(t *testing.T) {
	mux, ah, _, _ := newRBACWriteMux()
	rec := doJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "POST", "/api/v1/role-bindings",
		RoleBindingDTO{Project: "*", Group: "platform-engineers", Role: "developer"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 for group binding, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateRoleBinding_InvalidRole(t *testing.T) {
	mux, ah, _, _ := newRBACWriteMux()
	rec := doJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "POST", "/api/v1/role-bindings",
		RoleBindingDTO{Project: "api", Team: "devs", Role: "superuser"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for unknown role, got %d", rec.Code)
	}
}

func TestCreateRoleBinding_BothTeamAndGroup(t *testing.T) {
	mux, ah, _, _ := newRBACWriteMux()
	rec := doJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "POST", "/api/v1/role-bindings",
		RoleBindingDTO{Project: "api", Team: "devs", Group: "g", Role: "developer"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 when both team and group set, got %d", rec.Code)
	}
}

func TestDeleteRoleBinding(t *testing.T) {
	mux, ah, store, _ := newRBACWriteMux()
	rec := doJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "DELETE",
		"/api/v1/role-bindings?project=api&team=devs&role=developer", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	for _, rb := range store.org.RoleBindings {
		if rb.Project == "api" && rb.Team == "devs" {
			t.Error("binding not removed")
		}
	}
}

func TestListRoleBindings(t *testing.T) {
	mux, ah, _, _ := newRBACWriteMux()
	rec := doJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "GET", "/api/v1/role-bindings", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp RoleBindingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.RoleBindings) != 3 {
		t.Errorf("expected 3 bindings, got %d", len(resp.RoleBindings))
	}
}

// --- OIDC ---

func TestPutAuthConfig_WritesSecretAndHidesValue(t *testing.T) {
	mux, ah, store, kube := newRBACWriteMux()
	cookie := sessionCookieFor(ah, "alice", "org_admin")

	rec := doJSON(mux, cookie, "PUT", "/api/v1/org/auth", putAuthConfigRequest{
		Enabled:      true,
		IssuerURL:    "https://idp.example.com",
		ClientID:     "suparship",
		ClientSecret: "s3cr3t",
		RedirectURL:  "https://suparship.example.com/api/v1/auth/oidc/callback",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// The response must report the secret as set but never echo its value.
	if bytes.Contains(rec.Body.Bytes(), []byte("s3cr3t")) {
		t.Error("PUT response leaked the client secret value")
	}
	var put AuthConfigResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &put)
	if !put.OIDC.ClientSecretSet {
		t.Error("expected clientSecretSet=true after writing a secret")
	}

	// The secret must land in a k8s Secret, not the org config.
	sec, err := kube.CoreV1().Secrets(secrets.SystemNamespace).Get(context.Background(), oidcSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("oidc secret not created: %v", err)
	}
	if string(sec.Data[oidcSecretKey]) != "s3cr3t" {
		t.Errorf("secret value mismatch: %q", sec.Data[oidcSecretKey])
	}
	if store.org.Auth.OIDC == nil || !store.org.Auth.OIDC.Enabled {
		t.Error("OIDC config not persisted as enabled")
	}

	// GET must echo config but not the secret value, and report it as set.
	getRec := doJSON(mux, cookie, "GET", "/api/v1/org/auth", nil)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET expected 200, got %d", getRec.Code)
	}
	if bytes.Contains(getRec.Body.Bytes(), []byte("s3cr3t")) {
		t.Error("GET leaked the client secret value")
	}
	var got AuthConfigResponse
	_ = json.Unmarshal(getRec.Body.Bytes(), &got)
	if !got.OIDC.ClientSecretSet || got.OIDC.ClientID != "suparship" {
		t.Errorf("unexpected GET payload: %+v", got.OIDC)
	}
}

func TestPutAuthConfig_EnabledRequiresFields(t *testing.T) {
	mux, ah, _, _ := newRBACWriteMux()
	rec := doJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "PUT", "/api/v1/org/auth",
		putAuthConfigRequest{Enabled: true, ClientID: "x"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 when enabling OIDC without issuer/redirect, got %d", rec.Code)
	}
}

func TestPutAuthConfig_MetadataOnlyKeepsSecret(t *testing.T) {
	mux, ah, _, kube := newRBACWriteMux()
	cookie := sessionCookieFor(ah, "alice", "org_admin")

	// First write with a secret.
	if rec := doJSON(mux, cookie, "PUT", "/api/v1/org/auth", putAuthConfigRequest{
		Enabled: true, IssuerURL: "https://idp", ClientID: "a", ClientSecret: "keep-me",
		RedirectURL: "https://s/cb",
	}); rec.Code != http.StatusOK {
		t.Fatalf("setup PUT failed: %d %s", rec.Code, rec.Body.String())
	}
	// Second PUT without a secret (metadata-only) must keep the stored value.
	rec := doJSON(mux, cookie, "PUT", "/api/v1/org/auth", putAuthConfigRequest{
		Enabled: true, IssuerURL: "https://idp", ClientID: "a-renamed", RedirectURL: "https://s/cb",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("metadata PUT failed: %d %s", rec.Code, rec.Body.String())
	}
	sec, err := kube.CoreV1().Secrets(secrets.SystemNamespace).Get(context.Background(), oidcSecretName, metav1.GetOptions{})
	if err != nil || string(sec.Data[oidcSecretKey]) != "keep-me" {
		t.Errorf("metadata-only PUT should keep the existing secret, got %v / %q", err, sec.Data[oidcSecretKey])
	}
}
