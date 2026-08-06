package secrets

import (
	"strings"
	"testing"
)

// The whole point of per-scope policies: an env's policy must not grant any path
// outside that env's prefix, so a staging token cannot read prod.
func TestVaultReadPolicyHCL_ScopedToOnePrefix(t *testing.T) {
	hcl := VaultReadPolicyHCL("suparship", EnvScope("staging"))

	for _, want := range []string{
		`path "suparship/data/suparship-secrets-env-staging/*"`,
		`path "suparship/metadata/suparship-secrets-env-staging/*"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("policy missing %s:\n%s", want, hcl)
		}
	}
	// The regression this replaces: mount-wide read.
	for _, forbidden := range []string{
		`path "suparship/data/*"`,
		`path "suparship/metadata/*"`,
		"suparship-secrets-env-prod",
		"suparship-secrets-global",
	} {
		if strings.Contains(hcl, forbidden) {
			t.Errorf("policy must not grant %s:\n%s", forbidden, hcl)
		}
	}
	// Read-only — ESO never writes.
	for _, forbidden := range []string{"create", "update", "delete"} {
		if strings.Contains(hcl, forbidden) {
			t.Errorf("ESO read policy must not grant %q:\n%s", forbidden, hcl)
		}
	}
}

// Every scope that stores its items under an env prefix must resolve to that
// env's policy — cluster overrides, project/stack-env and previews are not
// separately grantable because they are not separately stored.
func TestVaultReadPolicyName_EnvNestedScopesShareOnePolicy(t *testing.T) {
	want := "suparship-eso-read-env-prod"
	scopes := map[string]Scope{
		"env":        EnvScope("prod"),
		"cluster":    ClusterScope("prod", "eu-1"),
		"projectenv": ProjectEnvScope("acme", "prod"),
		"preview":    PreviewScope("prod"),
	}
	for label, scope := range scopes {
		if got := VaultReadPolicyName(scope); got != want {
			t.Errorf("%s scope → %q, want %q", label, got, want)
		}
	}
	// Global-family scopes land on the global policy.
	if got := VaultReadPolicyName(GlobalScope()); got != "suparship-eso-read-global" {
		t.Errorf("global scope → %q", got)
	}
	if got := VaultReadPolicyName(ProjectScope("acme")); got != "suparship-eso-read-global" {
		t.Errorf("project scope → %q, want the global policy (its items live there)", got)
	}
}

// A cluster's token carries global + one policy per bound env, and nothing else.
func TestVaultClusterPolicyNames(t *testing.T) {
	got := VaultClusterPolicyNames([]string{"prod", "staging", "prod", "  "})
	want := []string{
		"suparship-eso-read-global",
		"suparship-eso-read-env-prod",
		"suparship-eso-read-env-staging",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("policy[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// An unbound cluster gets global only — least privilege, not an error.
func TestVaultClusterPolicyNames_NoBoundEnvs(t *testing.T) {
	got := VaultClusterPolicyNames(nil)
	if len(got) != 1 || got[0] != "suparship-eso-read-global" {
		t.Errorf("got %v, want just the global read policy", got)
	}
}

func TestVaultReadPolicies_GlobalFirstAndDeduped(t *testing.T) {
	got := VaultReadPolicies("", []string{"staging", "staging", "", "prod"})
	if len(got) != 3 {
		t.Fatalf("got %d policies, want 3 (global + 2 envs): %+v", len(got), got)
	}
	if got[0].Env != "global" {
		t.Errorf("first policy env = %q, want global", got[0].Env)
	}
	// An empty mount falls back to the default, so the HCL is still usable.
	if !strings.Contains(got[0].HCL, `path "suparship/data/suparship-secrets-global/*"`) {
		t.Errorf("default mount not applied:\n%s", got[0].HCL)
	}
	if got[1].Env != "staging" || got[2].Env != "prod" {
		t.Errorf("env order not preserved: %q, %q", got[1].Env, got[2].Env)
	}
}

// The control plane legitimately spans the mount — suparship writes every scope.
func TestVaultWritePolicyHCL_IsMountWide(t *testing.T) {
	hcl := VaultWritePolicyHCL("suparship")
	for _, want := range []string{
		`path "suparship/data/*"`,
		`path "suparship/metadata/*"`,
		"delete", // DeleteItem destroys all versions via a metadata delete
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("write policy missing %s:\n%s", want, hcl)
		}
	}
}

// The policy prefix and the ESO remoteRef key are derived from the same
// VaultName, and this test pins that they stay in step. If they drift, ESO looks
// up a path the token can't read and the ExternalSecret sits NotReady with no
// clue why — the same silent failure mode itemKeyFor's doc comment warns about.
func TestVaultReadPolicy_CoversEveryItemPathInScope(t *testing.T) {
	const mount = "suparship"
	// One scope per storage prefix family, with the item names really written.
	cases := []struct {
		label string
		scope Scope
		tier  Tier
		app   string
	}{
		{"env shared", EnvScope("staging"), TierShared, ""},
		{"env app", EnvScope("staging").WithProject("acme"), TierApp, "web"},
		{"cluster shared", ClusterScope("staging", "eu-1"), TierShared, ""},
		{"projectenv shared", ProjectEnvScope("acme", "staging"), TierShared, ""},
		{"preview app", PreviewScope("staging").WithProject("acme"), TierApp, "web"},
		{"global shared", GlobalScope(), TierShared, ""},
		{"global app", GlobalScope().WithProject("acme"), TierApp, "web"},
		{"project shared", ProjectScope("acme"), TierShared, ""},
	}
	for _, tc := range cases {
		// The path the store writes and ESO reads, mount-relative.
		itemPath := VaultName(tc.scope) + "/" + ItemName(tc.scope, tc.tier, tc.app)
		// The data-path glob the entitled policy grants.
		policyPrefix := mount + "/data/" + VaultName(tc.scope) + "/"

		if !strings.HasPrefix(mount+"/data/"+itemPath, policyPrefix) {
			t.Errorf("%s: item %q not covered by policy prefix %q", tc.label, itemPath, policyPrefix)
		}
		// And the policy an operator actually writes contains that glob.
		hcl := VaultReadPolicyHCL(mount, tc.scope)
		if !strings.Contains(hcl, `path "`+policyPrefix+`*"`) {
			t.Errorf("%s: policy does not grant %q*:\n%s", tc.label, policyPrefix, hcl)
		}
		// Item names never contain a slash, so a single-level glob suffices —
		// if that ever changes the glob above would silently under-match.
		if strings.Contains(ItemName(tc.scope, tc.tier, tc.app), "/") {
			t.Errorf("%s: item name contains a slash; revisit the policy glob", tc.label)
		}
	}
}

func TestVaultTokenCreateCommand(t *testing.T) {
	cmd := VaultTokenCreateCommand("eu-1", []string{"staging"}, "")

	for _, want := range []string{
		"-policy=suparship-eso-read-global",
		"-policy=suparship-eso-read-env-staging",
		"-display-name=suparship-eso-eu-1",
		"-ttl=" + DefaultVaultTokenTTL,
		"-orphan",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("command missing %q:\n%s", want, cmd)
		}
	}
	// -period tokens expire unless actively renewed and nothing renews these.
	if strings.Contains(cmd, "-period") {
		t.Errorf("must not suggest -period:\n%s", cmd)
	}
	// An explicit TTL overrides the default.
	if cmd := VaultTokenCreateCommand("eu-1", nil, "720h"); !strings.Contains(cmd, "-ttl=720h") {
		t.Errorf("explicit ttl not honoured:\n%s", cmd)
	}
}
