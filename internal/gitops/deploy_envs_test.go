package gitops_test

import (
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/gitops"
)

func deployEnvNames(envs []gitops.AppPublishEnv) map[string]bool {
	m := make(map[string]bool, len(envs))
	for _, e := range envs {
		m[e.EnvName] = true
	}
	return m
}

func directApp(name string, envDefaults map[string]domain.EnvironmentOverride) *domain.App {
	return &domain.App{
		Name:        name,
		ProjectName: "demo",
		Spec: domain.AppSpec{
			DeliveryMode:        domain.DeliveryDirect,
			EnvironmentDefaults: envDefaults,
		},
	}
}

func boolp(b bool) *bool { return &b }

func directEnvs() []gitops.AppPublishEnv {
	return []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true},
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: true},
		{EnvName: "qa", EnvType: domain.AppEnvStaging, Order: 3, Bound: false}, // unbound
		{EnvName: "pr-7", EnvType: domain.AppEnvPreview, Order: 0, Bound: true},
	}
}

// By default a direct app deploys only the base (lowest-Order) stable env; higher
// envs are opt-in. Previews always; unbound envs never.
func TestEnabledDeployEnvs_BaseOnlyByDefault(t *testing.T) {
	got := deployEnvNames(gitops.EnabledDeployEnvsForTest(directApp("a", nil), directEnvs()))
	if !got["staging"] {
		t.Errorf("base env (staging) must deploy by default: %v", got)
	}
	if got["prod"] {
		t.Errorf("higher env (prod) must be opt-in, not deployed by default: %v", got)
	}
	if !got["pr-7"] {
		t.Errorf("previews always deploy: %v", got)
	}
	if got["qa"] {
		t.Errorf("unbound env must be excluded: %v", got)
	}
}

// Opting a higher env in (Deploy=true) deploys it.
func TestEnabledDeployEnvs_HigherOptIn(t *testing.T) {
	app := directApp("a", map[string]domain.EnvironmentOverride{
		"prod": {Deploy: boolp(true)},
	})
	got := deployEnvNames(gitops.EnabledDeployEnvsForTest(app, directEnvs()))
	if !got["prod"] {
		t.Errorf("prod opted in must deploy: %v", got)
	}
}

// Opting the base env out (Deploy=false) excludes it from the publish set.
func TestEnabledDeployEnvs_BaseOptOut(t *testing.T) {
	app := directApp("a", map[string]domain.EnvironmentOverride{
		"staging": {Deploy: boolp(false)},
	})
	got := deployEnvNames(gitops.EnabledDeployEnvsForTest(app, directEnvs()))
	if got["staging"] {
		t.Errorf("base env opted out must not be in the publish set: %v", got)
	}
}
