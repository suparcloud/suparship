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

// appProjectGVR matches the GVR used internally by EnsureArgoCDSystemProject.
var appProjectGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "appprojects",
}

// newFakeDynClient creates a fake dynamic client that knows about the
// AppProject GVR (required for fake.NewSimpleDynamicClient to route calls).
func newFakeDynClient(objects ...runtime.Object) *fake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	return fake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			appProjectGVR: "AppProjectList",
		},
		objects...,
	)
}

func TestEnsureArgoCDSystemProject_CreatesWhenAbsent(t *testing.T) {
	dyn := newFakeDynClient()
	ctx := context.Background()

	if err := kube.EnsureArgoCDSystemProject(ctx, dyn, "argocd"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the AppProject was created.
	obj, err := dyn.Resource(appProjectGVR).Namespace("argocd").
		Get(ctx, "suparship-system", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected AppProject to exist after ensure, got error: %v", err)
	}
	if obj.GetName() != "suparship-system" {
		t.Errorf("unexpected name %q", obj.GetName())
	}
}

func TestEnsureArgoCDSystemProject_IdempotentWhenAlreadyExists(t *testing.T) {
	dyn := newFakeDynClient()
	ctx := context.Background()

	// First call — should create.
	if err := kube.EnsureArgoCDSystemProject(ctx, dyn, "argocd"); err != nil {
		t.Fatalf("first call: %v", err)
	}
	// Second call — should be a no-op.
	if err := kube.EnsureArgoCDSystemProject(ctx, dyn, "argocd"); err != nil {
		t.Fatalf("second call (idempotent): %v", err)
	}
}

func TestEnsureArgoCDSystemProject_NilClientIsNoop(t *testing.T) {
	// A nil dynamic client must not panic and must return nil (non-fatal).
	if err := kube.EnsureArgoCDSystemProject(context.Background(), nil, "argocd"); err != nil {
		t.Fatalf("expected nil error for nil client, got: %v", err)
	}
}

func TestEnsureArgoCDSystemProject_ArgoCDNotInstalled(t *testing.T) {
	// Simulate ArgoCD CRD not installed: both Get and Create return 404.
	dyn := newFakeDynClient()
	notFoundErr := apierrors.NewNotFound(appProjectGVR.GroupResource(), "suparship-system")

	dyn.Fake.PrependReactor("get", "appprojects", func(_ k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, notFoundErr
	})
	dyn.Fake.PrependReactor("create", "appprojects", func(_ k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, notFoundErr
	})

	// Should log a warning and return nil — non-fatal.
	if err := kube.EnsureArgoCDSystemProject(context.Background(), dyn, "argocd"); err != nil {
		t.Fatalf("expected non-fatal nil when ArgoCD CRD absent, got: %v", err)
	}
}

func TestArgoCDSystemProjectExists_TrueWhenPresent(t *testing.T) {
	dyn := newFakeDynClient()
	ctx := context.Background()

	// Create it first.
	if err := kube.EnsureArgoCDSystemProject(ctx, dyn, "argocd"); err != nil {
		t.Fatalf("setup: %v", err)
	}

	exists, err := kube.ArgoCDSystemProjectExists(ctx, dyn, "argocd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Error("expected exists=true after creating the AppProject")
	}
}

func TestArgoCDSystemProjectExists_FalseWhenAbsent(t *testing.T) {
	dyn := newFakeDynClient()
	ctx := context.Background()

	exists, err := kube.ArgoCDSystemProjectExists(ctx, dyn, "argocd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Error("expected exists=false when AppProject has not been created")
	}
}

func TestArgoCDSystemProjectExists_FalseForNilClient(t *testing.T) {
	exists, err := kube.ArgoCDSystemProjectExists(context.Background(), nil, "argocd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Error("expected exists=false for nil dynamic client")
	}
}
