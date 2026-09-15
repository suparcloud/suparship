package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/tpl"
)

// The "<env> only" scope: an env-scoped patch lands in
// EnvironmentDefaults[env].ComponentEnvVars and leaves the component's app-wide
// settings and the other env untouched.
func TestUpdateApp_EnvComponentEnvVarsPatchIsEnvScoped(t *testing.T) {
	mux, ah, store := newTestAppPromoteMuxWithGate(testProject, &recordingPublisher{}, nil, nil)
	app := envVarsTestApp(testProject)
	app.Spec.Components[0].EnvVars = []domain.ComponentEnvVar{{Name: "LOG_LEVEL", Value: "info"}}
	store.addApp(app)
	cookie := sessionCookieFor(ah, "alice", "org_admin")

	rec := patchComponentEnvJSON(mux, cookie, testProject, "my-app", map[string]any{
		"envComponentEnvVars": map[string]map[string]ComponentEnvVarsPatchDTO{
			"staging": {"web": {EnvVars: &[]ComponentEnvVarDTO{{Name: "FEATURE_X", Value: "on"}, {Name: "LOG_LEVEL", Value: "debug"}}}},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := store.GetApp(context.Background(), testProject, "my-app")
	if v := got.Spec.Components[0].EnvVars; len(v) != 1 || v[0].Value != "info" {
		t.Errorf("app-wide list must be untouched, got %+v", v)
	}
	ov := got.Spec.EnvironmentDefaults["staging"].ComponentEnvVars["web"]
	if len(ov.EnvVars) != 2 || ov.EnvVars[0].Name != "FEATURE_X" {
		t.Errorf("staging override = %+v", ov)
	}
	if _, leaked := got.Spec.EnvironmentDefaults["prod"].ComponentEnvVars["web"]; leaked {
		t.Error("prod must have no override")
	}
	// Effective staging list layers env over app-wide.
	_, eff := domain.EffectiveComponentEnvVars(got, got.Spec.Components[0], "staging")
	if len(eff) != 2 || eff[0].Name != "LOG_LEVEL" || eff[0].Value != "debug" || eff[1].Name != "FEATURE_X" {
		t.Errorf("effective staging = %+v", eff)
	}

	// The detail DTO exposes the override per env.
	dtos := componentDTOs(got.Spec.Components, got.Spec.EnvironmentDefaults)
	if e := dtos[0].EnvEnvVars["staging"]; len(e.EnvVars) != 2 {
		t.Errorf("DTO envEnvVars[staging] = %+v", dtos[0].EnvEnvVars)
	}

	// An empty patch removes the pair.
	rec = patchComponentEnvJSON(mux, cookie, testProject, "my-app", map[string]any{
		"envComponentEnvVars": map[string]map[string]ComponentEnvVarsPatchDTO{
			"staging": {"web": {EnvVars: &[]ComponentEnvVarDTO{}}},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("clear: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ = store.GetApp(context.Background(), testProject, "my-app")
	if got.Spec.EnvironmentDefaults["staging"].ComponentEnvVars != nil {
		t.Errorf("cleared override should be gone, got %+v", got.Spec.EnvironmentDefaults["staging"].ComponentEnvVars)
	}
}

func TestUpdateApp_EnvComponentEnvVarsPatchRejections(t *testing.T) {
	mux, ah, store := newTestAppPromoteMuxWithGate(testProject, &recordingPublisher{}, nil, nil)
	store.addApp(envVarsTestApp(testProject))
	cookie := sessionCookieFor(ah, "alice", "org_admin")

	// fromSecret while the effective posture inherits → 422.
	rec := patchComponentEnvJSON(mux, cookie, testProject, "my-app", map[string]any{
		"envComponentEnvVars": map[string]map[string]ComponentEnvVarsPatchDTO{
			"staging": {"web": {EnvVars: &[]ComponentEnvVarDTO{{Name: "A", FromSecret: "X"}}}},
		},
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("inherit-mode fromSecret: expected 422, got %d: %s", rec.Code, rec.Body.String())
	}
	// Same entry with the env posture flipped to curated → accepted.
	off := false
	rec = patchComponentEnvJSON(mux, cookie, testProject, "my-app", map[string]any{
		"envComponentEnvVars": map[string]map[string]ComponentEnvVarsPatchDTO{
			"staging": {"web": {InheritAppVars: &off, EnvVars: &[]ComponentEnvVarDTO{{Name: "A", FromSecret: "X"}}}},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("curated env override: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	// Unknown component → 400.
	rec = patchComponentEnvJSON(mux, cookie, testProject, "my-app", map[string]any{
		"envComponentEnvVars": map[string]map[string]ComponentEnvVarsPatchDTO{"staging": {"nope": {}}},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown component: expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := store.GetApp(context.Background(), testProject, "my-app")
	if got.Spec.Components[0].EnvVars != nil {
		t.Error("env-scoped patches must never touch the app-wide list")
	}
}

// Removing a component from the list takes its per-env state with it.
func TestUpdateApp_ComponentRemovalPrunesEnvState(t *testing.T) {
	pub := &updatePublisher{}
	mux, ah, store := newTestAppUpdateMuxWithTemplates(testProject, pub,
		[]*tpl.Template{templateAt("web-service", "2.0.0"), templateAt("worker", "3.1.0")})
	app := pinnedComponentApp(testProject)
	app.Spec.Components = append(app.Spec.Components, domain.ComponentSpec{
		Name: "jobs", Type: domain.ComponentWorker, Enabled: true,
		Template: &domain.AppTemplateRef{Name: "worker", Version: "3.1.0"},
	})
	app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{
		"staging": {
			ComponentValues:  map[string]map[string]any{"jobs": {"x": 1}, "web": {"y": 2}},
			ComponentEnvVars: map[string]domain.ComponentEnvOverride{"jobs": {EnvVars: []domain.ComponentEnvVar{{Name: "A", Value: "1"}}}},
			TemplateVersions: map[string]string{"jobs": "3.0.0"},
		},
	}
	store.addApp(app)

	rec := patchAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		updateAppRequest{Components: []ComponentCreateDTO{{
			Name: "web", Type: "web", Enabled: true, Template: &ComponentTemplateDTO{Name: "web-service"},
		}}})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := store.GetApp(context.Background(), testProject, "my-app")
	ov := got.Spec.EnvironmentDefaults["staging"]
	if _, stale := ov.ComponentValues["jobs"]; stale || ov.ComponentEnvVars != nil || ov.TemplateVersions != nil {
		t.Errorf("removed component's env state survived: %+v", ov)
	}
	if ov.ComponentValues["web"]["y"] != 2 {
		t.Errorf("kept component's env values lost: %+v", ov.ComponentValues)
	}
}
