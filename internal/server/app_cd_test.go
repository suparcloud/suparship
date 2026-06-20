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
		Values:   map[string]any{"image": "img:v1"},
		CD:       &CDConfigDTO{Managed: true, ImageTagPath: "image.tag"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp createAppResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.App.CD.Managed || resp.App.CD.ImageTagPath != "image.tag" {
		t.Errorf("response cd = %+v, want {Managed:true ImageTagPath:image.tag}", resp.App.CD)
	}

	app, err := appStore.GetApp(context.Background(), "demo", "cd-app")
	if err != nil {
		t.Fatalf("get persisted app: %v", err)
	}
	if !app.Spec.CD.Managed || app.Spec.CD.ImageTagPath != "image.tag" {
		t.Errorf("persisted cd = %+v, want {Managed:true ImageTagPath:image.tag}", app.Spec.CD)
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

// TestCreateApp_CDImageTagPathDetectedFromRootImage verifies that when CD is
// enabled without an explicit path, the chart shape is inferred from the app's
// overrides — here a root-level image block → "image.tag".
func TestCreateApp_CDImageTagPathDetectedFromRootImage(t *testing.T) {
	mux, ah, appStore, _ := newTestAppCreateMux()

	rec := postCreateAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "demo", createAppRequest{
		Name:     "root-img-app",
		Template: "web-service",
		Values:   map[string]any{"image": "img:v1"},
		RawValues: map[string]any{
			"image": map[string]any{"repository": "r", "tag": "seed"},
		},
		CD: &CDConfigDTO{Managed: true}, // no ImageTagPath → infer
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	app, _ := appStore.GetApp(context.Background(), "demo", "root-img-app")
	if app.Spec.CD.ImageTagPath != "image.tag" {
		t.Errorf("inferred path = %q, want %q", app.Spec.CD.ImageTagPath, "image.tag")
	}
}

// TestCreateApp_CDImageTagPathDetectedFromComponent verifies the canonical
// component layout is inferred to "components.web.image.tag".
func TestCreateApp_CDImageTagPathDetectedFromComponent(t *testing.T) {
	mux, ah, appStore, _ := newTestAppCreateMux()

	rec := postCreateAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "demo", createAppRequest{
		Name:     "comp-img-app",
		Template: "web-service",
		Values:   map[string]any{"image": "img:v1"},
		RawValues: map[string]any{
			"components": map[string]any{
				"web": map[string]any{"image": map[string]any{"tag": "seed"}},
			},
		},
		CD: &CDConfigDTO{Managed: true},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	app, _ := appStore.GetApp(context.Background(), "demo", "comp-img-app")
	if app.Spec.CD.ImageTagPath != "components.web.image.tag" {
		t.Errorf("inferred path = %q, want %q", app.Spec.CD.ImageTagPath, "components.web.image.tag")
	}
}

// TestUpdateApp_CDConfigPersists verifies a PATCH cd block is applied and saved.
func TestUpdateApp_CDConfigPersists(t *testing.T) {
	pub := &updatePublisher{}
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, pub)
	store.addApp(promoteTestApp(testProject))

	rec := patchAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		updateAppRequest{CD: &CDConfigDTO{Managed: true, ImageTagPath: "image.tag"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if pub.publishApps != 1 {
		t.Errorf("expected one PublishApp call, got %d", pub.publishApps)
	}

	got, _ := store.GetApp(context.Background(), testProject, "my-app")
	if !got.Spec.CD.Managed || got.Spec.CD.ImageTagPath != "image.tag" {
		t.Errorf("persisted cd = %+v, want {Managed:true ImageTagPath:image.tag}", got.Spec.CD)
	}
}
