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
