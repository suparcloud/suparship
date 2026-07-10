package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
)

// PATCH deployEnvs persists each env's per-env Deploy flag (direct-app opt-in).
func TestUpdateApp_DeployEnvsPersisted(t *testing.T) {
	pub := &recordingPublisher{}
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, pub)
	app := promoteTestApp(testProject)
	app.Spec.DeliveryMode = domain.DeliveryDirect
	store.addApp(app)
	seedFullPromotionChain(store, testProject)

	rec := patchAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		updateAppRequest{DeployEnvs: map[string]bool{"prod": true}})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := store.GetApp(context.Background(), testProject, "my-app")
	d := got.Spec.EnvironmentDefaults["prod"].Deploy
	if d == nil || !*d {
		t.Fatalf("expected prod Deploy=true, got %v", d)
	}
}

// The undeploy endpoint opts the env out (Deploy=false) and removes it from the
// cluster via the publisher.
func TestUndeployAppEnv(t *testing.T) {
	pub := &recordingPublisher{}
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, pub)
	app := promoteTestApp(testProject)
	app.Spec.DeliveryMode = domain.DeliveryDirect
	store.addApp(app)
	seedFullPromotionChain(store, testProject)

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/projects/"+testProject+"/apps/my-app/environments/prod/undeploy", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	got, _ := store.GetApp(context.Background(), testProject, "my-app")
	d := got.Spec.EnvironmentDefaults["prod"].Deploy
	if d == nil || *d {
		t.Errorf("expected prod Deploy=false after undeploy, got %v", d)
	}
	if len(pub.removedEnvs) != 1 || pub.removedEnvs[0] != "prod" {
		t.Errorf("expected publisher to remove prod, got %v", pub.removedEnvs)
	}
}

// Undeploying the base env (lowest-Order stable env) is rejected — it is the
// pipeline's warehouse source and the previews' clone target.
func TestUndeployAppEnv_BaseEnvRejected(t *testing.T) {
	pub := &recordingPublisher{}
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, pub)
	store.addApp(promoteTestApp(testProject)) // pipeline app (default)
	seedFullPromotionChain(store, testProject) // base env = staging (Order 1)

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/projects/"+testProject+"/apps/my-app/environments/staging/undeploy", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for base env, got %d: %s", w.Code, w.Body.String())
	}
	if len(pub.removedEnvs) != 0 {
		t.Errorf("base env undeploy must not remove anything, got %v", pub.removedEnvs)
	}
	got, _ := store.GetApp(context.Background(), testProject, "my-app")
	if d := got.Spec.EnvironmentDefaults["staging"].Deploy; d != nil {
		t.Errorf("base env Deploy must stay unset, got %v", *d)
	}
}

// A pipeline app's undeploy of a higher env republishes the app so the Kargo
// chain rebuilds without that env.
func TestUndeployAppEnv_PipelineRepublishes(t *testing.T) {
	pub := &recordingPublisher{}
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, pub)
	store.addApp(promoteTestApp(testProject)) // pipeline app
	seedFullPromotionChain(store, testProject)

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/projects/"+testProject+"/apps/my-app/environments/prod/undeploy", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(pub.removedEnvs) != 1 || pub.removedEnvs[0] != "prod" {
		t.Errorf("expected prod removed, got %v", pub.removedEnvs)
	}
	if pub.publishAppCalls != 1 {
		t.Errorf("expected one PublishApp (kargo chain rebuild) for a pipeline app, got %d", pub.publishAppCalls)
	}
}

// Promoting to a decommissioned env (Deploy=false) is rejected — re-enable first.
func TestPromoteApp_DecommissionedTargetRejected(t *testing.T) {
	pub := &recordingPublisher{}
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, pub)
	app := promoteTestApp(testProject)
	no := false
	app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{"prod": {Deploy: &no}}
	store.addApp(app)
	seedFullPromotionChain(store, testProject)

	rec := postAppPromoteJSON(mux, sessionCookieFor(ah, "alice", "org_admin"),
		testProject, "my-app", AppPromoteRequest{TargetEnvironment: "prod"})
	// Consistent with the other promote guards (pinned/preview): a bad promotion
	// request is a 400.
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for decommissioned target, got %d: %s", rec.Code, rec.Body.String())
	}
}

// A pipeline app reports the real per-env Deploy state so the UI can show a
// decommissioned env (vs. hardcoding true).
func TestBuildEnvSummaryDTOs_PipelineReportsDecommissioned(t *testing.T) {
	no := false
	app := promoteTestApp(testProject)
	app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{"prod": {Deploy: &no}}
	envs := []*domain.AppEnvironment{
		{AppName: "my-app", EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1},
		{AppName: "my-app", EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2},
	}
	dtos := buildEnvSummaryDTOs(app, envs)
	byEnv := map[string]AppEnvironmentSummaryDTO{}
	for _, d := range dtos {
		byEnv[d.EnvName] = d
	}
	if !byEnv["staging"].IsBase || !byEnv["staging"].Deploy {
		t.Errorf("staging should be base+deployed, got %+v", byEnv["staging"])
	}
	if byEnv["prod"].Deploy {
		t.Errorf("decommissioned prod should report Deploy=false, got %+v", byEnv["prod"])
	}
}
