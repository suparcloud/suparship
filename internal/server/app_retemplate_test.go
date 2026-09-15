package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/tpl"
)

func postRetemplateJSON(mux *http.ServeMux, cookie *http.Cookie, project, app string, body any, dryRun bool) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	url := "/api/v1/projects/" + project + "/apps/" + app + "/upgrade-template"
	if dryRun {
		url += "?dryRun=1"
	}
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// Migrating a component to another template moves its pin to the target's
// current version, mirrors the primary, drops the env-scoped pin authored
// against the old chart, keeps values, and publishes once.
func TestRetemplate_ComponentMovesToTargetTemplate(t *testing.T) {
	pub := &updatePublisher{}
	mux, ah, store := newTestAppUpdateMuxWithTemplates(testProject, pub,
		[]*tpl.Template{templateAt("web-service", "2.0.0"), templateAt("worker", "3.1.0")})
	app := pinnedComponentApp(testProject)
	app.Spec.Components[0].Values = map[string]any{"replicas": 2}
	app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{
		"staging": {TemplateVersions: map[string]string{"web": "1.0.0"}},
	}
	store.addApp(app)

	rec := postRetemplateJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		map[string]any{"retemplate": map[string]any{"web": map[string]any{"template": "worker"}}}, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Components []retemplatedComponentDTO `json:"components"`
		Warnings   []retemplateWarningDTO    `json:"warnings"`
		DryRun     bool                      `json:"dryRun"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Components) != 1 || resp.Components[0].FromTemplate != "web-service" || resp.Components[0].ToTemplate != "worker" || resp.Components[0].ToVersion != "3.1.0" {
		t.Errorf("moved = %+v", resp.Components)
	}
	got, _ := store.GetApp(context.Background(), testProject, "my-app")
	if tr := got.Spec.Components[0].Template; tr.Name != "worker" || tr.Version != "3.1.0" {
		t.Errorf("component pin = %+v, want worker@3.1.0", tr)
	}
	if got.Spec.Template.Name != "worker" {
		t.Errorf("mirror = %+v, want worker", got.Spec.Template)
	}
	if got.Spec.EnvironmentDefaults["staging"].TemplateVersions != nil {
		t.Errorf("stale env pin survived: %v", got.Spec.EnvironmentDefaults["staging"].TemplateVersions)
	}
	if got.Spec.Components[0].Values["replicas"] != 2 {
		t.Errorf("values must be kept on migration, got %v", got.Spec.Components[0].Values)
	}
	if pub.publishApps != 1 {
		t.Errorf("publishApps = %d, want 1", pub.publishApps)
	}
}

func TestRetemplate_DryRunChangesNothing(t *testing.T) {
	pub := &updatePublisher{}
	mux, ah, store := newTestAppUpdateMuxWithTemplates(testProject, pub,
		[]*tpl.Template{templateAt("web-service", "2.0.0"), templateAt("worker", "3.1.0")})
	store.addApp(pinnedComponentApp(testProject))

	rec := postRetemplateJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		map[string]any{"retemplate": map[string]any{"web": map[string]any{"template": "worker", "version": "3.0.0"}}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Components []retemplatedComponentDTO `json:"components"`
		DryRun     bool                      `json:"dryRun"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if !resp.DryRun || len(resp.Components) != 1 || resp.Components[0].ToVersion != "3.0.0" {
		t.Errorf("dry-run response = %+v", resp)
	}
	got, _ := store.GetApp(context.Background(), testProject, "my-app")
	if got.Spec.Components[0].Template.Name != "web-service" || pub.publishApps != 0 {
		t.Errorf("dry run mutated the app (pin=%+v, publishes=%d)", got.Spec.Components[0].Template, pub.publishApps)
	}
}

