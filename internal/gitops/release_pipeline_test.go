package gitops

import (
	"github.com/suparcloud/suparship/internal/domain"
	"testing"
)

func TestReleaseValidationRootsAreAlwaysProvisioned(t *testing.T) {
	app := &domain.App{}
	app.Spec.CD.ReleaseValidationEnvironment = "validation"
	envs := []AppPublishEnv{{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true}, {EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: true}, {EnvName: "validation", EnvType: domain.AppEnvStaging, Order: 3, Bound: true}}
	roots, err := releaseRootDeployEnvs(app, envs)
	if err != nil || len(roots) != 2 || roots[0].EnvName != "staging" || roots[1].EnvName != "validation" {
		t.Fatalf("unsafe deployment roots: %v %v", roots, err)
	}
	envs[2].Bound = false
	if _, err := releaseRootDeployEnvs(app, envs); err == nil {
		t.Fatal("unbound validator silently fell back to staging")
	}
}
