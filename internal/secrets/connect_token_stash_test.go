package secrets_test

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/suparcloud/suparship/internal/secrets"
)

func TestStashClusterCredential_CreateThenUpdate(t *testing.T) {
	client := fake.NewClientset()
	ctx := context.Background()
	name := secrets.ConnectTokenStashName("staging")

	if err := secrets.StashClusterCredential(ctx, client, name, []byte("first")); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := secrets.LoadClusterCredential(ctx, client, name)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if string(got) != "first" {
		t.Errorf("got %q, want first", got)
	}

	// Rotate.
	if err := secrets.StashClusterCredential(ctx, client, name, []byte("second")); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	got, _ = secrets.LoadClusterCredential(ctx, client, name)
	if string(got) != "second" {
		t.Errorf("rotated load = %q, want second", got)
	}
}

func TestLoadClusterCredential_MissingIsNotError(t *testing.T) {
	client := fake.NewClientset()
	got, err := secrets.LoadClusterCredential(context.Background(), client, "never-stashed")
	if err != nil {
		t.Fatalf("missing stash should not error, got: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil bytes for missing stash, got %q", got)
	}
}

func TestDeleteClusterCredential_IdempotentOnMissing(t *testing.T) {
	client := fake.NewClientset()
	if err := secrets.DeleteClusterCredential(context.Background(), client, "never-stashed"); err != nil {
		t.Fatalf("delete on missing should be no-op, got: %v", err)
	}
}

func TestStash_LabelsForDiscovery(t *testing.T) {
	client := fake.NewClientset()
	name := secrets.ConnectTokenStashName(secrets.ClusterStashKey("prod-eu"))
	if err := secrets.StashClusterCredential(context.Background(), client, name, []byte("x")); err != nil {
		t.Fatal(err)
	}
	sec, err := client.CoreV1().Secrets("suparship-system").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Stash carries the type label so an operator inheriting the platform can
	// `kubectl get secrets -n suparship-system -l
	// suparship.io/type=cluster-credential-stash` to find them all, whichever
	// backend wrote them.
	if sec.Labels["suparship.io/type"] != "cluster-credential-stash" {
		t.Errorf("stash label = %q", sec.Labels["suparship.io/type"])
	}
}

// The two backends' stash names must never alias — a backend switch that
// resealed the previous backend's credential into the new backend's store
// would publish a working-looking ClusterSecretStore with a token for the
// wrong system.
func TestClusterStashSecretName_BackendQualified(t *testing.T) {
	op := secrets.BackendConfig{Type: secrets.Backend1Password}
	hv := secrets.BackendConfig{Type: secrets.BackendVault}
	k8s := secrets.BackendConfig{Type: secrets.BackendK8s}

	opName := op.ClusterStashSecretName("eu-1")
	hvName := hv.ClusterStashSecretName("eu-1")
	if opName == "" || hvName == "" {
		t.Fatalf("credentialed backends must have stash names; got %q, %q", opName, hvName)
	}
	if opName == hvName {
		t.Errorf("stash names alias across backends: %q", opName)
	}
	if got := k8s.ClusterStashSecretName("eu-1"); got != "" {
		t.Errorf("k8s backend has no per-cluster credential, stash name = %q", got)
	}

	// A credential stashed under one backend is invisible to the other.
	client := fake.NewClientset()
	ctx := context.Background()
	if err := secrets.StashClusterCredential(ctx, client, opName, []byte("op-token")); err != nil {
		t.Fatal(err)
	}
	if got, _ := secrets.LoadClusterCredential(ctx, client, hvName); got != nil {
		t.Errorf("vault stash read the 1Password credential: %q", got)
	}
}
