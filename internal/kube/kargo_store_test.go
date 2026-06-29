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

// testKargoNS is the per-project Kargo namespace (kargo-{project}) for the test
// project "voiceai", where the store reads/writes its CRs.
const testKargoNS = "kargo-voiceai"

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
	return NewKargoStore(dyn), dyn
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

func freightWithImages(name, ns string, images []any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kargo.akuity.io/v1alpha1",
		"kind":       "Freight",
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"images":     images,
	}}
}

// freightFromWarehouse is a freight tagged with its origin Warehouse + a
// creationTimestamp, used to test newest-first per-app selection.
func freightFromWarehouse(name, ns, warehouse, created string, images []any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kargo.akuity.io/v1alpha1",
		"kind":       "Freight",
		"metadata":   map[string]any{"name": name, "namespace": ns, "creationTimestamp": created},
		"origin":     map[string]any{"kind": "Warehouse", "name": warehouse},
		"images":     images,
	}}
}

// LatestFreightImageTag picks the newest freight from THIS app's Warehouse,
// ignoring other apps' freight in the shared project namespace.
func TestLatestFreightImageTag(t *testing.T) {
	img := func(tag string) []any {
		return []any{map[string]any{"repoURL": "acr.io/example-lk-sh-web", "tag": tag}}
	}
	store, _ := newKargoStore(t,
		freightFromWarehouse("f-old", testKargoNS, "lk-sh-web", "2026-06-01T00:00:00Z", img("old1234")),
		freightFromWarehouse("f-new", testKargoNS, "lk-sh-web", "2026-06-29T00:00:00Z", img("new5678")),
		// Another app's freight, newer — must be ignored.
		freightFromWarehouse("f-other", testKargoNS, "lk-sh-api", "2026-06-30T00:00:00Z", img("other999")),
	)
	tag, err := store.LatestFreightImageTag(context.Background(), "voiceai", "lk-sh-web", "")
	if err != nil {
		t.Fatal(err)
	}
	if tag != "new5678" {
		t.Errorf("tag = %q, want new5678 (newest from this app's warehouse)", tag)
	}
}

// CurrentFreightImageTag resolves the stage's current freight down to its image
// tag — the real Kargo-owned image used to restore an env on unpin.
func TestCurrentFreightImageTag(t *testing.T) {
	stage := stageCR("lk-sh-web-staging", testKargoNS, "freight-abc", nil)
	fr := freightWithImages("freight-abc", testKargoNS, []any{
		map[string]any{"repoURL": "acr.io/other", "tag": "zzz"},
		map[string]any{"repoURL": "acr.io/example-lk-sh-web", "tag": "abc1234"},
	})
	store, _ := newKargoStore(t, stage, fr)

	// repoSubstr prefers the matching image.
	tag, err := store.CurrentFreightImageTag(context.Background(), "voiceai", "lk-sh-web", "staging", "lk-sh-web")
	if err != nil {
		t.Fatal(err)
	}
	if tag != "abc1234" {
		t.Errorf("tag = %q, want abc1234 (repo-matched)", tag)
	}

	// No repoSubstr → first image's tag.
	tag, _ = store.CurrentFreightImageTag(context.Background(), "voiceai", "lk-sh-web", "staging", "")
	if tag != "zzz" {
		t.Errorf("tag = %q, want zzz (first image)", tag)
	}

	// A stage with no current freight → empty, no error.
	store2, _ := newKargoStore(t, stageCR("lk-sh-web-staging", testKargoNS, "", nil))
	if tag, err := store2.CurrentFreightImageTag(context.Background(), "voiceai", "lk-sh-web", "staging", ""); err != nil || tag != "" {
		t.Errorf("no-freight: tag=%q err=%v, want empty", tag, err)
	}
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
	// Stages live in the project's kargo-voiceai namespace under {app}-{env} names.
	store, dyn := newKargoStore(t,
		stageCR("livekit-express-caller-staging", testKargoNS, "freight-abc", nil),
		stageCR("livekit-express-caller-production", testKargoNS, "", steps),
		freightCR("freight-abc", testKargoNS),
	)

	info, err := store.CreatePromotion(context.Background(), project, app, "staging", "production")
	if err != nil {
		t.Fatalf("CreatePromotion: unexpected error: %v", err)
	}
	if info.Stage != "livekit-express-caller-production" {
		t.Errorf("Stage = %q, want livekit-express-caller-production", info.Stage)
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
	if stage != "livekit-express-caller-production" {
		t.Errorf("spec.stage = %q, want livekit-express-caller-production", stage)
	}
}

// When the target Stage has no promotionTemplate steps, CreatePromotion must
// fail fast with a clear error rather than create a Promotion the webhook will
// reject.
func TestCreatePromotion_FailsWhenTargetStageHasNoSteps(t *testing.T) {
	const project, app = "voiceai", "livekit-express-caller"
	store, _ := newKargoStore(t,
		stageCR("livekit-express-caller-staging", testKargoNS, "freight-abc", nil),
		stageCR("livekit-express-caller-production", testKargoNS, "", nil),
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
