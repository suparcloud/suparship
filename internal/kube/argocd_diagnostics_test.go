package kube

import (
	"context"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/suparcloud/suparship/internal/domain"
)

func newDiagReader(t *testing.T, apps ...*unstructured.Unstructured) *ArgoCDStatusReader {
	t.Helper()
	scheme := k8sruntime.NewScheme()
	gvrMap := map[schema.GroupVersionResource]string{
		argoCDAppGVR: "ApplicationList",
	}
	objs := make([]k8sruntime.Object, len(apps))
	for i, a := range apps {
		objs[i] = a
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrMap, objs...)
	return NewArgoCDStatusReaderFromDynamic(dyn, "argocd")
}

func argoApp(name string, status map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]any{"name": name, "namespace": "argocd"},
		"status":     status,
	}}
}

func TestGetAppDiagnostics_AbsentAppIsEmpty(t *testing.T) {
	r := newDiagReader(t)
	diags, err := r.GetAppDiagnostics(context.Background(), "missing-staging", "argocd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("expected no diagnostics for absent app, got %d", len(diags))
	}
}

func TestGetAppDiagnostics_NonNotFoundErrorSurfaces(t *testing.T) {
	// A real read error (RBAC denial, throttling, API down) must NOT be
	// swallowed as "no diagnostics" — that would mask a broken env as healthy.
	scheme := k8sruntime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		scheme, map[schema.GroupVersionResource]string{argoCDAppGVR: "ApplicationList"})
	dyn.PrependReactor("get", "applications", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Group: "argoproj.io", Resource: "applications"}, "web-staging",
			errForbidden{})
	})
	r := NewArgoCDStatusReaderFromDynamic(dyn, "argocd")
	if _, err := r.GetAppDiagnostics(context.Background(), "web-staging", "argocd"); err == nil {
		t.Fatal("expected a non-NotFound Get error to be returned, not swallowed")
	}
}

type errForbidden struct{}

func (errForbidden) Error() string { return "forbidden" }

func TestGetAppDiagnostics_HealthyAppIsEmpty(t *testing.T) {
	app := argoApp("web-staging", map[string]any{
		"sync":   map[string]any{"status": "Synced"},
		"health": map[string]any{"status": "Healthy"},
	})
	r := newDiagReader(t, app)
	diags, _ := r.GetAppDiagnostics(context.Background(), "web-staging", "argocd")
	if len(diags) != 0 {
		t.Fatalf("expected no diagnostics for healthy app, got %+v", diags)
	}
}

func TestGetAppDiagnostics_ConditionError(t *testing.T) {
	app := argoApp("web-staging", map[string]any{
		"conditions": []any{
			map[string]any{
				"type":    "InvalidSpecError",
				"message": "application destination server '...' and namespace 'test' do not match any of the allowed destinations in project 'test'",
			},
		},
	})
	r := newDiagReader(t, app)
	diags, _ := r.GetAppDiagnostics(context.Background(), "web-staging", "argocd")
	if len(diags) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d: %+v", len(diags), diags)
	}
	d := diags[0]
	if d.Level != domain.DiagnosticError {
		t.Errorf("expected error level, got %q", d.Level)
	}
	if d.Source != "argocd" {
		t.Errorf("expected source argocd, got %q", d.Source)
	}
	if !strings.Contains(d.Hint, "allowed destinations") {
		t.Errorf("expected destination hint, got %q", d.Hint)
	}
}

func TestGetAppDiagnostics_DegradedHealthMessageWithESOHint(t *testing.T) {
	app := argoApp("web-staging-platform", map[string]any{
		"health": map[string]any{
			"status":  "Degraded",
			"message": `error processing spec.dataFrom[0].extract, err: ClusterSecretStore "suparship-store" is not ready`,
		},
	})
	r := newDiagReader(t, app)
	diags, _ := r.GetAppDiagnostics(context.Background(), "web-staging-platform", "external-secrets")
	if len(diags) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d: %+v", len(diags), diags)
	}
	if !strings.Contains(diags[0].Hint, "Connect Server URL") {
		t.Errorf("expected Connect URL hint for ESO not-ready, got %q", diags[0].Hint)
	}
}

func TestGetAppDiagnostics_FailedSyncOperation(t *testing.T) {
	app := argoApp("web-staging", map[string]any{
		"operationState": map[string]any{
			"phase":   "Failed",
			"message": "one or more objects failed to apply",
		},
	})
	r := newDiagReader(t, app)
	diags, _ := r.GetAppDiagnostics(context.Background(), "web-staging", "argocd")
	if len(diags) != 1 || diags[0].Level != domain.DiagnosticError {
		t.Fatalf("expected 1 error diagnostic for failed sync, got %+v", diags)
	}
}

// Progressing health with a message must NOT be reported as a problem.
func TestGetAppDiagnostics_ProgressingIsNotADiagnostic(t *testing.T) {
	app := argoApp("web-staging", map[string]any{
		"health": map[string]any{"status": "Progressing", "message": "waiting for rollout"},
	})
	r := newDiagReader(t, app)
	diags, _ := r.GetAppDiagnostics(context.Background(), "web-staging", "argocd")
	if len(diags) != 0 {
		t.Fatalf("expected no diagnostics while progressing, got %+v", diags)
	}
}
