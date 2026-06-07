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

	// Platform manifests no longer live in the app's chart dir — they moved to
	// the platform-owned _app-resources/ tree.
	for _, name := range []string{"env-configmap.yaml", "external-secret.yaml"} {
		if _, err := os.Stat(filepath.Join(appDir, name)); !os.IsNotExist(err) {
			t.Errorf("%s should NOT be in the app chart dir (moved to _app-resources/)", name)
		}
	}
	resDir := filepath.Join(dir, "_app-resources", "staging", "demo", "nginx")
	for _, name := range []string{"meta.yaml", "env-configmap.yaml", "external-secret.yaml"} {
		if _, err := os.Stat(filepath.Join(resDir, name)); err != nil {
			t.Errorf("expected %s under _app-resources, got: %v", name, err)
		}
	}
}

// TestBuildArgoAppSet_NoPlatformSource locks that the app's chart ApplicationSet
// carries only the values-ref + chart sources — platform manifests are shipped
// by the separate platform ApplicationSet, not bundled onto the app.
func TestBuildArgoAppSet_NoPlatformSource(t *testing.T) {
	appset := gitops.BuildArgoAppSet(
		gitops.AppSetEnv{EnvName: "staging", ClusterServer: "https://kubernetes.default.svc", BaseDomain: "localhost"},
		"https://gitea.example.com/org/gitops.git",
		gitops.AppSetOptions{ArgoCDNamespace: "argocd", TargetRevision: "main", SyncAutomated: true},
	)
	sources := appset.Spec.Template.Spec.Sources
	if len(sources) != 2 {
		t.Fatalf("expected 2 sources (values ref + chart), got %d", len(sources))
	}
	for _, s := range sources {
		if s.Directory != nil {
			t.Errorf("app AppSet must not carry a directory (platform-manifests) source, got %+v", s)
		}
	}
}

// TestBuildPlatformAppSet_DirectorySource verifies the platform ApplicationSet
// ships the per-app manifests from _app-resources via a directory source whose
// include filter lists the two manifests (and never kustomization.yaml).
func TestBuildPlatformAppSet_DirectorySource(t *testing.T) {
	appset := gitops.BuildPlatformAppSet(
		gitops.AppSetEnv{EnvName: "staging", ClusterServer: "https://k8s:6443"},
		"https://gitea.example.com/org/gitops.git",
		gitops.AppSetOptions{ArgoCDNamespace: "argocd", TargetRevision: "main", SyncAutomated: true},
	)
	if appset.Metadata.Name != "staging-platform" {
		t.Errorf("appset name = %q, want staging-platform", appset.Metadata.Name)
	}
	gen := appset.Spec.Generators[0].Git
	if gen == nil || len(gen.Files) != 1 || gen.Files[0].Path != "_app-resources/staging/*/*/meta.yaml" {
		t.Errorf("generator path wrong: %+v", gen)
	}
	srcs := appset.Spec.Template.Spec.Sources
	if len(srcs) != 1 || srcs[0].Directory == nil {
		t.Fatalf("expected single directory source, got %+v", srcs)
	}
	if srcs[0].Path != "_app-resources/staging/{{project}}/{{name}}" {
		t.Errorf("directory path = %q", srcs[0].Path)
	}
	inc := srcs[0].Directory.Include
	if !strings.Contains(inc, "env-configmap.yaml") || !strings.Contains(inc, "external-secret.yaml") {
		t.Errorf("include filter wrong: %q", inc)
	}
	if strings.Contains(inc, "kustomization.yaml") {
		t.Errorf("include must not reference kustomization.yaml: %q", inc)
	}
	if got := appset.Spec.Template.Spec.Destination; got.Server != "https://k8s:6443" || got.Namespace != "{{namespace}}" {
		t.Errorf("destination = %+v, want workload cluster + {{namespace}}", got)
	}
}

// Compile-time check that a couple of imports we depend on still exist
// — guards against a future refactor accidentally renaming the symbols
// these tests depend on.
var (
	_ = secrets.ResourceNaming{}
	_ = domain.AppEnvStaging
)
