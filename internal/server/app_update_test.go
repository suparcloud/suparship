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
	"github.com/suparcloud/suparship/internal/tpl"
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
func (p *updatePublisher) PublishAppPreview(_ context.Context, _ *domain.App, _ *domain.EnvironmentInstance, _, _ string) error {
	return nil
}
func (p *updatePublisher) UnpublishApp(_ context.Context, _, _ string) error        { return nil }
func (p *updatePublisher) RemoveAppEnv(_ context.Context, _, _, _ string) error     { return nil }
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

// newTestAppUpdateMuxWithTemplates is newTestAppPromoteMuxWithPublisher with a
// built-in template registry wired in, so component-carrying PATCHes can resolve
// (and pin) their templates. Registry versions here stand in for "what the
// registry currently holds" — the pin-preservation tests turn on the app's
// stored version differing from these.
func newTestAppUpdateMuxWithTemplates(projectName string, pub GitOpsPublisher, templates []*tpl.Template) (*http.ServeMux, *authHandler, *memAppStore) {
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

	appH := newAppHandler(store, templates, nil, nil)
	appH.gitOpsPublisher = pub

	rh := &rbacHandler{
		auth:       ah,
		orgStore:   &staticOrgProvider{org: testRBACOrg()},
		appHandler: appH,
	}
	rh.registerRoutes(mux)
	return mux, ah, store
}

// pinnedComponentApp is a single-component app pinned to web-service@1.0.0 while
// the registry has moved on — the exact shape the silent-re-pin bug corrupted.
func pinnedComponentApp(projectName string) *domain.App {
	return &domain.App{
		Name:        "my-app",
		ProjectName: projectName,
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "web-service", Version: "1.0.0"},
			Components: []domain.ComponentSpec{{
				Name:     "web",
				Type:     domain.ComponentWeb,
				Enabled:  true,
				Template: &domain.AppTemplateRef{Name: "web-service", Version: "1.0.0"},
			}},
		},
	}
}

func templateAt(name, version string) *tpl.Template {
	return &tpl.Template{
		APIVersion: tpl.CurrentAPIVersion,
		Kind:       tpl.TemplateKind,
		Metadata:   tpl.Metadata{Name: name, Version: version},
		Spec: tpl.TemplateSpec{
			Title:    name,
			Category: "web",
			Engine:   tpl.Engine{Type: tpl.EngineHelm},
		},
	}
}

// TestUpdateApp_PreservesComponentPin is the regression for the silent upgrade:
// the UI round-trips a component without a version (the DTO never carried one),
// and the server used to fill the gap with the registry's CURRENT version — so
// renaming a component or editing an env var moved the deployed chart. An edit
// that doesn't name a version must leave both the component pin and the app-level
// mirror exactly where they were.
func TestUpdateApp_PreservesComponentPin(t *testing.T) {
	pub := &updatePublisher{}
	mux, ah, store := newTestAppUpdateMuxWithTemplates(testProject, pub,
		[]*tpl.Template{templateAt("web-service", "2.0.0")})
	store.addApp(pinnedComponentApp(testProject))

	rec := patchAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		updateAppRequest{Components: []ComponentCreateDTO{{
			Name: "web", Type: "web", Enabled: true,
			Template: &ComponentTemplateDTO{Name: "web-service"}, // no version — the round-trip
		}}})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := store.GetApp(context.Background(), testProject, "my-app")
	if v := got.Spec.Components[0].Template.Version; v != "1.0.0" {
		t.Errorf("component pin moved to %q; an edit must not upgrade the chart", v)
	}
	if v := got.Spec.Template.Version; v != "1.0.0" {
		t.Errorf("app-level mirror moved to %q; it must track the component's pin", v)
	}
}

// A retemplate onto a DIFFERENT chart is a deliberate change, so the new
// component correctly lands on that template's current version.
func TestUpdateApp_RetemplatePinsToRegistryVersion(t *testing.T) {
	pub := &updatePublisher{}
	mux, ah, store := newTestAppUpdateMuxWithTemplates(testProject, pub,
		[]*tpl.Template{templateAt("web-service", "2.0.0"), templateAt("worker", "3.1.0")})
	store.addApp(pinnedComponentApp(testProject))

	rec := patchAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		updateAppRequest{Components: []ComponentCreateDTO{{
			Name: "web", Type: "worker", Enabled: true,
			Template: &ComponentTemplateDTO{Name: "worker"},
		}}})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := store.GetApp(context.Background(), testProject, "my-app")
	tmpl := got.Spec.Components[0].Template
	if tmpl.Name != "worker" || tmpl.Version != "3.1.0" {
		t.Errorf("retemplated component = %+v, want worker@3.1.0", tmpl)
	}
	if got.Spec.Template.Name != "worker" || got.Spec.Template.Version != "3.1.0" {
		t.Errorf("mirror = %+v, want worker@3.1.0", got.Spec.Template)
	}
}

// An explicit version on the wire always wins — that is how the upgrade flow and
// direct API callers move a pin on purpose.
func TestUpdateApp_ExplicitComponentVersionWins(t *testing.T) {
	pub := &updatePublisher{}
	mux, ah, store := newTestAppUpdateMuxWithTemplates(testProject, pub,
		[]*tpl.Template{templateAt("web-service", "2.0.0")})
	store.addApp(pinnedComponentApp(testProject))

	rec := patchAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		updateAppRequest{Components: []ComponentCreateDTO{{
			Name: "web", Type: "web", Enabled: true,
			Template: &ComponentTemplateDTO{Name: "web-service", Version: "1.5.0"},
		}}})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := store.GetApp(context.Background(), testProject, "my-app")
	if v := got.Spec.Components[0].Template.Version; v != "1.5.0" {
		t.Errorf("explicit version = %q, want 1.5.0", v)
	}
	if v := got.Spec.Template.Version; v != "1.5.0" {
		t.Errorf("mirror = %q, want 1.5.0", v)
	}
}

// A NEW component added to an existing app has no stored pin to preserve, so it
// pins to the registry's current version.
func TestUpdateApp_NewComponentPinsToRegistryVersion(t *testing.T) {
	pub := &updatePublisher{}
	mux, ah, store := newTestAppUpdateMuxWithTemplates(testProject, pub,
		[]*tpl.Template{templateAt("web-service", "2.0.0"), templateAt("worker", "3.1.0")})
	store.addApp(pinnedComponentApp(testProject))

	rec := patchAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		updateAppRequest{Components: []ComponentCreateDTO{
			{Name: "web", Type: "web", Enabled: true, Template: &ComponentTemplateDTO{Name: "web-service"}},
			{Name: "jobs", Type: "worker", Enabled: true, Template: &ComponentTemplateDTO{Name: "worker"}},
		}})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := store.GetApp(context.Background(), testProject, "my-app")
	byName := map[string]domain.ComponentSpec{}
	for _, c := range got.Spec.Components {
		byName[c.Name] = c
	}
	if v := byName["web"].Template.Version; v != "1.0.0" {
		t.Errorf("existing component pin = %q, want 1.0.0 preserved", v)
	}
	if v := byName["jobs"].Template.Version; v != "3.1.0" {
		t.Errorf("new component pin = %q, want registry 3.1.0", v)
	}
	// The mirror stays on web-service: match-by-name beats Components[0] ordering.
	if got.Spec.Template.Name != "web-service" || got.Spec.Template.Version != "1.0.0" {
		t.Errorf("mirror = %+v, want web-service@1.0.0", got.Spec.Template)
	}
}

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
