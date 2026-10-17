package kube

import (
	"context"
	"errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"testing"
	"time"
)

func TestVerifiedReleasePolicy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		verified  bool
		foreign   bool
		all       bool
		wantError bool
	}{
		{name: "historical verified release", verified: true},
		{name: "unverified release", wantError: true},
		{name: "foreign app", verified: true, foreign: true, wantError: true},
		{name: "all upstreams required", verified: true, all: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fr := freightCR("release-old", testKargoNS)
			origin := "web"
			if tc.foreign {
				origin = "other"
			}
			fr.Object["origin"] = map[string]any{"kind": "Warehouse", "name": origin}
			verified := map[string]any{}
			if tc.verified {
				verified["web-staging"] = map[string]any{}
			}
			fr.Object["status"] = map[string]any{"verifiedIn": verified, "approvedFor": map[string]any{"web-prod": map[string]any{}}}
			target := stageCR("web-prod", testKargoNS, "", sampleSteps())
			sources := map[string]any{"stages": []any{"web-staging"}}
			if tc.all {
				sources["stages"] = []any{"web-staging", "web-qa"}
				sources["availabilityStrategy"] = "All"
			}
			target.Object["spec"].(map[string]any)["requestedFreight"] = []any{map[string]any{"origin": map[string]any{"kind": "Warehouse", "name": "web"}, "sources": sources}}
			// No source Stage is needed: historical evidence, not current occupancy,
			// determines eligibility.
			store, dyn := newKargoStore(t, fr, target)
			info, err := store.PromoteVerifiedRelease(context.Background(), "voiceai", "web", "staging", "prod", "release-old")
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v", err)
			}
			for _, action := range dyn.Actions() {
				if action.GetVerb() == "patch" || action.GetVerb() == "update" {
					t.Fatalf("unexpected mutation: %v", action)
				}
				if tc.wantError && action.GetVerb() == "create" {
					t.Fatal("rejected release created a promotion")
				}
			}
			if !tc.wantError {
				obj, err := dyn.Resource(kargoPromotionGVR).Namespace(testKargoNS).Get(context.Background(), info.Name, metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				name, _, _ := unstructured.NestedString(obj.Object, "spec", "freight")
				if name != "release-old" {
					t.Fatal(name)
				}
			}
		})
	}
}

