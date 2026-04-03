package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/session"
)

// --- Test helpers ---

// newTestAppPromoteMux wires an appHandler for promotion tests.
// The returned store is pre-seeded with a project bucket for projectName.
func newTestAppPromoteMux(projectName string) (*http.ServeMux, *authHandler, *memAppStore) {
	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	ah.registerRoutes(mux)

	store := newMemAppStore()
	store.mu.Lock()
	store.apps[projectName] = make(map[string]*domain.App)
	store.mu.Unlock()

	rh := &rbacHandler{
		auth:        ah,
		orgProvider: &staticOrgProvider{org: testRBACOrg()},
		appHandler:  newAppHandler(store, nil, nil),
	}
	rh.registerRoutes(mux)

	return mux, ah, store
}

func postAppPromoteJSON(mux *http.ServeMux, cookie *http.Cookie, projectName, appName string, body any) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	url := "/api/v1/projects/" + projectName + "/apps/" + appName + "/promote"
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// promoteTestApp returns a minimal app fixture for use in promote tests.
func promoteTestApp(projectName string) *domain.App {
	return &domain.App{
		Name:        "my-app",
		ProjectName: projectName,
		Spec: domain.AppSpec{
			Template:   domain.AppTemplateRef{Name: "web-service"},
			Components: []domain.Component{{Name: "web", Type: domain.ComponentWeb, EnabledInPreview: true}},
		},
	}
}

// seedFullPromotionChain seeds preview → staging → prod environments for "my-app".
func seedFullPromotionChain(store *memAppStore, projectName string) {
	ctx := context.Background()
	_ = store.SaveAppEnvironment(ctx, projectName, &domain.AppEnvironment{
		AppName:   "my-app",
		EnvName:   "pr-1",
		EnvType:   domain.AppEnvPreview,
		Namespace: "my-app-preview-pr-1",
		Release:   &domain.AppReleaseRef{Tag: "pr-1-abc"},
		Status:    domain.AppRuntimeStatus{Phase: domain.StatusHealthy},
	})
	_ = store.SaveAppEnvironment(ctx, projectName, &domain.AppEnvironment{
		AppName:   "my-app",
		EnvName:   "staging",
		EnvType:   domain.AppEnvStaging,
		Namespace: "my-app-staging",
		Release:   &domain.AppReleaseRef{Tag: "v0.9.0"},
		Status:    domain.AppRuntimeStatus{Phase: domain.StatusHealthy},
	})
	_ = store.SaveAppEnvironment(ctx, projectName, &domain.AppEnvironment{
		AppName:   "my-app",
		EnvName:   "prod",
		EnvType:   domain.AppEnvProd,
		Namespace: "my-app-prod",
		Status:    domain.AppRuntimeStatus{Phase: domain.StatusNotDeployed},
	})
}

// --- Happy-path tests ---

// TestAppPromoteStagingFromPreview verifies that promoting to staging selects
// the preview environment as the source and returns a well-formed response.
func TestAppPromoteStagingFromPreview(t *testing.T) {
	mux, ah, store := newTestAppPromoteMux(testProject)
	store.addApp(promoteTestApp(testProject))
	seedFullPromotionChain(store, testProject)

	rec := postAppPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		AppPromoteRequest{TargetEnvironment: "staging"})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp AppPromoteResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Project != testProject {
		t.Errorf("Project = %q, want %q", resp.Project, testProject)
	}
	if resp.App != "my-app" {
		t.Errorf("App = %q, want %q", resp.App, "my-app")
	}
	if resp.Source != "pr-1" {
		t.Errorf("Source = %q, want %q (first preview, sorted)", resp.Source, "pr-1")
	}
	if resp.Destination != "staging" {
		t.Errorf("Destination = %q, want %q", resp.Destination, "staging")
	}
	if resp.Namespace != "my-app-staging" {
		t.Errorf("Namespace = %q, want %q", resp.Namespace, "my-app-staging")
	}
	if resp.Message == "" {
		t.Error("expected non-empty message")
	}
}

