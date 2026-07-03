package gitops_test

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/suparcloud/suparship/internal/gitops"
)

// assertSingleGitFilesGenerator asserts the AppSet uses exactly one plain
// git-files generator (no matrix, no list) — the per-(app,cluster) app.yaml
// layout: fan-out is expressed by one app.yaml file per target cluster under
// _targets/, so the AppSet itself no longer carries a cluster matrix/list.
func assertSingleGitFilesGenerator(t *testing.T, as *gitops.ApplicationSet) *gitops.GitFileGenerator {
	t.Helper()
	if len(as.Spec.Generators) != 1 {
		t.Fatalf("expected exactly one generator, got %d: %+v", len(as.Spec.Generators), as.Spec.Generators)
	}
	g := as.Spec.Generators[0]
	if g.Git == nil || g.Matrix != nil || g.List != nil {
		t.Fatalf("expected a single plain git-files generator (no matrix/list), got %+v", g)
	}
	return g.Git
}

// TestBuildArgoAppSet_SingleCluster asserts that a single-cluster env uses the
// per-(app,cluster) plain git-files generator whose glob descends into
// _targets/, the per-file {{appName}} template name, and the templated
// destination server — so fan-out is carried by the published app.yaml files,
// not an AppSet-level cluster matrix.
func TestBuildArgoAppSet_SingleCluster(t *testing.T) {
	as := gitops.BuildArgoAppSet(
		gitops.AppSetEnv{
			EnvName:  "staging",
			Clusters: []gitops.ClusterTarget{{Name: "staging-eastus", Server: "https://one:6443"}},
		},
		"http://gitea/gitops.git", gitops.AppSetOptions{},
	)
	git := assertSingleGitFilesGenerator(t, as)
	if len(git.Files) != 1 || git.Files[0].Path != "envs/staging/*/*/_targets/*/app.yaml" {
		t.Errorf("generator glob = %+v, want envs/staging/*/*/_targets/*/app.yaml", git.Files)
	}
	if got := as.Spec.Template.Metadata.Name; got != "{{appName}}" {
		t.Errorf("name = %q, want {{appName}}", got)
	}
	if as.Spec.Template.Metadata.Labels["suparship.io/cluster"] != "{{clusterName}}" {
		t.Errorf("missing cluster label, got %+v", as.Spec.Template.Metadata.Labels)
	}
	if as.Spec.Template.Spec.Destination.Server != "{{clusterServer}}" {
		t.Errorf("destination = %q, want {{clusterServer}}", as.Spec.Template.Spec.Destination.Server)
	}
}

// TestBuildArgoAppSet_UnboundFallback asserts that an env with no resolvable
// cluster still produces the same single git-files AppSet — the in-cluster
// fallback is now applied per-file by the publisher (which writes a
// _targets/in-cluster/app.yaml), so the AppSet is cluster-agnostic.
func TestBuildArgoAppSet_UnboundFallback(t *testing.T) {
	as := gitops.BuildArgoAppSet(
		gitops.AppSetEnv{EnvName: "staging", ClusterServer: "https://one:6443"},
		"http://gitea/gitops.git", gitops.AppSetOptions{},
	)
	git := assertSingleGitFilesGenerator(t, as)
	if len(git.Files) != 1 || git.Files[0].Path != "envs/staging/*/*/_targets/*/app.yaml" {
		t.Errorf("generator glob = %+v, want envs/staging/*/*/_targets/*/app.yaml", git.Files)
	}
	if got := as.Spec.Template.Metadata.Name; got != "{{appName}}" {
		t.Errorf("name = %q, want {{appName}}", got)
	}
	if as.Spec.Template.Spec.Destination.Server != "{{clusterServer}}" {
		t.Errorf("destination = %q, want {{clusterServer}}", as.Spec.Template.Spec.Destination.Server)
	}
}

// TestBuildArgoAppSet_CustomPatternWithEnv verifies the AppSet template name is
// the literal {{appName}} regardless of the configured naming pattern — the
// custom pattern is now baked per-file into each app.yaml's appName by the
// publisher (RenderArgoAppName), not templated in the AppSet.
func TestBuildArgoAppSet_CustomPatternWithEnv(t *testing.T) {
	as := gitops.BuildArgoAppSet(
		gitops.AppSetEnv{
			EnvName:  "prod",
			Clusters: []gitops.ClusterTarget{{Name: "prod-westus", Server: "https://one:6443"}},
		},
		"http://gitea/gitops.git",
		gitops.AppSetOptions{ArgoAppNamePattern: "{project}-{app}-{env}-{cluster}"},
	)
	if got := as.Spec.Template.Metadata.Name; got != "{{appName}}" {
		t.Errorf("name = %q, want {{appName}}", got)
	}
}

// TestBuildArgoAppSet_FanOut asserts that a multi-cluster env produces the SAME
// single git-files generator as a single-cluster env — fan-out to >1 cluster is
// expressed by the publisher writing one app.yaml per target under _targets/,
// each with its own {{appName}}/{{clusterServer}}/{{valuesPath}}, so the AppSet
// stays cluster-agnostic (no matrix/list).
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

	git := assertSingleGitFilesGenerator(t, as)
	if len(git.Files) != 1 || git.Files[0].Path != "envs/prod/*/*/_targets/*/app.yaml" {
		t.Errorf("generator glob = %+v, want envs/prod/*/*/_targets/*/app.yaml", git.Files)
	}
	if as.Spec.Template.Metadata.Name != "{{appName}}" {
		t.Errorf("fan-out name = %q, want {{appName}}", as.Spec.Template.Metadata.Name)
	}
	if as.Spec.Template.Spec.Destination.Server != "{{clusterServer}}" {
		t.Errorf("fan-out destination = %q, want {{clusterServer}}", as.Spec.Template.Spec.Destination.Server)
	}

	// Marshals to valid YAML carrying the per-file glob + templated params, and
	// NOT the old matrix/list generator keys.
	out, err := yaml.Marshal(as)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{"_targets/*/app.yaml", "{{appName}}", "{{clusterServer}}"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("rendered AppSet missing %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"matrix:", "list:"} {
		if strings.Contains(string(out), unwanted) {
			t.Errorf("rendered AppSet must not carry %q (single git-files generator):\n%s", unwanted, out)
		}
	}
}