func TestRetemplate_RejectsUnknownTemplateSameTemplateAndMixedForms(t *testing.T) {
	pub := &updatePublisher{}
	mux, ah, store := newTestAppUpdateMuxWithTemplates(testProject, pub,
		[]*tpl.Template{templateAt("web-service", "2.0.0"), templateAt("worker", "3.1.0")})
	store.addApp(pinnedComponentApp(testProject))
	cookie := sessionCookieFor(ah, "alice", "org_admin")

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"unknown template", map[string]any{"retemplate": map[string]any{"web": map[string]any{"template": "nope"}}}, http.StatusUnprocessableEntity},
		{"same template", map[string]any{"retemplate": map[string]any{"web": map[string]any{"template": "web-service"}}}, http.StatusBadRequest},
		{"unknown component", map[string]any{"retemplate": map[string]any{"api": map[string]any{"template": "worker"}}}, http.StatusBadRequest},
		{"mixed with components", map[string]any{"retemplate": map[string]any{"web": map[string]any{"template": "worker"}}, "components": map[string]string{"web": "2.0.0"}}, http.StatusBadRequest},
		{"env-scoped", map[string]any{"retemplate": map[string]any{"web": map[string]any{"template": "worker"}}, "environment": "staging"}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		rec := postRetemplateJSON(mux, cookie, testProject, "my-app", tc.body, false)
		if rec.Code != tc.want {
			t.Errorf("%s: got %d, want %d: %s", tc.name, rec.Code, tc.want, rec.Body.String())
		}
	}
	if pub.publishApps != 0 {
		t.Errorf("rejected requests must not publish, got %d", pub.publishApps)
	}
}

// A publish failure restores the pins and the mirror.
func TestRetemplate_PublishFailureRestoresPins(t *testing.T) {
	pub := &updatePublisher{failPublish: true}
	mux, ah, store := newTestAppUpdateMuxWithTemplates(testProject, pub,
		[]*tpl.Template{templateAt("web-service", "2.0.0"), templateAt("worker", "3.1.0")})
	store.addApp(pinnedComponentApp(testProject))

	rec := postRetemplateJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		map[string]any{"retemplate": map[string]any{"web": map[string]any{"template": "worker"}}}, false)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := store.GetApp(context.Background(), testProject, "my-app")
	if got.Spec.Components[0].Template.Name != "web-service" || got.Spec.Template.Name != "web-service" {
		t.Errorf("pins not restored: comp=%+v mirror=%+v", got.Spec.Components[0].Template, got.Spec.Template)
	}
}

// A component-less app migrates through the app-level pin.
func TestRetemplate_TemplatelessApp(t *testing.T) {
	pub := &updatePublisher{}
	mux, ah, store := newTestAppUpdateMuxWithTemplates(testProject, pub,
		[]*tpl.Template{templateAt("web-service", "2.0.0"), templateAt("worker", "3.1.0")})
	store.addApp(&domain.App{
		Name: "byo", ProjectName: testProject,
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "web-service", Version: "1.0.0"},
			EnvironmentDefaults: map[string]domain.EnvironmentOverride{
				"prod": {TemplateVersions: map[string]string{"": "1.0.0"}},
			},
		},
	})

	rec := postRetemplateJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "byo",
		map[string]any{"template": "worker"}, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := store.GetApp(context.Background(), testProject, "byo")
	if got.Spec.Template.Name != "worker" || got.Spec.Template.Version != "3.1.0" {
		t.Errorf("app pin = %+v, want worker@3.1.0", got.Spec.Template)
	}
	if got.Spec.EnvironmentDefaults["prod"].TemplateVersions != nil {
		t.Errorf("stale app-level env pin survived: %v", got.Spec.EnvironmentDefaults["prod"].TemplateVersions)
	}
	// The component form is refused on a component-less app.
	rec = postRetemplateJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "byo",
		map[string]any{"retemplate": map[string]any{"x": map[string]any{"template": "worker"}}}, false)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("component form on templateless app = %d, want 400", rec.Code)
	}
}

func TestUnknownValueKeys(t *testing.T) {
	known := map[string]any{
		"replicas": 1,
		"image":    map[string]any{"repository": "nginx", "tag": "1"},
		"service":  map[string]any{"port": 80},
		"flat":     "scalar",
	}
	overlays := []map[string]any{
		{"replicas": 3, "image": map[string]any{"tag": "2", "pullPolicy": "Always"}, "ingress": map[string]any{"enabled": true}},
		{"flat": map[string]any{"nested": 1}, "service": map[string]any{"port": 8080}, "ingress": map[string]any{"enabled": false}},
	}
	got := unknownValueKeys(overlays, known)
	want := []string{"flat.nested", "image.pullPolicy", "ingress.enabled"}
	if len(got) != len(want) {
		t.Fatalf("unknown = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("unknown[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
