package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/session"
)

// --- Shared test fixtures ---

// previewTestAppForProject returns an App with one web and one worker component,
// both enabled, with previews enabled — for use in preview endpoint tests.
func previewTestAppForProject(projectName string) *domain.App {
	return &domain.App{
		Name:        "my-app",
		ProjectName: projectName,
		Spec: domain.AppSpec{
			Template:        domain.AppTemplateRef{Name: "web-service"},
			PreviewsEnabled: true,
			Components: []domain.ComponentSpec{
				{Name: "web", Type: domain.ComponentWeb, Enabled: true},
				{Name: "worker", Type: domain.ComponentWorker, Enabled: true},
			},
		},
	}
}

// previewsDisabledAppForProject returns an App that has enabled components but
// has opted out of previews (PreviewsEnabled=false) — creating a preview must fail.
func previewsDisabledAppForProject(projectName string) *domain.App {
	return &domain.App{
		Name:        "noprev-app",
		ProjectName: projectName,
		Spec: domain.AppSpec{
			Template:        domain.AppTemplateRef{Name: "web-service"},
			PreviewsEnabled: false,
			Components: []domain.ComponentSpec{
				{Name: "web", Type: domain.ComponentWeb, Enabled: true},
			},
		},
	}
}

// newTestAppPreviewMux wires a mux with appHandler and returns the mux,
// authHandler, and store. projectName is used to pre-initialise the project
// bucket so addApp does not panic.
func newTestAppPreviewMux(projectName string) (*http.ServeMux, *authHandler, *memAppStore) {
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
		auth:       ah,
		orgStore:   &staticOrgProvider{org: testRBACOrg()},
		appHandler: newAppHandler(store, nil, nil, nil),
	}
	rh.registerRoutes(mux)

	return mux, ah, store
}

func postAppPreviewJSON(mux *http.ServeMux, cookie *http.Cookie, projectName, appName string, body any) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/projects/"+projectName+"/apps/"+appName+"/previews", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func deleteAppPreview(mux *http.ServeMux, cookie *http.Cookie, projectName, appName, previewName string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("DELETE", "/api/v1/projects/"+projectName+"/apps/"+appName+"/previews/"+previewName, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// testProject is the project name granted to "bob" (developer) in testRBACOrg.
const testProject = "api"

// --- Tests: POST .../apps/{app}/previews ---

