package kube

import (
	"context"
	"encoding/json"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clienttesting "k8s.io/client-go/testing"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestNativePromotionName(t *testing.T) {
	name := nativePromotionName(strings.Repeat("s", 240), "abcdef012345")
	parts := strings.Split(name, ".")
	if len(name) != 253 || len(parts) != 3 || parts[2] != "abcdef0" {
		t.Fatal(name)
	}
	if _, err := ulid.ParseStrict(parts[1]); err != nil {
		t.Fatal(err)
	}
}

func TestVerifiedRollbackVerifiedOrdering(t *testing.T) {
	ctx := context.Background()
	stage := stageCR("web-prod", testKargoNS, "release-a", sampleSteps())
	stage.Object["spec"].(map[string]any)["requestedFreight"] = []any{map[string]any{"origin": map[string]any{"kind": "Warehouse", "name": "web"}, "sources": map[string]any{"stages": []any{"web-staging"}}}}
	stage.Object["status"].(map[string]any)["freightHistory"] = []any{map[string]any{"items": map[string]any{"web": map[string]any{"name": "release-a"}}}}
	freight := freightCR("release-b", testKargoNS)
	freight.Object["origin"] = map[string]any{"kind": "Warehouse", "name": "web"}
	freight.Object["status"] = map[string]any{"verifiedIn": map[string]any{"web-staging": map[string]any{}}}
	store, dyn := newKargoStore(t, stage, freight, freightCR("release-a", testKargoNS))
	complete := func(info *KargoPromotionInfo) {
		t.Helper()
		obj, err := dyn.Resource(kargoPromotionGVR).Namespace(testKargoNS).Get(ctx, info.Name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		_ = unstructured.SetNestedField(obj.Object, "Succeeded", "status", "phase")
		if _, err = dyn.Resource(kargoPromotionGVR).Namespace(testKargoNS).Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		target, _ := dyn.Resource(kargoStageGVR).Namespace(testKargoNS).Get(ctx, "web-prod", metav1.GetOptions{})
		_ = unstructured.SetNestedField(target.Object, info.Name, "status", "lastPromotion", "name")
		_ = unstructured.SetNestedField(target.Object, info.Freight, "status", "currentFreight", "name")
		if _, err = dyn.Resource(kargoStageGVR).Namespace(testKargoNS).Update(ctx, target, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.PromoteVerifiedRelease(ctx, "voiceai", "web", "staging", "prod", "release-b")
	if err != nil {
		t.Fatal(err)
	}
	complete(first)
	rollback, err := store.PromoteFreight(ctx, "voiceai", "web", "prod", "release-a")
	if err != nil {
		t.Fatal(err)
	}
	complete(rollback)
	// Native Kargo uses this same format; include its completed operation too.
	native := nativePromotionName("web-prod", "release-a")
	nativeInfo, err := store.submitPromotion(ctx, testKargoNS, "voiceai", "web", "prod", "web-prod", "release-a", native)
	if err != nil {
		t.Fatal(err)
	}
	complete(nativeInfo)
	last, err := store.PromoteVerifiedRelease(ctx, "voiceai", "web", "staging", "prod", "release-b")
	if err != nil {
		t.Fatal(err)
	}
	if !(first.Name < rollback.Name && rollback.Name < native && native < last.Name) {
		t.Fatalf("wrong order: %s %s %s %s", first.Name, rollback.Name, native, last.Name)
	}
}

func TestPromotionReservationSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	store, dyn := newKargoStore(t, stageCR("web-prod", testKargoNS, "", sampleSteps()))
	first, _, err := store.verifiedPromotionAttempt(ctx, testKargoNS, "web-prod", "release-a")
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewKargoStore(dyn)
	second, _, err := restarted.verifiedPromotionAttempt(ctx, testKargoNS, "web-prod", "release-b")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("concurrent callers got different reservations: %s %s", first, second)
	}
}

func TestPromotionOrderingAcrossLegacyAndPrunedHistory(t *testing.T) {
	ctx := context.Background()
	stage := stageCR("web-prod", testKargoNS, "", sampleSteps())
	// Released Suparship used stage-UnixNano, which sorts before stage.ULID.
	_ = unstructured.SetNestedField(stage.Object, "web-prod-1792267200000000000", "status", "lastPromotion", "name")
	store, dyn := newKargoStore(t, stage)
	name, _, err := store.verifiedPromotionAttempt(ctx, testKargoNS, "web-prod", "release-a")
	if err != nil || name <= "web-prod-1792267200000000000" {
		t.Fatalf("legacy migration: %s %v", name, err)
	}
	stage, _ = dyn.Resource(kargoStageGVR).Namespace(testKargoNS).Get(ctx, "web-prod", metav1.GetOptions{})
	_ = unstructured.SetNestedField(stage.Object, name, "status", "lastPromotion", "name")
	_, _ = dyn.Resource(kargoStageGVR).Namespace(testKargoNS).Update(ctx, stage, metav1.UpdateOptions{})
	next, _, err := store.verifiedPromotionAttempt(ctx, testKargoNS, "web-prod", "release-a")
	if err != nil || next <= name {
		t.Fatalf("garbage-collected history: %s %s %v", name, next, err)
	}
}

func TestIncompatibleExperimentalHistoryFailsBeforeSubmission(t *testing.T) {
	stage := stageCR("bootstrap-prod", testKargoNS, "", sampleSteps())
	_ = unstructured.SetNestedField(stage.Object, "verified-experimental-9", "status", "lastPromotion", "name")
	store, dyn := newKargoStore(t, stage)
	if _, _, err := store.verifiedPromotionAttempt(context.Background(), testKargoNS, "bootstrap-prod", "release-a"); err == nil {
		t.Fatal("accepted a name Kargo would ignore")
	}
	for _, a := range dyn.Actions() {
		if a.GetVerb() == "create" {
			t.Fatal("created unsafe promotion")
		}
	}
}

func TestPromotionReservationRetriesResourceVersionConflict(t *testing.T) {
	ctx := context.Background()
	store, dyn := newKargoStore(t, stageCR("web-prod", testKargoNS, "", sampleSteps()))
	winner := nativePromotionName("web-prod", "release-a")
	conflicted := false
	dyn.PrependReactor("update", "stages", func(action clienttesting.Action) (bool, runtime.Object, error) {
		if conflicted {
			return false, nil, nil
		}
		conflicted = true
		obj := action.(clienttesting.UpdateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
		raw, _ := json.Marshal(promotionReservation{Name: winner})
		obj.SetAnnotations(map[string]string{promotionReservationAnnotation: string(raw)})
		if err := dyn.Tracker().Update(kargoStageGVR, obj, testKargoNS); err != nil {
			t.Fatal(err)
		}
		return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "kargo.akuity.io", Resource: "stages"}, "web-prod", nil)
	})
	name, _, err := store.verifiedPromotionAttempt(ctx, testKargoNS, "web-prod", "release-a")
	if err != nil || name != winner || !conflicted {
		t.Fatalf("lost concurrent reservation: %s %v", name, err)
	}
}
