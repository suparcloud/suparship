package kube

import (
	"context"
	"encoding/json"
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

func TestRegisterWithArgoCD_LinksToPreExistingExternalSecret(t *testing.T) {
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
	kubeconfig := minimalKubeconfig(apiServer)

	owned, err := store.registerWithArgoCD(context.Background(), cluster, kubeconfig)
	if err != nil {
		t.Fatalf("expected no error when linking to external ArgoCD secret, got: %v", err)
	}
	if owned {
		t.Error("expected owned=false when linking to pre-existing ArgoCD secret")
	}

	// The external secret must NOT have been overwritten.
	existing, _ := client.CoreV1().Secrets(argoCDNS).Get(context.Background(), secretName, metav1.GetOptions{})
	if existing.StringData["name"] == "my-cluster" {
		t.Error("external secret was overwritten — name should still be external-cluster")
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

	owned, err := store.registerWithArgoCD(context.Background(), cluster, minimalKubeconfig(apiServer))
	if err != nil {
		t.Fatalf("expected no error when updating owned secret, got: %v", err)
	}
	if !owned {
		t.Error("expected owned=true when updating suparship-owned ArgoCD secret")
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

// minimalKubeconfig returns a minimal but parseable kubeconfig for tests.
func minimalKubeconfig(apiServer string) []byte {
	return []byte(`apiVersion: v1
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
}

// ── CreateCluster with pre-existing ArgoCD secret ────────────────────────────

func TestCreateCluster_LinksPreExistingArgoCDSecret(t *testing.T) {
	apiServer := "https://10.0.0.5:6443"
	clusterName := "linked-cluster"
	secretName := argoCDClusterSecretName(apiServer)

	// Pre-existing ArgoCD secret without suparship ownership label.
	preExisting := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: argoCDNS,
			Labels:    map[string]string{argoCDSecretType: argoCDCluster},
		},
		StringData: map[string]string{
			"name":   "pre-registered",
			"server": apiServer,
			"config": "{}",
		},
	}

	client := newTestClient(preExisting)
	store := NewK8sClusterStore(client)

	err := store.CreateCluster(context.Background(), domain.Cluster{
		Name: clusterName, APIServer: apiServer, Status: "ready",
	}, minimalKubeconfig(apiServer))
	if err != nil {
		t.Fatalf("CreateCluster failed: %v", err)
	}

	// The cluster ConfigMap must exist and ArgoCDOwned must be false.
	cm, err := client.CoreV1().ConfigMaps(suparshipSystemNS).Get(
		context.Background(), clusterConfigMapPrefix+clusterName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("cluster ConfigMap not found: %v", err)
	}
	var stored domain.Cluster
	if err := json.Unmarshal([]byte(cm.Data[clusterConfigMapKey]), &stored); err != nil {
		t.Fatalf("unmarshal cluster: %v", err)
	}
	if stored.ArgoCDOwned {
		t.Error("ArgoCDOwned should be false when linking to a pre-existing ArgoCD secret")
	}

	// The pre-existing ArgoCD secret must not have been modified.
	existing, _ := client.CoreV1().Secrets(argoCDNS).Get(
		context.Background(), secretName, metav1.GetOptions{})
	if existing.StringData["name"] == clusterName {
		t.Error("pre-existing ArgoCD secret was overwritten")
	}
}

func TestCreateCluster_SetsArgoCDOwnedTrue_WhenSecretIsNew(t *testing.T) {
	apiServer := "https://10.0.0.6:6443"
	clusterName := "fresh-cluster"

	client := newTestClient()
	store := NewK8sClusterStore(client)

	err := store.CreateCluster(context.Background(), domain.Cluster{
		Name: clusterName, APIServer: apiServer, Status: "ready",
	}, minimalKubeconfig(apiServer))
	if err != nil {
		t.Fatalf("CreateCluster failed: %v", err)
	}

	cm, err := client.CoreV1().ConfigMaps(suparshipSystemNS).Get(
		context.Background(), clusterConfigMapPrefix+clusterName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("cluster ConfigMap not found: %v", err)
	}
	var stored domain.Cluster
	if err := json.Unmarshal([]byte(cm.Data[clusterConfigMapKey]), &stored); err != nil {
		t.Fatalf("unmarshal cluster: %v", err)
	}
	if !stored.ArgoCDOwned {
		t.Error("ArgoCDOwned should be true when suparship created the ArgoCD secret")
	}
}

// ── DeleteCluster with ArgoCDOwned flag ──────────────────────────────────────

func TestDeleteCluster_PreserversPreExistingArgoCDSecret_ViaFlag(t *testing.T) {
	apiServer := "https://10.0.0.7:6443"
	clusterName := "linked-del"
	secretName := argoCDClusterSecretName(apiServer)

	// Cluster metadata with ArgoCDOwned=false (linked, not owned).
	clusterData, _ := json.Marshal(domain.Cluster{
		Name: clusterName, APIServer: apiServer, Status: "ready", ArgoCDOwned: false,
	})
	// The ArgoCD secret has no suparship label (simulating pre-existing).
	preExistingArgoSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: argoCDNS,
			Labels:    map[string]string{argoCDSecretType: argoCDCluster},
		},
		StringData: map[string]string{"name": "external", "server": apiServer},
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
		Data: map[string]string{clusterConfigMapKey: string(clusterData)},
	}

	client := newTestClient(preExistingArgoSecret, clusterCM)
	store := NewK8sClusterStore(client)

	if err := store.DeleteCluster(context.Background(), clusterName); err != nil {
		t.Fatalf("DeleteCluster failed: %v", err)
	}

	// Pre-existing ArgoCD secret must survive.
	if _, err := client.CoreV1().Secrets(argoCDNS).Get(
		context.Background(), secretName, metav1.GetOptions{}); err != nil {
		t.Errorf("pre-existing ArgoCD secret was deleted: %v", err)
	}

	// Suparship ConfigMap must be gone.
	if _, err := client.CoreV1().ConfigMaps(suparshipSystemNS).Get(
		context.Background(), clusterConfigMapPrefix+clusterName, metav1.GetOptions{}); err == nil {
		t.Error("cluster ConfigMap should have been deleted")
	}
}

func TestDeleteCluster_DeletesOwnedArgoCDSecret_ViaFlag(t *testing.T) {
	apiServer := "https://10.0.0.8:6443"
	clusterName := "owned-del"
	secretName := argoCDClusterSecretName(apiServer)

	// Cluster metadata with ArgoCDOwned=true.
	clusterData, _ := json.Marshal(domain.Cluster{
		Name: clusterName, APIServer: apiServer, Status: "ready", ArgoCDOwned: true,
	})
	ownedArgoSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: argoCDNS,
			Labels: map[string]string{
				argoCDSecretType: argoCDCluster,
				labelManagedBy:   "suparship",
				labelCluster:     clusterName,
			},
		},
		StringData: map[string]string{"name": clusterName, "server": apiServer},
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
		Data: map[string]string{clusterConfigMapKey: string(clusterData)},
	}

	client := newTestClient(ownedArgoSecret, clusterCM)
	store := NewK8sClusterStore(client)

	if err := store.DeleteCluster(context.Background(), clusterName); err != nil {
		t.Fatalf("DeleteCluster failed: %v", err)
	}

	// Owned ArgoCD secret must be gone.
	if _, err := client.CoreV1().Secrets(argoCDNS).Get(
		context.Background(), secretName, metav1.GetOptions{}); err == nil {
		t.Error("owned ArgoCD secret should have been deleted")
	}
}

func TestUpdateClusterMetadata_RoutingRoundTrip(t *testing.T) {
	// Seed an existing cluster ConfigMap (skip CreateCluster's kubeconfig/argocd
	// machinery — we only exercise the metadata rewrite path).
	seed, _ := json.Marshal(domain.Cluster{
		Name: "prod", DisplayName: "Prod", APIServer: "https://prod:6443", Status: "ready",
	})
	client := newTestClient(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: clusterConfigMapPrefix + "prod", Namespace: suparshipSystemNS},
		Data:       map[string]string{clusterConfigMapKey: string(seed)},
	})
	store := NewK8sClusterStore(client)
	ctx := context.Background()

	updated, err := store.UpdateClusterMetadata(ctx, "prod", "Production", "aws.example.com", "",
		domain.RoutingProfiles{"external": {IngressClassName: "alb", ClusterIssuer: "le-aws"}})
	if err != nil {
		t.Fatalf("UpdateClusterMetadata: %v", err)
	}
	if updated.BaseDomain != "aws.example.com" || updated.RoutingProfiles["external"].IngressClassName != "alb" {
		t.Errorf("returned cluster missing routing: %+v", updated)
	}

	// Re-read: routing persisted, APIServer untouched.
	got, err := store.GetCluster(ctx, "prod")
	if err != nil {
		t.Fatalf("GetCluster: %v", err)
	}
	if got.BaseDomain != "aws.example.com" {
		t.Errorf("baseDomain = %q, want aws.example.com", got.BaseDomain)
	}
	if got.RoutingProfiles["external"].ClusterIssuer != "le-aws" {
		t.Errorf("issuer not persisted: %+v", got.RoutingProfiles)
	}
	if got.APIServer != "https://prod:6443" {
		t.Errorf("APIServer must be untouched, got %q", got.APIServer)
	}
	if got.DisplayName != "Production" {
		t.Errorf("displayName = %q, want Production", got.DisplayName)
	}
}