func TestCreateAppPreviewValid(t *testing.T) {
	mux, ah, store := newTestAppPreviewMux(testProject)
	store.addApp(previewTestAppForProject(testProject))

	rec := postAppPreviewJSON(mux, sessionCookieFor(ah, "bob", "developer"), testProject, "my-app",
		CreateAppPreviewRequest{Name: "PR-42"})

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var dto AppPreviewSummaryDTO
	if err := json.NewDecoder(rec.Body).Decode(&dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// PR-42 is sanitized to pr-42
	if dto.Name != "pr-42" {
		t.Errorf("Name = %q, want %q", dto.Name, "pr-42")
	}
	if dto.AppName != "my-app" {
		t.Errorf("AppName = %q, want %q", dto.AppName, "my-app")
	}
	if dto.Project != testProject {
		t.Errorf("Project = %q, want %q", dto.Project, testProject)
	}
	if dto.Namespace != testProject+"-my-app-preview-pr-42" {
		t.Errorf("Namespace = %q, want %q", dto.Namespace, testProject+"-my-app-preview-pr-42")
	}
	// BaseEnv records the stable env the preview clones (first stable env by
	// order = staging) so the UI can group it under that env.
	if dto.BaseEnv != "staging" {
		t.Errorf("BaseEnv = %q, want staging", dto.BaseEnv)
	}
	if dto.Status.Phase != domain.StatusNotDeployed {
		t.Errorf("Status.Phase = %q, want %q", dto.Status.Phase, domain.StatusNotDeployed)
	}
	if dto.URLs == nil {
		t.Error("URLs should be non-nil")
	}
}

func TestCreateAppPreviewSanitizesBranchName(t *testing.T) {
	mux, ah, store := newTestAppPreviewMux(testProject)
	store.addApp(previewTestAppForProject(testProject))

	rec := postAppPreviewJSON(mux, sessionCookieFor(ah, "bob", "developer"), testProject, "my-app",
		CreateAppPreviewRequest{Name: "feature/some_new-thing"})

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var dto AppPreviewSummaryDTO
	_ = json.NewDecoder(rec.Body).Decode(&dto)
	if dto.Name != "feature-some-new-thing" {
		t.Errorf("Name = %q, want sanitized %q", dto.Name, "feature-some-new-thing")
	}
}

func TestCreateAppPreviewOrgAdmin(t *testing.T) {
	mux, ah, store := newTestAppPreviewMux(testProject)
	store.addApp(previewTestAppForProject(testProject))

	rec := postAppPreviewJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		CreateAppPreviewRequest{Name: "pr-99"})

	if rec.Code != http.StatusCreated {
		t.Fatalf("org_admin should be able to create preview, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateAppPreviewViewerForbidden(t *testing.T) {
	mux, ah, store := newTestAppPreviewMux(testProject)
	store.addApp(previewTestAppForProject(testProject))

	rec := postAppPreviewJSON(mux, sessionCookieFor(ah, "carol", "viewer"), testProject, "my-app",
		CreateAppPreviewRequest{Name: "pr-42"})

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for viewer, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateAppPreviewUnauthenticated(t *testing.T) {
	mux, _, store := newTestAppPreviewMux(testProject)
	store.addApp(previewTestAppForProject(testProject))

	rec := postAppPreviewJSON(mux, nil, testProject, "my-app", CreateAppPreviewRequest{Name: "pr-42"})

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestCreateAppPreviewMissingName(t *testing.T) {
	mux, ah, store := newTestAppPreviewMux(testProject)
	store.addApp(previewTestAppForProject(testProject))

	rec := postAppPreviewJSON(mux, sessionCookieFor(ah, "bob", "developer"), testProject, "my-app",
		CreateAppPreviewRequest{})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateAppPreviewAppNotFound(t *testing.T) {
	mux, ah, _ := newTestAppPreviewMux(testProject)

	rec := postAppPreviewJSON(mux, sessionCookieFor(ah, "bob", "developer"), testProject, "nonexistent",
		CreateAppPreviewRequest{Name: "pr-42"})

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateAppPreviewPreviewsDisabled(t *testing.T) {
	mux, ah, store := newTestAppPreviewMux(testProject)
	store.addApp(previewsDisabledAppForProject(testProject))

	rec := postAppPreviewJSON(mux, sessionCookieFor(ah, "bob", "developer"), testProject, "noprev-app",
		CreateAppPreviewRequest{Name: "pr-42"})

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for app with previews disabled, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateAppPreviewInvalidBaseEnv(t *testing.T) {
	mux, ah, store := newTestAppPreviewMux(testProject)
	store.addApp(previewTestAppForProject(testProject))

	rec := postAppPreviewJSON(mux, sessionCookieFor(ah, "bob", "developer"), testProject, "my-app",
		CreateAppPreviewRequest{Name: "pr-42", BaseEnv: "bogus"})

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for unknown baseEnv, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateAppPreviewExplicitBaseEnv(t *testing.T) {
	mux, ah, store := newTestAppPreviewMux(testProject)
	store.addApp(previewTestAppForProject(testProject))

	rec := postAppPreviewJSON(mux, sessionCookieFor(ah, "bob", "developer"), testProject, "my-app",
		CreateAppPreviewRequest{Name: "pr-42", BaseEnv: "staging"})

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 for valid baseEnv, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateAppPreviewUpsert(t *testing.T) {
	mux, ah, store := newTestAppPreviewMux(testProject)
	store.addApp(previewTestAppForProject(testProject))

	cookie := sessionCookieFor(ah, "bob", "developer")

	// First create → 201.
	rec := postAppPreviewJSON(mux, cookie, testProject, "my-app",
		CreateAppPreviewRequest{Name: "pr-42", ImageTag: "sha-aaa"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("first create: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// Re-POST (e.g. CI on a new push) → 200 upsert, tag updated.
	rec = postAppPreviewJSON(mux, cookie, testProject, "my-app",
		CreateAppPreviewRequest{Name: "pr-42", ImageTag: "sha-bbb"})
	if rec.Code != http.StatusOK {
		t.Fatalf("re-create: expected 200 (upsert), got %d: %s", rec.Code, rec.Body.String())
	}

	env, err := store.GetAppEnvironment(context.Background(), testProject, "my-app", "pr-42")
	if err != nil {
		t.Fatalf("preview env not found after upsert: %v", err)
	}
	if env.Release == nil || env.Release.Tag != "sha-bbb" {
		t.Fatalf("expected preview release tag sha-bbb, got %+v", env.Release)
	}
}

// --- Tests: GET .../apps/{app}/previews ---

func TestListAppPreviewsEmpty(t *testing.T) {
	mux, ah, store := newTestAppPreviewMux(testProject)
	store.addApp(previewTestAppForProject(testProject))

	req := httptest.NewRequest("GET", "/api/v1/projects/"+testProject+"/apps/my-app/previews", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp AppPreviewsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Previews) != 0 {
		t.Fatalf("expected 0 previews, got %d", len(resp.Previews))
	}
	if resp.AppName != "my-app" {
		t.Errorf("AppName = %q, want %q", resp.AppName, "my-app")
	}
	if resp.Project != testProject {
		t.Errorf("Project = %q, want %q", resp.Project, testProject)
	}
}

func TestListAppPreviewsPopulated(t *testing.T) {
	mux, ah, store := newTestAppPreviewMux(testProject)
	store.addApp(previewTestAppForProject(testProject))

	ctx := context.Background()
	_ = store.SaveAppEnvironment(ctx, testProject, &domain.AppEnvironment{
		AppName: "my-app", EnvName: "pr-42", EnvType: domain.AppEnvPreview,
		Namespace: "my-app-pr-42", URLs: []string{},
		Status: domain.AppRuntimeStatus{Phase: domain.StatusNotDeployed},
	})
	_ = store.SaveAppEnvironment(ctx, testProject, &domain.AppEnvironment{
		AppName: "my-app", EnvName: "pr-99", EnvType: domain.AppEnvPreview,
		Namespace: "my-app-pr-99", URLs: []string{},
		Status: domain.AppRuntimeStatus{Phase: domain.StatusNotDeployed},
	})

	req := httptest.NewRequest("GET", "/api/v1/projects/"+testProject+"/apps/my-app/previews", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp AppPreviewsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Previews) != 2 {
		t.Fatalf("expected 2 previews, got %d", len(resp.Previews))
	}
	for _, p := range resp.Previews {
		if p.AppName != "my-app" {
			t.Errorf("AppName = %q, want %q", p.AppName, "my-app")
		}
		if p.Project != testProject {
			t.Errorf("Project = %q, want %q", p.Project, testProject)
		}
	}
}

func TestListAppPreviewsAppNotFound(t *testing.T) {
	mux, ah, _ := newTestAppPreviewMux(testProject)

	req := httptest.NewRequest("GET", "/api/v1/projects/"+testProject+"/apps/nonexistent/previews", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestListAppPreviewsUnauthenticated(t *testing.T) {
	mux, _, store := newTestAppPreviewMux(testProject)
	store.addApp(previewTestAppForProject(testProject))

	req := httptest.NewRequest("GET", "/api/v1/projects/"+testProject+"/apps/my-app/previews", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestListAppPreviewsForbidden(t *testing.T) {
	mux, ah, store := newTestAppPreviewMux(testProject)
	store.addApp(previewTestAppForProject(testProject))

	// "stranger" has no binding for any project.
	req := httptest.NewRequest("GET", "/api/v1/projects/"+testProject+"/apps/my-app/previews", nil)
	req.AddCookie(sessionCookieFor(ah, "stranger", "viewer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- Tests: DELETE .../apps/{app}/previews/{name} ---

func TestDeleteAppPreviewValid(t *testing.T) {
	mux, ah, store := newTestAppPreviewMux(testProject)
	store.addApp(previewTestAppForProject(testProject))
	_ = store.SaveAppEnvironment(context.Background(), testProject, &domain.AppEnvironment{
		AppName: "my-app", EnvName: "pr-42", EnvType: domain.AppEnvPreview,
		Namespace: "my-app-pr-42", URLs: []string{},
		Status: domain.AppRuntimeStatus{Phase: domain.StatusNotDeployed},
	})

	rec := deleteAppPreview(mux, sessionCookieFor(ah, "bob", "developer"), testProject, "my-app", "pr-42")

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	_, err := store.GetAppEnvironment(context.Background(), testProject, "my-app", "pr-42")
	if err == nil {
		t.Fatal("preview should have been deleted")
	}
}

func TestDeleteAppPreviewOrgAdmin(t *testing.T) {
	mux, ah, store := newTestAppPreviewMux(testProject)
	store.addApp(previewTestAppForProject(testProject))
	_ = store.SaveAppEnvironment(context.Background(), testProject, &domain.AppEnvironment{
		AppName: "my-app", EnvName: "pr-42", EnvType: domain.AppEnvPreview,
		Namespace: "my-app-pr-42", URLs: []string{},
		Status: domain.AppRuntimeStatus{Phase: domain.StatusNotDeployed},
	})

	rec := deleteAppPreview(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app", "pr-42")

	if rec.Code != http.StatusNoContent {
		t.Fatalf("org_admin should delete preview, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteAppPreviewNotFound(t *testing.T) {
	mux, ah, store := newTestAppPreviewMux(testProject)
	store.addApp(previewTestAppForProject(testProject))

	rec := deleteAppPreview(mux, sessionCookieFor(ah, "bob", "developer"), testProject, "my-app", "nonexistent")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteAppPreviewRejectsStableEnv(t *testing.T) {
	mux, ah, store := newTestAppPreviewMux(testProject)
	store.addApp(previewTestAppForProject(testProject))
	// Seed a staging environment (not a preview).
	_ = store.SaveAppEnvironment(context.Background(), testProject, &domain.AppEnvironment{
		AppName: "my-app", EnvName: "staging", EnvType: domain.AppEnvStaging,
		Namespace: "my-app-staging", URLs: []string{},
		Status: domain.AppRuntimeStatus{Phase: domain.StatusNotDeployed},
	})

	rec := deleteAppPreview(mux, sessionCookieFor(ah, "bob", "developer"), testProject, "my-app", "staging")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when deleting non-preview env, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteAppPreviewViewerForbidden(t *testing.T) {
	mux, ah, store := newTestAppPreviewMux(testProject)
	store.addApp(previewTestAppForProject(testProject))
	_ = store.SaveAppEnvironment(context.Background(), testProject, &domain.AppEnvironment{
		AppName: "my-app", EnvName: "pr-42", EnvType: domain.AppEnvPreview,
		Namespace: "my-app-pr-42", URLs: []string{},
		Status: domain.AppRuntimeStatus{Phase: domain.StatusNotDeployed},
	})

	rec := deleteAppPreview(mux, sessionCookieFor(ah, "carol", "viewer"), testProject, "my-app", "pr-42")

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for viewer, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteAppPreviewUnauthenticated(t *testing.T) {
	mux, _, store := newTestAppPreviewMux(testProject)
	store.addApp(previewTestAppForProject(testProject))
	_ = store.SaveAppEnvironment(context.Background(), testProject, &domain.AppEnvironment{
		AppName: "my-app", EnvName: "pr-42", EnvType: domain.AppEnvPreview,
		Namespace: "my-app-pr-42", URLs: []string{},
		Status: domain.AppRuntimeStatus{Phase: domain.StatusNotDeployed},
	})

	rec := deleteAppPreview(mux, nil, testProject, "my-app", "pr-42")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

// --- Tests: preview DTO fields ---

// TestListAppPreviewsDTOFields verifies that the preview DTO carries app name,
// project, namespace, URLs, status, and release (when set).
func TestListAppPreviewsDTOFields(t *testing.T) {
	mux, ah, store := newTestAppPreviewMux(testProject)
	store.addApp(previewTestAppForProject(testProject))
	_ = store.SaveAppEnvironment(context.Background(), testProject, &domain.AppEnvironment{
		AppName:   "my-app",
		EnvName:   "pr-10",
		EnvType:   domain.AppEnvPreview,
		Namespace: "my-app-pr-10",
		URLs:      []string{"https://pr-10.app.preview.localhost"},
		Release:   &domain.AppReleaseRef{Image: "ghcr.io/org/app:pr-10", Tag: "pr-10"},
		Status:    domain.AppRuntimeStatus{Phase: domain.StatusHealthy},
	})

	req := httptest.NewRequest("GET", "/api/v1/projects/"+testProject+"/apps/my-app/previews", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp AppPreviewsResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.Previews) != 1 {
		t.Fatalf("expected 1 preview, got %d", len(resp.Previews))
	}

	p := resp.Previews[0]
	if p.Name != "pr-10" {
		t.Errorf("Name = %q, want %q", p.Name, "pr-10")
	}
	if p.AppName != "my-app" {
		t.Errorf("AppName = %q, want %q", p.AppName, "my-app")
	}
	if p.Project != testProject {
		t.Errorf("Project = %q, want %q", p.Project, testProject)
	}
	if p.Namespace != "my-app-pr-10" {
		t.Errorf("Namespace = %q, want %q", p.Namespace, "my-app-pr-10")
	}
	if p.Release == nil {
		t.Fatal("expected non-nil Release in DTO")
	}
	if p.Release.Tag != "pr-10" {
		t.Errorf("Release.Tag = %q, want %q", p.Release.Tag, "pr-10")
	}
	if len(p.URLs) != 1 || p.URLs[0] != "https://pr-10.app.preview.localhost" {
		t.Errorf("URLs = %v, want [%q]", p.URLs, "https://pr-10.app.preview.localhost")
	}
	if p.Status.Phase != domain.StatusHealthy {
		t.Errorf("Status.Phase = %q, want %q", p.Status.Phase, domain.StatusHealthy)
	}
}

// previewDeleterPublisher embeds recordingPublisher (for the GitOpsPublisher
// method set) and implements AppPreviewDeleter, recording delete calls and
// optionally failing.
type previewDeleterPublisher struct {
	recordingPublisher
	deleted   []string
	deleteErr error
}

func (p *previewDeleterPublisher) DeleteAppPreview(_ context.Context, project, previewName, appName, baseEnv string) error {
	if p.deleteErr != nil {
		return p.deleteErr
	}
	p.deleted = append(p.deleted, baseEnv+"/"+project+"/"+previewName+"/"+appName)
	return nil
}

func TestDeleteAppPreviewPrunesGitops(t *testing.T) {
	pub := &previewDeleterPublisher{}
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, pub)
	store.addApp(previewTestAppForProject(testProject))
	store.addEnv(&domain.AppEnvironment{
		AppName: "my-app", ProjectName: testProject, EnvName: "pr-42",
		EnvType: domain.AppEnvPreview, BaseEnv: "staging",
		Namespace: testProject + "-my-app-preview-pr-42",
	})

	rec := deleteAppPreview(mux, sessionCookieFor(ah, "bob", "developer"), testProject, "my-app", "pr-42")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (%s)", rec.Code, rec.Body.String())
	}
	if len(pub.deleted) != 1 || pub.deleted[0] != "staging/"+testProject+"/pr-42/my-app" {
		t.Errorf("gitops delete calls = %v, want [staging/%s/pr-42/my-app]", pub.deleted, testProject)
	}
	if _, err := store.GetAppEnvironment(context.Background(), testProject, "my-app", "pr-42"); err == nil {
		t.Error("preview env should be removed from the store")
	}
}

func TestDeleteAppPreviewGitopsFailureKeepsRecord(t *testing.T) {
	pub := &previewDeleterPublisher{deleteErr: errTestGitops}
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, pub)
	store.addApp(previewTestAppForProject(testProject))
	store.addEnv(&domain.AppEnvironment{
		AppName: "my-app", ProjectName: testProject, EnvName: "pr-42",
		EnvType: domain.AppEnvPreview, Namespace: testProject + "-my-app-preview-pr-42",
	})

	rec := deleteAppPreview(mux, sessionCookieFor(ah, "bob", "developer"), testProject, "my-app", "pr-42")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	// Record must survive so the preview stays listed and the delete is retryable.
	if _, err := store.GetAppEnvironment(context.Background(), testProject, "my-app", "pr-42"); err != nil {
		t.Error("preview env should remain after a gitops failure")
	}
}

var errTestGitops = errors.New("gitops boom")
