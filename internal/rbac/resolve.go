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
// project. It considers both project-specific bindings and wildcard ("*")
// bindings. Returns ("", false) if the user has no role.
func (o *Org) EffectiveRole(username, project string) (Role, bool) {
	memberOf := make(map[string]bool)
	for _, t := range o.UserTeams(username) {
		memberOf[t] = true
	}
	if len(memberOf) == 0 {
		return "", false
	}

	var highest Role
	for _, rb := range o.RoleBindings {
		if rb.Project != project && rb.Project != "*" {
			continue
		}
		if !memberOf[rb.Team] {
			continue
		}
		if RoleLevel(rb.Role) > RoleLevel(highest) {
			highest = rb.Role
		}
	}

	if highest == "" {
		return "", false
	}
	return highest, true
}

// HasPermission checks whether a user has at least the required role for the
// given project.
func (o *Org) HasPermission(username, project string, required Role) bool {
	effective, ok := o.EffectiveRole(username, project)
	if !ok {
		return false
	}
	return RoleLevel(effective) >= RoleLevel(required)
}
