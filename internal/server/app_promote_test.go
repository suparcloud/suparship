package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/kube"
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
			Components: []domain.ComponentSpec{{Name: "web", Type: domain.ComponentWeb, Enabled: true}},
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

// A direct-delivery app has no promotion pipeline — promoting it is rejected.
func TestAppPromote_DirectAppRejected(t *testing.T) {
	mux, ah, store := newTestAppPromoteMux(testProject)
	app := promoteTestApp(testProject)
	app.Spec.DeliveryMode = domain.DeliveryDirect
	store.addApp(app)
	seedFullPromotionChain(store, testProject)

	rec := postAppPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		AppPromoteRequest{TargetEnvironment: "prod"})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for promoting a direct app, got %d: %s", rec.Code, rec.Body.String())
	}
}

// A pinned env is frozen — promoting to it is rejected until it's unpinned.
func TestAppPromote_PinnedEnvRejected(t *testing.T) {
	mux, ah, store := newTestAppPromoteMux(testProject)
	app := promoteTestApp(testProject)
	app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{
		"prod": {PinnedImageTag: "pr-1-abc", PinnedFrom: "pr-1"},
	}
	store.addApp(app)
	seedFullPromotionChain(store, testProject)

	rec := postAppPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		AppPromoteRequest{TargetEnvironment: "prod"})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 promoting to a pinned env, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- Happy-path tests ---

