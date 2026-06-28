package envconfig

// ResolveEnvLayers returns the EnvLayers struct and a resolved attribution map.
//
// The attribution map keys are env var names (from both Vars and SecretRef.EnvKey
// across all levels). The value describes which level the winning definition
// comes from, following last-wins order:
//
//	org → environment → project → project-environment → stack → stack-environment → app → app-environment → cluster
//
// Cluster wins last as the platform-engineering escape hatch — incident
// break-glass, regional tuning, per-cluster feature kill-switches — overriding
// even the app team's most specific app-env layer.
//
// The returned EnvLayers is a verbatim copy of the inputs (no merging of
// values is done here — merging is handled at runtime by Kubernetes envFrom
// ordering). ResolvedEnvVars is used only for the API resolved-view endpoint.
func ResolveEnvLayers(org, env, project, projectEnv, stack, stackEnv, app, appEnv, cluster EnvConfig) (EnvLayers, map[string]ResolvedEnvVar) {
	layers := EnvLayers{
		Org:        org,
		Env:        env,
		Project:    project,
		ProjectEnv: projectEnv,
		Stack:      stack,
		StackEnv:   stackEnv,
		App:        app,
		AppEnv:     appEnv,
		Cluster:    cluster,
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
		{LevelProjectEnv, projectEnv},
		{LevelStack, stack},
		{LevelStackEnv, stackEnv},
		{LevelApp, app},
		{LevelAppEnv, appEnv},
		{LevelCluster, cluster},
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
