package gitops

import "github.com/suparcloud/suparship/internal/domain"

func releaseRootDeployEnvs(app *domain.App, envs []AppPublishEnv) ([]AppPublishEnv, error) {
	if app.Spec.CD.ReleaseValidationEnvironment == "" {
		return firstDeployEnvs(envs), nil
	}
	stable := make([]*domain.AppEnvironment, 0, len(envs))
	for _, e := range envs {
		if e.Bound {
			stable = append(stable, &domain.AppEnvironment{EnvName: e.EnvName, EnvType: e.EnvType, Order: e.Order})
		}
	}
	sources, err := domain.ReleasePipeline(app, stable)
	if err != nil {
		return nil, err
	}
	out := []AppPublishEnv{}
	for _, e := range envs {
		source, exists := sources[e.EnvName]
		if e.EnvType == domain.AppEnvPreview || (e.Bound && exists && source == "") {
			out = append(out, e)
		}
	}
	return out, nil
}
