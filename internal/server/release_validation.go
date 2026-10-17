package server

import (
	"context"
	"fmt"
	"github.com/suparcloud/suparship/internal/domain"
)

func (ah *appHandler) validateReleaseValidation(ctx context.Context, app *domain.App, envs []*domain.AppEnvironment) error {
	name := app.Spec.CD.ReleaseValidationEnvironment
	if name == "" {
		return nil
	}
	if !app.Spec.CD.Managed {
		return fmt.Errorf("release validation requires image tags managed by Kargo")
	}
	sources, err := domain.ReleasePipeline(app, envs)
	if err != nil {
		return err
	}
	for env, source := range sources {
		if source == "" && env != name && len(app.Spec.CD.Verification[env]) > 0 && len(app.Spec.CD.Verification[name]) == 0 {
			return fmt.Errorf("configure acceptance checks for %q before replacing the checked release source %q", name, env)
		}
	}
	if len(ah.appDeployClusterRefs(ctx, app.ProjectName, app.Name, name)) == 0 {
		return fmt.Errorf("bind validation environment %q to a cluster in Settings > Environments before enabling release validation", name)
	}
	seen := map[string]string{}
	for env := range sources {
		for _, target := range ah.argoAppNamesForEnv(ctx, app.ProjectName, app.Name, env) {
			if previous, ok := seen[target.name]; ok && previous != env {
				return fmt.Errorf("environments %q and %q resolve to the same Argo CD Application %q; use distinct cluster registrations or an application naming pattern containing {env} before enabling release validation", previous, env, target.name)
			}
			seen[target.name] = env
		}
	}
	return nil
}
