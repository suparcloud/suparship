package domain

import "testing"

func TestReleaseValidationComponentSelection(t *testing.T) {
	no, yes := false, true
	for _, c := range []ComponentSpec{{}, {Stateful: true}, {PreviewEnabled: &no}, {ReleaseValidationEnabled: &yes}} {
		if !c.EnabledInReleaseValidation() {
			t.Fatal("components must be included by default independently of preview/type")
		}
	}
	app := &App{Spec: AppSpec{CD: CDConfig{ReleaseValidationEnvironment: "validation"}, Components: []ComponentSpec{{Name: "web"}, {Name: "redis", Stateful: true, ReleaseValidationEnabled: &no}}}}
	got := AppForReleaseValidation(app, "validation")
	if len(got.Spec.Components) != 1 || got.Spec.Components[0].Name != "web" || !got.Spec.IsComposed() {
		t.Fatal("filter lost component or composed rendering mode")
	}
	if len(app.Spec.Components) != 2 || AppForReleaseValidation(app, "staging") != app || AppForReleaseValidation(app, "production") != app {
		t.Fatal("stored or stable configuration changed")
	}
	if len(AppForReleaseValidation(got, "validation").Spec.Components) != 1 {
		t.Fatal("filter not idempotent")
	}
}
