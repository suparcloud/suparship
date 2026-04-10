package rbac

import "context"

// OrgProvider loads the current org configuration.
type OrgProvider interface {
	GetOrg(ctx context.Context) (*Org, error)
}

// OrgStore extends OrgProvider with a write operation for updating the org
// configuration (e.g. managing org-level environments, teams, role bindings).
// All write endpoints require OrgStore; read-only middleware accepts OrgProvider.
type OrgStore interface {
	OrgProvider
	SaveOrg(ctx context.Context, org *Org) error
}

// Permission check functions map business actions to minimum required roles.
// Each function is intentionally explicit so the authorization policy is
// auditable in one place.

// CanViewProject reports whether the user can read project resources.
// Minimum role: viewer.
func CanViewProject(org *Org, username, project string) bool {
	return org.HasPermission(username, project, RoleViewer)
}

// CanManageProject reports whether the user can modify project settings.
// Minimum role: project_admin.
func CanManageProject(org *Org, username, project string) bool {
	return org.HasPermission(username, project, RoleProjectAdmin)
}

// CanCreatePreview reports whether the user can create preview environments.
// Minimum role: developer.
func CanCreatePreview(org *Org, username, project string) bool {
	return org.HasPermission(username, project, RoleDeveloper)
}

// CanPromoteService reports whether the user can promote a service to a
// higher environment.
// Minimum role: project_admin.
func CanPromoteService(org *Org, username, project string) bool {
	return org.HasPermission(username, project, RoleProjectAdmin)
}
