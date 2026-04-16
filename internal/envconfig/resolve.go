package envconfig

// ResolveEnvLayers returns the EnvLayers struct and a resolved attribution map.
//
// The attribution map keys are env var names (from both Vars and SecretRef.EnvKey
// across all levels). The value describes which level the winning definition
// comes from, following last-wins order:
//
//	org → environment → project → app → app-environment
//
// The returned EnvLayers is a verbatim copy of the inputs (no merging of
// values is done here — merging is handled at runtime by Kubernetes envFrom
// ordering). ResolvedEnvVars is used only for the API resolved-view endpoint.
func ResolveEnvLayers(org, env, project, app, appEnv EnvConfig) (EnvLayers, map[string]ResolvedEnvVar) {
	layers := EnvLayers{
		Org:     org,
		Env:     env,
		Project: project,
		App:     app,
		AppEnv:  appEnv,
	}

	resolved := make(map[string]ResolvedEnvVar)

	// Apply levels in order — later levels overwrite earlier ones.
	type levelEntry struct {
		name   string
		config EnvConfig
	}
	ordered := []levelEntry{
		{LevelOrg, org},
		{LevelEnvironment, env},
		{LevelProject, project},
		{LevelApp, app},
		{LevelAppEnv, appEnv},
	}

	for _, le := range ordered {
		for k := range le.config.Vars {
			resolved[k] = ResolvedEnvVar{Source: le.name, IsSecret: false}
		}
		for _, ref := range le.config.SecretRefs {
			resolved[ref.EnvKey] = ResolvedEnvVar{Source: le.name, IsSecret: true}
		}
	}

	return layers, resolved
}
