package rbac

import (
	"sort"

	"github.com/suparcloud/suparship/internal/project"
)

// OriginOrg means the environment is defined at org level and the project has
// no override — values come entirely from the org defaults.
const OriginOrg = "org"

// OriginOverride means the environment is defined at org level but the project
// has overridden one or more fields.
const OriginOverride = "override"

// OriginProject means the environment is defined only at the project level
// (not present in the org defaults). It is fully project-owned.
const OriginProject = "project"

// MergedEnvironment is an environment resolved through the merge of org
// defaults and project overrides.
type MergedEnvironment struct {
	project.Environment

	// Origin describes how this environment was produced:
	//   "org"      — inherited from org with no project override
	//   "override" — org environment with project-level field overrides
	//   "project"  — project-specific environment not present in org defaults
	Origin string
}

// MergeEnvironments resolves the effective environment list for a project.
//
// Algorithm:
//  1. Start with the org canonical environments in order.
//  2. For each org environment, apply non-zero project override fields if a
//     project entry with the same name exists.
//  3. Append project-specific environments (names not present in org) sorted
//     by their order, then name.
func MergeEnvironments(orgEnvs []OrgEnvironment, projectOverrides []project.Environment) []MergedEnvironment {
	projectMap := make(map[string]project.Environment, len(projectOverrides))
	for _, e := range projectOverrides {
		projectMap[e.Name] = e
	}

	result := make([]MergedEnvironment, 0, len(orgEnvs)+len(projectOverrides))

	orgNames := make(map[string]bool, len(orgEnvs))
	for _, org := range orgEnvs {
		orgNames[org.Name] = true

		// Start from org defaults. ClusterRefs is copied so a project
		// override that mutates the slice doesn't poison the org config.
		merged := project.Environment{
			Name:             org.Name,
			DisplayName:      org.DisplayName,
			Order:            org.Order,
			ClusterRefs:      append([]string(nil), org.ClusterRefs...),
			ActiveClusterRef: org.ActiveClusterRef,
			BaseDomain:       org.BaseDomain,
			NamespacePattern: org.NamespacePattern,
		}

		origin := OriginOrg
		if override, ok := projectMap[org.Name]; ok {
			origin = OriginOverride
			// Only apply non-zero override fields so the project entry can be
			// a sparse patch (e.g. only overriding ActiveClusterRef without
			// changing the registered set).
			if override.DisplayName != "" {
				merged.DisplayName = override.DisplayName
			}
			if len(override.ClusterRefs) > 0 {
				// Wholesale replace — operator who wants to scope down or
				// add a cluster ships the full list, not a sparse merge.
				merged.ClusterRefs = append([]string(nil), override.ClusterRefs...)
			}
			if override.ActiveClusterRef != "" {
				merged.ActiveClusterRef = override.ActiveClusterRef
			}
			if override.BaseDomain != "" {
				merged.BaseDomain = override.BaseDomain
			}
			if override.NamespacePattern != "" {
				merged.NamespacePattern = override.NamespacePattern
			}
			if override.Order > 0 {
				merged.Order = override.Order
			}
		}

		result = append(result, MergedEnvironment{Environment: merged, Origin: origin})
	}

	// Append project-only environments (not defined at org level).
	projectOnly := make([]MergedEnvironment, 0)
	for _, e := range projectOverrides {
		if orgNames[e.Name] {
			continue
		}
		projectOnly = append(projectOnly, MergedEnvironment{Environment: e, Origin: OriginProject})
	}
	sort.Slice(projectOnly, func(i, j int) bool {
		if projectOnly[i].Order != projectOnly[j].Order {
			return projectOnly[i].Order < projectOnly[j].Order
		}
		return projectOnly[i].Name < projectOnly[j].Name
	})
	result = append(result, projectOnly...)

	return result
}
