package kube

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/suparcloud/suparship/internal/domain"
)

func newTestClient(objects ...metav1.Object) *fake.Clientset {
	runtimeObjects := make([]metav1.Object, 0, len(objects)+2)
	runtimeObjects = append(runtimeObjects,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: suparshipSystemNS}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: argoCDNS}},
	)
	runtimeObjects = append(runtimeObjects, objects...)
	objs := make([]interface{ GetObjectKind() interface{ GroupVersionKind() interface{} } }, 0)
	_ = objs
	// Use a simpler approach: create the clientset and pre-create objects
	client := fake.NewSimpleClientset()
	ctx := context.Background()
	for _, obj := range runtimeObjects {
		switch o := obj.(type) {
		case *corev1.Namespace:
			client.CoreV1().Namespaces().Create(ctx, o, metav1.CreateOptions{})
		case *corev1.Secret:
			client.CoreV1().Secrets(o.Namespace).Create(ctx, o, metav1.CreateOptions{})
		case *corev1.ConfigMap:
			client.CoreV1().ConfigMaps(o.Namespace).Create(ctx, o, metav1.CreateOptions{})
		}
	}
	return client
}

func TestIsOwnedBySuparship(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{"nil labels", nil, false},
		{"empty labels", map[string]string{}, false},
		{"wrong value", map[string]string{labelManagedBy: "helm"}, false},
		{"owned", map[string]string{labelManagedBy: "suparship"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isOwnedBySuparship(tt.labels); got != tt.want {
				t.Errorf("isOwnedBySuparship() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRegisterWithArgoCD_RefusesToOverwriteExternalSecret(t *testing.T) {
	apiServer := "https://10.0.0.1:6443"
	secretName := argoCDClusterSecretName(apiServer)

	externalSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: argoCDNS,
			Labels: map[string]string{
				argoCDSecretType: argoCDCluster,
			},
		},
		StringData: map[string]string{
			"name":   "external-cluster",
			"server": apiServer,
			"config": "{}",
		},
	}

	client := newTestClient(externalSecret)
	store := NewK8sClusterStore(client)

	cluster := domain.Cluster{
		Name:      "my-cluster",
		APIServer: apiServer,
		Status:    "ready",
	}
	// Minimal valid kubeconfig pointing at the same server.
	kubeconfig := []byte(`apiVersion: v1
kind: Config
clusters:
- cluster:
    server: ` + apiServer + `
    insecure-skip-tls-verify: true
  name: test
contexts:
- context:
    cluster: test
    user: test
  name: test
current-context: test
users:
- name: test
  user:
    token: fake-token
`)

	err := store.registerWithArgoCD(context.Background(), cluster, kubeconfig)
	if err == nil {
		t.Fatal("expected error when overwriting external ArgoCD secret, got nil")
	}

	// Verify the original secret was NOT overwritten.
	existing, _ := client.CoreV1().Secrets(argoCDNS).Get(context.Background(), secretName, metav1.GetOptions{})
	if existing.StringData["name"] == "my-cluster" {
		t.Error("external secret was overwritten — name changed to my-cluster")
	}
}

func TestRegisterWithArgoCD_UpdatesOwnedSecret(t *testing.T) {
	apiServer := "https://10.0.0.2:6443"
	secretName := argoCDClusterSecretName(apiServer)

	ownedSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: argoCDNS,
			Labels: map[string]string{
				argoCDSecretType: argoCDCluster,
				labelManagedBy:   "suparship",
				labelCluster:     "old-name",
			},
		},
		StringData: map[string]string{
			"name":   "old-name",
			"server": apiServer,
			"config": "{}",
		},
	}

	client := newTestClient(ownedSecret)
	store := NewK8sClusterStore(client)

	cluster := domain.Cluster{
		Name:      "new-name",
		APIServer: apiServer,
		Status:    "ready",
	}
	kubeconfig := []byte(`apiVersion: v1
kind: Config
clusters:
- cluster:
    server: ` + apiServer + `
    insecure-skip-tls-verify: true
  name: test
contexts:
- context:
    cluster: test
    user: test
  name: test
current-context: test
users:
- name: test
  user:
    token: fake-token
`)

	err := store.registerWithArgoCD(context.Background(), cluster, kubeconfig)
	if err != nil {
		t.Fatalf("expected no error when updating owned secret, got: %v", err)
	}
}

func TestDeleteCluster_SkipsExternalArgoCDSecret(t *testing.T) {
	apiServer := "https://10.0.0.3:6443"
	secretName := argoCDClusterSecretName(apiServer)
	clusterName := "test-cluster"

	clusterData := `{"name":"` + clusterName + `","apiServer":"` + apiServer + `","status":"ready"}`

	externalArgoCDSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: argoCDNS,
			Labels: map[string]string{
				argoCDSecretType: argoCDCluster,
			},
		},
		StringData: map[string]string{
			"name":   "external",
			"server": apiServer,
		},
	}

	clusterCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterConfigMapPrefix + clusterName,
			Namespace: suparshipSystemNS,
			Labels: map[string]string{
				labelManagedBy: "suparship",
				labelType:      "cluster",
				labelCluster:   clusterName,
			},
		},
		Data: map[string]string{
			clusterConfigMapKey: clusterData,
		},
	}

	client := newTestClient(externalArgoCDSecret, clusterCM)
	store := NewK8sClusterStore(client)

	err := store.DeleteCluster(context.Background(), clusterName)
	if err != nil {
		t.Fatalf("DeleteCluster failed: %v", err)
	}

	// The external ArgoCD secret must still exist.
	_, getErr := client.CoreV1().Secrets(argoCDNS).Get(context.Background(), secretName, metav1.GetOptions{})
	if getErr != nil {
		t.Errorf("external ArgoCD secret was deleted: %v", getErr)
	}
}

func TestDeleteCluster_RemovesOwnedArgoCDSecret(t *testing.T) {
	apiServer := "https://10.0.0.4:6443"
	secretName := argoCDClusterSecretName(apiServer)
	clusterName := "owned-cluster"

	clusterData := `{"name":"` + clusterName + `","apiServer":"` + apiServer + `","status":"ready"}`

	ownedArgoCDSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: argoCDNS,
			Labels: map[string]string{
				argoCDSecretType: argoCDCluster,
				labelManagedBy:   "suparship",
				labelCluster:     clusterName,
			},
		},
		StringData: map[string]string{
			"name":   clusterName,
			"server": apiServer,
		},
	}

	clusterCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterConfigMapPrefix + clusterName,
			Namespace: suparshipSystemNS,
			Labels: map[string]string{
				labelManagedBy: "suparship",
				labelType:      "cluster",
				labelCluster:   clusterName,
			},
		},
		Data: map[string]string{
			clusterConfigMapKey: clusterData,
		},
	}

	client := newTestClient(ownedArgoCDSecret, clusterCM)
	store := NewK8sClusterStore(client)

	err := store.DeleteCluster(context.Background(), clusterName)
	if err != nil {
		t.Fatalf("DeleteCluster failed: %v", err)
	}

	// The owned ArgoCD secret must be gone.
	_, getErr := client.CoreV1().Secrets(argoCDNS).Get(context.Background(), secretName, metav1.GetOptions{})
	if getErr == nil {
		t.Error("owned ArgoCD secret should have been deleted")
	}
}
