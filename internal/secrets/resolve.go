package secrets

// Level constants mirror envconfig level names for secret source attribution.
const (
	LevelOrg         = "org"
	LevelEnvironment = "environment"
	LevelProject     = "project"
	LevelApp         = "app"
	LevelAppEnv      = "app-environment"
	LevelCluster     = "cluster"
)

// ResolvedSecret describes a single secret key in the fully merged
// configuration, annotated with the hierarchy level it came from.
// Values are never included — only key names and source attribution.
type ResolvedSecret struct {
	Key    string `json:"key"`
	Source string `json:"source"`
}

// ResolveSecretLayers merges key names from all six hierarchy levels using
// last-wins semantics (org < environment < project < app < app-env < cluster).
//
// Cluster wins last as a platform-engineering escape hatch (incident break-glass,
// per-cluster feature kill-switches, regional tuning) — overriding even the
// app team's most specific app-env layer.
//
// Each input is a slice of key names present at that level. The output maps
// each unique key to its winning source level.
func ResolveSecretLayers(org, env, project, app, appEnv, cluster []string) map[string]ResolvedSecret {
	resolved := make(map[string]ResolvedSecret)

	type levelEntry struct {
		name string
		keys []string
	}
	ordered := []levelEntry{
		{LevelOrg, org},
		{LevelEnvironment, env},
		{LevelProject, project},
		{LevelApp, app},
		{LevelAppEnv, appEnv},
		{LevelCluster, cluster},
	}

	for _, le := range ordered {
		for _, k := range le.keys {
			resolved[k] = ResolvedSecret{Key: k, Source: le.name}
		}
	}

	return resolved
}
