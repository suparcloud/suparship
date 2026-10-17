package domain

import (
	"reflect"
	"testing"
)

func TestReleasePipeline(t *testing.T) {
	envs := []*AppEnvironment{{EnvName: "prod", EnvType: AppEnvProd, Order: 2}, {EnvName: "validation", EnvType: AppEnvStaging, Order: 3}, {EnvName: "staging", EnvType: AppEnvStaging, Order: 1}, {EnvName: "pr-1", EnvType: AppEnvPreview}}
	app := &App{}
	got, err := ReleasePipeline(app, envs)
	if err != nil || !reflect.DeepEqual(got, map[string]string{"staging": "", "prod": "staging", "validation": "prod"}) {
		t.Fatalf("default changed: %v %v", got, err)
	}
	app.Spec.CD.ReleaseValidationEnvironment = "validation"
	app.Spec.EnvironmentDefaults = map[string]EnvironmentOverride{"staging": {PinnedFrom: "pr-1"}}
	got, err = ReleasePipeline(app, envs)
	if err != nil || !reflect.DeepEqual(got, map[string]string{"staging": "", "validation": "", "prod": "validation"}) {
		t.Fatalf("independent validation: %v %v", got, err)
	}
	envs = append(envs, &AppEnvironment{EnvName: "region-two", Order: 4})
	got, err = ReleasePipeline(app, envs)
	if err != nil || got["region-two"] != "prod" {
		t.Fatalf("downstream chain changed: %v %v", got, err)
	}
	for _, name := range []string{"staging", "prod", "missing", "pr-1"} {
		app.Spec.CD.ReleaseValidationEnvironment = name
		if _, err := ReleasePipeline(app, envs); err == nil {
			t.Errorf("accepted %s", name)
		}
	}
	app.Spec.CD.ReleaseValidationEnvironment = "validation"
	disabled := false
	app.Spec.EnvironmentDefaults["validation"] = EnvironmentOverride{Deploy: &disabled}
	if _, err := ReleasePipeline(app, envs); err == nil {
		t.Fatal("disabled validator accepted")
	}
}
