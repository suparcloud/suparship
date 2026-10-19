package kube

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

func TestValidationApplicationShellReadiness(t *testing.T) {
	ctx := context.Background()
	source := map[string]any{"repoURL": "http://gitops", "path": ".suparship/validation-empty", "targetRevision": "main"}
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "validator", "namespace": "argocd", "annotations": map[string]any{"suparship.io/validation-idle": "true"}},
		"spec":     map[string]any{"source": source, "syncPolicy": map[string]any{"automated": map[string]any{"prune": true, "allowEmpty": true}}},
		"status":   map[string]any{"sync": map[string]any{"status": "Synced", "comparedTo": map[string]any{"source": source}}, "health": map[string]any{"status": "Healthy"}},
	}}
	for _, tc := range []struct {
		name            string
		mutate          func(*unstructured.Unstructured)
		prepared, clean bool
	}{
		{"empty shell", func(*unstructured.Unstructured) {}, false, true},
		{"prune pending", func(a *unstructured.Unstructured) {
			_ = unstructured.SetNestedSlice(a.Object, []any{map[string]any{"kind": "Deployment"}}, "status", "resources")
		}, false, false},
		{"stale status", func(a *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(a.Object, "old-path", "status", "sync", "comparedTo", "source", "path")
		}, false, false},
		{"sync running", func(a *unstructured.Unstructured) {
			_ = unstructured.SetNestedMap(a.Object, map[string]any{"sync": map[string]any{}}, "operation")
		}, false, false},
		{"prepared", func(a *unstructured.Unstructured) {
			a.SetAnnotations(map[string]string{"suparship.io/validation-idle": "false"})
			unstructured.RemoveNestedField(a.Object, "spec", "syncPolicy", "automated")
		}, true, false},
		{"old active", func(a *unstructured.Unstructured) { a.SetAnnotations(nil) }, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := app.DeepCopy()
			tc.mutate(candidate)
			store, dyn := newKargoStore(t)
			_, err := dyn.Resource(schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}).Namespace("argocd").Create(ctx, candidate, metav1.CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			for _, present := range []bool{true, false} {
				want := tc.clean
				if present {
					want = tc.prepared
				}
				got, err := store.ValidationApplicationsReady(ctx, []string{"validator"}, present)
				if err != nil || got != want {
					t.Fatalf("present=%t: got %t, want %t: %v", present, got, want, err)
				}
			}
		})
	}
}
