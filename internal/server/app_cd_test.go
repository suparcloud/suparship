package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/session"
	"github.com/suparcloud/suparship/internal/tpl"
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
	app.Spec.Images = []domain.AppImageBinding{{Name: "web", TagKey: "image.tag"}}
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

// TestUpdateApp_ImageSelectionMarksConfigured verifies that submitting an image
// selection — even an EMPTY one (the user disabled CD for every image) — flips
// cd.imagesConfigured, both persisted and echoed in the DTO. That is what makes a
// disable stick: a later publish then treats the empty selection as "watch nothing"
// instead of auto-binding the template's declared images.
func TestUpdateApp_ImageSelectionMarksConfigured(t *testing.T) {
	pub := &updatePublisher{}
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, pub)
	store.addApp(promoteTestApp(testProject))

	rec := patchAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		updateAppRequest{Images: &[]AppImageBindingDTO{}})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp updateAppResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.App.CD.ImagesConfigured {
		t.Errorf("response cd.imagesConfigured = false, want true")
	}
	got, _ := store.GetApp(context.Background(), testProject, "my-app")
	if !got.Spec.CD.ImagesConfigured {
		t.Errorf("persisted cd.imagesConfigured = false; empty selection must mark configured")
	}
}

// TestUpdateApp_CDOnlyEditPreservesImagesConfigured verifies a CD-only edit
// (toggling managed/autoPromote) does NOT reset the image-config intent. The CD DTO
// carries no imagesConfigured, so a bare replacement would otherwise silently clear
// it and re-enable the template auto-bind, undoing the user's disable.
func TestUpdateApp_CDOnlyEditPreservesImagesConfigured(t *testing.T) {
	pub := &updatePublisher{}
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, pub)
	app := promoteTestApp(testProject)
	// Watchable source so enabling cd.managed passes validation.
	app.Spec.Images = []domain.AppImageBinding{{Name: "web", TagKey: "image.tag"}}
	app.Spec.CD.ImagesConfigured = true // user already configured images earlier
	store.addApp(app)

	rec := patchAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		updateAppRequest{CD: &CDConfigDTO{Managed: true}})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := store.GetApp(context.Background(), testProject, "my-app")
	if !got.Spec.CD.ImagesConfigured {
		t.Errorf("CD-only edit reset imagesConfigured; want it preserved")
	}
	if !got.Spec.CD.Managed {
		t.Errorf("cd.managed = false, want true")
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

// The exact bug from the field: a COMPOSED app (images live per component, or
// auto-bind from component templates) could never enable cd.managed /
// auto-promote — validation only looked at app-level Spec.Images and
// values["image_repository"], which composed apps don't use.
func TestUpdateApp_CDComposedAppWithComponentTemplateImages(t *testing.T) {
	imageTemplate := &tpl.Template{
		APIVersion: tpl.CurrentAPIVersion,
		Kind:       tpl.TemplateKind,
		Metadata:   tpl.Metadata{Name: "svc-with-images", Version: "1.0.0"},
		Spec: tpl.TemplateSpec{
			Title:  "Svc",
			Engine: tpl.Engine{Type: tpl.EngineHelm},
			Images: []tpl.TemplateImage{{Name: "web", TagKey: "image.tag"}},
		},
	}

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
	appH := newAppHandler(store, []*tpl.Template{imageTemplate}, nil, nil)
	pub := &updatePublisher{}
	appH.gitOpsPublisher = pub
	rh := &rbacHandler{auth: ah, orgStore: &staticOrgProvider{org: testRBACOrg()}, appHandler: appH}
	rh.registerRoutes(mux)

	// Composed app: no app-level images, no image_repository — components carry
	// templates whose declared images publish auto-binds.
	app := &domain.App{
		Name:        "composed-cd",
		ProjectName: testProject,
		Spec: domain.AppSpec{
			Components: []domain.ComponentSpec{
				{Name: "frontend", Type: domain.ComponentWeb, Enabled: true, Template: &domain.AppTemplateRef{Name: "svc-with-images"}},
				{Name: "api", Type: domain.ComponentWeb, Enabled: true, Template: &domain.AppTemplateRef{Name: "svc-with-images"}},
			},
		},
	}
	store.addApp(app)

	rec := patchAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "composed-cd",
		updateAppRequest{CD: &CDConfigDTO{Managed: true, AutoPromote: true}})
	if rec.Code != http.StatusOK {
		t.Fatalf("composed app with component template images: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := store.GetApp(context.Background(), testProject, "composed-cd")
	if !got.Spec.CD.Managed || !got.Spec.CD.AutoPromote {
		t.Errorf("cd not persisted: %+v", got.Spec.CD)
	}

	// Component STORED selections also count, even once images are configured.
	app2 := &domain.App{
		Name:        "composed-cd-2",
		ProjectName: testProject,
		Spec: domain.AppSpec{
			CD: domain.CDConfig{ImagesConfigured: true},
			Components: []domain.ComponentSpec{
				{Name: "api", Type: domain.ComponentWeb, Enabled: true,
					Images: []domain.ComponentImage{{Name: "api", TagKey: "image.tag"}}},
			},
		},
	}
	store.addApp(app2)
	rec = patchAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "composed-cd-2",
		updateAppRequest{CD: &CDConfigDTO{Managed: true}})
	if rec.Code != http.StatusOK {
		t.Fatalf("composed app with stored component selection: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}
