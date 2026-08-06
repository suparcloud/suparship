package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/secrets"
)

// vaultPolicyOrg is a two-env org where staging and prod live on DIFFERENT
// clusters — the arrangement whose whole purpose is that the staging cluster
// cannot read prod.
func vaultPolicyOrg() *rbac.Org {
	return &rbac.Org{
		SecretBackend: secrets.BackendConfig{
			Type:  secrets.BackendVault,
			Vault: &secrets.HCVaultConfig{Address: "https://vault.example.com:8200"},
		},
		Environments: []rbac.OrgEnvironment{
			{Name: "staging", ClusterRefs: []string{"stg-1"}},
			{Name: "prod", ClusterRefs: []string{"prod-1", "prod-2"}},
		},
	}
}

func getVaultPolicies(t *testing.T, org *rbac.Org) (*httptest.ResponseRecorder, VaultPoliciesResponse) {
	t.Helper()
	h := &secretsHandler{orgStore: &staticOrgProvider{org: org}, logger: slog.Default()}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/org/secret-backend/vault-policies", nil)
	rec := httptest.NewRecorder()
	h.handleGetVaultPolicies(rec, req)

	var resp VaultPoliciesResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
	}
	return rec, resp
}

// The core guarantee: each cluster is entitled to global plus ONLY the envs bound
// to it, and no policy it can carry grants another env's prefix.
func TestVaultPolicies_ClusterEntitlementsFollowEnvBindings(t *testing.T) {
	rec, resp := getVaultPolicies(t, vaultPolicyOrg())
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	byCluster := map[string]VaultClusterPolicyDTO{}
	for _, c := range resp.Clusters {
		byCluster[c.Cluster] = c
	}
	if len(byCluster) != 3 {
		t.Fatalf("expected 3 clusters, got %d: %+v", len(byCluster), resp.Clusters)
	}

	stg := byCluster["stg-1"]
	wantStg := []string{"suparship-eso-read-global", "suparship-eso-read-env-staging"}
	if strings.Join(stg.Policies, ",") != strings.Join(wantStg, ",") {
		t.Errorf("stg-1 policies = %v, want %v", stg.Policies, wantStg)
	}
	// The regression this whole change exists to prevent.
	for _, p := range stg.Policies {
		if strings.Contains(p, "prod") {
			t.Errorf("staging cluster entitled to a prod policy (%q) — prod/non-prod segregation broken", p)
		}
	}
	if stg.TokenCommand == "" || !strings.Contains(stg.TokenCommand, "-display-name=suparship-eso-stg-1") {
		t.Errorf("token command not rendered for stg-1: %q", stg.TokenCommand)
	}

	// Both prod clusters are bound to prod, so both are entitled to it — a
	// standby must resolve secrets across a failover.
	for _, name := range []string{"prod-1", "prod-2"} {
		c := byCluster[name]
		if len(c.BoundEnvs) != 1 || c.BoundEnvs[0] != "prod" {
			t.Errorf("%s boundEnvs = %v, want [prod]", name, c.BoundEnvs)
		}
		if len(c.Policies) != 2 || c.Policies[1] != "suparship-eso-read-env-prod" {
			t.Errorf("%s policies = %v", name, c.Policies)
		}
	}
}

// One read policy per scope prefix (global + per env), each confined to its own
// prefix — never the mount.
func TestVaultPolicies_ReadPoliciesAreScopedPerEnv(t *testing.T) {
	_, resp := getVaultPolicies(t, vaultPolicyOrg())

	if len(resp.ReadPolicies) != 3 {
		t.Fatalf("expected global + 2 env policies, got %d", len(resp.ReadPolicies))
	}
	if resp.Mount != secrets.DefaultVaultMount {
		t.Errorf("mount = %q, want the default %q", resp.Mount, secrets.DefaultVaultMount)
	}
	for _, p := range resp.ReadPolicies {
		if strings.Contains(p.HCL, `/data/*"`) {
			t.Errorf("read policy %q grants the whole mount:\n%s", p.Name, p.HCL)
		}
		if strings.Contains(p.HCL, "create") || strings.Contains(p.HCL, "update") {
			t.Errorf("read policy %q is not read-only:\n%s", p.Name, p.HCL)
		}
	}
	// The prod policy must not mention staging's prefix, or vice versa.
	byEnv := map[string]string{}
	for _, p := range resp.ReadPolicies {
		byEnv[p.Env] = p.HCL
	}
	if strings.Contains(byEnv["staging"], "env-prod") {
		t.Errorf("staging policy references prod:\n%s", byEnv["staging"])
	}
	if strings.Contains(byEnv["prod"], "env-staging") {
		t.Errorf("prod policy references staging:\n%s", byEnv["prod"])
	}
}

// suparship's own token spans the mount — it manages items in every scope.
func TestVaultPolicies_WritePolicyIsMountWide(t *testing.T) {
	_, resp := getVaultPolicies(t, vaultPolicyOrg())

	if resp.WritePolicy.Name != "suparship-write" {
		t.Errorf("write policy name = %q", resp.WritePolicy.Name)
	}
	if !strings.Contains(resp.WritePolicy.HCL, `path "suparship/data/*"`) {
		t.Errorf("write policy should span the mount:\n%s", resp.WritePolicy.HCL)
	}
}

// A cluster with no environment bound to it is entitled to the global policy and
// nothing else — least privilege, not an error. (Such a cluster only reaches the
// response when a cluster store is wired; without one the listing is derived from
// env bindings, which by definition can't name it.)
func TestVaultPolicies_UnboundClusterGetsGlobalOnly(t *testing.T) {
	org := vaultPolicyOrg()

	boundEnvs := boundEnvsForCluster(org, "not-bound-anywhere")
	if len(boundEnvs) != 0 {
		t.Fatalf("boundEnvs = %v, want none", boundEnvs)
	}
	got := secrets.VaultClusterPolicyNames(boundEnvs)
	if len(got) != 1 || got[0] != "suparship-eso-read-global" {
		t.Errorf("policies = %v, want just the global read policy", got)
	}
}

// The endpoint is Vault-specific: 1Password scopes reads via its own vault list.
func TestVaultPolicies_RejectsNonVaultBackend(t *testing.T) {
	org := vaultPolicyOrg()
	org.SecretBackend.Type = secrets.Backend1Password

	rec, _ := getVaultPolicies(t, org)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for a non-Vault backend, got %d: %s", rec.Code, rec.Body.String())
	}
}

// A custom mount must flow into every rendered path, or the operator writes
// policies that silently match nothing.
func TestVaultPolicies_HonoursCustomMount(t *testing.T) {
	org := vaultPolicyOrg()
	org.SecretBackend.Vault.Mount = "platform-secrets"

	_, resp := getVaultPolicies(t, org)
	if resp.Mount != "platform-secrets" {
		t.Fatalf("mount = %q", resp.Mount)
	}
	for _, p := range resp.ReadPolicies {
		if !strings.Contains(p.HCL, `path "platform-secrets/data/`) {
			t.Errorf("policy %q ignores the custom mount:\n%s", p.Name, p.HCL)
		}
	}
	if !strings.Contains(resp.WritePolicy.HCL, `path "platform-secrets/data/*"`) {
		t.Errorf("write policy ignores the custom mount:\n%s", resp.WritePolicy.HCL)
	}
}
