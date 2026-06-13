package gitops_test

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/suparcloud/suparship/internal/gitops"
)

// TestBuildArgoAppSet_SingleClusterUnchanged asserts the legacy template:
// a bare git generator, hardcoded destination, and the {project}-{app}-{env}
// Application name (the Kargo/history/status contract).
func TestBuildArgoAppSet_SingleClusterUnchanged(t *testing.T) {
	as := gitops.BuildArgoAppSet(
		gitops.AppSetEnv{EnvName: "staging", ClusterServer: "https://one:6443"},
		"http://gitea/gitops.git", gitops.AppSetOptions{},
	)
	if len(as.Spec.Generators) != 1 || as.Spec.Generators[0].Git == nil {
		t.Fatalf("single-cluster should use a bare git generator, got %+v", as.Spec.Generators)
	}
	if as.Spec.Generators[0].Matrix != nil {
		t.Error("single-cluster must not use a matrix generator")
	}
	if as.Spec.Template.Metadata.Name != "{{project}}-{{name}}-staging" {
		t.Errorf("name = %q, want {{project}}-{{name}}-staging", as.Spec.Template.Metadata.Name)
	}
	if as.Spec.Template.Spec.Destination.Server != "https://one:6443" {
		t.Errorf("destination = %q, want the hardcoded server", as.Spec.Template.Spec.Destination.Server)
	}
}

// TestBuildArgoAppSet_FanOut asserts that >1 cluster produces a matrix
// (git × cluster list), a per-cluster Application name, and a templated
// destination server.
func TestBuildArgoAppSet_FanOut(t *testing.T) {
	as := gitops.BuildArgoAppSet(
		gitops.AppSetEnv{
			EnvName: "prod",
			Clusters: []gitops.ClusterTarget{
				{Name: "aks", Server: "https://aks:6443"},
				{Name: "eks", Server: "https://eks:6443"},
			},
		},
		"http://gitea/gitops.git", gitops.AppSetOptions{},
	)

	if len(as.Spec.Generators) != 1 || as.Spec.Generators[0].Matrix == nil {
		t.Fatalf("fan-out should use a matrix generator, got %+v", as.Spec.Generators)
	}
	gens := as.Spec.Generators[0].Matrix.Generators
	if len(gens) != 2 || gens[0].Git == nil || gens[1].List == nil {
		t.Fatalf("matrix should be git × list, got %+v", gens)
	}
	if len(gens[1].List.Elements) != 2 {
		t.Fatalf("list should have 2 cluster elements, got %d", len(gens[1].List.Elements))
	}
	if gens[1].List.Elements[0]["clusterName"] != "aks" || gens[1].List.Elements[0]["clusterServer"] != "https://aks:6443" {
		t.Errorf("first list element wrong: %+v", gens[1].List.Elements[0])
	}
	if as.Spec.Template.Metadata.Name != "{{project}}-{{name}}-prod-{{clusterName}}" {
		t.Errorf("fan-out name = %q, want the -{{clusterName}} suffix", as.Spec.Template.Metadata.Name)
	}
	if as.Spec.Template.Spec.Destination.Server != "{{clusterServer}}" {
		t.Errorf("fan-out destination = %q, want {{clusterServer}}", as.Spec.Template.Spec.Destination.Server)
	}

	// Marshals to valid YAML carrying the matrix/list keys.
	out, err := yaml.Marshal(as)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{"matrix:", "list:", "clusterName: aks", "clusterServer: https://eks:6443"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("rendered AppSet missing %q:\n%s", want, out)
		}
	}
}
