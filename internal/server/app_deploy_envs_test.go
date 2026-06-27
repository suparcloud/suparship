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
