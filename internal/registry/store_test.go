package registry

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestStore_GetNotFound(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}},
	)
	store := NewStore(client)

	_, err := store.Get(context.Background())
	if !errors.Is(err, ErrConfigNotFound) {
		t.Errorf("expected ErrConfigNotFound, got %v", err)
	}
}

func TestStore_SaveAndGet(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}},
	)
	store := NewStore(client)
	ctx := context.Background()

	cfg := &Config{
		Enabled:       true,
		URL:           "ghcr.io",
		Username:      "robot",
		AuthSecretRef: "reg-creds",
		Environments:  []string{"staging", "prod"},
	}

	if err := store.Save(ctx, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := store.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if !got.Enabled {
		t.Error("expected enabled=true")
	}
	if got.URL != "ghcr.io" {
		t.Errorf("url = %q, want ghcr.io", got.URL)
	}
	if got.Username != "robot" {
		t.Errorf("username = %q, want robot", got.Username)
	}
	if got.AuthSecretRef != "reg-creds" {
		t.Errorf("authSecretRef = %q, want reg-creds", got.AuthSecretRef)
	}
	if len(got.Environments) != 2 {
		t.Errorf("environments count = %d, want 2", len(got.Environments))
	}
}

func TestStore_Update(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}},
	)
	store := NewStore(client)
	ctx := context.Background()

	cfg := &Config{Enabled: true, URL: "ghcr.io"}
	if err := store.Save(ctx, cfg); err != nil {
		t.Fatalf("initial save: %v", err)
	}

	cfg.URL = "registry.example.com"
	cfg.Environments = []string{"prod"}
	if err := store.Save(ctx, cfg); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := store.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.URL != "registry.example.com" {
		t.Errorf("url = %q, want registry.example.com", got.URL)
	}
}

func TestStore_SaveValidation(t *testing.T) {
	client := fake.NewSimpleClientset()
	store := NewStore(client)

	err := store.Save(context.Background(), &Config{Enabled: true})
	if !errors.Is(err, ErrMissingURL) {
		t.Errorf("expected ErrMissingURL, got %v", err)
	}
}
