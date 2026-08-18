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
		Vars: map[string]string{"LOG_LEVEL": "error", "PROJ_ONLY": "yes", "PROJ_KEY": "proj"},
	}
	projectEnv := envconfig.EnvConfig{
		Vars: map[string]string{"PROJENV_ONLY": "yes", "PROJ_KEY": "projenv", "STACK_KEY": "projenv"},
	}
	stack := envconfig.EnvConfig{
		Vars: map[string]string{"STACK_ONLY": "yes", "STACK_KEY": "stack", "STACKENV_KEY": "stack"},
	}
	stackEnv := envconfig.EnvConfig{
		Vars: map[string]string{"STACKENV_ONLY": "yes", "STACKENV_KEY": "stackenv"},
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

	_, resolved := envconfig.ResolveEnvLayers(org, env, project, projectEnv, stack, stackEnv, app, appEnv, envconfig.EnvConfig{})

	cases := []struct {
		key      string
		wantSrc  string
		wantSec  bool
	}{
		{"LOG_LEVEL", envconfig.LevelAppEnv, false},    // app-environment wins
		{"ORG_ONLY", envconfig.LevelOrg, false},
		{"ENV_ONLY", envconfig.LevelEnvironment, false},
		{"PROJ_ONLY", envconfig.LevelProject, false},
		{"PROJENV_ONLY", envconfig.LevelProjectEnv, false},
		{"PROJ_KEY", envconfig.LevelProjectEnv, false}, // project-env overrides project
		{"STACK_ONLY", envconfig.LevelStack, false},
		{"STACK_KEY", envconfig.LevelStack, false},      // stack overrides project-env
		{"STACKENV_ONLY", envconfig.LevelStackEnv, false},
		{"STACKENV_KEY", envconfig.LevelStackEnv, false}, // stack-env overrides stack
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
	cluster := envconfig.EnvConfig{Vars: map[string]string{"C": "z"}}

	layers, _ := envconfig.ResolveEnvLayers(
		envconfig.EnvConfig{},
		envconfig.EnvConfig{},
		envconfig.EnvConfig{},
		envconfig.EnvConfig{},
		envconfig.EnvConfig{},
		envconfig.EnvConfig{},
		app,
		appEnv,
		cluster,
	)

	if layers.App.Vars["K"] != "v" {
		t.Error("App layer Vars not preserved")
	}
	if layers.AppEnv.Vars["X"] != "y" {
		t.Error("AppEnv layer Vars not preserved")
	}
	if layers.Cluster.Vars["C"] != "z" {
		t.Error("Cluster layer Vars not preserved")
	}
}

func TestResolveEnvLayers_ClusterWinsLast(t *testing.T) {
	// Same key at every layer; cluster must win as the platform escape hatch.
	cfg := envconfig.EnvConfig{Vars: map[string]string{"FEATURE": "x"}}
	_, resolved := envconfig.ResolveEnvLayers(cfg, cfg, cfg, cfg, cfg, cfg, cfg, cfg, cfg)
	if got := resolved["FEATURE"].Source; got != envconfig.LevelCluster {
		t.Errorf("FEATURE source = %q, want %q", got, envconfig.LevelCluster)
	}
}

func TestResolveEnvLayers_ClusterOverridesAppEnv(t *testing.T) {
	appEnv := envconfig.EnvConfig{Vars: map[string]string{"K": "appenv"}}
	cluster := envconfig.EnvConfig{Vars: map[string]string{"K": "cluster"}}
	_, resolved := envconfig.ResolveEnvLayers(envconfig.EnvConfig{}, envconfig.EnvConfig{}, envconfig.EnvConfig{}, envconfig.EnvConfig{}, envconfig.EnvConfig{}, envconfig.EnvConfig{}, envconfig.EnvConfig{}, appEnv, cluster)
	if got := resolved["K"].Source; got != envconfig.LevelCluster {
		t.Errorf("K source = %q, want %q", got, envconfig.LevelCluster)
	}
}
