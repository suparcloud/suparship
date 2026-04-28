package gitops_test

import (
	"strings"
	"testing"

	"github.com/suparcloud/suparship/internal/gitops"
)

func TestBuildProjectNamespaceManifest_LabelsProjectAndManagedBy(t *testing.T) {
	got, err := gitops.BuildProjectNamespaceManifest("demo", gitops.ProjectNamespaceEnv{
		EnvName:   "staging",
		Namespace: "demo-staging",
	})
	if err != nil {
		t.Fatalf("BuildProjectNamespaceManifest: %v", err)
	}
	out := string(got)
	for _, want := range []string{
		"name: demo-staging",
		"suparship.io/project: demo",
		"suparship.io/managed-by: suparship",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected manifest to contain %q, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "suparship.io/cluster") {
		t.Errorf("did not expect suparship.io/cluster label when ClusterRef is empty, got:\n%s", out)
	}
}

func TestBuildProjectNamespaceManifest_ClusterLabelWhenBound(t *testing.T) {
	got, err := gitops.BuildProjectNamespaceManifest("demo", gitops.ProjectNamespaceEnv{
		EnvName:    "staging",
		Namespace:  "demo-staging",
		ClusterRef: "kind-staging",
	})
	if err != nil {
		t.Fatalf("BuildProjectNamespaceManifest: %v", err)
	}
	out := string(got)
	if !strings.Contains(out, "suparship.io/cluster: kind-staging") {
		t.Errorf("expected cluster label, got:\n%s", out)
	}
}
