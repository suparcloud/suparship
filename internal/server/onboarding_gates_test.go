package server

import (
	"testing"

	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/secrets"
)

func gateByKey(gates []SetupGate, key string) SetupGate {
	for _, g := range gates {
		if g.Key == key {
			return g
		}
	}
	return SetupGate{}
}

// A blank install: every actionable gate should be error/incomplete.
func TestComputeSetupGates_Empty(t *testing.T) {
	gates := computeSetupGates(SetupGateInputs{})
	for _, key := range []string{"auth", "gitops", "clusters", "environments"} {
		if g := gateByKey(gates, key); g.Status == gateOK {
			t.Errorf("gate %q unexpectedly ok on a blank install", key)
		}
	}
	// k8s backend (the zero value) needs no setup → secret-backend is ok.
	if g := gateByKey(gates, "secret-backend"); g.Status != gateOK {
		t.Errorf("secret-backend gate = %q, want ok for default k8s backend", g.Status)
	}
}

// A fully wired k8s-backend install: all gates ok.
func TestComputeSetupGates_K8sComplete(t *testing.T) {
	gates := computeSetupGates(SetupGateInputs{
		AuthConfigured:   true,
		GitOpsConfigured: true,
		OrgName:          "acme",
		ClusterNames:     []string{"staging-aks"},
		Environments: []rbac.OrgEnvironment{
			{Name: "staging", ClusterRefs: []string{"staging-aks"}},
		},
	})
	for _, g := range gates {
		if g.Status != gateOK {
			t.Errorf("gate %q = %q (%s), want ok", g.Key, g.Status, g.Message)
		}
	}
}

// Environments defined but unbound → environments gate incomplete.
func TestComputeSetupGates_UnboundEnv(t *testing.T) {
	gates := computeSetupGates(SetupGateInputs{
		AuthConfigured:   true,
		GitOpsConfigured: true,
		ClusterNames:     []string{"staging-aks"},
		Environments:     []rbac.OrgEnvironment{{Name: "staging"}}, // no clusterRef
	})
	if g := gateByKey(gates, "environments"); g.Status != gateIncomplete {
		t.Errorf("environments gate = %q, want incomplete", g.Status)
	}
}

// 1Password backend missing the per-cluster token → secret-backend incomplete,
// with a message naming the unsealed cluster.
func TestComputeSetupGates_OnePasswordIncomplete(t *testing.T) {
	backend := secrets.BackendConfig{
		Type: secrets.Backend1Password,
		OnePassword: &secrets.OnePasswordConfig{
			GlobalVault: secrets.VaultRef{VaultID: "g1"},
			EnvVaults:   []secrets.VaultRef{{Key: "staging", VaultID: "e1"}},
			// no ClusterTokens → staging-aks unsealed
		},
	}
	gates := computeSetupGates(SetupGateInputs{
		AuthConfigured:   true,
		GitOpsConfigured: true,
		ClusterNames:     []string{"staging-aks"},
		Environments: []rbac.OrgEnvironment{
			{Name: "staging", ClusterRefs: []string{"staging-aks"}},
		},
		Backend: backend,
	})
	g := gateByKey(gates, "secret-backend")
	if g.Status != gateIncomplete {
		t.Fatalf("secret-backend gate = %q, want incomplete", g.Status)
	}
	if g.Message == "" {
		t.Error("expected a remediation message naming the unsealed cluster")
	}
}
