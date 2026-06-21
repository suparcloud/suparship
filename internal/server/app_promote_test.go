package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
		orgStore: &staticOrgProvider{org: testRBACOrg()},
		appHandler:  newAppHandler(store, nil, nil, nil),
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
			Components: []domain.ComponentSpec{{Name: "web", Type: domain.ComponentWeb, PreviewEnabled: true}},
		},
	}
}

// seedFullPromotionChain seeds preview → staging → prod environments for "my-app".
// Order values: staging=1, prod=2. Preview envs have Order=0 (excluded from chain).
func seedFullPromotionChain(store *memAppStore, projectName string) {
	ctx := context.Background()
	_ = store.SaveAppEnvironment(ctx, projectName, &domain.AppEnvironment{
		AppName:   "my-app",
		EnvName:   "pr-1",
		EnvType:   domain.AppEnvPreview,
		Order:     0,
		Namespace: "my-app-pr-1",
		Release:   &domain.AppReleaseRef{Tag: "pr-1-abc"},
		Status:    domain.AppRuntimeStatus{Phase: domain.StatusHealthy},
	})
	_ = store.SaveAppEnvironment(ctx, projectName, &domain.AppEnvironment{
		AppName:   "my-app",
		EnvName:   "staging",
		EnvType:   domain.AppEnvStaging,
		Order:     1,
		Namespace: "my-app-staging",
		Release:   &domain.AppReleaseRef{Tag: "v0.9.0"},
		Status:    domain.AppRuntimeStatus{Phase: domain.StatusHealthy},
	})
	_ = store.SaveAppEnvironment(ctx, projectName, &domain.AppEnvironment{
		AppName:   "my-app",
		EnvName:   "prod",
		EnvType:   domain.AppEnvProd,
		Order:     2,
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
	// Each preview must have a release so Promote can copy it.
	for _, name := range []string{"pr-99", "pr-1", "pr-42"} {
		_ = store.SaveAppEnvironment(ctx, testProject, &domain.AppEnvironment{
			AppName:   "my-app",
			EnvName:   name,
			EnvType:   domain.AppEnvPreview,
			Namespace: "my-app-" + name,
			Release:   &domain.AppReleaseRef{Tag: name + "-sha"},
			Status:    domain.AppRuntimeStatus{Phase: domain.StatusHealthy},
		})
	}
	_ = store.SaveAppEnvironment(ctx, testProject, &domain.AppEnvironment{
		AppName:   "my-app",
		EnvName:   "staging",
		EnvType:   domain.AppEnvStaging,
		Order:     1,
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
// earlier environment exists returns 400 (no source available).
func TestAppPromoteNoSourceEnvironment(t *testing.T) {
	mux, ah, store := newTestAppPromoteMux(testProject)
	store.addApp(promoteTestApp(testProject))

	// Seed only a prod environment (Order=2) — no lower-Order env to promote from.
	_ = store.SaveAppEnvironment(context.Background(), testProject, &domain.AppEnvironment{
		AppName:   "my-app",
		EnvName:   "prod",
		EnvType:   domain.AppEnvProd,
		Order:     2,
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
	if !contains(errResp.Error, "no environment found") {
		t.Errorf("expected 'no environment found' in error message, got %q", errResp.Error)
	}
}

// TestAppPromoteNoPreviewForStaging verifies that promoting to staging when
// no source environment exists returns 400.
func TestAppPromoteNoPreviewForStaging(t *testing.T) {
	mux, ah, store := newTestAppPromoteMux(testProject)
	store.addApp(promoteTestApp(testProject))

	// Seed only a staging environment (Order=1) — no lower-Order env or preview to promote from.
	_ = store.SaveAppEnvironment(context.Background(), testProject, &domain.AppEnvironment{
		AppName:   "my-app",
		EnvName:   "staging",
		EnvType:   domain.AppEnvStaging,
		Order:     1,
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
	if !contains(errResp.Error, "no environment found") {
		t.Errorf("expected 'no environment found' in error message, got %q", errResp.Error)
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

// --- Release propagation tests ---

// TestAppPromoteReleaseCopiedToTarget verifies that after a successful
// promotion the target environment's release in the store equals the source's.
func TestAppPromoteReleaseCopiedToTarget(t *testing.T) {
	mux, ah, store := newTestAppPromoteMux(testProject)
	store.addApp(promoteTestApp(testProject))
	seedFullPromotionChain(store, testProject)

	rec := postAppPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		AppPromoteRequest{TargetEnvironment: "prod"})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// The response must include the promoted release.
	var resp AppPromoteResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Release == nil {
		t.Fatal("expected Release in response, got nil")
	}
	if resp.Release.Tag != "v0.9.0" {
		t.Errorf("Release.Tag = %q, want %q (staging tag)", resp.Release.Tag, "v0.9.0")
	}
	// No Kargo promoter wired → the response must label the mechanism so a
	// non-pipeline promotion is never mistaken for a Kargo-driven rollout.
	if resp.Mechanism != "in-store" {
		t.Errorf("Mechanism = %q, want %q", resp.Mechanism, "in-store")
	}

	// Verify the store was updated.
	prodEnv, err := store.GetAppEnvironment(context.Background(), testProject, "my-app", "prod")
	if err != nil {
		t.Fatalf("GetAppEnvironment: %v", err)
	}
	if prodEnv.Release == nil {
		t.Fatal("prod environment Release is nil after promotion")
	}
	if prodEnv.Release.Tag != "v0.9.0" {
		t.Errorf("stored prod Release.Tag = %q, want %q", prodEnv.Release.Tag, "v0.9.0")
	}
}

// TestAppPromoteSourceReleaseUnchanged verifies that the source environment's
// release is not mutated after promotion.
func TestAppPromoteSourceReleaseUnchanged(t *testing.T) {
	mux, ah, store := newTestAppPromoteMux(testProject)
	store.addApp(promoteTestApp(testProject))
	seedFullPromotionChain(store, testProject)

	rec := postAppPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		AppPromoteRequest{TargetEnvironment: "prod"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	stagingEnv, _ := store.GetAppEnvironment(context.Background(), testProject, "my-app", "staging")
	if stagingEnv.Release == nil || stagingEnv.Release.Tag != "v0.9.0" {
		t.Errorf("staging release changed after promotion: %+v", stagingEnv.Release)
	}
}

// TestAppPromoteNoReleaseFails verifies that promoting a source with no release
// returns 400 with ErrNoRelease in the error message.
func TestAppPromoteNoReleaseFails(t *testing.T) {
	mux, ah, store := newTestAppPromoteMux(testProject)
	store.addApp(promoteTestApp(testProject))

	ctx := context.Background()
	// Seed staging (Order=1) with no release; prod (Order=2) as target.
	_ = store.SaveAppEnvironment(ctx, testProject, &domain.AppEnvironment{
		AppName:   "my-app",
		EnvName:   "staging",
		EnvType:   domain.AppEnvStaging,
		Order:     1,
		Namespace: "my-app-staging",
		// Release intentionally nil.
	})
	_ = store.SaveAppEnvironment(ctx, testProject, &domain.AppEnvironment{
		AppName:   "my-app",
		EnvName:   "prod",
		EnvType:   domain.AppEnvProd,
		Order:     2,
		Namespace: "my-app-prod",
	})

	rec := postAppPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		AppPromoteRequest{TargetEnvironment: "prod"})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for source with no release, got %d: %s", rec.Code, rec.Body.String())
	}
	var errResp errorResponse
	_ = json.NewDecoder(rec.Body).Decode(&errResp)
	if !contains(errResp.Error, "no release") {
		t.Errorf("expected 'no release' in error, got %q", errResp.Error)
	}
}

// TestAppPromoteThreeEnvChain verifies that in a dev(Order=1) → staging(Order=2) →
// prod(Order=3) pipeline, promoting to staging selects dev as source, and
// promoting to prod selects staging (highest Order < prod.Order) as source.
func TestAppPromoteThreeEnvChain(t *testing.T) {
	mux, ah, store := newTestAppPromoteMux(testProject)
	store.addApp(promoteTestApp(testProject))

	ctx := context.Background()
	_ = store.SaveAppEnvironment(ctx, testProject, &domain.AppEnvironment{
		AppName:   "my-app",
		EnvName:   "dev",
		EnvType:   domain.AppEnvStaging,
		Order:     1,
		Namespace: "my-app-dev",
		Release:   &domain.AppReleaseRef{Tag: "dev-sha"},
		Status:    domain.AppRuntimeStatus{Phase: domain.StatusHealthy},
	})
	_ = store.SaveAppEnvironment(ctx, testProject, &domain.AppEnvironment{
		AppName:   "my-app",
		EnvName:   "staging",
		EnvType:   domain.AppEnvStaging,
		Order:     2,
		Namespace: "my-app-staging",
		Release:   &domain.AppReleaseRef{Tag: "staging-sha"},
		Status:    domain.AppRuntimeStatus{Phase: domain.StatusHealthy},
	})
	_ = store.SaveAppEnvironment(ctx, testProject, &domain.AppEnvironment{
		AppName:   "my-app",
		EnvName:   "prod",
		EnvType:   domain.AppEnvProd,
		Order:     3,
		Namespace: "my-app-prod",
		Status:    domain.AppRuntimeStatus{Phase: domain.StatusNotDeployed},
	})

	// Promote to staging → source should be dev (Order=1, closest below Order=2).
	recStaging := postAppPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		AppPromoteRequest{TargetEnvironment: "staging"})
	if recStaging.Code != http.StatusOK {
		t.Fatalf("staging promote: expected 200, got %d: %s", recStaging.Code, recStaging.Body.String())
	}
	var respStaging AppPromoteResponse
	_ = json.NewDecoder(recStaging.Body).Decode(&respStaging)
	if respStaging.Source != "dev" {
		t.Errorf("staging promote source = %q, want %q", respStaging.Source, "dev")
	}

	// Promote to prod → source should be staging (Order=2, closest below Order=3).
	recProd := postAppPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		AppPromoteRequest{TargetEnvironment: "prod"})
	if recProd.Code != http.StatusOK {
		t.Fatalf("prod promote: expected 200, got %d: %s", recProd.Code, recProd.Body.String())
	}
	var respProd AppPromoteResponse
	_ = json.NewDecoder(recProd.Body).Decode(&respProd)
	if respProd.Source != "staging" {
		t.Errorf("prod promote source = %q, want %q", respProd.Source, "staging")
	}
}

// --- PublishAppEnv is called on promote ---

// recordingPublisher is a GitOpsPublisher stub that records PublishAppEnv calls.
type recordingPublisher struct {
	publishedEnvs []string
}

func (r *recordingPublisher) PublishApp(_ context.Context, _ *domain.App, _ []*domain.AppEnvironment) error {
	return nil
}
func (r *recordingPublisher) PublishAppEnv(_ context.Context, _ *domain.App, env *domain.AppEnvironment) error {
	r.publishedEnvs = append(r.publishedEnvs, env.EnvName)
	return nil
}
func (r *recordingPublisher) UnpublishApp(_ context.Context, _, _ string) error       { return nil }
func (r *recordingPublisher) UnpublishProjectApps(_ context.Context, _ string) error  { return nil }
func (r *recordingPublisher) UnpublishProjectInfra(_ context.Context, _ string) error { return nil }

// newTestAppPromoteMuxWithPublisher wires an appHandler for promotion tests
// with a GitOpsPublisher injected.
func newTestAppPromoteMuxWithPublisher(projectName string, pub GitOpsPublisher) (*http.ServeMux, *authHandler, *memAppStore) {
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

	appH := newAppHandler(store, nil, nil, nil)
	appH.gitOpsPublisher = pub

	rh := &rbacHandler{
		auth:        ah,
		orgStore: &staticOrgProvider{org: testRBACOrg()},
		appHandler:  appH,
	}
	rh.registerRoutes(mux)

	return mux, ah, store
}

// TestPromote_CallsPublishAppEnv verifies that handlePromoteApp calls
// GitOpsPublisher.PublishAppEnv for the target environment before returning.
func TestPromote_CallsPublishAppEnv(t *testing.T) {
	rec := &recordingPublisher{}
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, rec)

	store.addApp(promoteTestApp(testProject))
	seedFullPromotionChain(store, testProject)

	// Promote to prod (from staging which has a release).
	resp := postAppPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		AppPromoteRequest{TargetEnvironment: "prod"})

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}

	if len(rec.publishedEnvs) == 0 {
		t.Fatal("expected PublishAppEnv to be called, but no calls were recorded")
	}
	if rec.publishedEnvs[0] != "prod" {
		t.Errorf("expected PublishAppEnv called with env=prod, got %q", rec.publishedEnvs[0])
	}
}

// TestPromote_PublishAppEnvFailureContinues verifies that a PublishAppEnv
// failure does not abort the promotion — the promote response is still 200.
type failingPublisher struct{}

func (f *failingPublisher) PublishApp(_ context.Context, _ *domain.App, _ []*domain.AppEnvironment) error {
	return nil
}
func (f *failingPublisher) PublishAppEnv(_ context.Context, _ *domain.App, _ *domain.AppEnvironment) error {
	return fmt.Errorf("simulated publish failure")
}
func (f *failingPublisher) UnpublishApp(_ context.Context, _, _ string) error       { return nil }
func (f *failingPublisher) UnpublishProjectApps(_ context.Context, _ string) error  { return nil }
func (f *failingPublisher) UnpublishProjectInfra(_ context.Context, _ string) error { return nil }

func TestPromote_PublishAppEnvFailureContinues(t *testing.T) {
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, &failingPublisher{})

	store.addApp(promoteTestApp(testProject))
	seedFullPromotionChain(store, testProject)

	resp := postAppPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		AppPromoteRequest{TargetEnvironment: "prod"})

	// Promotion should succeed even though file publishing failed.
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 even with publish failure, got %d: %s", resp.Code, resp.Body.String())
	}
}
