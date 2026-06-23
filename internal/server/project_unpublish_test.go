package server

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
)

// twoPhasePublisher records the order of unpublish calls.
type twoPhasePublisher struct {
	mu    sync.Mutex
	calls []string
}

func (p *twoPhasePublisher) record(call string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, call)
}

func (p *twoPhasePublisher) recorded() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...)
}

func (p *twoPhasePublisher) PublishApp(_ context.Context, _ *domain.App, _ []*domain.AppEnvironment) error {
	return nil
}
func (p *twoPhasePublisher) PublishAppEnv(_ context.Context, _ *domain.App, _ *domain.AppEnvironment) error {
	return nil
}
func (p *twoPhasePublisher) PublishAppPreview(_ context.Context, _ *domain.App, _ *domain.EnvironmentInstance, _ string) error {
	return nil
}
func (p *twoPhasePublisher) UnpublishApp(_ context.Context, _, _ string) error { return nil }
func (p *twoPhasePublisher) UnpublishProjectApps(_ context.Context, _ string) error {
	p.record("apps")
	return nil
}
func (p *twoPhasePublisher) UnpublishProjectInfra(_ context.Context, _ string) error {
	p.record("infra")
	return nil
}

// countdownCounter returns decreasing app counts until it reaches zero.
type countdownCounter struct {
	mu     sync.Mutex
	counts []int
}

func (c *countdownCounter) CountProjectApplications(_ context.Context, _ string) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.counts) == 0 {
		return 0, nil
	}
	n := c.counts[0]
	c.counts = c.counts[1:]
	return n, nil
}

func withFastUnpublishTimings(t *testing.T, timeout time.Duration) {
	t.Helper()
	origPoll, origTimeout, origGrace := unpublishPollInterval, unpublishPruneTimeout, unpublishGraceDelay
	unpublishPollInterval = time.Millisecond
	unpublishPruneTimeout = timeout
	unpublishGraceDelay = 5 * time.Millisecond
	t.Cleanup(func() {
		unpublishPollInterval, unpublishPruneTimeout, unpublishGraceDelay = origPoll, origTimeout, origGrace
	})
}

// TestUnpublishProjectTwoPhase_WaitsForPrune verifies the AppProject is only
// removed after the project's Applications are gone, and that apps go first.
func TestUnpublishProjectTwoPhase_WaitsForPrune(t *testing.T) {
	withFastUnpublishTimings(t, time.Second)
	pub := &twoPhasePublisher{}
	counter := &countdownCounter{counts: []int{2, 1}} // then 0 forever

	pruned := false
	unpublishProjectTwoPhase(context.Background(), pub, counter, "demo", func() { pruned = true })

	if got := pub.recorded(); len(got) != 2 || got[0] != "apps" || got[1] != "infra" {
		t.Fatalf("expected [apps infra], got %v", got)
	}
	if !pruned {
		t.Error("expected afterPrune to run after phase 2 completed")
	}
}

// TestUnpublishProjectTwoPhase_TimeoutKeepsAppProject verifies the AppProject
// is kept when Applications never finish pruning — a dangling AppProject is
// benign, stuck Applications are not.
func TestUnpublishProjectTwoPhase_TimeoutKeepsAppProject(t *testing.T) {
	withFastUnpublishTimings(t, 10*time.Millisecond)
	pub := &twoPhasePublisher{}
	counter := &countdownCounter{counts: func() []int {
		c := make([]int, 1000)
		for i := range c {
			c[i] = 1 // never reaches zero within the timeout
		}
		return c
	}()}

	pruned := false
	unpublishProjectTwoPhase(context.Background(), pub, counter, "demo", func() { pruned = true })

	if got := pub.recorded(); len(got) != 1 || got[0] != "apps" {
		t.Fatalf("expected only [apps] on timeout, got %v", got)
	}
	if pruned {
		t.Error("afterPrune must NOT run when phase 2 didn't complete (workloads still present)")
	}
}

// TestUnpublishProjectTwoPhase_NoCounterFallsBackToGraceDelay verifies both
// phases run when no ArgoCD reader is wired.
func TestUnpublishProjectTwoPhase_NoCounterFallsBackToGraceDelay(t *testing.T) {
	withFastUnpublishTimings(t, time.Second)
	pub := &twoPhasePublisher{}

	pruned := false
	unpublishProjectTwoPhase(context.Background(), pub, nil, "demo", func() { pruned = true })

	if got := pub.recorded(); len(got) != 2 || got[0] != "apps" || got[1] != "infra" {
		t.Fatalf("expected [apps infra], got %v", got)
	}
	if !pruned {
		t.Error("expected afterPrune to run after phase 2 completed")
	}
}
