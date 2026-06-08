package rbac

import (
	"strings"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
)

func TestParseOrgValid(t *testing.T) {
	data := []byte(`
name: acme
displayName: Acme Corp
teams:
  - name: admins
    displayName: Admins
    members: [alice]
roleBindings:
  - project: "*"
    team: admins
    role: org_admin
`)

	org, err := ParseOrg(data)
	if err != nil {
		t.Fatalf("ParseOrg: %v", err)
	}
	if org.Name != "acme" {
		t.Fatalf("expected name %q, got %q", "acme", org.Name)
	}
	if len(org.Teams) != 1 {
		t.Fatalf("expected 1 team, got %d", len(org.Teams))
	}
	if org.Teams[0].Members[0] != "alice" {
		t.Fatalf("expected member alice, got %q", org.Teams[0].Members[0])
	}
	if org.RoleBindings[0].Role != RoleOrgAdmin {
		t.Fatalf("expected role %q, got %q", RoleOrgAdmin, org.RoleBindings[0].Role)
	}
}

func TestParseOrgInvalidYAML(t *testing.T) {
	_, err := ParseOrg([]byte(`{{{not yaml`))
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestValidateMissingName(t *testing.T) {
	org := &Org{Teams: []Team{{Name: "t"}}}
	err := org.Validate()
	if err == nil || !strings.Contains(err.Error(), "org name") {
		t.Fatalf("expected org name error, got: %v", err)
	}
}

func TestValidateDuplicateTeam(t *testing.T) {
	org := &Org{
		Name:  "test",
		Teams: []Team{{Name: "dup"}, {Name: "dup"}},
	}
	err := org.Validate()
	if err == nil || !strings.Contains(err.Error(), "duplicate team") {
		t.Fatalf("expected duplicate team error, got: %v", err)
	}
}

func TestValidateEmptyTeamName(t *testing.T) {
	org := &Org{
		Name:  "test",
		Teams: []Team{{Name: ""}},
	}
	err := org.Validate()
	if err == nil || !strings.Contains(err.Error(), "name must not be empty") {
		t.Fatalf("expected empty team name error, got: %v", err)
	}
}

func TestValidateUnknownRole(t *testing.T) {
	org := &Org{
		Name:         "test",
		Teams:        []Team{{Name: "t"}},
		RoleBindings: []RoleBinding{{Project: "*", Team: "t", Role: "superuser"}},
	}
	err := org.Validate()
	if err == nil || !strings.Contains(err.Error(), "unknown role") {
		t.Fatalf("expected unknown role error, got: %v", err)
	}
}

func TestValidateBindingReferencesUnknownTeam(t *testing.T) {
	org := &Org{
		Name:         "test",
		Teams:        []Team{{Name: "t"}},
		RoleBindings: []RoleBinding{{Project: "*", Team: "ghost", Role: RoleViewer}},
	}
	err := org.Validate()
	if err == nil || !strings.Contains(err.Error(), "unknown team") {
		t.Fatalf("expected unknown team error, got: %v", err)
	}
}

func TestValidateGroupBinding(t *testing.T) {
	// A Group-only binding (the SSO feature) must validate — no team required.
	org := &Org{
		Name:         "test",
		RoleBindings: []RoleBinding{{Project: "*", Group: "platform-admins", Role: RoleOrgAdmin}},
	}
	if err := org.Validate(); err != nil {
		t.Fatalf("group-only binding should validate, got: %v", err)
	}
}

func TestValidateBindingRejectsBothTeamAndGroup(t *testing.T) {
	org := &Org{
		Name:         "test",
		Teams:        []Team{{Name: "t"}},
		RoleBindings: []RoleBinding{{Project: "*", Team: "t", Group: "g", Role: RoleViewer}},
	}
	if err := org.Validate(); err == nil || !strings.Contains(err.Error(), "only one of team or group") {
		t.Fatalf("expected both-set error, got: %v", err)
	}
}

func TestValidateBindingRejectsNeitherTeamNorGroup(t *testing.T) {
	org := &Org{
		Name:         "test",
		RoleBindings: []RoleBinding{{Project: "*", Role: RoleViewer}},
	}
	if err := org.Validate(); err == nil || !strings.Contains(err.Error(), "one of team or group") {
		t.Fatalf("expected neither-set error, got: %v", err)
	}
}

func TestValidateBindingMissingProject(t *testing.T) {
	org := &Org{
		Name:         "test",
		Teams:        []Team{{Name: "t"}},
		RoleBindings: []RoleBinding{{Project: "", Team: "t", Role: RoleViewer}},
	}
	err := org.Validate()
	if err == nil || !strings.Contains(err.Error(), "project must not be empty") {
		t.Fatalf("expected empty project error, got: %v", err)
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	original := &Org{
		Name:        "acme",
		DisplayName: "Acme Corp",
		Teams: []Team{
			{Name: "admins", DisplayName: "Admins", Members: []string{"alice"}},
			{Name: "devs", DisplayName: "Developers", Members: []string{"bob", "carol"}},
		},
		RoleBindings: []RoleBinding{
			{Project: "*", Team: "admins", Role: RoleOrgAdmin},
			{Project: "api", Team: "devs", Role: RoleDeveloper},
		},
	}

	data, err := original.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	restored, err := ParseOrg(data)
	if err != nil {
		t.Fatalf("ParseOrg after Marshal: %v", err)
	}

	if restored.Name != original.Name {
		t.Fatalf("name mismatch: %q vs %q", restored.Name, original.Name)
	}
	if len(restored.Teams) != len(original.Teams) {
		t.Fatalf("teams count mismatch: %d vs %d", len(restored.Teams), len(original.Teams))
	}
	if len(restored.RoleBindings) != len(original.RoleBindings) {
		t.Fatalf("bindings count mismatch: %d vs %d", len(restored.RoleBindings), len(original.RoleBindings))
	}
	if restored.RoleBindings[1].Role != RoleDeveloper {
		t.Fatalf("role mismatch after round-trip: %q", restored.RoleBindings[1].Role)
	}
}

func TestNewDefaultOrg(t *testing.T) {
	org := NewDefaultOrg("myorg", "My Org", "admin")

	if err := org.Validate(); err != nil {
		t.Fatalf("default org should be valid: %v", err)
	}
	if org.Name != "myorg" {
		t.Fatalf("expected name %q, got %q", "myorg", org.Name)
	}
	if len(org.Teams) != 1 || org.Teams[0].Name != "admins" {
		t.Fatal("expected single admins team")
	}
	if org.Teams[0].Members[0] != "admin" {
		t.Fatalf("expected admin in admins team, got %q", org.Teams[0].Members[0])
	}
	if org.RoleBindings[0].Role != RoleOrgAdmin {
		t.Fatalf("expected org_admin binding, got %q", org.RoleBindings[0].Role)
	}
	if org.CreatedAt == "" {
		t.Fatal("createdAt should be set")
	}
}

func TestIsValidRole(t *testing.T) {
	for _, r := range AllRoles {
		if !IsValidRole(r) {
			t.Fatalf("expected %q to be valid", r)
		}
	}
	if IsValidRole("superuser") {
		t.Fatal("superuser should not be a valid role")
	}
}

func TestOrgEnvironment_EffectivePatterns(t *testing.T) {
	tests := []struct {
		name       string
		env        OrgEnvironment
		wantApp    string
		wantProj   string
	}{
		{
			name:    "all empty",
			env:     OrgEnvironment{},
			wantApp: "", wantProj: "",
		},
		{
			name: "explicit fields preferred over legacy",
			env: OrgEnvironment{
				NamespacePattern:        "{app}",
				AppNamespacePattern:     "{project}-{app}",
				ProjectNamespacePattern: "{project}-{env}",
			},
			wantApp:  "{project}-{app}",
			wantProj: "{project}-{env}",
		},
		// Back-compat: configs written by older suparship versions only have
		// NamespacePattern. It must continue to drive app-scope resolution so
		// existing operators don't see a behaviour change after upgrade.
		{
			name: "legacy NamespacePattern falls back to app only",
			env: OrgEnvironment{
				NamespacePattern: "{app}",
			},
			wantApp:  "{app}",
			wantProj: "", // intentionally empty — {app} doesn't make sense for project scope
		},
		{
			name: "legacy NamespacePattern with explicit ProjectNamespacePattern",
			env: OrgEnvironment{
				NamespacePattern:        "{app}",
				ProjectNamespacePattern: "{project}",
			},
			wantApp:  "{app}",
			wantProj: "{project}",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.env.EffectiveAppNamespacePattern(); got != tt.wantApp {
				t.Errorf("EffectiveAppNamespacePattern() = %q, want %q", got, tt.wantApp)
			}
			if got := tt.env.EffectiveProjectNamespacePattern(); got != tt.wantProj {
				t.Errorf("EffectiveProjectNamespacePattern() = %q, want %q", got, tt.wantProj)
			}
		})
	}
}

func TestRoutingProfiles_RoundTrip(t *testing.T) {
	original := &Org{
		Name: "acme",
		Teams: []Team{
			{Name: "admins", DisplayName: "Admins", Members: []string{"alice"}},
		},
		RoleBindings: []RoleBinding{
			{Project: "*", Team: "admins", Role: RoleOrgAdmin},
		},
		RoutingProfiles: domain.RoutingProfiles{
			"internal": {IngressClassName: "nginx-internal"},
			"external": {IngressClassName: "nginx", ClusterIssuer: "letsencrypt-prod", BaseDomain: "acme.com"},
		},
		Environments: []OrgEnvironment{
			{
				Name:  "staging",
				Order: 1,
				RoutingProfiles: domain.RoutingProfiles{
					"external": {IngressClassName: "nginx", ClusterIssuer: "letsencrypt-staging"},
				},
			},
		},
	}

	data, err := original.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	restored, err := ParseOrg(data)
	if err != nil {
		t.Fatalf("ParseOrg: %v", err)
	}

	if got := restored.RoutingProfiles["external"].ClusterIssuer; got != "letsencrypt-prod" {
		t.Errorf("org external issuer = %q, want letsencrypt-prod", got)
	}
	if got := restored.Environments[0].RoutingProfiles["external"].ClusterIssuer; got != "letsencrypt-staging" {
		t.Errorf("staging override issuer = %q, want letsencrypt-staging", got)
	}
}

func TestValidateRoutingProfiles_Rejects(t *testing.T) {
	tests := []struct {
		name string
		org  *Org
		want string // substring of expected error
	}{
		{
			name: "unknown mode name",
			org: &Org{
				Name: "x", Teams: []Team{{Name: "a", Members: []string{"u"}}},
				RoleBindings: []RoleBinding{{Project: "*", Team: "a", Role: RoleOrgAdmin}},
				RoutingProfiles: domain.RoutingProfiles{
					"public": {IngressClassName: "nginx"},
				},
			},
			want: "internal",
		},
		{
			name: "disabled is not a profile name",
			org: &Org{
				Name: "x", Teams: []Team{{Name: "a", Members: []string{"u"}}},
				RoleBindings: []RoleBinding{{Project: "*", Team: "a", Role: RoleOrgAdmin}},
				RoutingProfiles: domain.RoutingProfiles{
					"disabled": {IngressClassName: "nginx"},
				},
			},
			want: "disabled",
		},
		{
			name: "empty IngressClassName",
			org: &Org{
				Name: "x", Teams: []Team{{Name: "a", Members: []string{"u"}}},
				RoleBindings: []RoleBinding{{Project: "*", Team: "a", Role: RoleOrgAdmin}},
				RoutingProfiles: domain.RoutingProfiles{
					"internal": {ClusterIssuer: "letsencrypt-prod"},
				},
			},
			want: "ingressClassName",
		},
		{
			name: "env override with empty IngressClassName",
			org: &Org{
				Name: "x", Teams: []Team{{Name: "a", Members: []string{"u"}}},
				RoleBindings: []RoleBinding{{Project: "*", Team: "a", Role: RoleOrgAdmin}},
				Environments: []OrgEnvironment{
					{
						Name:  "staging",
						Order: 1,
						RoutingProfiles: domain.RoutingProfiles{
							"external": {ClusterIssuer: "letsencrypt-staging"},
						},
					},
				},
			},
			want: "environments[staging]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.org.Validate()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q missing substring %q", err.Error(), tt.want)
			}
		})
	}
}
