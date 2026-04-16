package tpl

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestRegistryStore_GetNotFound(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: systemNamespace}},
	)
	store := NewRegistryStore(client)

	_, err := store.Get(context.Background())
	if !errors.Is(err, ErrRegistryNotFound) {
		t.Errorf("expected ErrRegistryNotFound, got %v", err)
	}
}

func TestRegistryStore_SaveAndGet(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: systemNamespace}},
	)
	store := NewRegistryStore(client)
	ctx := context.Background()

	reg := &TemplateRegistry{
		BuiltIn: []string{"web-service", "color-app"},
		External: []ExternalTemplateRepo{
			{
				Name:    "company-templates",
				RepoURL: "https://github.com/myorg/templates.git",
				Ref:     "v1.2.0",
				Path:    "templates/",
			},
		},
		Sources: []TemplateSource{
			{Name: "web-service", Origin: "builtin", Version: "1.0.0", SyncedAt: "2024-01-15T10:00:00Z"},
			{Name: "color-app", Origin: "builtin", Version: "1.0.0", SyncedAt: "2024-01-15T10:00:00Z"},
			{
				Name:         "company-web",
				Origin:       "external",
				Version:      "2.1.0",
				ExternalRepo: "https://github.com/myorg/templates.git",
				ExternalRef:  "v1.2.0",
				ExternalPath: "templates/company-web",
				SyncedAt:     "2024-01-15T10:00:00Z",
			},
		},
	}

	if err := store.Save(ctx, reg); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := store.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if len(got.BuiltIn) != 2 {
		t.Errorf("builtIn count = %d, want 2", len(got.BuiltIn))
	}
	if len(got.External) != 1 {
		t.Errorf("external count = %d, want 1", len(got.External))
	}
	if len(got.Sources) != 3 {
		t.Errorf("sources count = %d, want 3", len(got.Sources))
	}

	src := got.FindSource("company-web")
	if src == nil {
		t.Fatal("expected to find company-web source")
	}
	if src.ExternalRef != "v1.2.0" {
		t.Errorf("externalRef = %q, want v1.2.0", src.ExternalRef)
	}
}

func TestRegistryStore_Update(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: systemNamespace}},
	)
	store := NewRegistryStore(client)
	ctx := context.Background()

	reg := &TemplateRegistry{
		BuiltIn: []string{"web-service"},
		Sources: []TemplateSource{
			{Name: "web-service", Origin: "builtin", Version: "1.0.0"},
		},
	}
	if err := store.Save(ctx, reg); err != nil {
		t.Fatalf("initial save: %v", err)
	}

	reg.Sources[0].Version = "2.0.0"
	reg.BuiltIn = append(reg.BuiltIn, "color-app")
	reg.Sources = append(reg.Sources, TemplateSource{Name: "color-app", Origin: "builtin", Version: "1.0.0"})
	if err := store.Save(ctx, reg); err != nil {
		t.Fatalf("update save: %v", err)
	}

	got, err := store.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.BuiltIn) != 2 {
		t.Errorf("builtIn count = %d, want 2", len(got.BuiltIn))
	}
	src := got.FindSource("web-service")
	if src == nil || src.Version != "2.0.0" {
		t.Errorf("web-service version = %v, want 2.0.0", src)
	}
}
