package kube

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/suparcloud/suparship/internal/domain"
)

func TestTemplateOverride_SaveLoadRoundTrip(t *testing.T) {
	client := fake.NewSimpleClientset()
	ctx := context.Background()

	ov := &domain.TemplateOverride{
		DefaultValues: map[string]any{
			"resources": map[string]any{"requests": map[string]any{"cpu": "100m"}},
		},
		EnvValues: map[string]map[string]any{
			"prod": {"replicaCount": 4},
		},
	}
	if err := SaveTemplateOverride(ctx, client, "voiceai-agent", ov); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := LoadTemplateOverride(ctx, client, "voiceai-agent")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got == nil {
		t.Fatal("expected an override, got nil")
	}
	cpu := got.DefaultValues["resources"].(map[string]any)["requests"].(map[string]any)["cpu"]
	if cpu != "100m" {
		t.Errorf("defaultValues cpu = %v, want 100m", cpu)
	}
	if got.EnvValues["prod"]["replicaCount"] != 4 {
		t.Errorf("prod replicaCount = %v, want 4", got.EnvValues["prod"]["replicaCount"])
	}
}

func TestTemplateOverride_LoadAbsentReturnsNil(t *testing.T) {
	got, err := LoadTemplateOverride(context.Background(), fake.NewSimpleClientset(), "nope")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for absent override, got %+v", got)
	}
}

func TestTemplateOverride_SaveEmptyDeletes(t *testing.T) {
	client := fake.NewSimpleClientset()
	ctx := context.Background()
	_ = SaveTemplateOverride(ctx, client, "web", &domain.TemplateOverride{
		DefaultValues: map[string]any{"a": 1},
	})
	// Saving an empty override should delete the ConfigMap (clear semantics).
	if err := SaveTemplateOverride(ctx, client, "web", &domain.TemplateOverride{}); err != nil {
		t.Fatalf("save empty: %v", err)
	}
	got, _ := LoadTemplateOverride(ctx, client, "web")
	if got != nil {
		t.Fatalf("expected override cleared, got %+v", got)
	}
}

func TestTemplateOverride_SeparateFromTemplateConfigMap(t *testing.T) {
	// The override CM must be distinct from the template CM, so an external
	// template sync (which overwrites the template CM) never touches it.
	client := fake.NewSimpleClientset()
	ctx := context.Background()
	_ = SaveTemplateOverride(ctx, client, "web", &domain.TemplateOverride{
		DefaultValues: map[string]any{"a": 1},
	})

	if TemplateOverrideConfigMapName("web") == TemplateConfigMapName("web") {
		t.Fatal("override CM name must differ from the template CM name")
	}
	// The override CM exists; the template CM does not (only the override was written).
	if _, err := client.CoreV1().ConfigMaps(systemNamespace).Get(ctx, TemplateOverrideConfigMapName("web"), metav1.GetOptions{}); err != nil {
		t.Fatalf("override CM should exist: %v", err)
	}
	if _, err := client.CoreV1().ConfigMaps(systemNamespace).Get(ctx, TemplateConfigMapName("web"), metav1.GetOptions{}); err == nil {
		t.Fatal("template CM should NOT exist (override is stored separately)")
	}
}