// TestAppPromoteProdFromStaging verifies that promoting to prod selects
// the staging environment as the source.
func TestAppPromoteProdFromStaging(t *testing.T) {
	mux, ah, store := newTestAppPromoteMux(testProject)
	store.addApp(promoteTestApp(testProject))
	seedFullPromotionChain(store, testProject)

	rec := postAppPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		AppPromoteRequest{TargetEnvironment: "prod"})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp AppPromoteResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Source != "staging" {
		t.Errorf("Source = %q, want %q", resp.Source, "staging")
	}
	if resp.Destination != "prod" {
		t.Errorf("Destination = %q, want %q", resp.Destination, "prod")
	}
	if resp.Namespace != "my-app-prod" {
		t.Errorf("Namespace = %q, want %q", resp.Namespace, "my-app-prod")
	}
}

// TestAppPromoteSourceDeterminismMultiplePreviews verifies that when multiple
// preview environments exist, the lexicographically first is chosen as source.
func TestAppPromoteSourceDeterminismMultiplePreviews(t *testing.T) {
	mux, ah, store := newTestAppPromoteMux(testProject)
	store.addApp(promoteTestApp(testProject))

	ctx := context.Background()
	// Seed previews out of alphabetical order to test sorting.
	for _, name := range []string{"pr-99", "pr-1", "pr-42"} {
		_ = store.SaveAppEnvironment(ctx, testProject, &domain.AppEnvironment{
			AppName:   "my-app",
			EnvName:   name,
			EnvType:   domain.AppEnvPreview,
			Namespace: "my-app-preview-" + name,
			Status:    domain.AppRuntimeStatus{Phase: domain.StatusHealthy},
		})
	}
	_ = store.SaveAppEnvironment(ctx, testProject, &domain.AppEnvironment{
		AppName:   "my-app",
		EnvName:   "staging",
		EnvType:   domain.AppEnvStaging,
		Namespace: "my-app-staging",
		Status:    domain.AppRuntimeStatus{Phase: domain.StatusNotDeployed},
	})

	rec := postAppPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		AppPromoteRequest{TargetEnvironment: "staging"})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp AppPromoteResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	// "pr-1" < "pr-42" < "pr-99" lexicographically.
	if resp.Source != "pr-1" {
		t.Errorf("Source = %q, want %q (lexicographically first preview)", resp.Source, "pr-1")
	}
}

// --- Validation error tests ---

// TestAppPromoteToPreviewFails verifies that promoting to a preview environment
// is rejected with 400.
func TestAppPromoteToPreviewFails(t *testing.T) {
	mux, ah, store := newTestAppPromoteMux(testProject)
	store.addApp(promoteTestApp(testProject))
	seedFullPromotionChain(store, testProject)

	rec := postAppPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		AppPromoteRequest{TargetEnvironment: "pr-1"})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	var errResp errorResponse
	_ = json.NewDecoder(rec.Body).Decode(&errResp)
	if !contains(errResp.Error, "preview") {
		t.Errorf("expected 'preview' in error, got %q", errResp.Error)
	}
}

// TestAppPromoteMissingTargetEnvironment verifies that an empty
// targetEnvironment field returns 400.
func TestAppPromoteMissingTargetEnvironment(t *testing.T) {
	mux, ah, store := newTestAppPromoteMux(testProject)
	store.addApp(promoteTestApp(testProject))
	seedFullPromotionChain(store, testProject)

	rec := postAppPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		AppPromoteRequest{})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAppPromoteUnknownTargetEnvironment verifies that a target environment
// name that does not exist for the app returns 400.
func TestAppPromoteUnknownTargetEnvironment(t *testing.T) {
	mux, ah, store := newTestAppPromoteMux(testProject)
	store.addApp(promoteTestApp(testProject))
	seedFullPromotionChain(store, testProject)

	rec := postAppPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		AppPromoteRequest{TargetEnvironment: "nonexistent"})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAppPromoteAppNotFound verifies that a 404 is returned when the app does
