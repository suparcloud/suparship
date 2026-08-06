package secrets

import (
	"fmt"
	"sort"
	"strings"
)

// ── Vault policies (least privilege for the Vault backend) ──────────────────
//
// A suparship vault is a path prefix inside one KV v2 mount (see the hcvault
// package doc), so a Vault POLICY on that prefix is what actually isolates one
// environment's secrets from another's. Granting a workload cluster read on the
// whole mount would let a staging cluster read production — the 1Password
// backend never allowed that, because a Connect token is minted against a
// specific list of vaults (see clusterVaultIDs).
//
// Policies are per SCOPE, not per cluster, and composed on the token: Vault
// policies are additive, so a cluster bound to staging + prod gets
//
//	-policy=suparship-eso-read-global
//	-policy=suparship-eso-read-env-staging
//	-policy=suparship-eso-read-env-prod
//
// Keeping them per scope means an environment's policy is written once and does
// not change as clusters are added, removed, or failed over — only the token's
// policy list does.
//
// There are exactly two prefix families to cover, because VaultName maps every
// scope onto one of them: the global prefix (global/project/stack scopes) and an
// env prefix (env/cluster/project-env/stack-env/preview/preview-PR scopes). So
// "global + one per bound env" is complete — a cluster needs nothing else.

const (
	// vaultWritePolicyName is the control-plane policy: suparship itself
	// manages items in every scope, so this one is legitimately mount-wide.
	vaultWritePolicyName = "suparship-write"
	// vaultReadPolicyPrefix prefixes the per-scope ESO read policies.
	vaultReadPolicyPrefix = "suparship-eso-read-"
)

// VaultWritePolicyName returns the name of the policy for suparship's own write
// token.
func VaultWritePolicyName() string { return vaultWritePolicyName }

// VaultReadPolicyName returns the name of the ESO read policy for scope's prefix:
// "suparship-eso-read-global" or "suparship-eso-read-env-<env>". Scopes that
// live inside an env prefix (cluster, project-env, stack-env, preview) resolve to
// that env's policy — they are not separately grantable, by design.
func VaultReadPolicyName(scope Scope) string {
	name := VaultName(scope)
	switch name {
	case GlobalVaultName():
		return vaultReadPolicyPrefix + "global"
	default:
		return vaultReadPolicyPrefix + "env-" + scope.Env
	}
}

// VaultWritePolicyHCL renders the control-plane policy for suparship's write
// token. Mount-wide on purpose: suparship is the component that creates and
// rotates items across every scope. KV v2 routes values through data/ and
// listings + item deletion through metadata/, so both prefixes are needed
// (DeleteItem destroys all versions via a metadata delete).
func VaultWritePolicyHCL(mount string) string {
	m := effectiveMountName(mount)
	return fmt.Sprintf(`path "%s/data/*" {
  capabilities = ["create", "update", "read", "delete"]
}
path "%s/metadata/*" {
  capabilities = ["read", "list", "delete"]
}
`, m, m)
}

// VaultReadPolicyHCL renders the ESO read policy for ONE scope's prefix — the
// only paths a cluster bound to that scope ever resolves. Read-only, and scoped
// to the prefix rather than the mount, so a token carrying it cannot reach
// another environment's items.
func VaultReadPolicyHCL(mount string, scope Scope) string {
	m := effectiveMountName(mount)
	prefix := VaultName(scope)
	return fmt.Sprintf(`path "%s/data/%s/*" {
  capabilities = ["read"]
}
path "%s/metadata/%s/*" {
  capabilities = ["read", "list"]
}
`, m, prefix, m, prefix)
}

// VaultPolicy pairs a policy's name with its HCL body, ready to hand to
// `vault policy write <name> -`.
type VaultPolicy struct {
	Name string `json:"name"`
	// Scope describes what the policy grants in human terms ("global" or an env
	// name); empty for the write policy.
	Env string `json:"env,omitempty"`
	HCL string `json:"hcl"`
}

// VaultReadPolicies returns the ESO read policy for the global prefix plus one
// per environment in envs, in the order given (global first). Duplicate and blank
// env names are dropped so a caller can pass an org's environment list verbatim.
func VaultReadPolicies(mount string, envs []string) []VaultPolicy {
	out := []VaultPolicy{{
		Name: VaultReadPolicyName(GlobalScope()),
		Env:  "global",
		HCL:  VaultReadPolicyHCL(mount, GlobalScope()),
	}}
	seen := map[string]bool{}
	for _, env := range envs {
		env = strings.TrimSpace(env)
		if env == "" || seen[env] {
			continue
		}
		seen[env] = true
		scope := EnvScope(env)
		out = append(out, VaultPolicy{
			Name: VaultReadPolicyName(scope),
			Env:  env,
			HCL:  VaultReadPolicyHCL(mount, scope),
		})
	}
	return out
}

// VaultClusterPolicyNames returns the policies to attach to one cluster's ESO
// token: the global read policy plus the read policy of each environment bound
// to that cluster. Env order is normalised (sorted) so the rendered token command
// is stable across calls and doesn't churn in the UI.
//
// A cluster with no bound environments still gets the global policy — it can
// resolve org-wide values and nothing else, which is the correct least-privilege
// answer rather than an error.
func VaultClusterPolicyNames(boundEnvs []string) []string {
	names := []string{VaultReadPolicyName(GlobalScope())}
	seen := map[string]bool{}
	envs := make([]string, 0, len(boundEnvs))
	for _, env := range boundEnvs {
		env = strings.TrimSpace(env)
		if env == "" || seen[env] {
			continue
		}
		seen[env] = true
		envs = append(envs, env)
	}
	sort.Strings(envs)
	for _, env := range envs {
		names = append(names, VaultReadPolicyName(EnvScope(env)))
	}
	return names
}

// VaultTokenCreateCommand renders the `vault token create` line for one
// cluster's ESO token, with one -policy flag per entitled scope.
//
// -orphan so the token outlives the operator token that minted it; a plain TTL
// (not -period) because nothing in the delivery pipeline renews these — they are
// rotated by re-pasting before expiry. The default policy is left attached:
// suparship's Probe authenticates via token self-lookup, which "default" grants.
func VaultTokenCreateCommand(cluster string, boundEnvs []string, ttl string) string {
	if strings.TrimSpace(ttl) == "" {
		ttl = DefaultVaultTokenTTL
	}
	var sb strings.Builder
	sb.WriteString("vault token create -orphan \\\n")
	for _, p := range VaultClusterPolicyNames(boundEnvs) {
		fmt.Fprintf(&sb, "  -policy=%s \\\n", p)
	}
	fmt.Fprintf(&sb, "  -display-name=suparship-eso-%s -ttl=%s", cluster, ttl)
	return sb.String()
}

// DefaultVaultTokenTTL is the TTL suggested for the generated token commands.
// Deliberately shorter than a year: these are static credentials rotated by
// re-paste, so the exposure window should be the rotation cadence an operator
// can actually keep, not the longest one Vault accepts.
const DefaultVaultTokenTTL = "2160h" // 90 days

// effectiveMountName mirrors HCVaultConfig.EffectiveMount for callers that only
// have the raw string (the policy renderers take a mount, not a config).
func effectiveMountName(mount string) string {
	if strings.TrimSpace(mount) == "" {
		return DefaultVaultMount
	}
	return strings.TrimSpace(mount)
}
