package runtime

import (
	"context"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"testing"
)

func TestPreviewScopeExcludesOtherReleases(t *testing.T) {
	client := fake.NewSimpleClientset()
	ctx := context.Background()
	for _, preview := range []string{"pr-724", "pr-893", "pr-8930"} {
		meta := metav1.ObjectMeta{Name: "web-" + preview, Namespace: "previews", Labels: map[string]string{instanceLabel: "web"}, Annotations: map[string]string{"argocd.argoproj.io/tracking-id": "app-" + preview + ":apps/Deployment:previews/web-" + preview}}
		replicas := int32(2)
		_, err := client.AppsV1().Deployments("previews").Create(ctx, &appsv1.Deployment{ObjectMeta: meta, Spec: appsv1.DeploymentSpec{Replicas: &replicas, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web", Image: "app:" + preview}}}}}, Status: appsv1.DeploymentStatus{Replicas: 2, ReadyReplicas: 2, AvailableReplicas: 2, UpdatedReplicas: 2}}, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.NetworkingV1().Ingresses("previews").Create(ctx, &networkingv1.Ingress{ObjectMeta: meta, Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: preview + ".example.com"}}}}, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
	}
	provider := NewK8sProvider(client, nil)
	scoped := provider.WithApplicationScope("app-pr-893")
	info, err := scoped.GetAppRuntime(ctx, "previews", "web", "web-pr-724")
	if err != nil {
		t.Fatal(err)
	}
	if info.Replicas != 2 || info.Image != "app:pr-893" || len(info.IngressURLs) != 1 || info.IngressURLs[0] != "https://pr-893.example.com" {
		t.Fatalf("preview leaked: %+v", info)
	}
	missing, err := provider.WithApplicationScope("app-pr-999").GetAppRuntime(ctx, "previews", "web", "web-pr-724")
	if err != nil || missing.Status != StatusNotDeployed || missing.Image != "" {
		t.Fatalf("unscoped fallback leaked: %+v %v", missing, err)
	}
	all, err := provider.GetAppRuntime(ctx, "previews", "web", "web")
	if err != nil || all.Replicas != 6 {
		t.Fatalf("stable reader changed: %+v %v", all, err)
	}
}
