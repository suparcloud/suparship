package rbac

// roleLevels maps each role to a numeric privilege level.
// Higher values indicate more privilege.
var roleLevels = map[Role]int{
	RoleViewer:       10,
	RoleDeveloper:    20,
	RoleProjectAdmin: 30,
	RoleOrgAdmin:     40,
}

// RoleLevel returns the numeric privilege level of a role.
// Unknown roles return 0.
func RoleLevel(r Role) int {
	return roleLevels[r]
}

// UserTeams returns the names of all teams the user belongs to.
func (o *Org) UserTeams(username string) []string {
	var teams []string
	for _, t := range o.Teams {
		for _, m := range t.Members {
			if m == username {
				teams = append(teams, t.Name)
				break
			}
		}
	}
	return teams
}

// EffectiveRole returns the highest-privilege role a user has for the given
// project, considering only local team membership (username-based). It is the
// identity-without-SSO-groups case; SSO logins should use
// EffectiveRoleForIdentity with their group claims. Returns ("", false) if the
// user has no role.
func (o *Org) EffectiveRole(username, project string) (Role, bool) {
	return o.EffectiveRoleForIdentity(username, nil, project)
}

// EffectiveRoleForIdentity returns the highest-privilege role for an identity
// described by its username (local team membership) AND its IdP group claims
// (group bindings). A binding matches when its Team is one the user belongs to,
// OR its Group is one of the supplied groups. Considers project-specific and
// wildcard ("*") bindings. Returns ("", false) if nothing matches.
func (o *Org) EffectiveRoleForIdentity(username string, groups []string, project string) (Role, bool) {
	memberOf := make(map[string]bool)
	for _, t := range o.UserTeams(username) {
		memberOf[t] = true
	}
	groupSet := make(map[string]bool, len(groups))
	for _, g := range groups {
		groupSet[g] = true
	}

	var highest Role
	found := false
	for _, rb := range o.RoleBindings {
		if rb.Project != project && rb.Project != "*" {
			continue
		}
		matched := (rb.Team != "" && memberOf[rb.Team]) || (rb.Group != "" && groupSet[rb.Group])
		if !matched {
			continue
		}
		found = true
		if RoleLevel(rb.Role) > RoleLevel(highest) {
			highest = rb.Role
		}
	}
	if !found {
		return "", false
	}
	return highest, true
}

// HasPermission checks whether a user has at least the required role for the
// given project, by local team membership only.
func (o *Org) HasPermission(username, project string, required Role) bool {
	return o.HasPermissionForIdentity(username, nil, project, required)
}

// HasPermissionForIdentity checks whether an identity (username + IdP groups)
// has at least the required role for the given project.
func (o *Org) HasPermissionForIdentity(username string, groups []string, project string, required Role) bool {
	effective, ok := o.EffectiveRoleForIdentity(username, groups, project)
	if !ok {
		return false
	}
	return RoleLevel(effective) >= RoleLevel(required)
}