// TestAppPromoteToFirstStageRejected: promotion advances freight between Kargo
// Stages, and a preview has no Stage — so promoting to the first stage (staging)
// is rejected. A PR's build reaches staging by merging (auto-promote) or pinning.
func TestAppPromoteToFirstStageRejected(t *testing.T) {
	mux, ah, store := newTestAppPromoteMux(testProject)
	store.addApp(promoteTestApp(testProject))
	seedFullPromotionChain(store, testProject) // preview + staging + prod

	rec := postAppPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		AppPromoteRequest{TargetEnvironment: "staging"})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 promoting to the first stage, got %d: %s", rec.Code, rec.Body.String())
	}
	var errResp errorResponse
	_ = json.NewDecoder(rec.Body).Decode(&errResp)
	if !contains(errResp.Error, "no upstream stage") {
		t.Errorf("expected 'no upstream stage' in error, got %q", errResp.Error)
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
	if !contains(errResp.Error, "no upstream stage") {
		t.Errorf("expected 'no upstream stage' in error message, got %q", errResp.Error)
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
	if !contains(errResp.Error, "no upstream stage") {
		t.Errorf("expected 'no upstream stage' in error message, got %q", errResp.Error)
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
	// publishedEnvApps records the app value passed to each PublishAppEnv call
	// (the promote path passes a transient clone carrying the source-tag pin).
	publishedEnvApps []*domain.App
	removedEnvs   []string
	// batchCalls counts PublishAppsEnv invocations; batchTargets records the
	// number of targets in each, so a test can assert one batched git op.
	batchCalls   int
	batchTargets []int
	// batchAppCalls/batchAppTargets do the same for the full-app PublishApps
	// batch (pin/unpin).
	batchAppCalls   int
	batchAppTargets []int
	// batchPreviewCalls/batchPreviewTargets track the PublishPreviews batch
	// (stack preview); previewCalls counts per-member PublishAppPreview so a test
	// can assert the batch path was taken (previewCalls stays 0).
	batchPreviewCalls   int
	batchPreviewTargets []int
	previewCalls        int
	// publishAppCalls counts full-app PublishApp invocations, so a test can
	// assert the pipeline undeploy path republishes to rebuild the Kargo chain.
	publishAppCalls int
}

// PublishPreviews makes recordingPublisher a BatchPreviewPublisher so tests can
// assert the stack preview fan-out publishes in one batched call.
func (r *recordingPublisher) PublishPreviews(_ context.Context, targets []PreviewPublishTarget) error {
	r.batchPreviewCalls++
	r.batchPreviewTargets = append(r.batchPreviewTargets, len(targets))
	return nil
}

// PublishAppsEnv makes recordingPublisher a BatchEnvPublisher so tests can assert
// the stack fan-out publishes in one batched call rather than N per-env calls.
func (r *recordingPublisher) PublishAppsEnv(_ context.Context, targets []AppEnvTarget) error {
	r.batchCalls++
	r.batchTargets = append(r.batchTargets, len(targets))
	for _, t := range targets {
		r.publishedEnvs = append(r.publishedEnvs, t.Env.EnvName)
	}
	return nil
}

// PublishApps makes recordingPublisher a BatchAppPublisher so tests can assert
// the stack pin/unpin fan-out publishes in one batched call.
func (r *recordingPublisher) PublishApps(_ context.Context, targets []AppPublishTarget) error {
	r.batchAppCalls++
	r.batchAppTargets = append(r.batchAppTargets, len(targets))
	return nil
}

func (r *recordingPublisher) PublishApp(_ context.Context, _ *domain.App, _ []*domain.AppEnvironment) error {
	r.publishAppCalls++
	return nil
}
func (r *recordingPublisher) PublishAppEnv(_ context.Context, app *domain.App, env *domain.AppEnvironment) error {
	r.publishedEnvs = append(r.publishedEnvs, env.EnvName)
	r.publishedEnvApps = append(r.publishedEnvApps, app)
	return nil
}
func (r *recordingPublisher) PublishAppPreview(_ context.Context, _ *domain.App, _ *domain.EnvironmentInstance, _, _ string) error {
	r.previewCalls++
	return nil
}
func (r *recordingPublisher) UnpublishApp(_ context.Context, _, _ string) error { return nil }
func (r *recordingPublisher) RemoveAppEnv(_ context.Context, _, _, env string) error {
	r.removedEnvs = append(r.removedEnvs, env)
	return nil
}
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
func (f *failingPublisher) PublishAppPreview(_ context.Context, _ *domain.App, _ *domain.EnvironmentInstance, _, _ string) error {
	return fmt.Errorf("simulated publish failure")
}
func (f *failingPublisher) UnpublishApp(_ context.Context, _, _ string) error       { return nil }
func (f *failingPublisher) RemoveAppEnv(_ context.Context, _, _, _ string) error    { return nil }
func (f *failingPublisher) UnpublishProjectApps(_ context.Context, _ string) error  { return nil }
func (f *failingPublisher) UnpublishProjectInfra(_ context.Context, _ string) error { return nil }

// A PublishAppEnv failure must ABORT the promotion. The old behavior —
// proceed and return 200 — promoted into an env with no manifests in git:
// Kargo had nothing deployable to update and the UI showed a green promotion
// that deployed nothing.
func TestPromote_PublishAppEnvFailureAborts(t *testing.T) {
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, &failingPublisher{})

	store.addApp(promoteTestApp(testProject))
	seedFullPromotionChain(store, testProject)

	resp := postAppPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		AppPromoteRequest{TargetEnvironment: "prod"})

	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on publish failure, got %d: %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "nothing was promoted") {
		t.Errorf("error should say nothing was promoted: %s", resp.Body.String())
	}
}

// ── ArgoCD Application gate ─────────────────────────────────────────────────

type fakeArgoAppGate struct {
	exists bool
	err    error
	calls  int
}

func (g *fakeArgoAppGate) HasAppForEnv(_ context.Context, _, _, _ string) (bool, error) {
	g.calls++
	return g.exists, g.err
}

type recordingPromoter struct {
	calls  int
	result KargoPromotionResult
	err    error // returned instead of a result when set (e.g. kube.ErrKargoNoFreight)
}

func (p *recordingPromoter) CreatePromotion(_ context.Context, _, appName, fromStage, toStage string) (KargoPromotionResult, error) {
	p.calls++
	if p.err != nil {
		return KargoPromotionResult{}, p.err
	}
	if p.result.Name == "" {
		p.result = KargoPromotionResult{Name: appName + "-to-" + toStage, Stage: toStage, Freight: "f-1", Phase: "Pending"}
	}
	return p.result, nil
}

