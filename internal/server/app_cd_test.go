package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// TestCreateApp_CDConfigRoundTrips verifies the create request's cd block is
// persisted onto the app spec and echoed back in the detail DTO.
func TestCreateApp_CDConfigRoundTrips(t *testing.T) {
	mux, ah, appStore, _ := newTestAppCreateMux()

	rec := postCreateAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "demo", createAppRequest{
		Name:     "cd-app",
		Template: "web-service",
		// image satisfies the template's required input; image_repository is the
		// watchable source CD needs (the template declares no Images mapping).
		Values: map[string]any{"image": "img:v1", "image_repository": "ghcr.io/acme/cd-app"},
		CD:     &CDConfigDTO{Managed: true},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp createAppResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.App.CD.Managed {
		t.Errorf("response cd.managed = false, want true")
	}

	app, err := appStore.GetApp(context.Background(), "demo", "cd-app")
	if err != nil {
		t.Fatalf("get persisted app: %v", err)
	}
	if !app.Spec.CD.Managed {
		t.Errorf("persisted cd.managed = false, want true")
	}
}

// TestCreateApp_NoCDDefaultsDisabled confirms omitting cd leaves it disabled.
func TestCreateApp_NoCDDefaultsDisabled(t *testing.T) {
	mux, ah, appStore, _ := newTestAppCreateMux()

	rec := postCreateAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "demo", createAppRequest{
		Name:     "plain-app",
		Template: "web-service",
		Values:   map[string]any{"image": "img:v1"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	app, _ := appStore.GetApp(context.Background(), "demo", "plain-app")
	if app.Spec.CD.Managed {
		t.Errorf("cd should default disabled, got %+v", app.Spec.CD)
	}
}

// TestUpdateApp_CDConfigPersists verifies a PATCH cd block is applied and saved.
func TestUpdateApp_CDConfigPersists(t *testing.T) {
	pub := &updatePublisher{}
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, pub)
	app := promoteTestApp(testProject)
	// Give the app a watchable image source so enabling CD passes validation.
	app.Spec.Values = map[string]any{"image_repository": "ghcr.io/acme/my-app"}
	store.addApp(app)

	rec := patchAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		updateAppRequest{CD: &CDConfigDTO{Managed: true}})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if pub.publishApps != 1 {
		t.Errorf("expected one PublishApp call, got %d", pub.publishApps)
	}

	got, _ := store.GetApp(context.Background(), testProject, "my-app")
	if !got.Spec.CD.Managed {
		t.Errorf("persisted cd.managed = false, want true")
	}
}

// TestCreateApp_CDWithoutImageSourceAllowed documents that cd.managed does NOT
// require an image source at creation: image discovery needs the app's effective
// values (the canonical base only exists for a created app+env), so the operator
// selects which images Kargo manages from the app's Overview after create. The
// create therefore succeeds and persists cd.managed; the edit path still guards
// the selection (see TestUpdateApp_CDWithoutImageSourceRejected).
func TestCreateApp_CDWithoutImageSourceAllowed(t *testing.T) {
	mux, ah, appStore, _ := newTestAppCreateMux()

	rec := postCreateAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "demo", createAppRequest{
		Name:     "cd-noimg",
		Template: "web-service",
		CD:       &CDConfigDTO{Managed: true},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 (CD image selection deferred to Overview), got %d: %s", rec.Code, rec.Body.String())
	}
	app, err := appStore.GetApp(context.Background(), "demo", "cd-noimg")
	if err != nil {
		t.Fatalf("app should have been created: %v", err)
	}
	if !app.Spec.CD.Managed {
		t.Errorf("persisted cd.managed = false, want true")
	}
}

// TestUpdateApp_CDWithoutImageSourceRejected is the update-path counterpart:
// turning on cd.managed for an app with no watchable image must be rejected.
func TestUpdateApp_CDWithoutImageSourceRejected(t *testing.T) {
	pub := &updatePublisher{}
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, pub)
	store.addApp(promoteTestApp(testProject)) // no image_repository in Values

	rec := patchAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		updateAppRequest{CD: &CDConfigDTO{Managed: true}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 enabling CD with no image source, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := store.GetApp(context.Background(), testProject, "my-app")
	if got.Spec.CD.Managed {
		t.Errorf("cd.managed must not persist when validation rejects it")
	}
	if pub.publishApps != 0 {
		t.Errorf("rejected update must not publish, got %d PublishApp calls", pub.publishApps)
	}
}
