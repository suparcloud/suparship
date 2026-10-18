package kube

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestValidationLeaseSerializesAndRestoresProgress(t *testing.T) {
	store, dyn := newKargoStore(t, stageCR("web-validation", testKargoNS, "", sampleSteps()))
	ctx := context.Background()
	expected := errors.New("publish interrupted")
	err := store.WithValidationLease(ctx, "voiceai", "web", "validation", func(ctx context.Context, run *domain.ValidationRun) error {
		run.Phase = "waiting"
		run.Freight = "release"
		second := NewKargoStore(dyn).WithValidationLease(ctx, "voiceai", "web", "validation", func(context.Context, *domain.ValidationRun) error {
			t.Fatal("concurrent lifecycle entered")
			return nil
		})
		if !errors.Is(second, ErrValidationLeaseBusy) {
			t.Fatalf("lease not exclusive: %v", second)
		}
		return expected
	})
	if !errors.Is(err, expected) {
		t.Fatal(err)
	}
	err = NewKargoStore(dyn).WithValidationLease(ctx, "voiceai", "web", "validation", func(_ context.Context, run *domain.ValidationRun) error {
		if run.Phase != "waiting" || run.Freight != "release" {
			t.Fatal("lost persisted progress")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	stage, _ := dyn.Resource(kargoStageGVR).Namespace(testKargoNS).Get(ctx, "web-validation", metav1.GetOptions{})
	a := stage.GetAnnotations()
	lease, _ := json.Marshal(validationLease{Token: "dead-process", Until: time.Now().Add(-time.Minute)})
	a[validationLeaseAnnotation] = string(lease)
	stage.SetAnnotations(a)
	_, _ = dyn.Resource(kargoStageGVR).Namespace(testKargoNS).Update(ctx, stage, metav1.UpdateOptions{})
	if err = store.WithValidationLease(ctx, "voiceai", "web", "validation", func(context.Context, *domain.ValidationRun) error { return nil }); err != nil {
		t.Fatalf("expired lease not recovered: %v", err)
	}
}

func TestValidationLeaseDoesNotOverwriteNewOwner(t *testing.T) {
	store, dyn := newKargoStore(t, stageCR("web-validation", testKargoNS, "", sampleSteps()))
	ctx := context.Background()
	err := store.WithValidationLease(ctx, "voiceai", "web", "validation", func(ctx context.Context, run *domain.ValidationRun) error {
		stage, _ := dyn.Resource(kargoStageGVR).Namespace(testKargoNS).Get(ctx, "web-validation", metav1.GetOptions{})
		a := stage.GetAnnotations()
		raw, _ := json.Marshal(validationLease{Token: "replacement", Until: time.Now().Add(time.Hour)})
		a[validationLeaseAnnotation] = string(raw)
		stage.SetAnnotations(a)
		_, err := dyn.Resource(kargoStageGVR).Namespace(testKargoNS).Update(ctx, stage, metav1.UpdateOptions{})
		return err
	})
	if !errors.Is(err, ErrValidationLeaseBusy) {
		t.Fatalf("stale owner saved progress: %v", err)
	}
}

func TestValidationObservesPromotionFailureWithoutVerifying(t *testing.T) {
	stage := stageCR("web-validation", testKargoNS, "", sampleSteps())
	stage.Object["status"] = map[string]any{"lastPromotion": map[string]any{"freight": map[string]any{"name": "bad"}, "status": map[string]any{"phase": "Errored"}}}
	store, _ := newKargoStore(t, stage, freightCR("bad", testKargoNS))
	obs, err := store.ObserveValidation(context.Background(), "voiceai", "web", "validation", "bad")
	if err != nil || !obs.Failed || obs.Verified {
		t.Fatalf("bad promotion observation: %+v %v", obs, err)
	}
}

func TestValidationLeaseReportsMissingStageBeforeRunningAction(t *testing.T) {
	store, _ := newKargoStore(t)
	called := false
	err := store.WithValidationLease(context.Background(), "voiceai", "missing", "release-validation", func(context.Context, *domain.ValidationRun) error { called = true; return nil })
	if !errors.Is(err, ErrValidationStageMissing) || called {
		t.Fatalf("unsafe missing-stage recovery: called=%t err=%v", called, err)
	}
}
