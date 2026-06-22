package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestEnsureNamespaceOwned_CreatesWithLabels(t *testing.T) {
	client := fake.NewSimpleClientset()
	labels := map[string]string{"app.kubernetes.io/managed-by": "suparship", "suparship.io/project": "demo"}

	created, err := EnsureNamespaceOwned(context.Background(), client, "demo-ns", labels)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !created {
		t.Fatal("expected created=true for a new namespace")
	}
	ns, err := client.CoreV1().Namespaces().Get(context.Background(), "demo-ns", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("namespace not created: %v", err)
	}
	if ns.Labels["suparship.io/project"] != "demo" || ns.Labels["app.kubernetes.io/managed-by"] != "suparship" {
		t.Errorf("ownership labels missing: %+v", ns.Labels)
	}
}

func TestEnsureNamespaceOwned_AdoptsWithoutMutating(t *testing.T) {
	// A pre-existing namespace (created by someone else) carries no labels.
	existing := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "preexisting"}}
	client := fake.NewSimpleClientset(existing)

	created, err := EnsureNamespaceOwned(context.Background(), client, "preexisting",
		map[string]string{"app.kubernetes.io/managed-by": "suparship", "suparship.io/project": "demo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if created {
		t.Fatal("expected created=false for an adopted namespace")
	}
	// Must NOT have stamped ownership labels onto the adopted namespace.
	ns, _ := client.CoreV1().Namespaces().Get(context.Background(), "preexisting", metav1.GetOptions{})
	if len(ns.Labels) != 0 {
		t.Errorf("adopted namespace must not be mutated, got labels %+v", ns.Labels)
	}
}

func TestEnsureKargoProjectNamespace_CreatesLabeled(t *testing.T) {
	client := fake.NewSimpleClientset()

	if err := EnsureKargoProjectNamespace(context.Background(), client, "suparship-kargo"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ns, err := client.CoreV1().Namespaces().Get(context.Background(), "suparship-kargo", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("namespace not created: %v", err)
	}
	if ns.Labels[KargoProjectNamespaceLabel] != "true" {
		t.Errorf("missing kargo project label, got %+v", ns.Labels)
	}
}

func TestEnsureKargoProjectNamespace_LabelsPreexistingUnlabeled(t *testing.T) {
	// Reproduces the bug: the shared namespace was created earlier WITHOUT the
	// Kargo project label, so Kargo refused to adopt it as a Project.
	existing := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "suparship-kargo"}}
	client := fake.NewSimpleClientset(existing)

	if err := EnsureKargoProjectNamespace(context.Background(), client, "suparship-kargo"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ns, _ := client.CoreV1().Namespaces().Get(context.Background(), "suparship-kargo", metav1.GetOptions{})
	if ns.Labels[KargoProjectNamespaceLabel] != "true" {
		t.Errorf("pre-existing namespace was not labeled, got %+v", ns.Labels)
	}
}

func TestDeleteOwnedNamespaces_OnlyMatchingLabels(t *testing.T) {
	owned1 := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   "demo-app-staging",
		Labels: map[string]string{"app.kubernetes.io/managed-by": "suparship", "suparship.io/project": "demo"},
	}}
	owned2 := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   "demo",
		Labels: map[string]string{"app.kubernetes.io/managed-by": "suparship", "suparship.io/project": "demo"},
	}}
	adopted := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "adopted"}}        // no labels
	otherProj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "other", Labels: map[string]string{ // different project
		"app.kubernetes.io/managed-by": "suparship", "suparship.io/project": "other",
	}}}
	client := fake.NewSimpleClientset(owned1, owned2, adopted, otherProj)

	deleted, err := DeleteOwnedNamespaces(context.Background(), client,
		"app.kubernetes.io/managed-by=suparship,suparship.io/project=demo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deleted) != 2 {
		t.Fatalf("expected 2 owned namespaces deleted, got %v", deleted)
	}
	// The adopted and other-project namespaces survive.
	for _, keep := range []string{"adopted", "other"} {
		if _, err := client.CoreV1().Namespaces().Get(context.Background(), keep, metav1.GetOptions{}); err != nil {
			t.Errorf("namespace %q must not be deleted: %v", keep, err)
		}
	}
}

func TestDeleteNamespaceIfOwned(t *testing.T) {
	owned := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   "proj-web-staging",
		Labels: map[string]string{"app.kubernetes.io/managed-by": "suparship", "suparship.io/project": "proj"},
	}}
	adopted := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "proj-legacy-staging"}} // no labels
	client := fake.NewSimpleClientset(owned, adopted)
	want := map[string]string{"app.kubernetes.io/managed-by": "suparship", "suparship.io/project": "proj"}

	// Owned → deleted.
	if deleted, err := DeleteNamespaceIfOwned(context.Background(), client, "proj-web-staging", want); err != nil || !deleted {
		t.Fatalf("owned namespace: deleted=%v err=%v, want deleted=true", deleted, err)
	}
	if _, err := client.CoreV1().Namespaces().Get(context.Background(), "proj-web-staging", metav1.GetOptions{}); err == nil {
		t.Error("expected owned namespace removed")
	}
	// Not owned (no labels) → skipped.
	if deleted, err := DeleteNamespaceIfOwned(context.Background(), client, "proj-legacy-staging", want); err != nil || deleted {
		t.Errorf("adopted namespace: deleted=%v err=%v, want deleted=false", deleted, err)
	}
	if _, err := client.CoreV1().Namespaces().Get(context.Background(), "proj-legacy-staging", metav1.GetOptions{}); err != nil {
		t.Error("adopted namespace must survive")
	}
	// Missing → no error, not deleted.
	if deleted, err := DeleteNamespaceIfOwned(context.Background(), client, "nope", want); err != nil || deleted {
		t.Errorf("missing namespace: deleted=%v err=%v, want false/nil", deleted, err)
	}
}
