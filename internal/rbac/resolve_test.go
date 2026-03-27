package rbac

import (
	"testing"
)

func testOrg() *Org {
	return &Org{
		Name:        "acme",
		DisplayName: "Acme Corp",
		Teams: []Team{
			{Name: "admins", DisplayName: "Admins", Members: []string{"alice"}},
			{Name: "backend", DisplayName: "Backend", Members: []string{"bob", "carol"}},
			{Name: "viewers", DisplayName: "Viewers", Members: []string{"dave"}},
			{Name: "multi", DisplayName: "Multi", Members: []string{"carol"}},
		},
		RoleBindings: []RoleBinding{
			{Project: "*", Team: "admins", Role: RoleOrgAdmin},
			{Project: "api", Team: "backend", Role: RoleDeveloper},
			{Project: "api", Team: "multi", Role: RoleProjectAdmin},
			{Project: "web", Team: "backend", Role: RoleViewer},
			{Project: "*", Team: "viewers", Role: RoleViewer},
		},
	}
}

func TestUserTeams(t *testing.T) {
	org := testOrg()

	tests := []struct {
		user     string
		expected []string
	}{
		{"alice", []string{"admins"}},
		{"bob", []string{"backend"}},
		{"carol", []string{"backend", "multi"}},
		{"dave", []string{"viewers"}},
		{"unknown", nil},
	}

	for _, tt := range tests {
		t.Run(tt.user, func(t *testing.T) {
			got := org.UserTeams(tt.user)
			if len(got) != len(tt.expected) {
				t.Fatalf("UserTeams(%q) = %v, want %v", tt.user, got, tt.expected)
			}
			for i, g := range got {
				if g != tt.expected[i] {
					t.Fatalf("UserTeams(%q)[%d] = %q, want %q", tt.user, i, g, tt.expected[i])
				}
			}
		})
	}
}

func TestEffectiveRoleDirectBinding(t *testing.T) {
	org := testOrg()

	role, ok := org.EffectiveRole("bob", "api")
	if !ok {
		t.Fatal("expected a role for bob on api")
	}
	if role != RoleDeveloper {
		t.Fatalf("expected %q, got %q", RoleDeveloper, role)
	}
}

func TestEffectiveRoleWildcard(t *testing.T) {
	org := testOrg()

	role, ok := org.EffectiveRole("alice", "any-project")
	if !ok {
		t.Fatal("expected a role for alice (wildcard binding)")
	}
	if role != RoleOrgAdmin {
		t.Fatalf("expected %q, got %q", RoleOrgAdmin, role)
	}
}

func TestEffectiveRoleHighestWins(t *testing.T) {
	org := testOrg()

	// carol is in "backend" (developer on api) and "multi" (project_admin on api)
	role, ok := org.EffectiveRole("carol", "api")
	if !ok {
		t.Fatal("expected a role for carol on api")
	}
	if role != RoleProjectAdmin {
		t.Fatalf("expected %q (highest), got %q", RoleProjectAdmin, role)
	}
}

func TestEffectiveRoleNoMembership(t *testing.T) {
	org := testOrg()

	_, ok := org.EffectiveRole("unknown-user", "api")
	if ok {
		t.Fatal("expected no role for unknown user")
	}
}

func TestEffectiveRoleNoBindingForProject(t *testing.T) {
	org := testOrg()

	// bob is in "backend", which has bindings for "api" and "web" but not "infra"
	_, ok := org.EffectiveRole("bob", "infra")
	if ok {
		t.Fatal("expected no role for bob on infra")
	}
}

func TestEffectiveRoleWildcardFallback(t *testing.T) {
	org := testOrg()

	// dave is in "viewers" which has wildcard binding
	role, ok := org.EffectiveRole("dave", "any-project")
	if !ok {
		t.Fatal("expected a role for dave (wildcard)")
	}
	if role != RoleViewer {
		t.Fatalf("expected %q, got %q", RoleViewer, role)
	}
}

func TestHasPermissionSufficient(t *testing.T) {
	org := testOrg()

	if !org.HasPermission("alice", "api", RoleDeveloper) {
		t.Fatal("org_admin should satisfy developer requirement")
	}
}

func TestHasPermissionExact(t *testing.T) {
	org := testOrg()

	if !org.HasPermission("bob", "api", RoleDeveloper) {
		t.Fatal("developer should satisfy developer requirement")
	}
}

func TestHasPermissionInsufficient(t *testing.T) {
	org := testOrg()

	if org.HasPermission("dave", "api", RoleDeveloper) {
		t.Fatal("viewer should not satisfy developer requirement")
	}
}

func TestHasPermissionNoRole(t *testing.T) {
	org := testOrg()

	if org.HasPermission("unknown", "api", RoleViewer) {
		t.Fatal("unknown user should not have any permission")
	}
}

func TestRoleLevel(t *testing.T) {
	if RoleLevel(RoleOrgAdmin) <= RoleLevel(RoleProjectAdmin) {
		t.Fatal("org_admin should outrank project_admin")
	}
	if RoleLevel(RoleProjectAdmin) <= RoleLevel(RoleDeveloper) {
		t.Fatal("project_admin should outrank developer")
	}
	if RoleLevel(RoleDeveloper) <= RoleLevel(RoleViewer) {
		t.Fatal("developer should outrank viewer")
	}
	if RoleLevel(Role("bogus")) != 0 {
		t.Fatal("unknown role should have level 0")
	}
}
