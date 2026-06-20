package secrets

// Source attribution values for a resolved secret key.
const (
	SourceGlobal  = "global"
	SourceEnv     = "env"
	SourceCluster = "cluster"
	SourceProject = "project"
	SourceStack   = "stack"
)

// ResolvedSecret describes a single secret key in the fully merged
// configuration a workload sees, annotated with the scope it came from and
// whether it came from the shared or app tier. Values are never included —
// only key names and attribution.
type ResolvedSecret struct {
	Key    string `json:"key"`
	Source string `json:"source"`
	Tier   string `json:"tier,omitempty"`
}

// ScopeKeys holds the key names present at one scope, split by tier.
type ScopeKeys struct {
	Shared []string
	App    []string
}

// ResolveScopes merges key names using last-wins semantics. Env specificity is
// primary (global band < env band < cluster band); within each band ownership
// runs org-shared < project-shared < app. Project layers are shared-tier only
// (a project secret applies to every app in the project); their App slices are
// ignored.
//
// Cluster wins last as a platform-engineering escape hatch (incident
// break-glass, per-cluster kill-switches, regional tuning) — overriding the
// app's env, project, and global values.
//
// The output maps each unique key to its winning source scope and tier.
func ResolveScopes(global, projectGlobal, stackGlobal, env, projectEnv, stackEnv, cluster ScopeKeys) map[string]ResolvedSecret {
	resolved := make(map[string]ResolvedSecret)

	type layer struct {
		source string
		tier   Tier
		keys   []string
	}
	// Order matters: later layers overwrite earlier ones. Within each band
	// (global → env → cluster) ownership runs org-shared → project-shared →
	// stack-shared → app.
	ordered := []layer{
		{SourceGlobal, TierShared, global.Shared},
		{SourceProject, TierShared, projectGlobal.Shared},
		{SourceStack, TierShared, stackGlobal.Shared},
		{SourceGlobal, TierApp, global.App},
		{SourceEnv, TierShared, env.Shared},
		{SourceProject, TierShared, projectEnv.Shared},
		{SourceStack, TierShared, stackEnv.Shared},
		{SourceEnv, TierApp, env.App},
		{SourceCluster, TierShared, cluster.Shared},
		{SourceCluster, TierApp, cluster.App},
	}

	for _, l := range ordered {
		for _, k := range l.keys {
			resolved[k] = ResolvedSecret{Key: k, Source: l.source, Tier: string(l.tier)}
		}
	}

	return resolved
}
