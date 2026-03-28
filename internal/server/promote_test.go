package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/session"
)

func promoteTestProject() *project.Project {
	return &project.Project{
		APIVersion: project.CurrentAPIVersion,
		Kind:       project.ProjectKind,
		Metadata:   project.ProjectMeta{Name: "api"},
		Spec: project.ProjectSpec{
			Environments: []project.Environment{
				{Name: "dev", Order: 1},
				{Name: "staging", Order: 2},
				{Name: "prod", Order: 3},
			},
			Services: []project.Service{
				{Name: "backend", Template: project.TemplateRef{Name: "web-service"}},
			},
		},
	}
}

func newTestPromoteMux() (*http.ServeMux, *authHandler, *memProjectStore) {
	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	ah.registerRoutes(mux)

	store := newMemProjectStore()

	rh := &rbacHandler{
		auth:           ah,
		orgProvider:    &staticOrgProvider{org: testRBACOrg()},
		promoteHandler: newPromoteHandler(store),
	}
	rh.registerRoutes(mux)

	return mux, ah, store
}

func postPromoteJSON(mux *http.ServeMux, cookie *http.Cookie, projectName, serviceName string, body any) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	url := "/api/v1/projects/" + projectName + "/services/" + serviceName + "/promote"
	req := httptest.NewRequest("POST", url, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// --- Tests ---

func TestPromoteToStaging(t *testing.T) {
	mux, ah, store := newTestPromoteMux()
	_ = store.Save(context.Background(), promoteTestProject())

	rec := postPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "api", "backend",
		PromoteRequest{TargetEnvironment: "staging"})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp PromoteResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Project != "api" {
		t.Fatalf("project: want %q, got %q", "api", resp.Project)
	}
	if resp.Service != "backend" {
		t.Fatalf("service: want %q, got %q", "backend", resp.Service)
	}
	if resp.Source != "dev" {
		t.Fatalf("source: want %q, got %q", "dev", resp.Source)
	}
	if resp.Destination != "staging" {
		t.Fatalf("destination: want %q, got %q", "staging", resp.Destination)
	}
	if resp.Namespace != "api-staging" {
		t.Fatalf("namespace: want %q, got %q", "api-staging", resp.Namespace)
	}
	if resp.Message == "" {
		t.Fatal("expected non-empty message")
	}
}

func TestPromoteToProd(t *testing.T) {
	mux, ah, store := newTestPromoteMux()
	_ = store.Save(context.Background(), promoteTestProject())

	rec := postPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "api", "backend",
		PromoteRequest{TargetEnvironment: "prod"})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp PromoteResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Source != "staging" {
		t.Fatalf("source should be staging (previous env), got %q", resp.Source)
	}
	if resp.Destination != "prod" {
		t.Fatalf("destination: want %q, got %q", "prod", resp.Destination)
	}
}

func TestPromoteToLowestEnvFails(t *testing.T) {
	mux, ah, store := newTestPromoteMux()
	_ = store.Save(context.Background(), promoteTestProject())

	rec := postPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "api", "backend",
		PromoteRequest{TargetEnvironment: "dev"})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp errorResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if !contains(resp.Error, "lowest environment") {
		t.Fatalf("expected 'lowest environment' error, got %q", resp.Error)
	}
}

func TestPromoteMissingTargetEnv(t *testing.T) {
	mux, ah, store := newTestPromoteMux()
	_ = store.Save(context.Background(), promoteTestProject())

	rec := postPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "api", "backend",
		PromoteRequest{})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPromoteUnknownEnv(t *testing.T) {
	mux, ah, store := newTestPromoteMux()
	_ = store.Save(context.Background(), promoteTestProject())

	rec := postPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "api", "backend",
		PromoteRequest{TargetEnvironment: "nonexistent"})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPromoteProjectNotFound(t *testing.T) {
	mux, ah, _ := newTestPromoteMux()

	rec := postPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "nonexistent", "backend",
		PromoteRequest{TargetEnvironment: "prod"})

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPromoteServiceNotFound(t *testing.T) {
	mux, ah, store := newTestPromoteMux()
	_ = store.Save(context.Background(), promoteTestProject())

	rec := postPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "api", "nonexistent",
		PromoteRequest{TargetEnvironment: "prod"})

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPromoteDeveloperForbidden(t *testing.T) {
	mux, ah, store := newTestPromoteMux()
	_ = store.Save(context.Background(), promoteTestProject())

	rec := postPromoteJSON(mux, sessionCookieFor(ah, "bob", "developer"), "api", "backend",
		PromoteRequest{TargetEnvironment: "prod"})

	if rec.Code != http.StatusForbidden {
		t.Fatalf("developer should not promote (requires project_admin), got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPromoteViewerForbidden(t *testing.T) {
	mux, ah, store := newTestPromoteMux()
	_ = store.Save(context.Background(), promoteTestProject())

	rec := postPromoteJSON(mux, sessionCookieFor(ah, "carol", "viewer"), "api", "backend",
		PromoteRequest{TargetEnvironment: "prod"})

	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer should not promote, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPromoteUnauthenticated(t *testing.T) {
	mux, _, store := newTestPromoteMux()
	_ = store.Save(context.Background(), promoteTestProject())

	rec := postPromoteJSON(mux, nil, "api", "backend",
		PromoteRequest{TargetEnvironment: "prod"})

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}
