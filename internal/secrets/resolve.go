package secrets

// Source attribution values for a resolved secret key.
const (
	SourceGlobal  = "global"
	SourceEnv     = "env"
	SourceCluster = "cluster"
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

// ResolveScopes merges key names from the three scopes using last-wins
// semantics (global < env < cluster). Within a scope, app overrides shared.
//
// Cluster wins last as a platform-engineering escape hatch (incident
// break-glass, per-cluster kill-switches, regional tuning) — overriding the
// app's env and global values.
//
// The output maps each unique key to its winning source scope and tier.
func ResolveScopes(global, env, cluster ScopeKeys) map[string]ResolvedSecret {
	resolved := make(map[string]ResolvedSecret)

	type layer struct {
		source string
		tier   Tier
		keys   []string
	}
	// Order matters: later layers overwrite earlier ones.
	ordered := []layer{
		{SourceGlobal, TierShared, global.Shared},
		{SourceGlobal, TierApp, global.App},
		{SourceEnv, TierShared, env.Shared},
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
