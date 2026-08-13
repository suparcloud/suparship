package domain

import "testing"

func TestAppForEnvTemplateVersions(t *testing.T) {
	base := func() *App {
		return &App{
			Name: "my-app",
			Spec: AppSpec{
				Template: AppTemplateRef{Name: "web-service", Version: "1.0.0"},
				Components: []ComponentSpec{
					{Name: "web", Template: &AppTemplateRef{Name: "web-service", Version: "1.0.0"}},
					{Name: "worker", Template: &AppTemplateRef{Name: "worker", Version: "2.0.0"}},
				},
			},
		}
	}

	t.Run("no overrides returns the app itself", func(t *testing.T) {
		app := base()
		if got := AppForEnvTemplateVersions(app, "staging"); got != app {
			t.Fatal("expected the identical pointer when the env pins nothing")
		}
	})

	t.Run("component override applies and syncs the primary mirror", func(t *testing.T) {
		app := base()
		app.Spec.EnvironmentDefaults = map[string]EnvironmentOverride{
			"staging": {TemplateVersions: map[string]string{"web": "1.1.0"}},
		}
		got := AppForEnvTemplateVersions(app, "staging")
		if got == app {
			t.Fatal("expected a copy when the env pins versions")
		}
		if v := got.Spec.Components[0].Template.Version; v != "1.1.0" {
			t.Errorf("web version = %q, want 1.1.0", v)
		}
		if v := got.Spec.Components[1].Template.Version; v != "2.0.0" {
			t.Errorf("worker version = %q, want 2.0.0 (unpinned)", v)
		}
		if v := got.Spec.Template.Version; v != "1.1.0" {
			t.Errorf("primary mirror = %q, want 1.1.0", v)
		}
		// The original must be untouched — the copy must not share Template refs.
		if v := app.Spec.Components[0].Template.Version; v != "1.0.0" {
			t.Errorf("original mutated to %q", v)
		}
		if v := app.Spec.Template.Version; v != "1.0.0" {
			t.Errorf("original mirror mutated to %q", v)
		}
	})

	t.Run("other envs resolve to the app itself", func(t *testing.T) {
		app := base()
		app.Spec.EnvironmentDefaults = map[string]EnvironmentOverride{
			"staging": {TemplateVersions: map[string]string{"web": "1.1.0"}},
		}
		if got := AppForEnvTemplateVersions(app, "prod"); got != app {
			t.Fatal("prod pins nothing — expected the identical pointer")
		}
	})

	t.Run("reserved empty key pins a templateless app's app-level template", func(t *testing.T) {
		app := &App{
			Name: "byo",
			Spec: AppSpec{
				Template: AppTemplateRef{Name: "byo-chart", Version: "0.5.0"},
				EnvironmentDefaults: map[string]EnvironmentOverride{
					"staging": {TemplateVersions: map[string]string{"": "0.6.0"}},
				},
			},
		}
		got := AppForEnvTemplateVersions(app, "staging")
		if v := got.Spec.Template.Version; v != "0.6.0" {
			t.Errorf("app-level version = %q, want 0.6.0", v)
		}
		if v := app.Spec.Template.Version; v != "0.5.0" {
			t.Errorf("original mutated to %q", v)
		}
	})
}
