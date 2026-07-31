package server

import (
	"context"
	"net/http"
	"testing"
)

// The create wizard sets per-(env, component) values only for the base deploy env
// (a new app deploys there); other envs are tuned later via the per-component
// editor. These tests exercise the EnvComponentValues create field folding into
// EnvironmentDefaults[env].ComponentValues, mirroring the update path.

func composedCreateReq(name string, env map[string]map[string]map[string]any) createAppRequest {
	return createAppRequest{
		Name:     name,
		Template: "web-service",
		Components: []ComponentCreateDTO{
			{Name: "api", Type: "web", Enabled: true, Template: &ComponentTemplateDTO{Name: "web-service"}},
			{Name: "worker", Type: "worker", Enabled: true, Template: &ComponentTemplateDTO{Name: "web-service"}},
		},
		EnvComponentValues: env,
	}
}

func TestCreateApp_EnvComponentValues_FoldedPerEnv(t *testing.T) {
	mux, ah, appStore, _ := newTestAppCreateMux()

	rec := postCreateAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "demo",
		composedCreateReq("my-app", map[string]map[string]map[string]any{
			"staging": {
				"api":    {"replicaCount": 3},
				"worker": {}, // empty overlay is skipped, not stored
			},
		}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	app, err := appStore.GetApp(context.Background(), "demo", "my-app")
	if err != nil {
		t.Fatalf("expected persisted app: %v", err)
	}
	staging := app.Spec.EnvironmentDefaults["staging"]
	if got := staging.ComponentValues["api"]["replicaCount"]; got != float64(3) {
		t.Errorf("EnvironmentDefaults[staging].ComponentValues[api].replicaCount = %v (%T), want 3", got, got)
	}
	if _, ok := staging.ComponentValues["worker"]; ok {
		t.Errorf("empty overlay for worker must be skipped, got %v", staging.ComponentValues["worker"])
	}
	// No base (all-envs) component overlay is set at creation — values are per-env only.
	for _, c := range app.Spec.Components {
		if len(c.Values) != 0 {
			t.Errorf("component %s must have no base Values at creation, got %v", c.Name, c.Values)
		}
	}
	// A different env has no component overrides.
	if ov, ok := app.Spec.EnvironmentDefaults["prod"]; ok && len(ov.ComponentValues) > 0 {
		t.Errorf("prod must have no component values, got %v", ov.ComponentValues)
	}
}

func TestCreateApp_EnvComponentValues_RejectsUnknownComponent(t *testing.T) {
	mux, ah, appStore, _ := newTestAppCreateMux()

	rec := postCreateAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "demo",
		composedCreateReq("my-app", map[string]map[string]map[string]any{
			"staging": {"nope": {"replicaCount": 1}},
		}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown component, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := appStore.GetApp(context.Background(), "demo", "my-app"); err == nil {
		t.Error("app must not be persisted when an unknown component is referenced")
	}
}
