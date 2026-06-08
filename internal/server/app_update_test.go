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

// updatePublisher records PublishApp calls and can be made to fail, to test
// the edit handler's publish + rollback behaviour.
type updatePublisher struct {
	publishApps int
	failPublish bool
}

func (p *updatePublisher) PublishApp(_ context.Context, _ *domain.App, _ []*domain.AppEnvironment) error {
	p.publishApps++
	if p.failPublish {
		return fmt.Errorf("simulated publish failure")
	}
	return nil
}
func (p *updatePublisher) PublishAppEnv(_ context.Context, _ *domain.App, _ *domain.AppEnvironment) error {
	return nil
}
func (p *updatePublisher) UnpublishApp(_ context.Context, _, _ string) error        { return nil }
func (p *updatePublisher) UnpublishProjectApps(_ context.Context, _ string) error   { return nil }
func (p *updatePublisher) UnpublishProjectInfra(_ context.Context, _ string) error  { return nil }

func patchAppJSON(mux *http.ServeMux, cookie *http.Cookie, project, app string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("PATCH", "/api/v1/projects/"+project+"/apps/"+app, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func ptr[T any](v T) *T { return &v }

func TestUpdateApp_RejectsTemplateChange(t *testing.T) {
	pub := &updatePublisher{}
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, pub)
	store.addApp(promoteTestApp(testProject))

	rec := patchAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		updateAppRequest{Template: "different-template"})

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for template change, got %d: %s", rec.Code, rec.Body.String())
	}
	if pub.publishApps != 0 {
		t.Error("publish must not run when the request is rejected")
	}
}

func TestUpdateApp_MetadataEditPersistsAndPublishes(t *testing.T) {
	pub := &updatePublisher{}
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, pub)
	store.addApp(promoteTestApp(testProject))

	rec := patchAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		updateAppRequest{DisplayName: ptr("Renamed App"), Description: ptr("new desc")})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if pub.publishApps != 1 {
		t.Errorf("expected one PublishApp call, got %d", pub.publishApps)
	}
	got, _ := store.GetApp(context.Background(), testProject, "my-app")
	if got.Spec.DisplayName != "Renamed App" || got.Spec.Description != "new desc" {
		t.Errorf("metadata not persisted: %+v", got.Spec)
	}
}

func TestUpdateApp_RejectsBadImageRepository(t *testing.T) {
	pub := &updatePublisher{}
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, pub)
	store.addApp(promoteTestApp(testProject))

	// image_repository with a scheme is invalid — rejected before any publish.
	rec := patchAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		updateAppRequest{Values: ptr(map[string]any{"image_repository": "https://ghcr.io/acme/web"})})

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for bad image repo, got %d: %s", rec.Code, rec.Body.String())
	}
	if pub.publishApps != 0 {
		t.Error("publish must not run on validation failure")
	}
}

func TestUpdateApp_RollsBackOnPublishFailure(t *testing.T) {
	pub := &updatePublisher{failPublish: true}
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, pub)
	app := promoteTestApp(testProject)
	app.Spec.DisplayName = "Original"
	store.addApp(app)

	rec := patchAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		updateAppRequest{DisplayName: ptr("Should Roll Back")})

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on publish failure, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := store.GetApp(context.Background(), testProject, "my-app")
	if got.Spec.DisplayName != "Original" {
		t.Errorf("displayName should have rolled back to Original, got %q", got.Spec.DisplayName)
	}
}
