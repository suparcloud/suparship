package server

import (
	"context"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
)

type fakePipelineReader struct {
	statuses []KargoStageStatusResult
}

func (f *fakePipelineReader) ListAppStageStatuses(_ context.Context, _, _ string) ([]KargoStageStatusResult, error) {
	return f.statuses, nil
}

// autoPromoteFixture wires an appHandler with a healthy staging→prod pipeline
// where staging runs fr-2 and prod still runs fr-1 — a promotion is due.
func autoPromoteFixture(t *testing.T, autoPromote bool) (*appHandler, *memAppStore, *recordingPromoter, *fakePipelineReader) {
	t.Helper()
	store := newMemAppStore()
	store.mu.Lock()
	store.apps[testProject] = make(map[string]*domain.App)
	store.mu.Unlock()

	app := promoteTestApp(testProject)
	app.Spec.CD = domain.CDConfig{Managed: true, AutoPromote: autoPromote}
	store.addApp(app)
	seedFullPromotionChain(store, testProject)

	promoter := &recordingPromoter{}
	pipeline := &fakePipelineReader{statuses: []KargoStageStatusResult{
		{EnvName: "staging", Phase: "Steady", Health: "Healthy", CurrentFreight: "fr-2"},
		{EnvName: "prod", Phase: "Steady", Health: "Healthy", CurrentFreight: "fr-1"},
	}}

	ah := newAppHandler(store, nil, nil, nil)
	ah.gitOpsPublisher = &recordingPublisher{}
	ah.kargoPromoter = promoter
	ah.kargoPipelineReader = pipeline
	return ah, store, promoter, pipeline
}

func storedApp(t *testing.T, store *memAppStore) *domain.App {
	t.Helper()
	app, err := store.GetApp(context.Background(), testProject, "my-app")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	return app
}

// When staging runs newer freight than prod, an opted-in app is promoted once;
// the retry cooldown stops the same freight from being promoted again next tick,
// but NEW freight promotes immediately.
func TestAutoPromote_PromotesOnceThenCoolsDown(t *testing.T) {
	ah, store, promoter, pipeline := autoPromoteFixture(t, true)
	ctx := context.Background()

	ah.autoPromoteApp(ctx, storedApp(t, store))
	if promoter.calls != 1 {
		t.Fatalf("expected 1 promotion, got %d", promoter.calls)
	}

	// Same tick conditions again (promotion hasn't landed yet) → cooldown holds.
	ah.autoPromoteApp(ctx, storedApp(t, store))
	if promoter.calls != 1 {
		t.Fatalf("expected cooldown to hold at 1 promotion, got %d", promoter.calls)
	}

	// Newer freight in staging → promotes despite the earlier attempt.
	pipeline.statuses[0].CurrentFreight = "fr-3"
	ah.autoPromoteApp(ctx, storedApp(t, store))
	if promoter.calls != 2 {
		t.Fatalf("expected new freight to promote (2 total), got %d", promoter.calls)
	}
}

// The reconciler skips: apps that didn't opt in, held (pinned/rolled-back) or
// decommissioned targets, held sources, unhealthy or mid-promotion stages, and
// targets already running the source's freight.
func TestAutoPromote_Guards(t *testing.T) {
	cases := []struct {
		name  string
		setup func(ah *appHandler, store *memAppStore, pipeline *fakePipelineReader)
	}{
		{"not opted in", func(ah *appHandler, store *memAppStore, _ *fakePipelineReader) {
			app := storedApp(t, store)
			app.Spec.CD.AutoPromote = false
		}},
		{"target pinned", func(_ *appHandler, store *memAppStore, _ *fakePipelineReader) {
			app := storedApp(t, store)
			app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{
				"prod": {PinnedFrom: "rollback"},
			}
		}},
		{"target decommissioned", func(_ *appHandler, store *memAppStore, _ *fakePipelineReader) {
			off := false
			app := storedApp(t, store)
			app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{
				"prod": {Deploy: &off},
			}
		}},
		{"source pinned", func(_ *appHandler, store *memAppStore, _ *fakePipelineReader) {
			app := storedApp(t, store)
			app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{
				"staging": {PinnedFrom: "pr-1", PinnedImageTag: "pr-1-abc"},
			}
		}},
		{"source unhealthy", func(_ *appHandler, _ *memAppStore, pipeline *fakePipelineReader) {
			pipeline.statuses[0].Health = "Unhealthy"
		}},
		{"source promoting", func(_ *appHandler, _ *memAppStore, pipeline *fakePipelineReader) {
			pipeline.statuses[0].Phase = "Promoting"
		}},
		{"target promoting", func(_ *appHandler, _ *memAppStore, pipeline *fakePipelineReader) {
			pipeline.statuses[1].Phase = "Promoting"
		}},
		{"already caught up", func(_ *appHandler, _ *memAppStore, pipeline *fakePipelineReader) {
			pipeline.statuses[1].CurrentFreight = "fr-2"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ah, store, promoter, pipeline := autoPromoteFixture(t, true)
			tc.setup(ah, store, pipeline)
			ah.autoPromoteApp(context.Background(), storedApp(t, store))
			if promoter.calls != 0 {
				t.Fatalf("expected no promotion, got %d", promoter.calls)
			}
		})
	}
}
