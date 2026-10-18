package server

import (
	"context"
	"fmt"
	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/gitops"
)

func (ah *appHandler) validateReleaseValidation(ctx context.Context, app *domain.App, envs []*domain.AppEnvironment) error {
	mode := app.Spec.CD.ReleaseValidationMode
	if mode != "" && mode != "persistent" && mode != "ephemeral" {
		return fmt.Errorf("release validation mode must be persistent or ephemeral")
	}
	name := app.Spec.CD.ReleaseValidationEnvironment
	if mode == "ephemeral" && name == "" {
		return fmt.Errorf("select a release validation environment before enabling ephemeral mode")
	}
	if app.Spec.CD.EphemeralValidation() && len(app.Spec.StatefulComponents()) > 0 {
		return fmt.Errorf("ephemeral validation requires external persistent data; remove stateful components or use persistent validation")
	}
	if app.Spec.CD.EphemeralValidation() && app.Spec.EnvironmentDefaults[name].PinnedFrom != "" {
		return fmt.Errorf("unpin the validation environment before enabling ephemeral mode")
	}
	if name == "" {
		return nil
	}
	if !app.Spec.CD.Managed {
		return fmt.Errorf("release validation requires image tags managed by Kargo")
	}
	if app.Spec.CD.EphemeralValidation() {
		var namespace string
		for _, env := range envs {
			if env.EnvName == name {
				namespace = env.Namespace
			}
		}
		if namespace == "" {
			return fmt.Errorf("ephemeral validation requires a dedicated namespace")
		}
		for _, env := range envs {
			if env.EnvName != name && env.Namespace == namespace {
				return fmt.Errorf("ephemeral validation cannot share namespace %q with %q", namespace, env.EnvName)
			}
		}
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
	boundEnv := name
	if app.Spec.CD.ManagedValidation() {
		if base := domain.BaseEnvironment(envs, name); base != nil {
			boundEnv = base.EnvName
		}
	}
	if len(ah.appDeployClusterRefs(ctx, app.ProjectName, app.Name, boundEnv)) == 0 {
		return fmt.Errorf("bind validation environment %q to a cluster in Settings > Environments before enabling release validation", name)
	}
	seen := map[string]string{}
	for env := range sources {
		targets := ah.argoAppNamesForEnv(ctx, app.ProjectName, app.Name, env)
		if app.Spec.CD.ManagedValidation() && env == name {
			targets = nil
			for _, ref := range ah.appDeployClusterRefs(ctx, app.ProjectName, app.Name, boundEnv) {
				targets = append(targets, argoClusterApp{cluster: ref, name: gitops.RenderArgoAppName(gitops.ValidationNamePattern, app.ProjectName, app.Name, env, ref)})
			}
		}
		for _, target := range targets {
			if previous, ok := seen[target.name]; ok && previous != env {
				return fmt.Errorf("environments %q and %q resolve to the same Argo CD Application %q; use distinct cluster registrations or an application naming pattern containing {env} before enabling release validation", previous, env, target.name)
			}
			seen[target.name] = env
		}
	}
	return nil
}

func (ah *appHandler) validationNamePattern(ctx context.Context, project, app, env, pattern string) string {
	if env == domain.ManagedValidationEnv && ah.appStore != nil {
		if a, err := ah.appOnce(ctx, project, app); err == nil && a.Spec.CD.ManagedValidation() {
			return "{projectApp}-{env}-{cluster}"
		}
	}
	return pattern
}
