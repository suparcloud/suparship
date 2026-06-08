package rbac

import (
	"context"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// conflictStore is an OrgStore stub that returns a Conflict on the first N
// SaveOrg calls, then succeeds — to exercise MutateOrg's retry loop.
type conflictStore struct {
	org            *Org
	conflictsLeft  int
	getCalls       int
	saveCalls      int
}

func (s *conflictStore) GetOrg(_ context.Context) (*Org, error) {
	s.getCalls++
	cp := *s.org
	return &cp, nil
}

func (s *conflictStore) SaveOrg(_ context.Context, org *Org) error {
	s.saveCalls++
	if s.conflictsLeft > 0 {
		s.conflictsLeft--
		return apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, "org", nil)
	}
	s.org = org
	return nil
}

func TestMutateOrg_RetriesOnConflict(t *testing.T) {
	store := &conflictStore{org: &Org{Name: "acme"}, conflictsLeft: 2}
	calls := 0
	err := MutateOrg(context.Background(), store, func(o *Org) error {
		calls++
		o.DisplayName = "Acme Inc"
		return nil
	})
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	// 2 conflicts + 1 success = 3 attempts; fn re-applied each attempt.
	if store.saveCalls != 3 || calls != 3 || store.getCalls != 3 {
		t.Errorf("expected 3 attempts (get/fn/save), got get=%d fn=%d save=%d", store.getCalls, calls, store.saveCalls)
	}
	if store.org.DisplayName != "Acme Inc" {
		t.Errorf("mutation not applied: %+v", store.org)
	}
}

func TestMutateOrg_GivesUpAfterMaxConflicts(t *testing.T) {
	store := &conflictStore{org: &Org{Name: "acme"}, conflictsLeft: 99}
	err := MutateOrg(context.Background(), store, func(o *Org) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "conflict retries") {
		t.Fatalf("expected failure after exhausting retries, got %v", err)
	}
	if !apierrors.IsConflict(err) { // the wrapped cause is still a conflict
		t.Errorf("wrapped error should remain a conflict: %v", err)
	}
}

// Sanity: a non-conflict error is returned immediately, not retried.
func TestMutateOrg_NonConflictNotRetried(t *testing.T) {
	store := &errStore{}
	err := MutateOrg(context.Background(), store, func(o *Org) error { return nil })
	if err == nil {
		t.Fatal("expected the store's error")
	}
	if store.saveCalls != 1 {
		t.Errorf("non-conflict error should not retry, saveCalls=%d", store.saveCalls)
	}
}

type errStore struct{ saveCalls int }

func (s *errStore) GetOrg(_ context.Context) (*Org, error) { return &Org{Name: "x"}, nil }
func (s *errStore) SaveOrg(_ context.Context, _ *Org) error {
	s.saveCalls++
	return apierrors.NewInternalError(errInternal{})
}

type errInternal struct{}

func (errInternal) Error() string { return "boom" }
