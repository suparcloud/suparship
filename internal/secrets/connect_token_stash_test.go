package secrets_test

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/suparcloud/suparship/internal/secrets"
)

func TestStashConnectToken_CreateThenUpdate(t *testing.T) {
	client := fake.NewClientset()
	ctx := context.Background()

	if err := secrets.StashConnectToken(ctx, client, "staging", []byte("first")); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := secrets.LoadConnectToken(ctx, client, "staging")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if string(got) != "first" {
		t.Errorf("got %q, want first", got)
	}

	// Rotate.
	if err := secrets.StashConnectToken(ctx, client, "staging", []byte("second")); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	got, _ = secrets.LoadConnectToken(ctx, client, "staging")
	if string(got) != "second" {
		t.Errorf("rotated load = %q, want second", got)
	}
}

func TestLoadConnectToken_MissingIsNotError(t *testing.T) {
	client := fake.NewClientset()
	got, err := secrets.LoadConnectToken(context.Background(), client, "never-stashed")
	if err != nil {
		t.Fatalf("missing stash should not error, got: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil bytes for missing stash, got %q", got)
	}
}

func TestDeleteConnectToken_IdempotentOnMissing(t *testing.T) {
	client := fake.NewClientset()
	if err := secrets.DeleteConnectToken(context.Background(), client, "never-stashed"); err != nil {
		t.Fatalf("delete on missing should be no-op, got: %v", err)
	}
}

func TestStash_LabelsForDiscovery(t *testing.T) {
	client := fake.NewClientset()
	if err := secrets.StashConnectToken(context.Background(), client, "prod", []byte("x")); err != nil {
		t.Fatal(err)
	}
	sec, err := client.CoreV1().Secrets("suparship-system").Get(context.Background(),
		secrets.ConnectTokenStashName("prod"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Stash carries the type label so an operator inheriting the
	// platform can `kubectl get secrets -n suparship-system -l
	// suparship.io/type=onepassword-connect-token-stash` to find them all.
	if sec.Labels["suparship.io/type"] != "onepassword-connect-token-stash" {
		t.Errorf("missing type label: %v", sec.Labels)
	}
	if sec.Labels["suparship.io/env"] != "prod" {
		t.Errorf("missing env label: %v", sec.Labels)
	}
}
