package rbac

import "testing"

func groupOrg() *Org {
	return &Org{
		Name: "acme",
		Teams: []Team{
			{Name: "admins", Members: []string{"alice"}},
		},
		RoleBindings: []RoleBinding{
			{Project: "*", Team: "admins", Role: RoleOrgAdmin},
			{Project: "*", Group: "platform-admins", Role: RoleOrgAdmin},
			{Project: "api", Group: "api-devs", Role: RoleDeveloper},
			{Project: "*", Group: "everyone", Role: RoleViewer},
		},
	}
}

func TestEffectiveRoleForIdentity_GroupBinding(t *testing.T) {
	org := groupOrg()

	// SSO user in no local team, but in the api-devs group → developer on api.
	if role, ok := org.EffectiveRoleForIdentity("sso@acme.com", []string{"api-devs"}, "api"); !ok || role != RoleDeveloper {
		t.Errorf("api-devs on api = (%q,%v), want (developer,true)", role, ok)
	}
	// Same user has no binding on web.
	if _, ok := org.EffectiveRoleForIdentity("sso@acme.com", []string{"api-devs"}, "web"); ok {
		t.Errorf("api-devs on web should have no role")
	}
	// platform-admins group → org_admin via wildcard.
	if role, ok := org.EffectiveRoleForIdentity("x@acme.com", []string{"platform-admins"}, "anything"); !ok || role != RoleOrgAdmin {
		t.Errorf("platform-admins = (%q,%v), want (org_admin,true)", role, ok)
	}
}

func TestEffectiveRoleForIdentity_HighestOfTeamAndGroup(t *testing.T) {
	org := groupOrg()
	// alice is a local admin AND carries the low-priv "everyone" group — the
	// higher role (org_admin) must win.
	if role, ok := org.EffectiveRoleForIdentity("alice", []string{"everyone"}, "api"); !ok || role != RoleOrgAdmin {
		t.Errorf("alice (team admins + group everyone) = (%q,%v), want (org_admin,true)", role, ok)
	}
}

func TestEffectiveRole_BackwardCompatIgnoresGroups(t *testing.T) {
	org := groupOrg()
	// The username-only API must not grant anything from group bindings.
	if _, ok := org.EffectiveRole("sso@acme.com", "api"); ok {
		t.Errorf("EffectiveRole must ignore IdP groups (got a role for a group-only identity)")
	}
	// A local team member still resolves.
	if role, ok := org.EffectiveRole("alice", "api"); !ok || role != RoleOrgAdmin {
		t.Errorf("alice via team = (%q,%v), want (org_admin,true)", role, ok)
	}
}

func TestHasPermissionForIdentity(t *testing.T) {
	org := groupOrg()
	if !org.HasPermissionForIdentity("sso@acme.com", []string{"api-devs"}, "api", RoleDeveloper) {
		t.Error("api-devs should have developer on api")
	}
	if org.HasPermissionForIdentity("sso@acme.com", []string{"api-devs"}, "api", RoleProjectAdmin) {
		t.Error("api-devs (developer) must not satisfy project_admin")
	}
}

func TestOIDCConfig_Defaulted(t *testing.T) {
	got := OIDCConfig{}.Defaulted()
	if got.UsernameClaim != "email" {
		t.Errorf("UsernameClaim default = %q, want email", got.UsernameClaim)
	}
	if got.GroupsClaim != "groups" {
		t.Errorf("GroupsClaim default = %q, want groups", got.GroupsClaim)
	}
	if got.ClientSecretRef.Key != "client-secret" {
		t.Errorf("ClientSecretRef.Key default = %q, want client-secret", got.ClientSecretRef.Key)
	}
	if len(got.Scopes) == 0 || got.Scopes[0] != "openid" {
		t.Errorf("Scopes default = %v, want openid-led", got.Scopes)
	}
	// Explicit values are preserved.
	custom := OIDCConfig{UsernameClaim: "sub", Scopes: []string{"openid"}}.Defaulted()
	if custom.UsernameClaim != "sub" || len(custom.Scopes) != 1 {
		t.Errorf("Defaulted must not overwrite explicit values: %+v", custom)
	}
}
