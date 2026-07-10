package kube

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func newStuckReader(t *testing.T, objs ...*unstructured.Unstructured) *ArgoCDStatusReader {
	t.Helper()
	scheme := k8sruntime.NewScheme()
	gvrMap := map[schema.GroupVersionResource]string{
		argoCDAppGVR:  "ApplicationList",
		appProjectGVR: "AppProjectList",
	}
	ro := make([]k8sruntime.Object, len(objs))
	for i, o := range objs {
		ro[i] = o
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrMap, ro...)
	return NewArgoCDStatusReaderFromDynamic(dyn, "argocd")
}

// appCR builds a suparship-managed Application (it carries the suparship.io/project
// ownership label, as every ApplicationSet-generated Application does).
func appCR(name, project string, terminating bool, finalizers []string) *unstructured.Unstructured {
	app := foreignAppCR(name, project, terminating, finalizers)
	app.SetLabels(map[string]string{"suparship.io/project": project, "suparship.io/app": name})
	return app
}

// foreignAppCR builds an Application with NO suparship.io/* ownership label —
// the kind another tool (the platform team's own ArgoCD app-of-apps) puts in
// the shared ArgoCD namespace.
func foreignAppCR(name, project string, terminating bool, finalizers []string) *unstructured.Unstructured {
	meta := map[string]any{"name": name, "namespace": "argocd"}
	if terminating {
		meta["deletionTimestamp"] = time.Now().UTC().Format(time.RFC3339)
	}
	if len(finalizers) > 0 {
		fz := make([]any, len(finalizers))
		for i, f := range finalizers {
			fz[i] = f
		}
		meta["finalizers"] = fz
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   meta,
		"spec":       map[string]any{"project": project},
	}}
}

func appProjectCR(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "AppProject",
		"metadata":   map[string]any{"name": name, "namespace": "argocd"},
	}}
}

func TestListStuckApplications(t *testing.T) {
	r := newStuckReader(t,
		// healthy, not terminating → ignored
		appCR("healthy", "test", false, []string{argoResourcesFinalizer}),
		// terminating but no finalizer → not stuck (will GC normally)
		appCR("deleting-clean", "test", true, nil),
		// terminating + finalizer, project missing → stuck w/ project reason
		appCR("stuck-missing-proj", "gone", true, []string{argoResourcesFinalizer}),
		// terminating + finalizer, project exists → stuck, generic reason
		appCR("stuck-proj-ok", "test", true, []string{argoResourcesFinalizer}),
		// foreign app (no suparship.io/* label) stuck Terminating → ignored,
		// it belongs to the platform team's own ArgoCD, not suparship
		foreignAppCR("cert-manager-staging", "infra", true, []string{argoResourcesFinalizer}),
		appProjectCR("test"),
	)

	stuck, err := r.ListStuckApplications(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := map[string]StuckApplication{}
	for _, s := range stuck {
		got[s.Name] = s
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 stuck apps, got %d: %v", len(got), got)
	}
	if _, ok := got["healthy"]; ok {
		t.Error("non-terminating app reported as stuck")
	}
	if _, ok := got["deleting-clean"]; ok {
		t.Error("terminating app with no finalizer reported as stuck")
	}
	if s, ok := got["stuck-missing-proj"]; !ok || s.Project != "gone" {
		t.Errorf("missing-project app not detected: %+v", s)
	} else if !strings.Contains(s.Reason, "no longer exists") {
		t.Errorf("missing-project reason should mention the missing AppProject, got %q", s.Reason)
	}
	if _, ok := got["stuck-proj-ok"]; !ok {
		t.Error("terminating app with finalizer + existing project should still be stuck")
	}
	if _, ok := got["cert-manager-staging"]; ok {
		t.Error("foreign Application (no suparship.io/* label) must not be reported as stuck")
	}
}

func TestUnstickApplication(t *testing.T) {
	r := newStuckReader(t,
		appCR("stuck", "gone", true, []string{argoResourcesFinalizer, "keep.me/other"}),
	)
	if err := r.UnstickApplication(context.Background(), "stuck"); err != nil {
		t.Fatalf("unstick: %v", err)
	}
	got, err := r.dynamic.Resource(argoCDAppGVR).Namespace("argocd").Get(context.Background(), "stuck", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get after unstick: %v", err)
	}
	fz := got.GetFinalizers()
	if len(fz) != 1 || fz[0] != "keep.me/other" {
		t.Errorf("expected only the non-argocd finalizer to remain, got %v", fz)
	}
}

func TestUnstickApplication_RefusesLiveApp(t *testing.T) {
	r := newStuckReader(t,
		appCR("live", "test", false, []string{argoResourcesFinalizer}),
	)
	if err := r.UnstickApplication(context.Background(), "live"); err == nil {
		t.Error("expected refusal to strip finalizers from a non-terminating app")
	}
}

func TestUnstickApplication_RefusesForeignApp(t *testing.T) {
	r := newStuckReader(t,
		// foreign app, terminating + finalizer — would otherwise be unstuck,
		// but it carries no suparship.io/* label so we refuse to touch it
		foreignAppCR("cert-manager-staging", "infra", true, []string{argoResourcesFinalizer}),
	)
	if err := r.UnstickApplication(context.Background(), "cert-manager-staging"); err == nil {
		t.Error("expected refusal to modify finalizers on a non-suparship Application")
	}
}