// newTestAppPromoteMuxWithGate wires publisher + Kargo promoter + Application
// gate, with a near-zero gate timeout so absence tests don't sleep.
func newTestAppPromoteMuxWithGate(projectName string, pub GitOpsPublisher, promoter KargoPromoter, gate ArgoAppGate) (*http.ServeMux, *authHandler, *memAppStore) {
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
	appH.kargoPromoter = promoter
	appH.argoAppGate = gate
	appH.argoAppWaitTimeout = 10 * time.Millisecond

	rh := &rbacHandler{
		auth:       ah,
		orgStore:   &staticOrgProvider{org: testRBACOrg()},
		appHandler: appH,
	}
	rh.registerRoutes(mux)

	return mux, ah, store
}

// The heart of the fix: when the target env's ArgoCD Application does not
// exist, the promotion is REFUSED with a retryable 409 and no Kargo Promotion
// CR is created — previously Kargo promoted into the void and reported green.
func TestPromote_RefusedWhenTargetArgoAppMissing(t *testing.T) {
	rec := &recordingPublisher{}
	promoter := &recordingPromoter{}
	gate := &fakeArgoAppGate{exists: false}
	mux, ah, store := newTestAppPromoteMuxWithGate(testProject, rec, promoter, gate)

	store.addApp(promoteTestApp(testProject))
	seedFullPromotionChain(store, testProject)

	resp := postAppPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		AppPromoteRequest{TargetEnvironment: "prod"})

	if resp.Code != http.StatusConflict {
		t.Fatalf("expected 409 when Application missing, got %d: %s", resp.Code, resp.Body.String())
	}
	if promoter.calls != 0 {
		t.Errorf("Kargo promotion must NOT be created when the Application is missing (calls=%d)", promoter.calls)
	}
	// The publish DID happen — that's what makes the retry succeed later.
	if len(rec.publishedEnvs) == 0 || rec.publishedEnvs[0] != "prod" {
		t.Errorf("prod manifests should be published before the gate: %v", rec.publishedEnvs)
	}
	if !strings.Contains(resp.Body.String(), "retry") {
		t.Errorf("409 body should tell the operator to retry: %s", resp.Body.String())
	}
}

func TestPromote_ProceedsWhenTargetArgoAppExists(t *testing.T) {
	promoter := &recordingPromoter{}
	gate := &fakeArgoAppGate{exists: true}
	mux, ah, store := newTestAppPromoteMuxWithGate(testProject, &recordingPublisher{}, promoter, gate)

	store.addApp(promoteTestApp(testProject))
	seedFullPromotionChain(store, testProject)

	resp := postAppPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		AppPromoteRequest{TargetEnvironment: "prod"})

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}
	if promoter.calls != 1 {
		t.Errorf("expected exactly one Kargo promotion, got %d", promoter.calls)
	}
	if gate.calls == 0 {
		t.Error("gate was never consulted")
	}
	var pr AppPromoteResponse
	mustDecode(t, resp.Body.Bytes(), &pr)
	if pr.Mechanism != "kargo" {
		t.Errorf("mechanism = %q, want kargo", pr.Mechanism)
	}
}

// A broken gate (dynamic client error) must not freeze promotions — fail open
// with a logged warning rather than blocking every release on an RBAC or API
// hiccup.
func TestPromote_GateErrorFailsOpen(t *testing.T) {
	promoter := &recordingPromoter{}
	gate := &fakeArgoAppGate{err: fmt.Errorf("rbac denied")}
	mux, ah, store := newTestAppPromoteMuxWithGate(testProject, &recordingPublisher{}, promoter, gate)

	store.addApp(promoteTestApp(testProject))
	seedFullPromotionChain(store, testProject)

	resp := postAppPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		AppPromoteRequest{TargetEnvironment: "prod"})

	if resp.Code != http.StatusOK {
		t.Fatalf("gate error should fail open, got %d: %s", resp.Code, resp.Body.String())
	}
	if promoter.calls != 1 {
		t.Errorf("promotion should proceed on gate error, calls=%d", promoter.calls)
	}
}

// No gate wired (fake mode / no dynamic client): prior behavior is preserved.
func TestPromote_NoGateWiredProceeds(t *testing.T) {
	promoter := &recordingPromoter{}
	mux, ah, store := newTestAppPromoteMuxWithGate(testProject, &recordingPublisher{}, promoter, nil)

	store.addApp(promoteTestApp(testProject))
	seedFullPromotionChain(store, testProject)

	resp := postAppPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		AppPromoteRequest{TargetEnvironment: "prod"})

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 with no gate, got %d: %s", resp.Code, resp.Body.String())
	}
	if promoter.calls != 1 {
		t.Errorf("expected one promotion, got %d", promoter.calls)
	}
}


