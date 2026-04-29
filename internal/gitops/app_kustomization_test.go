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

// TestPublishAppFiles_KustomizationListsExternalSecretWhenStoreSet verifies
// the per-app kustomization.yaml lists every platform manifest actually
// emitted to the dir. With a StoreName set, both env-configmap.yaml AND
// external-secret.yaml must appear so ArgoCD's per-app directory source
// applies them in lockstep with the chart's Helm source.
func TestPublishAppFiles_KustomizationListsExternalSecretWhenStoreSet(t *testing.T) {
	dir := t.TempDir()

	app := &domain.App{
		Name:        "nginx",
		ProjectName: "demo",
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "web-service"}},
	}
	envs := []gitops.AppPublishEnv{
		{
			EnvName:        "staging",
			EnvType:        domain.AppEnvStaging,
			Order:          1,
			Bound:          true,
			Namespace:      "demo-nginx-staging",
			StoreName:      "op-staging",
			VaultItemTitle: "demo-nginx-staging",
		},
	}

	p := newTestPublisher(t)
	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishAppFilesForTest: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "staging", "demo", "nginx", "kustomization.yaml"))
	if err != nil {
		t.Fatalf("read kustomization.yaml: %v", err)
	}
	got := string(raw)
	for _, want := range []string{
		"kind: Kustomization",
		"- env-configmap.yaml",
		"- external-secret.yaml",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("kustomization.yaml missing %q, got:\n%s", want, got)
		}
	}
	// app.yaml and values.yaml are NOT k8s manifests; kustomize would
	// reject them. They must NEVER appear in the resource list.
	for _, banned := range []string{"- app.yaml", "- values.yaml"} {
		if strings.Contains(got, banned) {
			t.Errorf("kustomization.yaml must not list %q (it's a parameter/values file), got:\n%s", banned, got)
		}
	}
}

// TestPublishAppFiles_KustomizationOmitsExternalSecretWhenNoStore verifies
// the kustomization adapts to what's actually in the dir: when no store
// is configured, only env-configmap.yaml is emitted, so the kustomization
// must not reference a non-existent external-secret.yaml (kustomize would
// fail to render).
func TestPublishAppFiles_KustomizationOmitsExternalSecretWhenNoStore(t *testing.T) {
	dir := t.TempDir()

	app := &domain.App{
		Name:        "nginx",
		ProjectName: "demo",
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "web-service"}},
	}
	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, Namespace: "demo-nginx-staging"},
	}

	p := newTestPublisher(t)
	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishAppFilesForTest: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "staging", "demo", "nginx", "kustomization.yaml"))
	if err != nil {
		t.Fatalf("read kustomization.yaml: %v", err)
	}
	got := string(raw)
	if !strings.Contains(got, "- env-configmap.yaml") {
		t.Errorf("kustomization missing env-configmap.yaml: %s", got)
	}
	if strings.Contains(got, "external-secret.yaml") {
		t.Errorf("kustomization should not reference external-secret.yaml when no StoreName is set, got:\n%s", got)
	}
}

// TestBuildArgoAppSet_HasPerAppDirectorySource locks the wiring that closes
// the orphan-manifest gap: the rendered ApplicationSet must carry a third
// source pointing at the per-app dir with an include filter. Without this,
// env-configmap.yaml + external-secret.yaml are written to gitops but
// never reach the cluster.
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
	wantPath := "staging/{{project}}/{{name}}"
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
	if !strings.Contains(manifestSrc.Directory.Include, "kustomization.yaml") {
		t.Errorf("include filter missing kustomization.yaml: %q", manifestSrc.Directory.Include)
	}
	// Sanity: the chart source still points at charts/{{template}}.
	if !strings.HasPrefix(sources[1].Path, "charts/") {
		t.Errorf("expected chart source still at charts/{{template}}, got %q", sources[1].Path)
	}
}

// TestBuildAppKustomizationYAML_IsValidKustomize verifies the generated
// YAML matches the kustomize.config.k8s.io/v1beta1 shape kustomize and
// ArgoCD expect — apiVersion + kind + resources list.
func TestBuildAppKustomizationYAML_IsValidKustomize(t *testing.T) {
	got := gitops.BuildAppKustomizationYAML([]string{"env-configmap.yaml", "external-secret.yaml"})
	for _, want := range []string{
		"apiVersion: kustomize.config.k8s.io/v1beta1",
		"kind: Kustomization",
		"resources:",
		"- env-configmap.yaml",
		"- external-secret.yaml",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// Compile-time check that a couple of imports we depend on still exist
// — guards against a future refactor accidentally renaming the symbols
// these tests depend on.
var (
	_ = secrets.ResourceNaming{}
	_ = domain.AppEnvStaging
)
