package gitops_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/gitops"
	"github.com/suparcloud/suparship/internal/secrets"
)

// TestPublishAppFiles_NoKustomizationEmitted locks in the post-incident
// invariant: the publisher must NOT write a kustomization.yaml in per-app
// dirs. ArgoCD's `directory:` source treats files in `Include` as plain
// manifests and would ship the kustomization.yaml to the API server as a
// Kustomization CRD object — which doesn't exist on workload clusters and
// breaks sync with "could not find kustomize.config.k8s.io/Kustomization".
//
// If this test starts failing, you've reintroduced the kustomization-writes
// path. The right fix is to keep relying on the include filter alone.
func TestPublishAppFiles_NoKustomizationEmitted(t *testing.T) {
	dir := t.TempDir()

	app := &domain.App{
		Name:        "nginx",
		ProjectName: "demo",
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "web-service"}},
	}
	envs := []gitops.AppPublishEnv{
		{
			EnvName:   "staging",
			EnvType:   domain.AppEnvStaging,
			Order:     1,
			Bound:     true,
			Namespace: "demo-nginx-staging",
			ScopeKeys: gitops.ScopePresence{GlobalApp: true},
		},
	}

	p := newTestPublisher(t)
	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishAppFilesForTest: %v", err)
	}

	appDir := filepath.Join(dir, "envs", "staging", "demo", "nginx")
	if _, err := os.Stat(filepath.Join(appDir, "kustomization.yaml")); !os.IsNotExist(err) {
		t.Errorf("kustomization.yaml should NOT be written by the publisher (it breaks ArgoCD's directory mode), got err=%v", err)
	}

	// Sanity: the manifests the include filter applies still exist.
	for _, name := range []string{"env-configmap.yaml", "external-secret.yaml"} {
		if _, err := os.Stat(filepath.Join(appDir, name)); err != nil {
			t.Errorf("expected %s in app dir, got: %v", name, err)
		}
	}
}

// TestBuildArgoAppSet_HasPerAppDirectorySource locks the wiring that closes
// the orphan-manifest gap: the rendered ApplicationSet must carry a third
// source pointing at the per-app dir with an include filter. Without this,
// env-configmap.yaml + external-secret.yaml are written to gitops but
// never reach the cluster.
//
// The include filter must NOT reference kustomization.yaml — see
// TestPublishAppFiles_NoKustomizationEmitted for the why.
func TestBuildArgoAppSet_HasPerAppDirectorySource(t *testing.T) {
	appset := gitops.BuildArgoAppSet(
		gitops.AppSetEnv{
			EnvName:       "staging",
			ClusterServer: "https://kubernetes.default.svc",
			BaseDomain:    "localhost",
		},
		"https://gitea.example.com/org/gitops.git",
		gitops.AppSetOptions{
			ArgoCDNamespace: "argocd",
			TargetRevision:  "main",
			SyncAutomated:   true,
		},
	)
	sources := appset.Spec.Template.Spec.Sources
	if len(sources) != 3 {
		t.Fatalf("expected 3 sources (ref + chart + per-app manifests), got %d", len(sources))
	}
	manifestSrc := sources[2]
	wantPath := "envs/staging/{{project}}/{{name}}"
	if manifestSrc.Path != wantPath {
		t.Errorf("per-app source path = %q, want %q", manifestSrc.Path, wantPath)
	}
	if manifestSrc.Directory == nil {
		t.Fatal("per-app source missing Directory section")
	}
	if !strings.Contains(manifestSrc.Directory.Include, "env-configmap.yaml") {
		t.Errorf("include filter missing env-configmap.yaml: %q", manifestSrc.Directory.Include)
	}
	if !strings.Contains(manifestSrc.Directory.Include, "external-secret.yaml") {
		t.Errorf("include filter missing external-secret.yaml: %q", manifestSrc.Directory.Include)
	}
	if strings.Contains(manifestSrc.Directory.Include, "kustomization.yaml") {
		t.Errorf("include filter must NOT reference kustomization.yaml — directory mode would apply it as a Kustomization CRD; got %q", manifestSrc.Directory.Include)
	}
	// Sanity: the chart source still points at .../charts/{{template}}.
	if !strings.HasSuffix(sources[1].Path, "charts/{{template}}") {
		t.Errorf("expected chart source still at charts/{{template}}, got %q", sources[1].Path)
	}
}

// Compile-time check that a couple of imports we depend on still exist
// — guards against a future refactor accidentally renaming the symbols
// these tests depend on.
var (
	_ = secrets.ResourceNaming{}
	_ = domain.AppEnvStaging
)