// fakeChainNudger records ArgoCD chain-nudge calls during the gate wait.
type fakeChainNudger struct {
	appNames    [][]string
	appSetNames [][]string
}

func (n *fakeChainNudger) RefreshAppsByName(_ context.Context, names []string) error {
	n.appNames = append(n.appNames, names)
	return nil
}
func (n *fakeChainNudger) RefreshAppSets(_ context.Context, names []string) error {
	n.appSetNames = append(n.appSetNames, names)
	return nil
}

// A first promotion must not wait out ArgoCD's poll cycles: while the gate
// polls for the target Application, the handler nudges the generator chain —
// the root app + {env}-composed by name, and the env's ApplicationSets.
func TestPromote_NudgesArgoChainWhileWaiting(t *testing.T) {
	rec := &recordingPublisher{}
	promoter := &recordingPromoter{}
	gate := &fakeArgoAppGate{exists: false}
	nudger := &fakeChainNudger{}
	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	ah.registerRoutes(mux)
	store := newMemAppStore()
	store.mu.Lock()
	store.apps[testProject] = make(map[string]*domain.App)
	store.mu.Unlock()
	appH := newAppHandler(store, nil, nil, nil)
	appH.gitOpsPublisher = rec
	appH.kargoPromoter = promoter
	appH.argoAppGate = gate
	appH.argoChainNudger = nudger
	appH.argoAppWaitTimeout = 10 * time.Millisecond
	rh := &rbacHandler{auth: ah, orgStore: &staticOrgProvider{org: testRBACOrg()}, appHandler: appH}
	rh.registerRoutes(mux)

	store.addApp(promoteTestApp(testProject))
	seedFullPromotionChain(store, testProject)

	resp := postAppPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		AppPromoteRequest{TargetEnvironment: "prod"})
	if resp.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", resp.Code, resp.Body.String())
	}
	if len(nudger.appNames) == 0 || len(nudger.appSetNames) == 0 {
		t.Fatalf("expected chain nudges during the gate wait, got apps=%v appsets=%v", nudger.appNames, nudger.appSetNames)
	}
	wantApps := []string{"suparship-apps", "prod-composed"}
	for i, n := range nudger.appNames[0] {
		if n != wantApps[i] {
			t.Errorf("nudged apps = %v, want %v", nudger.appNames[0], wantApps)
			break
		}
	}
	wantSets := []string{"prod", "prod-platform"}
	for i, n := range nudger.appSetNames[0] {
		if n != wantSets[i] {
			t.Errorf("nudged appsets = %v, want %v", nudger.appSetNames[0], wantSets)
			break
		}
	}
}

// The promote publish must carry the SOURCE env's current tag as a transient
// pin — never the create-time seed — so a first materialization of the target
// env deploys the promoted image, not a stale one. The stored app spec must
// stay unmutated (the pin is a publish-time clone, not state).
func TestPromote_PublishesTargetWithSourceTagPinned(t *testing.T) {
	rec := &recordingPublisher{}
	promoter := &recordingPromoter{}
	gate := &fakeArgoAppGate{exists: true}
	mux, ah, store := newTestAppPromoteMuxWithGate(testProject, rec, promoter, gate)

	store.addApp(promoteTestApp(testProject))
	seedFullPromotionChain(store, testProject) // staging release tag: v0.9.0

	resp := postAppPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		AppPromoteRequest{TargetEnvironment: "prod"})
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}
	if len(rec.publishedEnvApps) != 1 {
		t.Fatalf("expected exactly one PublishAppEnv, got %d", len(rec.publishedEnvApps))
	}
	got := rec.publishedEnvApps[0].Spec.EnvironmentDefaults["prod"]
	if got.PinnedImageTag != "v0.9.0" {
		t.Errorf("published prod PinnedImageTag = %q, want v0.9.0 (the staging tag)", got.PinnedImageTag)
	}
	if got.PinnedFrom != "" {
		t.Errorf("transient pin must not set PinnedFrom (env is not user-pinned), got %q", got.PinnedFrom)
	}
	stored, _ := store.GetApp(context.Background(), testProject, "my-app")
	if stored.Spec.EnvironmentDefaults["prod"].PinnedImageTag != "" {
		t.Error("stored app spec must be unmutated by the transient publish pin")
	}
}


