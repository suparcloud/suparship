package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
)

// renamePublisher records which app names were published / unpublished so the
// rename test can assert the new app was created and the old one torn down.
type renamePublisher struct {
	published   []string
	unpublished []string
	failPublish bool
}

func (p *renamePublisher) PublishApp(_ context.Context, app *domain.App, _ []*domain.AppEnvironment) error {
	if p.failPublish {
		return fmt.Errorf("simulated publish failure")
	}
	p.published = append(p.published, app.Name)
	return nil
}
func (p *renamePublisher) PublishAppEnv(_ context.Context, _ *domain.App, _ *domain.AppEnvironment) error {
	return nil
}
func (p *renamePublisher) PublishAppPreview(_ context.Context, _ *domain.App, _ *domain.EnvironmentInstance, _, _ string) error {
	return nil
}
func (p *renamePublisher) UnpublishApp(_ context.Context, _, app string) error {
	p.unpublished = append(p.unpublished, app)
	return nil
}
func (p *renamePublisher) UnpublishProjectApps(_ context.Context, _ string) error  { return nil }
func (p *renamePublisher) UnpublishProjectInfra(_ context.Context, _ string) error { return nil }

func postRenameJSON(mux *http.ServeMux, cookie *http.Cookie, project, app, newName string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(renameAppRequest{NewName: newName})
	req := httptest.NewRequest("POST", "/api/v1/projects/"+project+"/apps/"+app+"/rename", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestRenameApp_RecreatesUnderNewNameAndTearsDownOld(t *testing.T) {
	pub := &renamePublisher{}
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, pub)
	store.addApp(promoteTestApp(testProject))
	store.addEnv(&domain.AppEnvironment{
		AppName: "my-app", ProjectName: testProject, EnvName: "staging",
		EnvType: domain.AppEnvStaging, Namespace: testProject + "-my-app-staging",
	})

	rec := postRenameJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app", "renamed-app")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// New app exists with its env; old app is gone.
	if _, err := store.GetApp(context.Background(), testProject, "renamed-app"); err != nil {
		t.Errorf("renamed app not in store: %v", err)
	}
	if envs, _ := store.ListAppEnvironments(context.Background(), testProject, "renamed-app"); len(envs) != 1 {
		t.Errorf("expected the env copied to the new app, got %d", len(envs))
	}
	if _, err := store.GetApp(context.Background(), testProject, "my-app"); err == nil {
		t.Error("old app must be deleted from the store")
	}

	// New name published; old name unpublished.
	if len(pub.published) != 1 || pub.published[0] != "renamed-app" {
		t.Errorf("expected publish of renamed-app, got %v", pub.published)
	}
	if len(pub.unpublished) != 1 || pub.unpublished[0] != "my-app" {
		t.Errorf("expected unpublish of my-app, got %v", pub.unpublished)
	}
}

func TestRenameApp_ConflictWhenNameTaken(t *testing.T) {
	pub := &renamePublisher{}
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, pub)
	store.addApp(promoteTestApp(testProject)) // my-app
	taken := promoteTestApp(testProject)
	taken.Name = "taken"
	store.addApp(taken)

	rec := postRenameJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app", "taken")
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 when target name exists, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(pub.published) != 0 {
		t.Error("nothing should be published on a conflict")
	}
}

func TestRenameApp_RejectsInvalidName(t *testing.T) {
	pub := &renamePublisher{}
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, pub)
	store.addApp(promoteTestApp(testProject))

	rec := postRenameJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app", "Bad_Name")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for invalid DNS label, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRenameApp_RollsBackOnPublishFailure(t *testing.T) {
	pub := &renamePublisher{failPublish: true}
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, pub)
	store.addApp(promoteTestApp(testProject))

	rec := postRenameJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app", "renamed-app")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on publish failure, got %d: %s", rec.Code, rec.Body.String())
	}
	// Old app intact; new app rolled back; old not unpublished.
	if _, err := store.GetApp(context.Background(), testProject, "my-app"); err != nil {
		t.Error("old app must remain on publish failure")
	}
	if _, err := store.GetApp(context.Background(), testProject, "renamed-app"); err == nil {
		t.Error("new app must be rolled back on publish failure")
	}
	if len(pub.unpublished) != 0 {
		t.Error("old app must not be torn down when the rename failed")
	}
}
