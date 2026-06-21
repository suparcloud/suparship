package gitops_test

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/suparcloud/suparship/internal/gitops"
)

// TestBuildArgoAppSet_SingleCluster asserts that a single-cluster env now also
// uses the per-cluster matrix generator and a cluster-suffixed name, so adding a
// second cluster later never renames the first cluster's Application.
func TestBuildArgoAppSet_SingleCluster(t *testing.T) {
	as := gitops.BuildArgoAppSet(
		gitops.AppSetEnv{
			EnvName:  "staging",
			Clusters: []gitops.ClusterTarget{{Name: "staging-eastus", Server: "https://one:6443"}},
		},
		"http://gitea/gitops.git", gitops.AppSetOptions{},
	)
	if len(as.Spec.Generators) != 1 || as.Spec.Generators[0].Matrix == nil {
		t.Fatalf("single-cluster should now use a matrix generator, got %+v", as.Spec.Generators)
	}
	gens := as.Spec.Generators[0].Matrix.Generators
	if len(gens) != 2 || gens[0].Git == nil || gens[1].List == nil || len(gens[1].List.Elements) != 1 {
		t.Fatalf("matrix should be git × 1-element list, got %+v", gens)
	}
	if got := as.Spec.Template.Metadata.Name; got != "{{project}}-{{name}}-{{clusterName}}" {
		t.Errorf("name = %q, want default {{project}}-{{name}}-{{clusterName}}", got)
	}
	if as.Spec.Template.Metadata.Labels["suparship.io/cluster"] != "{{clusterName}}" {
		t.Errorf("missing cluster label, got %+v", as.Spec.Template.Metadata.Labels)
	}
	if as.Spec.Template.Spec.Destination.Server != "{{clusterServer}}" {
		t.Errorf("destination = %q, want {{clusterServer}}", as.Spec.Template.Spec.Destination.Server)
	}
}

// TestBuildArgoAppSet_UnboundFallback asserts that an env with no resolvable
// cluster still renders a uniform per-cluster name via the in-cluster fallback.
func TestBuildArgoAppSet_UnboundFallback(t *testing.T) {
	as := gitops.BuildArgoAppSet(
		gitops.AppSetEnv{EnvName: "staging", ClusterServer: "https://one:6443"},
		"http://gitea/gitops.git", gitops.AppSetOptions{},
	)
	if as.Spec.Generators[0].Matrix == nil {
		t.Fatalf("unbound env should still use a matrix generator, got %+v", as.Spec.Generators)
	}
	el := as.Spec.Generators[0].Matrix.Generators[1].List.Elements
	if len(el) != 1 || el[0]["clusterName"] != "in-cluster" || el[0]["clusterServer"] != "https://one:6443" {
		t.Errorf("expected one in-cluster fallback element pointing at the env server, got %+v", el)
	}
}

// TestBuildArgoAppSet_CustomPatternWithEnv verifies a configured pattern that
// keeps {env} bakes the env as a literal while {cluster} stays templated.
func TestBuildArgoAppSet_CustomPatternWithEnv(t *testing.T) {
	as := gitops.BuildArgoAppSet(
		gitops.AppSetEnv{
			EnvName:  "prod",
			Clusters: []gitops.ClusterTarget{{Name: "prod-westus", Server: "https://one:6443"}},
		},
		"http://gitea/gitops.git",
		gitops.AppSetOptions{ArgoAppNamePattern: "{project}-{app}-{env}-{cluster}"},
	)
	if got := as.Spec.Template.Metadata.Name; got != "{{project}}-{{name}}-prod-{{clusterName}}" {
		t.Errorf("name = %q, want {{project}}-{{name}}-prod-{{clusterName}}", got)
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
	if as.Spec.Template.Metadata.Name != "{{project}}-{{name}}-{{clusterName}}" {
		t.Errorf("fan-out name = %q, want {{project}}-{{name}}-{{clusterName}}", as.Spec.Template.Metadata.Name)
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
