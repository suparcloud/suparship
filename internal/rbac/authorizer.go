package rbac

import "context"

// Identity is the authenticated subject of an authorization decision: a
// username plus any IdP groups carried from SSO.
type Identity struct {
	Username string
	Groups   []string
}

// Authorizer decides whether an identity may act with at least the given role
// on a project ("*" denotes org-wide).
//
// The open-source core ships OrgAuthorizer, which applies the built-in role
// hierarchy over the org's teams and role bindings. Enterprise builds can
// supply an implementation backed by custom roles or an external policy engine
// (ABAC / OPA), wired via server.Config.Authorizer — without modifying the
// core enforcement middleware.
type Authorizer interface {
	// Authorize reports whether id holds at least role on project.
	//
	// A non-nil error means the decision could not be made (e.g. the org
	// config could not be loaded) and must be surfaced as a server error,
	// distinct from a definitive deny (allowed == false, err == nil).
	Authorize(ctx context.Context, id Identity, project string, role Role) (allowed bool, err error)
}

// OrgAuthorizer is the core's default Authorizer. It loads the org via the
// provider and applies the built-in role hierarchy (RoleLevel ordering over
// the identity's effective role on the project).
type OrgAuthorizer struct {
	provider OrgProvider
}

// NewOrgAuthorizer returns an OrgAuthorizer backed by the given provider.
func NewOrgAuthorizer(provider OrgProvider) *OrgAuthorizer {
	return &OrgAuthorizer{provider: provider}
}

// Authorize implements Authorizer using the org role hierarchy. It mirrors the
// previous inline check (Org.HasPermissionForIdentity), returning a server
// error when the org cannot be loaded.
func (a *OrgAuthorizer) Authorize(ctx context.Context, id Identity, project string, role Role) (bool, error) {
	org, err := a.provider.GetOrg(ctx)
	if err != nil {
		return false, err
	}
	return org.HasPermissionForIdentity(id.Username, id.Groups, project, role), nil
}