func TestVerifiedPromotionAttemptReuseAndConflict(t *testing.T) {
	store, dyn := newKargoStore(t, stageCR("web-prod", testKargoNS, "", sampleSteps()))
	ctx := context.Background()
	name, active, err := store.verifiedPromotionAttempt(ctx, testKargoNS, "web-prod", "release-a")
	if err != nil || active != nil {
		t.Fatalf("initial attempt: %v %v", active, err)
	}
	first, err := store.submitPromotion(ctx, testKargoNS, "voiceai", "web", "prod", "web-prod", "release-a", name)
	if err != nil {
		t.Fatal(err)
	}
	_, active, err = store.verifiedPromotionAttempt(ctx, testKargoNS, "web-prod", "release-a")
	if err != nil || active == nil || active.Name != first.Name {
		t.Fatalf("did not reuse active promotion: %v %v", active, err)
	}
	if _, _, err = store.verifiedPromotionAttempt(ctx, testKargoNS, "web-prod", "release-b"); !errors.Is(err, ErrPromotionBusy) {
		t.Fatalf("conflict = %v", err)
	}
	// A second caller with the same pre-read attempt must also reuse the CR.
	repeat, err := store.submitPromotion(ctx, testKargoNS, "voiceai", "web", "prod", "web-prod", "release-a", name)
	if err != nil || repeat.Name != first.Name {
		t.Fatalf("concurrent repeat: %v %v", repeat, err)
	}
	obj, _ := dyn.Resource(kargoPromotionGVR).Namespace(testKargoNS).Get(ctx, name, metav1.GetOptions{})
	_ = unstructured.SetNestedField(obj.Object, "Failed", "status", "phase")
	_, err = dyn.Resource(kargoPromotionGVR).Namespace(testKargoNS).Update(ctx, obj, metav1.UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	retry, active, err := store.verifiedPromotionAttempt(ctx, testKargoNS, "web-prod", "release-a")
	if err != nil || active != nil || retry == name {
		t.Fatalf("failed attempt cannot retry: %s %v %v", retry, active, err)
	}
}

func TestNextVerifiedReleaseDoesNotGoBackwards(t *testing.T) {
	for _, newer := range []bool{false, true} {
		current := freightCR("current", testKargoNS)
		current.SetCreationTimestamp(metav1.NewTime(time.Unix(100, 0)))
		candidate := freightCR("verified", testKargoNS)
		stamp := int64(50)
		if newer {
			stamp = 150
		}
		candidate.SetCreationTimestamp(metav1.NewTime(time.Unix(stamp, 0)))
		candidate.Object["origin"] = map[string]any{"kind": "Warehouse", "name": "web"}
		candidate.Object["status"] = map[string]any{"verifiedIn": map[string]any{"web-staging": map[string]any{}}}
		target := stageCR("web-prod", testKargoNS, "current", sampleSteps())
		target.Object["spec"].(map[string]any)["requestedFreight"] = []any{map[string]any{"origin": map[string]any{"kind": "Warehouse", "name": "web"}, "sources": map[string]any{"stages": []any{"web-staging"}}}}
		store, _ := newKargoStore(t, current, candidate, target)
		selected, err := store.NextVerifiedRelease(context.Background(), "voiceai", "web", "staging", "prod")
		if err != nil {
			t.Fatal(err)
		}
		want := ""
		if newer {
			want = "verified"
		}
		if selected != want {
			t.Fatalf("newer=%v selected=%q", newer, selected)
		}
	}
}

func TestCompletedVerifiedPromotionIsIdempotentAtCurrentRelease(t *testing.T) {
	ctx := context.Background()
	stage := stageCR("web-prod", testKargoNS, "release-a", sampleSteps())
	store, dyn := newKargoStore(t, stage)
	name, _, err := store.verifiedPromotionAttempt(ctx, testKargoNS, "web-prod", "release-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.submitPromotion(ctx, testKargoNS, "voiceai", "web", "prod", "web-prod", "release-a", name); err != nil {
		t.Fatal(err)
	}
	obj, _ := dyn.Resource(kargoPromotionGVR).Namespace(testKargoNS).Get(ctx, name, metav1.GetOptions{})
	_ = unstructured.SetNestedField(obj.Object, "Succeeded", "status", "phase")
	if _, err = dyn.Resource(kargoPromotionGVR).Namespace(testKargoNS).Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	_, existing, err := store.verifiedPromotionAttempt(ctx, testKargoNS, "web-prod", "release-a")
	if err != nil || existing == nil || existing.Name != name {
		t.Fatalf("completed retry duplicated deployment: %v %v", existing, err)
	}
}

func TestFailedAttemptDoesNotReuseOlderSuccess(t *testing.T) {
	ctx := context.Background()
	store, dyn := newKargoStore(t, stageCR("web-prod", testKargoNS, "release-a", sampleSteps()))
	var first string
	for _, phase := range []string{"Succeeded", "Errored"} {
		name, _, err := store.verifiedPromotionAttempt(ctx, testKargoNS, "web-prod", "release-b")
		if err != nil {
			t.Fatal(err)
		}
		if first == "" {
			first = name
		}
		_, err = store.submitPromotion(ctx, testKargoNS, "voiceai", "web", "prod", "web-prod", "release-a", name)
		if err != nil {
			t.Fatal(err)
		}
		obj, _ := dyn.Resource(kargoPromotionGVR).Namespace(testKargoNS).Get(ctx, name, metav1.GetOptions{})
		_ = unstructured.SetNestedField(obj.Object, phase, "status", "phase")
		if _, err = dyn.Resource(kargoPromotionGVR).Namespace(testKargoNS).Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	name, reused, err := store.verifiedPromotionAttempt(ctx, testKargoNS, "web-prod", "release-a")
	if err != nil || reused != nil || name == first {
		t.Fatalf("failed write suppressed recovery: %s %v %v", name, reused, err)
	}
}

func TestAutoPromotionRepairsFailedWriteOfCurrentRelease(t *testing.T) {
	current := freightCR("current", testKargoNS)
	current.Object["origin"] = map[string]any{"kind": "Warehouse", "name": "web"}
	current.Object["status"] = map[string]any{"verifiedIn": map[string]any{"web-staging": map[string]any{}}}
	target := stageCR("web-prod", testKargoNS, "current", sampleSteps())
	target.Object["spec"].(map[string]any)["requestedFreight"] = []any{map[string]any{"origin": map[string]any{"kind": "Warehouse", "name": "web"}, "sources": map[string]any{"stages": []any{"web-staging"}}}}
	_ = unstructured.SetNestedField(target.Object, "Errored", "status", "lastPromotion", "status", "phase")
	store, _ := newKargoStore(t, current, target)
	selected, err := store.NextVerifiedRelease(context.Background(), "voiceai", "web", "staging", "prod")
	if err != nil || selected != "current" {
		t.Fatalf("failed deployment cannot recover: %q %v", selected, err)
	}
}

func TestPromotionAttemptNamesPreserveKargoOrdering(t *testing.T) {
	if verifiedAttemptName("verified-stage-", 9) >= verifiedAttemptName("verified-stage-", 10) {
		t.Fatal("tenth promotion sorts before ninth")
	}
}
