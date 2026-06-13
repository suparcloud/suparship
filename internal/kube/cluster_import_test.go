package kube

import (
	"context"
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/suparcloud/suparship/internal/domain"
)

// argoCDClusterSecret builds an ArgoCD cluster Secret with values in Data, the
// way ArgoCD itself stores them (the fake clientset does not merge StringData).
func argoCDClusterSecret(objName, dataName, server, config string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      objName,
			Namespace: argoCDNS,
			Labels:    map[string]string{argoCDSecretType: argoCDCluster},
		},
		Data: map[string][]byte{
			"name":   []byte(dataName),
			"server": []byte(server),
			"config": []byte(config),
		},
	}
}

func TestListArgoCDClusters_Classification(t *testing.T) {
	// A suparship cluster already targeting this server → AlreadyRegistered.
	registeredServer := "https://registered:6443"
	registeredCM, _ := json.Marshal(domain.Cluster{
		Name: "reg", APIServer: registeredServer, Status: "ready",
	})

	client := newTestClient(
		argoCDClusterSecret("cluster-1", "tok", "https://tok:6443",
			`{"bearerToken":"abc","tlsClientConfig":{"insecure":true}}`),
		argoCDClusterSecret("cluster-2", "eks", "https://eks:6443",
			`{"awsAuthConfig":{"clusterName":"prod"}}`),
		argoCDClusterSecret("cluster-3", "gke", "https://gke:6443",
			`{"execProviderConfig":{"command":"gke-gcloud-auth-plugin"}}`),
		argoCDClusterSecret("cluster-4", "reg-argo", registeredServer,
			`{"bearerToken":"xyz"}`),
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterConfigMapPrefix + "reg",
				Namespace: suparshipSystemNS,
				Labels:    map[string]string{labelType: "cluster"},
			},
			Data: map[string]string{clusterConfigMapKey: string(registeredCM)},
		},
	)
	store := NewK8sClusterStore(client)

	got, err := store.ListArgoCDClusters(context.Background())
	if err != nil {
		t.Fatalf("ListArgoCDClusters: %v", err)
	}
	byName := map[string]domain.ArgoCDClusterCandidate{}
	for _, c := range got {
		byName[c.Name] = c
	}

	if c := byName["tok"]; !c.Importable || c.AuthType != "token" {
		t.Errorf("token cluster: got importable=%v authType=%q, want true/token", c.Importable, c.AuthType)
	}
	if c := byName["eks"]; c.Importable || c.AuthType != "exec" || c.Reason == "" {
		t.Errorf("aws cluster: got importable=%v authType=%q reason=%q, want false/exec/reason", c.Importable, c.AuthType, c.Reason)
	}
	if c := byName["gke"]; c.Importable || c.AuthType != "exec" {
		t.Errorf("gke cluster: got importable=%v authType=%q, want false/exec", c.Importable, c.AuthType)
	}
	if c := byName["reg-argo"]; !c.AlreadyRegistered {
		t.Errorf("cluster on a registered server should be AlreadyRegistered, got %+v", c)
	}
}

func TestBuildKubeconfigFromArgoCD_RoundTrip(t *testing.T) {
	server := "https://api.example.com:6443"
	kc, err := buildKubeconfigFromArgoCD(server, argoCDClusterConfig{
		BearerToken:     "secret-token",
		TLSClientConfig: argoCDTLSConfig{Insecure: true},
	})
	if err != nil {
		t.Fatalf("buildKubeconfigFromArgoCD: %v", err)
	}
	rest, err := clientcmd.RESTConfigFromKubeConfig(kc)
	if err != nil {
		t.Fatalf("reconstructed kubeconfig does not parse: %v", err)
	}
	if rest.Host != server {
		t.Errorf("host = %q, want %q", rest.Host, server)
	}
	if rest.BearerToken != "secret-token" {
		t.Errorf("bearer token = %q, want secret-token", rest.BearerToken)
	}
	if !rest.TLSClientConfig.Insecure {
		t.Error("expected insecure-skip-tls-verify to round-trip")
	}
}

func TestBuildKubeconfigFromArgoCD_NoCreds(t *testing.T) {
	_, err := buildKubeconfigFromArgoCD("https://x:6443", argoCDClusterConfig{})
	if err == nil {
		t.Fatal("expected error when config has no credentials")
	}
}

func TestImportArgoCDCluster_TokenAuth(t *testing.T) {
	server := "https://import-me:6443"
	argoSecret := argoCDClusterSecret("cluster-import", "imported", server,
		`{"bearerToken":"tok","tlsClientConfig":{"insecure":true}}`)

	client := newTestClient(argoSecret)
	store := NewK8sClusterStore(client)
	ctx := context.Background()

	cluster, err := store.ImportArgoCDCluster(ctx, "imported")
	if err != nil {
		t.Fatalf("ImportArgoCDCluster: %v", err)
	}
	if cluster.APIServer != server {
		t.Errorf("APIServer = %q, want %q", cluster.APIServer, server)
	}
	if cluster.ArgoCDOwned {
		t.Error("imported cluster must have ArgoCDOwned=false (links the source secret)")
	}

	// Metadata ConfigMap stored with ArgoCDOwned=false.
	stored, err := store.GetCluster(ctx, "imported")
	if err != nil {
		t.Fatalf("GetCluster: %v", err)
	}
	if stored.ArgoCDOwned {
		t.Error("stored cluster must have ArgoCDOwned=false")
	}

	// Kubeconfig stored + usable.
	kc, err := store.GetKubeconfig(ctx, "imported")
	if err != nil {
		t.Fatalf("GetKubeconfig: %v", err)
	}
	if _, err := clientcmd.RESTConfigFromKubeConfig(kc); err != nil {
		t.Fatalf("stored kubeconfig does not parse: %v", err)
	}

	// No NEW ArgoCD secret was created (only the original one exists).
	secrets, _ := client.CoreV1().Secrets(argoCDNS).List(ctx, metav1.ListOptions{
		LabelSelector: argoCDSecretType + "=" + argoCDCluster,
	})
	if len(secrets.Items) != 1 {
		t.Errorf("expected exactly 1 ArgoCD cluster secret (the source), got %d", len(secrets.Items))
	}

	// Deleting the imported cluster leaves the source ArgoCD secret intact.
	if err := store.DeleteCluster(ctx, "imported"); err != nil {
		t.Fatalf("DeleteCluster: %v", err)
	}
	if _, err := client.CoreV1().Secrets(argoCDNS).Get(ctx, "cluster-import", metav1.GetOptions{}); err != nil {
		t.Errorf("source ArgoCD secret was deleted on cluster removal: %v", err)
	}
}

func TestImportArgoCDCluster_ExecSkipped(t *testing.T) {
	client := newTestClient(
		argoCDClusterSecret("cluster-eks", "eks", "https://eks:6443",
			`{"awsAuthConfig":{"clusterName":"prod"}}`),
	)
	store := NewK8sClusterStore(client)

	_, err := store.ImportArgoCDCluster(context.Background(), "eks")
	if err == nil {
		t.Fatal("expected exec/cloud-IAM cluster import to be rejected")
	}
}

func TestImportArgoCDCluster_NotFound(t *testing.T) {
	store := NewK8sClusterStore(newTestClient())
	if _, err := store.ImportArgoCDCluster(context.Background(), "nope"); err == nil {
		t.Fatal("expected error for unknown ArgoCD cluster")
	}
}
