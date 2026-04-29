package gitops_test

import (
	"strings"
	"testing"

	"github.com/suparcloud/suparship/internal/branding"
	"github.com/suparcloud/suparship/internal/gitops"
)

func TestBuildProjectNamespaceManifest_LabelsProjectAndManagedBy(t *testing.T) {
	got, err := gitops.BuildProjectNamespaceManifest("demo", gitops.ProjectNamespaceEnv{
		EnvName:   "staging",
		Namespace: "demo-staging",
	}, branding.Config{})
	if err != nil {
		t.Fatalf("BuildProjectNamespaceManifest: %v", err)
	}
	out := string(got)
	// Default branding stamps managed-by + generator-version + the
	// project label using the suparship.io domain — matching what the
	// envconfig replicator selectors expect by default.
	for _, want := range []string{
		"name: demo-staging",
		"app.kubernetes.io/managed-by: suparship",
		"suparship.io/project: demo",
		"suparship.io/generator-version:",
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
	}, branding.Config{})
	if err != nil {
		t.Fatalf("BuildProjectNamespaceManifest: %v", err)
	}
	out := string(got)
	if !strings.Contains(out, "suparship.io/cluster: kind-staging") {
		t.Errorf("expected cluster label, got:\n%s", out)
	}
}

// TestBuildProjectNamespaceManifest_CustomBrandingOverridesDomain verifies
// the white-label path: a custom Branding config swaps both the managed-by
// value and the label domain so a SRE contractor's GitOps repo carries
// their platform identity, not "suparship".
func TestBuildProjectNamespaceManifest_CustomBrandingOverridesDomain(t *testing.T) {
	got, err := gitops.BuildProjectNamespaceManifest("demo", gitops.ProjectNamespaceEnv{
		EnvName:    "staging",
		Namespace:  "demo-staging",
		ClusterRef: "kind-staging",
	}, branding.Config{Name: "acme-platform", LabelDomain: "platform.acme.io"})
	if err != nil {
		t.Fatalf("BuildProjectNamespaceManifest: %v", err)
	}
	out := string(got)
	for _, want := range []string{
		"app.kubernetes.io/managed-by: acme-platform",
		"platform.acme.io/project: demo",
		"platform.acme.io/cluster: kind-staging",
		"platform.acme.io/generator-version:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected manifest to contain %q, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "suparship.io/") || strings.Contains(out, "managed-by: suparship") {
		t.Errorf("custom branding should leave no suparship.io/ traces, got:\n%s", out)
	}
}