// not exist in the project.
func TestAppPromoteAppNotFound(t *testing.T) {
	mux, ah, _ := newTestAppPromoteMux(testProject)

	rec := postAppPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "nonexistent",
		AppPromoteRequest{TargetEnvironment: "prod"})

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAppPromoteNoSourceEnvironment verifies that promoting to prod when no
// staging environment exists returns 400 (no source available).
func TestAppPromoteNoSourceEnvironment(t *testing.T) {
	mux, ah, store := newTestAppPromoteMux(testProject)
	store.addApp(promoteTestApp(testProject))

	// Seed only a prod environment — no staging to promote from.
	_ = store.SaveAppEnvironment(context.Background(), testProject, &domain.AppEnvironment{
		AppName:   "my-app",
		EnvName:   "prod",
		EnvType:   domain.AppEnvProd,
		Namespace: "my-app-prod",
		Status:    domain.AppRuntimeStatus{Phase: domain.StatusNotDeployed},
	})

	rec := postAppPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		AppPromoteRequest{TargetEnvironment: "prod"})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	var errResp errorResponse
	_ = json.NewDecoder(rec.Body).Decode(&errResp)
	if !contains(errResp.Error, "staging") {
		t.Errorf("expected 'staging' in error message, got %q", errResp.Error)
	}
}

// TestAppPromoteNoPreviewForStaging verifies that promoting to staging when
// no preview environment exists returns 400.
func TestAppPromoteNoPreviewForStaging(t *testing.T) {
	mux, ah, store := newTestAppPromoteMux(testProject)
	store.addApp(promoteTestApp(testProject))

	// Seed only a staging environment — no preview to promote from.
	_ = store.SaveAppEnvironment(context.Background(), testProject, &domain.AppEnvironment{
		AppName:   "my-app",
		EnvName:   "staging",
		EnvType:   domain.AppEnvStaging,
		Namespace: "my-app-staging",
		Status:    domain.AppRuntimeStatus{Phase: domain.StatusNotDeployed},
	})

	rec := postAppPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		AppPromoteRequest{TargetEnvironment: "staging"})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	var errResp errorResponse
	_ = json.NewDecoder(rec.Body).Decode(&errResp)
	if !contains(errResp.Error, "preview") {
		t.Errorf("expected 'preview' in error message, got %q", errResp.Error)
	}
}

// --- RBAC tests ---

// TestAppPromoteOrgAdminAllowed verifies that an org_admin can promote.
func TestAppPromoteOrgAdminAllowed(t *testing.T) {
	mux, ah, store := newTestAppPromoteMux(testProject)
	store.addApp(promoteTestApp(testProject))
	seedFullPromotionChain(store, testProject)

	rec := postAppPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		AppPromoteRequest{TargetEnvironment: "prod"})

	if rec.Code != http.StatusOK {
		t.Fatalf("org_admin should be allowed to promote, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAppPromoteDeveloperForbidden verifies that a developer cannot promote
// (promote requires project_admin or higher).
func TestAppPromoteDeveloperForbidden(t *testing.T) {
	mux, ah, store := newTestAppPromoteMux(testProject)
	store.addApp(promoteTestApp(testProject))
	seedFullPromotionChain(store, testProject)

	rec := postAppPromoteJSON(mux, sessionCookieFor(ah, "bob", "developer"), testProject, "my-app",
		AppPromoteRequest{TargetEnvironment: "prod"})

	if rec.Code != http.StatusForbidden {
		t.Fatalf("developer should not promote (requires project_admin), got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAppPromoteViewerForbidden verifies that a viewer cannot promote.
func TestAppPromoteViewerForbidden(t *testing.T) {
	mux, ah, store := newTestAppPromoteMux(testProject)
	store.addApp(promoteTestApp(testProject))
	seedFullPromotionChain(store, testProject)

	rec := postAppPromoteJSON(mux, sessionCookieFor(ah, "carol", "viewer"), testProject, "my-app",
		AppPromoteRequest{TargetEnvironment: "prod"})

	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer should not promote, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAppPromoteUnauthenticated verifies that an unauthenticated request
// returns 401.
func TestAppPromoteUnauthenticated(t *testing.T) {
	mux, _, store := newTestAppPromoteMux(testProject)
	store.addApp(promoteTestApp(testProject))
	seedFullPromotionChain(store, testProject)

	rec := postAppPromoteJSON(mux, nil, testProject, "my-app",
		AppPromoteRequest{TargetEnvironment: "prod"})

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

// TestAppPromoteInvalidBody verifies that a malformed JSON body returns 400.
func TestAppPromoteInvalidBody(t *testing.T) {
	mux, ah, store := newTestAppPromoteMux(testProject)
	store.addApp(promoteTestApp(testProject))
	seedFullPromotionChain(store, testProject)

	url := "/api/v1/projects/" + testProject + "/apps/my-app/promote"
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewBufferString("not-json"))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}
