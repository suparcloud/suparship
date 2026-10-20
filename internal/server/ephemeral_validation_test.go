package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
)

func TestEphemeralValidationLifecycle(t *testing.T) {
	now := time.Now()
	run := domain.ValidationRun{}
	obs := domain.ValidationObservation{}
	active := false
	present := false
	promotions := 0
	nextCalls := 0
	actions := validationRunActions{
		next:      func() (string, time.Time, error) { nextCalls++; return "new", now, nil },
		workloads: func(on bool) error { active = on; return nil },
		ready:     func(want bool) (bool, error) { return present == want, nil },
		promote:   func() error { promotions++; return nil },
	}
	tick := func(want string) {
		t.Helper()
		if err := advanceValidationRun(context.Background(), &run, obs, now, actions); err != nil {
			t.Fatal(err)
		}
		if run.Phase != want {
			t.Fatalf("phase=%s want %s", run.Phase, want)
		}
	}
	tick("cleaning")
	tick("deleting")
	tick("idle")
	actions.paused = true
	tick("idle")
	if nextCalls != 0 {
		t.Fatal("suspended validation discovered new work")
	}
	actions.paused = false
	tick("provisioning")
	tick("waiting")
	if !active {
		t.Fatal("workloads not provisioned")
	}
	tick("waiting")
	if promotions != 0 {
		t.Fatal("promoted before Application existed")
	}
	present = true
	tick("running")
	if promotions != 1 {
		t.Fatal("missing promotion")
	}
	// Simulate a restart: progress is the serialized state, not process memory.
	copy := run
	run = copy
	obs.Active = true
	obs.Verified = true
	tick("running")
	obs.Active = false
	obs.Verified = false
	tick("running")
	obs.Verified = true
	tick("cleaning")
	tick("deleting")
	if active {
		t.Fatal("successful workloads retained")
	}
	tick("deleting")
	if nextCalls != 1 {
		t.Fatal("started next run before deletion")
	}
	present = false
	tick("idle")
	if run.Cursor != now || run.Freight != "" {
		t.Fatal("lost cursor or retained active freight")
	}
}

func TestFailedValidationRetentionAndCleanupRetry(t *testing.T) {
	now := time.Now()
	run := domain.ValidationRun{Phase: "running", Freight: "bad", StartedAt: now}
	obs := domain.ValidationObservation{Failed: true}
	actions := validationRunActions{workloads: func(bool) error { return errors.New("git unavailable") }}
	for _, elapsed := range []time.Duration{0, time.Hour - time.Second} {
		if err := advanceValidationRun(context.Background(), &run, obs, now.Add(elapsed), actions); err != nil {
			t.Fatal(err)
		}
		if run.Phase != "failed" {
			t.Fatal("failed run removed before debug retention expired")
		}
	}
	if err := advanceValidationRun(context.Background(), &run, obs, now.Add(time.Hour), actions); err != nil {
		t.Fatal(err)
	}
	if run.Phase != "cleaning" {
		t.Fatal("failure did not expire")
	}
	if err := advanceValidationRun(context.Background(), &run, obs, now.Add(time.Hour), actions); err == nil || run.Phase != "cleaning" {
		t.Fatal("failed cleanup was not retryable")
	}
}

func TestValidationAdoptsExistingDeployment(t *testing.T) {
	now := time.Now()
	run := domain.ValidationRun{}
	obs := domain.ValidationObservation{CurrentFreight: "old", CurrentCreatedAt: now.Add(-time.Hour)}
	if err := advanceValidationRun(context.Background(), &run, obs, now, validationRunActions{}); err != nil {
		t.Fatal(err)
	}
	if run.Phase != "running" || run.Freight != "old" || !run.Cursor.Equal(obs.CurrentCreatedAt) {
		t.Fatalf("unsafe adoption: %+v", run)
	}
}

func TestValidationPreparationTimeoutRetainsThenCleans(t *testing.T) {
	now := time.Now()
	run := domain.ValidationRun{Phase: "waiting", Freight: "pending", StartedAt: now.Add(-30 * time.Minute)}
	if err := advanceValidationRun(context.Background(), &run, domain.ValidationObservation{}, now, validationRunActions{}); err != nil {
		t.Fatal(err)
	}
	if run.Phase != "failed" || !run.FailedAt.Equal(now) {
		t.Fatalf("unbounded preparation: %+v", run)
	}
	if err := advanceValidationRun(context.Background(), &run, domain.ValidationObservation{}, now.Add(time.Hour), validationRunActions{}); err != nil {
		t.Fatal(err)
	}
	if run.Phase != "cleaning" {
		t.Fatal("timed out preparation never cleaned up")
	}
}

func TestDeletingValidationRepairsDormantStateBeforeWaiting(t *testing.T) {
	run := domain.ValidationRun{Phase: "deleting"}
	repaired := false
	actions := validationRunActions{
		workloads: func(active bool) error {
			if active {
				t.Fatal("cleanup activated workloads")
			}
			repaired = true
			return nil
		},
		ready: func(present bool) (bool, error) {
			if !repaired || present {
				t.Fatal("checked deletion before repairing dormant state")
			}
			return false, nil
		},
	}
	if err := advanceValidationRun(context.Background(), &run, domain.ValidationObservation{}, time.Now(), actions); err != nil {
		t.Fatal(err)
	}
	if run.Phase != "deleting" {
		t.Fatal("finished before Applications disappeared")
	}
}

func TestIdleValidationMigratesWithoutRevalidatingFreight(t *testing.T) {
	cursor := time.Now()
	run := domain.ValidationRun{Phase: "idle", Cursor: cursor}
	actions := validationRunActions{next: func() (string, time.Time, error) {
		t.Fatal("migration discovered a new release")
		return "", time.Time{}, nil
	}}
	if err := advanceValidationRun(context.Background(), &run, domain.ValidationObservation{}, cursor, actions); err != nil {
		t.Fatal(err)
	}
	if run.Phase != "cleaning" || !run.Cursor.Equal(cursor) {
		t.Fatalf("migration lost release history: %+v", run)
	}
}

func TestValidationRefreshOnlyWhileWaitingForActivationOrVerification(t *testing.T) {
	for _, phase := range []string{"idle", "provisioning", "waiting", "running", "failed", "cleaning", "deleting"} {
		if got := validationNeedsRefresh(phase); got != (phase == "waiting" || phase == "running") {
			t.Fatalf("unexpected refresh in %s", phase)
		}
	}
}
