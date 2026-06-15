package gitops

import (
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/helmvalues"
	"gopkg.in/yaml.v3"
)

func TestDeepMerge_NestedMergeAndReplace(t *testing.T) {
	base := map[string]any{
		"a":   map[string]any{"x": 1, "y": 2},
		"b":   "keep",
		"arr": []any{1, 2},
	}
	overlay := map[string]any{
		"a":   map[string]any{"y": 20, "z": 3}, // merge: x kept, y replaced, z added
		"arr": []any{9},                        // slice replaced wholesale
		"c":   "new",
	}
	got := deepMerge(base, overlay)
	a := got["a"].(map[string]any)
	if a["x"] != 1 || a["y"] != 20 || a["z"] != 3 {
		t.Errorf("nested merge wrong: %v", a)
	}
	if got["b"] != "keep" || got["c"] != "new" {
		t.Errorf("top-level merge wrong: %v", got)
	}
	if arr := got["arr"].([]any); len(arr) != 1 || arr[0] != 9 {
		t.Errorf("slice should be replaced, got %v", arr)
	}
}

func TestEnvOverlay_LayersPlatformThenEnvThenRaw(t *testing.T) {
	app := &domain.App{
		Name: "hello", ProjectName: "demo",
		Spec: domain.AppSpec{
			RawValues: map[string]any{"replicas": 5, "image": map[string]any{"tag": "dev"}},
		},
	}
	env := AppPublishEnv{
		EnvName: "prod",
		// PE platform defaults (all envs).
		PlatformDefaultValues: map[string]any{
			"replicas":  1,
			"image":     map[string]any{"repository": "ghcr.io/org/app", "tag": "base"},
			"resources": map[string]any{"requests": map[string]any{"cpu": "100m"}},
		},
		// PE per-env (prod) baseline.
		PlatformEnvValues: map[string]any{
			"replicas":  3,
			"resources": map[string]any{"requests": map[string]any{"cpu": "500m"}},
		},
	}
	got := envOverlay(app, env, "")

	// rawValues (dev) wins over env over default.
	if got["replicas"] != 5 {
		t.Errorf("replicas = %v, want 5 (dev rawValues wins)", got["replicas"])
	}
	// env baseline (cpu 500m) wins over platform default (100m); no rawValues for it.
	cpu := got["resources"].(map[string]any)["requests"].(map[string]any)["cpu"]
	if cpu != "500m" {
		t.Errorf("cpu = %v, want 500m (env over default)", cpu)
	}
	// nested merge: repository from default survives, tag overridden by rawValues.
	img := got["image"].(map[string]any)
	if img["repository"] != "ghcr.io/org/app" || img["tag"] != "dev" {
		t.Errorf("image merge wrong: %v", img)
	}
	// app spec not mutated.
	if app.Spec.RawValues["replicas"] != 5 {
		t.Error("envOverlay mutated the app spec")
	}
}

func TestEnvOverlay_ClusterLayerForTargetClusterOnly(t *testing.T) {
	app := &domain.App{Name: "hello", ProjectName: "demo"}
	env := AppPublishEnv{
		EnvName:           "prod",
		PlatformEnvValues: map[string]any{"replicas": 3},
		PlatformClusterValues: map[string]map[string]any{
			"aks-eastus": {"ingress": map[string]any{"annotations": map[string]any{"azure": "internal"}}},
			"eks-uswest": {"ingress": map[string]any{"annotations": map[string]any{"aws": "nlb"}}},
		},
	}

	// Target cluster's block applies; the other cluster's does not.
	got := envOverlay(app, env, "aks-eastus")
	ann := got["ingress"].(map[string]any)["annotations"].(map[string]any)
	if ann["azure"] != "internal" {
		t.Errorf("azure cluster block missing: %v", ann)
	}
	if _, leaked := ann["aws"]; leaked {
		t.Errorf("other cluster's block leaked: %v", ann)
	}
	if got["replicas"] != 3 {
		t.Errorf("env layer lost: %v", got["replicas"])
	}

	// No cluster → no cluster block.
	if none := envOverlay(app, env, ""); none["ingress"] != nil {
		t.Errorf("cluster block applied without a target cluster: %v", none["ingress"])
	}
}

