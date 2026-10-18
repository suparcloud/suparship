package domain

import (
	"fmt"
	"sort"
)

// ReleasePipeline resolves explicit environment-to-source edges. An empty source
// means direct Warehouse delivery. The optional validator replaces the base as
// the first downstream gate; the base and validator remain independent roots.
func ReleasePipeline(app *App, envs []*AppEnvironment) (map[string]string, error) {
	stable := make([]*AppEnvironment, 0, len(envs))
	for _, e := range envs {
		if e.EnvType == AppEnvPreview {
			continue
		}
		if d := app.Spec.EnvironmentDefaults[e.EnvName].Deploy; d != nil && !*d {
			continue
		}
		stable = append(stable, e)
	}
	sort.Slice(stable, func(i, j int) bool {
		if stable[i].Order != stable[j].Order {
			return stable[i].Order < stable[j].Order
		}
		return stable[i].EnvName < stable[j].EnvName
	})
	sources := map[string]string{}
	validation := app.Spec.CD.ReleaseValidationEnvironment
	if validation != "" {
		if app.Spec.IsDirect() {
			return nil, fmt.Errorf("release validation requires pipeline delivery")
		}
		found := false
		for i, e := range stable {
			if e.EnvName == validation {
				if i == 0 || e.EnvType == AppEnvProd {
					return nil, fmt.Errorf("release validation must use a separate non-production environment, not the base environment")
				}
				found = true
			}
		}
		if !found || len(stable) < 3 {
			return nil, fmt.Errorf("release validation requires an enabled validation environment, a base environment, and a downstream release target")
		}
	}
	previous := ""
	for _, e := range stable {
		if e.EnvName == validation {
			sources[e.EnvName] = ""
			continue
		}
		sources[e.EnvName] = previous
		if previous == "" && validation != "" {
			previous = validation
		} else {
			previous = e.EnvName
		}
	}
	return sources, nil
}
