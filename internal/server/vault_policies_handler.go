package server

import (
	"net/http"

	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/secrets"
)

// VaultPolicyDTO is one Vault policy an operator must write: its name, what it
// covers, and the HCL body to pipe into `vault policy write <name> -`.
type VaultPolicyDTO struct {
	Name string `json:"name"`
	// Env is "global" for the org-wide prefix, otherwise the environment name.
	// Empty on the write policy, which spans the mount.
	Env string `json:"env,omitempty"`
	HCL string `json:"hcl"`
}

// VaultClusterPolicyDTO tells an operator exactly which policies one cluster's
// ESO token may carry, and the command that mints it.
type VaultClusterPolicyDTO struct {
	Cluster string `json:"cluster"`
	// BoundEnvs are the environments bound to this cluster — the reason it is
	// entitled to those env policies and no others.
	BoundEnvs []string `json:"boundEnvs"`
	Policies  []string `json:"policies"`
	// TokenCommand is a ready-to-run `vault token create` with one -policy flag
	// per entitled scope.
	TokenCommand string `json:"tokenCommand"`
}

// VaultPoliciesResponse is the full least-privilege setup material for the Vault
// backend.
type VaultPoliciesResponse struct {
	Mount string `json:"mount"`
	// WritePolicy is suparship's own control-plane policy (mount-wide by design:
	// suparship manages items in every scope).
	WritePolicy VaultPolicyDTO `json:"writePolicy"`
	// ReadPolicies is the global read policy plus one per environment. Clusters
	// compose these; no cluster ever gets mount-wide read.
	ReadPolicies []VaultPolicyDTO `json:"readPolicies"`
	// Clusters maps each registered cluster to its entitled policies.
	Clusters []VaultClusterPolicyDTO `json:"clusters"`
}

// handleGetVaultPolicies serves
// GET /api/v1/org/secret-backend/vault-policies.
//
// It renders the least-privilege Vault policy set for this org: one read policy
// per scope prefix (global + per env), and per cluster the subset it is entitled
// to based on its environment bindings. This is what keeps a staging cluster's
// ESO token from reading production — the Vault equivalent of minting a
// 1Password Connect token against a specific list of vaults (clusterVaultIDs).
//
// Read-only and computed: nothing is applied to Vault. suparship holds a write
// token for the KV mount, not the sys/policy privileges that writing policies
// and minting tokens require — so the operator runs these against their own
// Vault, and we make sure the paths are exactly right.
func (h *secretsHandler) handleGetVaultPolicies(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	org, err := h.orgStore.GetOrg(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}
	if org.SecretBackend.Type != secrets.BackendVault {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{
			Error: "vault policies apply to the HashiCorp Vault backend only",
		})
		return
	}

	mount := org.SecretBackend.Vault.EffectiveMount()

	resp := VaultPoliciesResponse{
		Mount: mount,
		WritePolicy: VaultPolicyDTO{
			Name: secrets.VaultWritePolicyName(),
			HCL:  secrets.VaultWritePolicyHCL(mount),
		},
	}
	envNames := make([]string, 0, len(org.Environments))
	for _, e := range org.Environments {
		envNames = append(envNames, e.Name)
	}
	for _, p := range secrets.VaultReadPolicies(mount, envNames) {
		resp.ReadPolicies = append(resp.ReadPolicies, VaultPolicyDTO{Name: p.Name, Env: p.Env, HCL: p.HCL})
	}

	// Per-cluster entitlements. Clusters come from the cluster store when wired;
	// the org's bindings alone can't enumerate a cluster that has no env bound
	// yet, and that cluster still needs the global policy.
	for _, name := range h.clusterNamesForPolicies(r) {
		boundEnvs := boundEnvsForCluster(org, name)
		resp.Clusters = append(resp.Clusters, VaultClusterPolicyDTO{
			Cluster:      name,
			BoundEnvs:    boundEnvs,
			Policies:     secrets.VaultClusterPolicyNames(boundEnvs),
			TokenCommand: secrets.VaultTokenCreateCommand(name, boundEnvs, ""),
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// clusterNamesForPolicies lists registered cluster names, degrading to the
// clusters named in the org's env bindings when no cluster store is wired.
func (h *secretsHandler) clusterNamesForPolicies(r *http.Request) []string {
	if h.clusterStore != nil {
		if clusters, err := h.clusterStore.ListClusters(r.Context()); err == nil {
			names := make([]string, 0, len(clusters))
			for _, c := range clusters {
				names = append(names, c.Name)
			}
			return names
		}
	}
	org, err := h.orgStore.GetOrg(r.Context())
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var names []string
	for _, e := range org.Environments {
		for _, ref := range e.ClusterRefs {
			if ref != "" && !seen[ref] {
				seen[ref] = true
				names = append(names, ref)
			}
		}
	}
	return names
}

// boundEnvsForCluster returns the environments bound to clusterName, in org
// order. Mirrors clusterVaultIDs' binding rule: every cluster an env is bound to
// reads that env, not just the active one, so a standby resolves secrets across
// a failover.
func boundEnvsForCluster(org *rbac.Org, clusterName string) []string {
	var envs []string
	for _, e := range org.Environments {
		if e.IsBoundTo(clusterName) {
			envs = append(envs, e.Name)
		}
	}
	return envs
}
