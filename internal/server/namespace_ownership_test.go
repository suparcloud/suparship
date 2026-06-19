package server

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/suparcloud/suparship/internal/rbac"
)

// deleteOwnedProjectNamespaces removes namespaces suparship created for the
// project (ownership-labelled) and leaves adopted/external ones in place. With
// an unbound env it operates on the local kubeClient (single-cluster).
func TestDeleteOwnedProjectNamespaces(t *testing.T) {
	owned := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: "demo-api-staging",
		Labels: map[string]string{
			"app.kubernetes.io/managed-by": "suparship",
			"suparship.io/project":         "demo",
		},
	}}
	adopted := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "demo"}} // pre-existing, no labels
	client := fake.NewSimpleClientset(owned, adopted)

	ah := &appHandler{
		kubeClient: client,
		orgProvider: &staticOrgProvider{org: &rbac.Org{
			// default branding → managed-by=suparship, domain=suparship.io
			Environments: []rbac.OrgEnvironment{{Name: "staging"}}, // unbound → local cluster
		}},
	}

	ah.deleteOwnedProjectNamespaces(context.Background(), "demo")

	if _, err := client.CoreV1().Namespaces().Get(context.Background(), "demo-api-staging", metav1.GetOptions{}); err == nil {
		t.Error("expected suparship-owned namespace to be deleted")
	}
	if _, err := client.CoreV1().Namespaces().Get(context.Background(), "demo", metav1.GetOptions{}); err != nil {
		t.Errorf("adopted namespace must survive teardown: %v", err)
	}
}