func TestRawValuesOverlay_EnvWinsOverApp(t *testing.T) {
	app := &domain.App{
		Name: "hello", ProjectName: "demo",
		Spec: domain.AppSpec{
			RawValues: map[string]any{
				"podAnnotations": map[string]any{"a": "app", "shared": "app"},
			},
			EnvironmentDefaults: map[string]domain.EnvironmentOverride{
				"prod": {RawValues: map[string]any{
					"podAnnotations": map[string]any{"shared": "env", "e": "env"},
				}},
			},
		},
	}
	got := rawValuesOverlay(app, "prod")
	ann := got["podAnnotations"].(map[string]any)
	if ann["a"] != "app" || ann["e"] != "env" || ann["shared"] != "env" {
		t.Errorf("overlay = %v, want a=app e=env shared=env", ann)
	}
	// The stored app spec must be untouched (deep copy).
	if app.Spec.RawValues["podAnnotations"].(map[string]any)["shared"] != "app" {
		t.Error("rawValuesOverlay mutated the stored app spec")
	}
}

func TestMarshalValuesWithOverlay_NoOverlayNoToken_Unchanged(t *testing.T) {
	hv := helmvalues.HelmValues{App: helmvalues.AppContext{Name: "hello", Env: "prod"}}
	got, err := marshalValuesWithOverlay(hv, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := yaml.Marshal(hv)
	if string(got) != string(want) {
		t.Errorf("expected struct-marshalled bytes unchanged when no overlay/token")
	}
}

func TestMarshalValuesWithOverlay_MergesAndInterpolates(t *testing.T) {
	hv := helmvalues.HelmValues{
		App:      helmvalues.AppContext{Name: "hello", Env: "prod"},
		Platform: helmvalues.PlatformValues{Env: "prod", RoutingHost: "hello.prod.acme.com"},
	}
	overlay := map[string]any{
		"podAnnotations": map[string]any{"host": "{platform.routingHost}", "region": "{vars.REGION}"},
	}
	got, err := marshalValuesWithOverlay(hv, overlay, map[string]string{"REGION": "us-east"})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := yaml.Unmarshal(got, &out); err != nil {
		t.Fatal(err)
	}
	ann := out["podAnnotations"].(map[string]any)
	if ann["host"] != "hello.prod.acme.com" {
		t.Errorf("overlay host = %v, want interpolated routingHost", ann["host"])
	}
	if ann["region"] != "us-east" {
		t.Errorf("overlay region = %v, want us-east", ann["region"])
	}
	// Base platform block survives the merge.
	plat := out["platform"].(map[string]any)
	if plat["routingHost"] != "hello.prod.acme.com" {
		t.Errorf("platform block lost in merge: %v", plat)
	}
}

func TestMarshalValuesWithOverlay_InterpolatesBaseTokens(t *testing.T) {
	// A curated input value that reached hv (image tag) carrying a token.
	hv := helmvalues.HelmValues{
		Platform: helmvalues.PlatformValues{Env: "staging"},
		Components: map[string]*helmvalues.ComponentValues{
			"web": {Image: helmvalues.ImageValues{Repository: "ghcr.io/org/hello", Tag: "{platform.env}-latest"}},
		},
	}
	got, err := marshalValuesWithOverlay(hv, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = yaml.Unmarshal(got, &out)
	tag := out["components"].(map[string]any)["web"].(map[string]any)["image"].(map[string]any)["tag"]
	if tag != "staging-latest" {
		t.Errorf("base image tag = %v, want staging-latest", tag)
	}
}
