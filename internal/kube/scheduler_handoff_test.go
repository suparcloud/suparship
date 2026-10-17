package kube

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func handoffConfig(policies []any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "kargo.akuity.io/v1alpha1", "kind": "ProjectConfig", "metadata": map[string]any{"name": testKargoNS, "namespace": testKargoNS, "resourceVersion": "1"}, "spec": map[string]any{"promotionPolicies": policies}}}
}

func TestSchedulerHandoffRequiresLivePolicyAndAcknowledgement(t *testing.T) {
	ctx := context.Background()
	config := handoffConfig([]any{map[string]any{"stage": "web-prod", "autoPromotionEnabled": true}})
	store, dyn := newKargoStore(t, config, stageCR("web-prod", testKargoNS, "", sampleSteps()))
	ready, err := store.DownstreamSchedulerReady(ctx, "voiceai", "web", "prod")
	if err != nil || ready {
		t.Fatalf("legacy scheduler enabled: %v %v", ready, err)
	}
	config.Object["spec"] = map[string]any{"promotionPolicies": []any{map[string]any{"stage": "web-prod"}}}
	config.SetResourceVersion("2")
	if _, err := dyn.Resource(kargoProjectConfigGVR).Namespace(testKargoNS).Update(ctx, config, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	ready, err = store.DownstreamSchedulerReady(ctx, "voiceai", "web", "prod")
	if err != nil || ready {
		t.Fatalf("unacknowledged handoff: %v %v", ready, err)
	}
	stage, _ := dyn.Resource(kargoStageGVR).Namespace(testKargoNS).Get(ctx, "web-prod", metav1.GetOptions{})
	token := stage.GetAnnotations()["kargo.akuity.io/refresh"]
	if token == "" {
		t.Fatal("no reconciliation requested")
	}
	_ = unstructured.SetNestedField(stage.Object, token, "status", "lastHandledRefresh")
	_, _ = dyn.Resource(kargoStageGVR).Namespace(testKargoNS).Update(ctx, stage, metav1.UpdateOptions{})
	ready, err = NewKargoStore(dyn).DownstreamSchedulerReady(ctx, "voiceai", "web", "prod")
	if err != nil || !ready {
		t.Fatalf("acknowledged handoff after restart: %v %v", ready, err)
	}
	config.SetResourceVersion("3")
	_, _ = dyn.Resource(kargoProjectConfigGVR).Namespace(testKargoNS).Update(ctx, config, metav1.UpdateOptions{})
	ready, err = store.DownstreamSchedulerReady(ctx, "voiceai", "web", "prod")
	if err != nil || ready {
		t.Fatalf("stale acknowledgement accepted: %v %v", ready, err)
	}
}

func TestSchedulerHandoffFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		policies []any
	}{
		{"missing policy", nil},
		{"enabled", []any{map[string]any{"stage": "web-prod", "autoPromotionEnabled": true}}},
		{"first match enabled", []any{map[string]any{"stage": "web-prod", "autoPromotionEnabled": true}, map[string]any{"stage": "web-prod"}}},
		{"wildcard before exact", []any{map[string]any{"stageSelector": map[string]any{"name": "glob:*"}, "autoPromotionEnabled": true}, map[string]any{"stage": "web-prod"}}},
		{"label selector before exact", []any{map[string]any{"stageSelector": map[string]any{"labelSelector": map[string]any{}}, "autoPromotionEnabled": true}, map[string]any{"stage": "web-prod"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, dyn := newKargoStore(t, handoffConfig(tc.policies), stageCR("web-prod", testKargoNS, "", sampleSteps()))
			ready, _ := store.DownstreamSchedulerReady(context.Background(), "voiceai", "web", "prod")
			if ready {
				t.Fatal("unsafe ownership accepted")
			}
			for _, a := range dyn.Actions() {
				if a.GetVerb() == "patch" {
					t.Fatal("requested acknowledgement before policy was safe")
				}
			}
		})
	}
	store, _ := newKargoStore(t)
	if ready, err := store.DownstreamSchedulerReady(context.Background(), "voiceai", "web", "prod"); ready || err == nil {
		t.Fatal("missing config accepted")
	}
}
