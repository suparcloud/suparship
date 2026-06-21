package kube

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// kargoFreightGVR mirrors the GVR approveFreightForStage builds inline.
var kargoFreightGVR = schema.GroupVersionResource{
	Group:    "kargo.akuity.io",
	Version:  "v1alpha1",
	Resource: "freights",
}

// testKargoNS is the single shared Kargo namespace used by the store under test.
const testKargoNS = "suparship-kargo"

func newKargoStore(t *testing.T, objs ...*unstructured.Unstructured) (*KargoStore, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	scheme := k8sruntime.NewScheme()
	gvrMap := map[schema.GroupVersionResource]string{
		kargoStageGVR:     "StageList",
		kargoPromotionGVR: "PromotionList",
		kargoFreightGVR:   "FreightList",
		kargoWarehouseGVR: "WarehouseList",
	}
	ro := make([]k8sruntime.Object, len(objs))
	for i, o := range objs {
		ro[i] = o
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrMap, ro...)
	return NewKargoStore(dyn, testKargoNS), dyn
}

// stageCR builds a Kargo Stage with the given current freight and promotion steps.
// Pass freight="" to leave the stage without current freight, and nil steps to
// leave the promotionTemplate empty.
func stageCR(name, ns, freight string, steps []any) *unstructured.Unstructured {
	spec := map[string]any{}
	if steps != nil {
		spec["promotionTemplate"] = map[string]any{
			"spec": map[string]any{"steps": steps},
		}
	}
	status := map[string]any{}
	if freight != "" {
		status["currentFreight"] = map[string]any{"name": freight}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kargo.akuity.io/v1alpha1",
		"kind":       "Stage",
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec":       spec,
		"status":     status,
	}}
}

func freightCR(name, ns string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kargo.akuity.io/v1alpha1",
		"kind":       "Freight",
		"metadata":   map[string]any{"name": name, "namespace": ns},
	}}
}

func sampleSteps() []any {
	return []any{
		map[string]any{
			"uses":   "git-clone",
			"config": map[string]any{"repoURL": "https://example.com/gitops.git"},
		},
		map[string]any{
			"uses":   "git-commit",
			"config": map[string]any{"path": "./src", "message": "promote"},
		},
	}
}

// CreatePromotion must embed the target Stage's promotionTemplate steps into the
// Promotion CR — Kargo v1.x rejects a stepless Promotion created via the raw K8s
// API with "Stage ... defines no promotion steps".
func TestCreatePromotion_EmbedsTargetStagePromotionSteps(t *testing.T) {
	const project, app = "voiceai", "livekit-express-caller"
	steps := sampleSteps()
	// Stages live in the shared namespace under fully-qualified {project}-{app}-{env} names.
	store, dyn := newKargoStore(t,
		stageCR("voiceai-livekit-express-caller-staging", testKargoNS, "freight-abc", nil),
		stageCR("voiceai-livekit-express-caller-production", testKargoNS, "", steps),
		freightCR("freight-abc", testKargoNS),
	)

	info, err := store.CreatePromotion(context.Background(), project, app, "staging", "production")
	if err != nil {
		t.Fatalf("CreatePromotion: unexpected error: %v", err)
	}
	if info.Stage != "voiceai-livekit-express-caller-production" {
		t.Errorf("Stage = %q, want voiceai-livekit-express-caller-production", info.Stage)
	}
	if info.Freight != "freight-abc" {
		t.Errorf("Freight = %q, want freight-abc", info.Freight)
	}

	created, err := dyn.Resource(kargoPromotionGVR).Namespace(testKargoNS).Get(context.Background(), info.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get created promotion: %v", err)
	}
	gotSteps, found, err := unstructured.NestedSlice(created.Object, "spec", "steps")
	if err != nil || !found {
		t.Fatalf("promotion spec.steps not found (found=%v err=%v)", found, err)
	}
	if len(gotSteps) != len(steps) {
		t.Fatalf("promotion has %d steps, want %d", len(gotSteps), len(steps))
	}
	first, _ := gotSteps[0].(map[string]any)
	if first["uses"] != "git-clone" {
		t.Errorf("first step uses = %v, want git-clone", first["uses"])
	}
	stage, _, _ := unstructuredString(created.Object, "spec", "stage")
	if stage != "voiceai-livekit-express-caller-production" {
		t.Errorf("spec.stage = %q, want voiceai-livekit-express-caller-production", stage)
	}
}

// When the target Stage has no promotionTemplate steps, CreatePromotion must
// fail fast with a clear error rather than create a Promotion the webhook will
// reject.
func TestCreatePromotion_FailsWhenTargetStageHasNoSteps(t *testing.T) {
	const project, app = "voiceai", "livekit-express-caller"
	store, _ := newKargoStore(t,
		stageCR("voiceai-livekit-express-caller-staging", testKargoNS, "freight-abc", nil),
		stageCR("voiceai-livekit-express-caller-production", testKargoNS, "", nil),
		freightCR("freight-abc", testKargoNS),
	)

	_, err := store.CreatePromotion(context.Background(), project, app, "staging", "production")
	if err == nil {
		t.Fatal("CreatePromotion: expected error for stage with no promotion steps, got nil")
	}
	if !strings.Contains(err.Error(), "defines no promotion steps") {
		t.Errorf("error = %q, want it to mention 'defines no promotion steps'", err.Error())
	}
}
