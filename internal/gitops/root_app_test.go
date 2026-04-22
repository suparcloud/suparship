package gitops

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func newFakeDynClient(objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			argoCDAppGVR: "ApplicationList",
		},
		objects...,
	)
}

func TestEnsureRootApplication_CreatesWhenAbsent(t *testing.T) {
	dyn := newFakeDynClient()

	cfg := RootAppConfig{
		RepoURL: "https://github.com/org/gitops.git",
		Branch:  "main",
	}

	if err := EnsureRootApplication(context.Background(), dyn, cfg); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	app, err := dyn.Resource(argoCDAppGVR).Namespace(ArgoCDNamespace).Get(context.Background(), rootAppName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("root app should exist: %v", err)
	}

	labels := app.GetLabels()
	if labels[labelManagedBy] != "suparship" {
		t.Errorf("expected managed-by=suparship, got %q", labels[labelManagedBy])
	}
	if labels[labelRole] != "root-app" {
		t.Errorf("expected role=root-app, got %q", labels[labelRole])
	}

	// Verify the source repo URL.
	source, _, _ := unstructured.NestedString(app.Object, "spec", "source", "repoURL")
	if source != cfg.RepoURL {
		t.Errorf("expected repoURL=%q, got %q", cfg.RepoURL, source)
	}

	path, _, _ := unstructured.NestedString(app.Object, "spec", "source", "path")
	if path != rootAppInfraPath {
		t.Errorf("expected path=%q, got %q", rootAppInfraPath, path)
	}
}

func TestEnsureRootApplication_UsesArgoCDRepoURL(t *testing.T) {
	dyn := newFakeDynClient()

	cfg := RootAppConfig{
		RepoURL:       "https://github.com/org/gitops.git",
		ArgoCDRepoURL: "http://gitea-http.gitea.svc:3000/org/gitops.git",
		Branch:        "main",
	}

	if err := EnsureRootApplication(context.Background(), dyn, cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	app, _ := dyn.Resource(argoCDAppGVR).Namespace(ArgoCDNamespace).Get(context.Background(), rootAppName, metav1.GetOptions{})
	source, _, _ := unstructured.NestedString(app.Object, "spec", "source", "repoURL")
	if source != cfg.ArgoCDRepoURL {
		t.Errorf("expected argoCDRepoURL=%q, got %q", cfg.ArgoCDRepoURL, source)
	}
}

func TestEnsureRootApplication_UpdatesOwnedApp(t *testing.T) {
	existing := buildRootApp("https://old-url.git", "HEAD")
	dyn := newFakeDynClient(existing)

	cfg := RootAppConfig{
		RepoURL: "https://new-url.git",
		Branch:  "main",
	}

	if err := EnsureRootApplication(context.Background(), dyn, cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	app, _ := dyn.Resource(argoCDAppGVR).Namespace(ArgoCDNamespace).Get(context.Background(), rootAppName, metav1.GetOptions{})
	source, _, _ := unstructured.NestedString(app.Object, "spec", "source", "repoURL")
	if source != cfg.RepoURL {
		t.Errorf("expected repoURL=%q after update, got %q", cfg.RepoURL, source)
	}
}

func TestEnsureRootApplication_SkipsExternalApp(t *testing.T) {
	external := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Application",
			"metadata": map[string]interface{}{
				"name":      rootAppName,
				"namespace": ArgoCDNamespace,
			},
			"spec": map[string]interface{}{
				"source": map[string]interface{}{
					"repoURL": "https://external-managed.git",
				},
			},
		},
	}
	dyn := newFakeDynClient(external)

	cfg := RootAppConfig{
		RepoURL: "https://suparship-url.git",
		Branch:  "main",
	}

	if err := EnsureRootApplication(context.Background(), dyn, cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The external app should NOT be modified.
	app, _ := dyn.Resource(argoCDAppGVR).Namespace(ArgoCDNamespace).Get(context.Background(), rootAppName, metav1.GetOptions{})
	source, _, _ := unstructured.NestedString(app.Object, "spec", "source", "repoURL")
	if source != "https://external-managed.git" {
		t.Errorf("external app was overwritten: repoURL=%q", source)
	}

	labels := app.GetLabels()
	if labels[labelManagedBy] == "suparship" {
		t.Error("external app should not have suparship managed-by label")
	}
}

func TestEnsureRootApplication_ErrorOnEmptyURL(t *testing.T) {
	dyn := newFakeDynClient()

	err := EnsureRootApplication(context.Background(), dyn, RootAppConfig{})
	if err == nil {
		t.Fatal("expected error for empty URL, got nil")
	}
}
