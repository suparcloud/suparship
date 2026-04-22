package kube_test

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/suparcloud/suparship/internal/kube"
)

// applicationGVR matches the GVR used internally by EnsureRootArgoApp.
var applicationGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "applications",
}

// newFakeDynClientForApps creates a fake dynamic client that knows about the
// Application GVR (required for fake.NewSimpleDynamicClientWithCustomListKinds).
func newFakeDynClientForApps(objects ...runtime.Object) *fake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	return fake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			applicationGVR: "ApplicationList",
		},
		objects...,
	)
}

func TestEnsureRootArgoApp_CreatesWhenAbsent(t *testing.T) {
	dyn := newFakeDynClientForApps()
	ctx := context.Background()

	if err := kube.EnsureRootArgoApp(ctx, dyn, "http://gitea.cluster.local/gitops/gitops.git", "main", "argocd"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	obj, err := dyn.Resource(applicationGVR).Namespace("argocd").
		Get(ctx, "suparship-apps", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected Application to exist after ensure, got error: %v", err)
	}
	if obj.GetName() != "suparship-apps" {
		t.Errorf("unexpected name %q", obj.GetName())
	}

	// Verify source repoURL is wired from the parameter.
	spec, _, _ := unstructuredNestedMap(obj.Object, "spec")
	source, _, _ := unstructuredNestedMap(spec, "source")
	if source["repoURL"] != "http://gitea.cluster.local/gitops/gitops.git" {
		t.Errorf("unexpected repoURL %v", source["repoURL"])
	}
	if source["targetRevision"] != "main" {
		t.Errorf("unexpected targetRevision %v", source["targetRevision"])
	}
}

func TestEnsureRootArgoApp_IdempotentWhenAlreadyExists(t *testing.T) {
	dyn := newFakeDynClientForApps()
	ctx := context.Background()

	if err := kube.EnsureRootArgoApp(ctx, dyn, "http://gitea/gitops.git", "main", "argocd"); err != nil {
		t.Fatalf("first call: %v", err)
	}
	// Second call must be a no-op — not return an error.
	if err := kube.EnsureRootArgoApp(ctx, dyn, "http://gitea/gitops.git", "main", "argocd"); err != nil {
		t.Fatalf("second call (idempotent): %v", err)
	}
}

func TestEnsureRootArgoApp_NilClientIsNoop(t *testing.T) {
	if err := kube.EnsureRootArgoApp(context.Background(), nil, "http://gitea/gitops.git", "main", "argocd"); err != nil {
		t.Fatalf("expected nil error for nil client, got: %v", err)
	}
}

func TestEnsureRootArgoApp_EmptyRepoURLIsNoop(t *testing.T) {
	dyn := newFakeDynClientForApps()
	if err := kube.EnsureRootArgoApp(context.Background(), dyn, "", "main", "argocd"); err != nil {
		t.Fatalf("expected nil error for empty repoURL, got: %v", err)
	}
	// Nothing should have been created.
	_, err := dyn.Resource(applicationGVR).Namespace("argocd").
		Get(context.Background(), "suparship-apps", metav1.GetOptions{})
	if err == nil {
		t.Error("expected Application to NOT exist when repoURL is empty")
	}
}

func TestEnsureRootArgoApp_ArgoCDNotInstalled(t *testing.T) {
	dyn := newFakeDynClientForApps()
	notFoundErr := apierrors.NewNotFound(applicationGVR.GroupResource(), "suparship-apps")

	dyn.Fake.PrependReactor("get", "applications", func(_ k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, notFoundErr
	})
	dyn.Fake.PrependReactor("create", "applications", func(_ k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, notFoundErr
	})

	// Non-fatal: should log a warning and return nil.
	if err := kube.EnsureRootArgoApp(context.Background(), dyn, "http://gitea/gitops.git", "main", "argocd"); err != nil {
		t.Fatalf("expected non-fatal nil when ArgoCD CRD absent, got: %v", err)
	}
}

func TestRootArgoAppExists_TrueWhenPresent(t *testing.T) {
	dyn := newFakeDynClientForApps()
	ctx := context.Background()

	if err := kube.EnsureRootArgoApp(ctx, dyn, "http://gitea/gitops.git", "main", "argocd"); err != nil {
		t.Fatalf("setup: %v", err)
	}

	exists, err := kube.RootArgoAppExists(ctx, dyn, "argocd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Error("expected exists=true after creating the Application")
	}
}

func TestRootArgoAppExists_FalseWhenAbsent(t *testing.T) {
	dyn := newFakeDynClientForApps()

	exists, err := kube.RootArgoAppExists(context.Background(), dyn, "argocd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Error("expected exists=false when Application has not been created")
	}
}

func TestRootArgoAppExists_FalseForNilClient(t *testing.T) {
	exists, err := kube.RootArgoAppExists(context.Background(), nil, "argocd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Error("expected exists=false for nil dynamic client")
	}
}

// unstructuredNestedMap is a small helper to navigate nested map[string]interface{}
// without importing a full helper library.
func unstructuredNestedMap(obj map[string]interface{}, key string) (map[string]interface{}, bool, error) {
	v, ok := obj[key]
	if !ok {
		return nil, false, nil
	}
	m, ok := v.(map[string]interface{})
	return m, ok, nil
}
