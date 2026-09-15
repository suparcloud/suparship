package domain

import "testing"

func TestMergeComponentEnvVars(t *testing.T) {
	base := []ComponentEnvVar{{Name: "A", Value: "1"}, {Name: "B", Value: "2"}}
	over := []ComponentEnvVar{{Name: "B", Value: "env"}, {Name: "C", FromConfig: "X"}}
	got := MergeComponentEnvVars(base, over)
	want := []ComponentEnvVar{{Name: "A", Value: "1"}, {Name: "B", Value: "env"}, {Name: "C", FromConfig: "X"}}
	if len(got) != len(want) {
		t.Fatalf("merged = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("merged[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	// No override = a copy of base (never the same backing array).
	cp := MergeComponentEnvVars(base, nil)
	cp[0].Value = "mutated"
	if base[0].Value != "1" {
		t.Error("MergeComponentEnvVars(base, nil) must copy")
	}
}

func TestAppForEnvComponentEnvVars(t *testing.T) {
	off := false
	base := func() *App {
		return &App{Name: "a", Spec: AppSpec{Components: []ComponentSpec{
			{Name: "web", EnvVars: []ComponentEnvVar{{Name: "LOG_LEVEL", Value: "info"}}},
			{Name: "worker", InheritAppVars: &off, EnvVars: []ComponentEnvVar{{Name: "DB", FromSecret: "DATABASE_URL"}}},
		}}}
	}

	t.Run("no override returns the app itself", func(t *testing.T) {
		app := base()
		if AppForEnvComponentEnvVars(app, "staging") != app {
			t.Fatal("expected identical pointer")
		}
	})

	t.Run("env override layers on the app-wide list and can flip the posture", func(t *testing.T) {
		app := base()
		app.Spec.EnvironmentDefaults = map[string]EnvironmentOverride{
			"staging": {ComponentEnvVars: map[string]ComponentEnvOverride{
				"web":    {EnvVars: []ComponentEnvVar{{Name: "LOG_LEVEL", Value: "debug"}, {Name: "FEATURE_X", Value: "on"}}},
				"worker": {InheritAppVars: boolPtr(true), EnvVars: []ComponentEnvVar{{Name: "DB", Value: "literal"}}},
			}},
		}
		got := AppForEnvComponentEnvVars(app, "staging")
		if got == app {
			t.Fatal("expected a copy when the env overrides something")
		}
		web := got.Spec.Components[0]
		if len(web.EnvVars) != 2 || web.EnvVars[0].Value != "debug" || web.EnvVars[1].Name != "FEATURE_X" {
			t.Errorf("web effective = %+v", web.EnvVars)
		}
		worker := got.Spec.Components[1]
		if worker.InheritAppVars == nil || !*worker.InheritAppVars {
			t.Errorf("worker posture should be flipped to inherit in staging, got %v", worker.InheritAppVars)
		}
		if len(worker.EnvVars) != 1 || worker.EnvVars[0].Value != "literal" || worker.EnvVars[0].FromSecret != "" {
			t.Errorf("worker effective = %+v, want DB replaced by the literal", worker.EnvVars)
		}
		// Other envs and the stored spec are untouched.
		if AppForEnvComponentEnvVars(app, "prod") != app {
			t.Error("prod must be unaffected")
		}
		if app.Spec.Components[0].EnvVars[0].Value != "info" {
			t.Error("stored spec mutated")
		}
	})
}

func TestCuratesSecrets_EnvOverride(t *testing.T) {
	off := false
	app := &App{Spec: AppSpec{
		Components: []ComponentSpec{{Name: "web"}},
		EnvironmentDefaults: map[string]EnvironmentOverride{
			"prod": {ComponentEnvVars: map[string]ComponentEnvOverride{
				"web": {InheritAppVars: &off, EnvVars: []ComponentEnvVar{{Name: "K", FromSecret: "KEY"}}},
			}},
		},
	}}
	if !app.Spec.CuratesSecrets() {
		t.Error("an env override that curates a secret must count")
	}
	if (AppSpec{Components: []ComponentSpec{{Name: "web"}}}).CuratesSecrets() {
		t.Error("nothing curated")
	}
}

func TestValidateEnvComponentEnvVars(t *testing.T) {
	off := false
	comps := []ComponentSpec{{Name: "web", Type: ComponentWeb}}
	cases := []struct {
		name    string
		ed      map[string]EnvironmentOverride
		wantErr bool
	}{
		{"literal while inheriting", map[string]EnvironmentOverride{"staging": {ComponentEnvVars: map[string]ComponentEnvOverride{
			"web": {EnvVars: []ComponentEnvVar{{Name: "A", Value: "1"}}}}}}, false},
		{"fromSecret while inheriting", map[string]EnvironmentOverride{"staging": {ComponentEnvVars: map[string]ComponentEnvOverride{
			"web": {EnvVars: []ComponentEnvVar{{Name: "A", FromSecret: "S"}}}}}}, true},
		{"fromSecret with env posture curated", map[string]EnvironmentOverride{"staging": {ComponentEnvVars: map[string]ComponentEnvOverride{
			"web": {InheritAppVars: &off, EnvVars: []ComponentEnvVar{{Name: "A", FromSecret: "S"}}}}}}, false},
		{"unknown component", map[string]EnvironmentOverride{"staging": {ComponentEnvVars: map[string]ComponentEnvOverride{
			"nope": {EnvVars: []ComponentEnvVar{{Name: "A", Value: "1"}}}}}}, true},
		{"bad name", map[string]EnvironmentOverride{"staging": {ComponentEnvVars: map[string]ComponentEnvOverride{
			"web": {EnvVars: []ComponentEnvVar{{Name: "1A", Value: "1"}}}}}}, true},
	}
	for _, tc := range cases {
		err := ValidateEnvComponentEnvVars(comps, tc.ed)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", tc.name, err, tc.wantErr)
		}
	}
}

func boolPtr(b bool) *bool { return &b }

func TestAppForPreviewComponentEnvVars(t *testing.T) {
	off := false
	app := &App{Spec: AppSpec{
		Components: []ComponentSpec{{Name: "web", EnvVars: []ComponentEnvVar{{Name: "A", Value: "app"}}}},
		EnvironmentDefaults: map[string]EnvironmentOverride{
			"staging":          {ComponentEnvVars: map[string]ComponentEnvOverride{"web": {EnvVars: []ComponentEnvVar{{Name: "A", Value: "staging"}, {Name: "B", Value: "1"}}}}},
			PreviewOverrideKey: {ComponentEnvVars: map[string]ComponentEnvOverride{"web": {InheritAppVars: &off, EnvVars: []ComponentEnvVar{{Name: "B", Value: "preview"}, {Name: "C", FromSecret: "S"}}}}},
		},
	}}
	got := AppForPreviewComponentEnvVars(app, "staging").Spec.Components[0]
	if got.InheritAppVars == nil || *got.InheritAppVars {
		t.Error("preview band posture must apply")
	}
	want := []ComponentEnvVar{{Name: "A", Value: "staging"}, {Name: "B", Value: "preview"}, {Name: "C", FromSecret: "S"}}
	if len(got.EnvVars) != len(want) {
		t.Fatalf("effective = %+v, want %+v", got.EnvVars, want)
	}
	for i := range want {
		if got.EnvVars[i] != want[i] {
			t.Errorf("effective[%d] = %+v, want %+v", i, got.EnvVars[i], want[i])
		}
	}
	// A preview of prod (no prod override) layers the band on the app-wide list.
	prod := AppForPreviewComponentEnvVars(app, "prod").Spec.Components[0]
	if len(prod.EnvVars) != 3 || prod.EnvVars[0].Value != "app" {
		t.Errorf("prod preview effective = %+v", prod.EnvVars)
	}
	// No band, no base override → the app itself.
	plain := &App{Spec: AppSpec{Components: []ComponentSpec{{Name: "web"}}}}
	if AppForPreviewComponentEnvVars(plain, "staging") != plain {
		t.Error("expected identical pointer without overrides")
	}
	// CuratesSecrets sees the composite: only the band + base env together curate a secret here.
	if !app.Spec.CuratesSecrets() {
		t.Error("preview composite curates a secret; CuratesSecrets must report it")
	}
}