// An initial promotion where NOTHING has flowed through the CD pipeline yet:
// the source Stage has no Freight, but the publish already pinned the source
// env's running tag into the target's values — so the promotion succeeds via
// the git mechanism instead of failing with a Kargo error.
func TestPromote_NoFreightFallsBackToPublishedPin(t *testing.T) {
	rec := &recordingPublisher{}
	promoter := &recordingPromoter{err: kube.ErrKargoNoFreight}
	gate := &fakeArgoAppGate{exists: true}
	mux, ah, store := newTestAppPromoteMuxWithGate(testProject, rec, promoter, gate)

	store.addApp(promoteTestApp(testProject))
	seedFullPromotionChain(store, testProject) // staging release tag: v0.9.0

	resp := postAppPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		AppPromoteRequest{TargetEnvironment: "prod"})
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}
	var out AppPromoteResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if out.Mechanism != "gitops" {
		t.Errorf("mechanism = %q, want gitops", out.Mechanism)
	}
	if out.KargoPromotion != nil {
		t.Error("no Kargo promotion should be reported when freight was absent")
	}
	if len(rec.publishedEnvs) != 1 {
		t.Fatalf("expected the target env publish to have happened, got %v", rec.publishedEnvs)
	}
	if got := rec.publishedEnvApps[0].Spec.EnvironmentDefaults["prod"].PinnedImageTag; got != "v0.9.0" {
		t.Errorf("published pin = %q, want the staging tag v0.9.0", got)
	}
}

// Without a publisher there is no pinned publish to fall back on, so the
// no-freight error must surface instead of a false-green promotion.
func TestPromote_NoFreightWithoutPublisherStillFails(t *testing.T) {
	promoter := &recordingPromoter{err: kube.ErrKargoNoFreight}
	gate := &fakeArgoAppGate{exists: true}
	mux, ah, store := newTestAppPromoteMuxWithGate(testProject, nil, promoter, gate)

	store.addApp(promoteTestApp(testProject))
	seedFullPromotionChain(store, testProject)

	resp := postAppPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		AppPromoteRequest{TargetEnvironment: "prod"})
	if resp.Code == http.StatusOK {
		t.Fatalf("expected an error status, got 200: %s", resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "no current freight") {
		t.Errorf("expected the no-freight error to surface, got: %s", resp.Body.String())
	}
}


// ANY Kargo failure — not just missing freight — degrades to the git-pin
// mechanism when the pinned publish happened: the user's release ships, and
// the platform problem surfaces as a human-friendly warning instead of a
// failed promotion.
func TestPromote_KargoErrorDegradesWithWarning(t *testing.T) {
	rec := &recordingPublisher{}
	promoter := &recordingPromoter{err: fmt.Errorf("Argo CD integration is disabled on this controller; cannot update Argo CD Application resources")}
	gate := &fakeArgoAppGate{exists: true}
	mux, ah, store := newTestAppPromoteMuxWithGate(testProject, rec, promoter, gate)

	store.addApp(promoteTestApp(testProject))
	seedFullPromotionChain(store, testProject)

	resp := postAppPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		AppPromoteRequest{TargetEnvironment: "prod"})
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}
	var out AppPromoteResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if out.Mechanism != "gitops" {
		t.Errorf("mechanism = %q, want gitops", out.Mechanism)
	}
	if out.Warning == "" {
		t.Fatal("expected a human-friendly warning naming the platform issue")
	}
	if !strings.Contains(out.Warning, "platform") {
		t.Errorf("warning should frame it as a platform issue, got %q", out.Warning)
	}
	if !strings.Contains(out.Warning, "Argo CD integration is disabled") {
		t.Errorf("warning should retain the raw detail for bug reports, got %q", out.Warning)
	}
}
