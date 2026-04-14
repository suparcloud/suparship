package envconfig_test

import (
	"testing"

	"github.com/suparcloud/suparship/internal/envconfig"
)

func TestResolveEnvLayers_Attribution(t *testing.T) {
	org := envconfig.EnvConfig{
		Vars: map[string]string{"LOG_LEVEL": "info", "ORG_ONLY": "yes"},
	}
	env := envconfig.EnvConfig{
		Vars: map[string]string{"LOG_LEVEL": "warn", "ENV_ONLY": "yes"},
	}
	project := envconfig.EnvConfig{
		Vars: map[string]string{"LOG_LEVEL": "error", "PROJ_ONLY": "yes"},
	}
	app := envconfig.EnvConfig{
		Vars: map[string]string{"APP_ONLY": "yes"},
		SecretRefs: []envconfig.SecretRef{
			{Provider: "k8s", Name: "app-secret", Key: "token", EnvKey: "API_TOKEN"},
		},
	}
	appEnv := envconfig.EnvConfig{
		Vars: map[string]string{"LOG_LEVEL": "debug", "APPENV_ONLY": "yes"},
	}

	_, resolved := envconfig.ResolveEnvLayers(org, env, project, app, appEnv)

	cases := []struct {
		key      string
		wantSrc  string
		wantSec  bool
	}{
		{"LOG_LEVEL", envconfig.LevelAppEnv, false},    // app-environment wins
		{"ORG_ONLY", envconfig.LevelOrg, false},
		{"ENV_ONLY", envconfig.LevelEnvironment, false},
		{"PROJ_ONLY", envconfig.LevelProject, false},
		{"APP_ONLY", envconfig.LevelApp, false},
		{"API_TOKEN", envconfig.LevelApp, true},         // SecretRef attribution
		{"APPENV_ONLY", envconfig.LevelAppEnv, false},
	}

	for _, tc := range cases {
		rv, ok := resolved[tc.key]
		if !ok {
			t.Errorf("key %q not found in resolved map", tc.key)
			continue
		}
		if rv.Source != tc.wantSrc {
			t.Errorf("key %q: got source %q, want %q", tc.key, rv.Source, tc.wantSrc)
		}
		if rv.IsSecret != tc.wantSec {
			t.Errorf("key %q: got isSecret=%v, want %v", tc.key, rv.IsSecret, tc.wantSec)
		}
	}
}

func TestResolveEnvLayers_EmptyLayers(t *testing.T) {
	_, resolved := envconfig.ResolveEnvLayers(
		envconfig.EnvConfig{},
		envconfig.EnvConfig{},
		envconfig.EnvConfig{},
		envconfig.EnvConfig{},
		envconfig.EnvConfig{},
	)
	if len(resolved) != 0 {
		t.Errorf("expected empty resolved map, got %d entries", len(resolved))
	}
}

func TestResolveEnvLayers_LayersPreserved(t *testing.T) {
	app := envconfig.EnvConfig{Vars: map[string]string{"K": "v"}}
	appEnv := envconfig.EnvConfig{Vars: map[string]string{"X": "y"}}

	layers, _ := envconfig.ResolveEnvLayers(
		envconfig.EnvConfig{},
		envconfig.EnvConfig{},
		envconfig.EnvConfig{},
		app,
		appEnv,
	)

	if layers.App.Vars["K"] != "v" {
		t.Error("App layer Vars not preserved")
	}
	if layers.AppEnv.Vars["X"] != "y" {
		t.Error("AppEnv layer Vars not preserved")
	}
}

func TestToHelmEnvLayers_OnlyAppAndAppEnv(t *testing.T) {
	layers := envconfig.EnvLayers{
		Org:     envconfig.EnvConfig{Vars: map[string]string{"ORG": "x"}},
		Env:     envconfig.EnvConfig{Vars: map[string]string{"ENV": "x"}},
		Project: envconfig.EnvConfig{Vars: map[string]string{"PROJ": "x"}},
		App:     envconfig.EnvConfig{Vars: map[string]string{"APP": "x"}},
		AppEnv:  envconfig.EnvConfig{Vars: map[string]string{"APPENV": "x"}},
	}

	h := envconfig.ToHelmEnvLayers(layers)

	if h.App == nil || h.App.Vars["APP"] != "x" {
		t.Error("App layer not correctly converted")
	}
	if h.AppEnv == nil || h.AppEnv.Vars["APPENV"] != "x" {
		t.Error("AppEnv layer not correctly converted")
	}
	// Upper levels must NOT appear in HelmEnvLayers
	// (no Org/Env/Project fields on HelmEnvLayers struct — compile-time guarantee)
}

func TestToHelmEnvLayers_EmptyLayersAreNil(t *testing.T) {
	layers := envconfig.EnvLayers{}
	h := envconfig.ToHelmEnvLayers(layers)
	if h.App != nil {
		t.Error("expected nil App layer for empty config")
	}
	if h.AppEnv != nil {
		t.Error("expected nil AppEnv layer for empty config")
	}
}

func TestSecretRefsByProvider(t *testing.T) {
	refs := []envconfig.HelmSecretRef{
		{Provider: "k8s", Name: "s1", Key: "k1", EnvKey: "A"},
		{Provider: "vault", Name: "s2", Key: "k2", EnvKey: "B"},
		{Provider: "k8s", Name: "s3", Key: "k3", EnvKey: "C"},
	}
	groups := envconfig.SecretRefsByProvider(refs)
	if len(groups["k8s"]) != 2 {
		t.Errorf("expected 2 k8s refs, got %d", len(groups["k8s"]))
	}
	if len(groups["vault"]) != 1 {
		t.Errorf("expected 1 vault ref, got %d", len(groups["vault"]))
	}
}
