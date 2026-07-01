package main

import (
	"context"
	"testing"

	"github.com/suparcloud/suparship/internal/secrets"
)

// countingVault records how many times each (scope,tier,app) key hits the
// underlying EnsureItem/ListKeys, so a test can assert the cache dedupes.
type countingVault struct {
	ensure map[string]int
	list   map[string]int
}

func newCountingVault() *countingVault {
	return &countingVault{ensure: map[string]int{}, list: map[string]int{}}
}

func (v *countingVault) Upsert(context.Context, secrets.Scope, secrets.Tier, string, map[string][]byte) error {
	return nil
}
func (v *countingVault) EnsureItem(_ context.Context, scope secrets.Scope, tier secrets.Tier, app string) error {
	v.ensure[vaultKey(scope, tier, app)]++
	return nil
}
func (v *countingVault) ListKeys(_ context.Context, scope secrets.Scope, tier secrets.Tier, app string) ([]secrets.SecretEntry, error) {
	v.list[vaultKey(scope, tier, app)]++
	return nil, nil
}
func (v *countingVault) DeleteKey(context.Context, secrets.Scope, secrets.Tier, string, string) error {
	return nil
}
func (v *countingVault) Probe(context.Context, secrets.Scope) error { return nil }

// TestCachedVault_DedupesSharedScopeLookups verifies the request-scoped cache
// collapses repeated identical EnsureItem/ListKeys calls (the shared project/
// stack scopes a stack fan-out would otherwise re-list once per member) to one
// underlying call, while keeping distinct keys separate.
func TestCachedVault_DedupesSharedScopeLookups(t *testing.T) {
	cv := newCountingVault()
	c := newCachedVault(cv)
	ctx := context.Background()

	stack := secrets.StackScope("voiceai", "lk-sh")
	proj := secrets.ProjectScope("voiceai")
	for i := 0; i < 6; i++ { // simulate 6 members hitting the same shared scopes
		_ = c.EnsureItem(ctx, stack, secrets.TierShared, "")
		_, _ = c.ListKeys(ctx, proj, secrets.TierShared, "")
	}
	if got := cv.ensure[vaultKey(stack, secrets.TierShared, "")]; got != 1 {
		t.Errorf("shared-scope EnsureItem hit the backend %d times, want 1", got)
	}
	if got := cv.list[vaultKey(proj, secrets.TierShared, "")]; got != 1 {
		t.Errorf("shared-scope ListKeys hit the backend %d times, want 1", got)
	}

	// A distinct per-app key is not deduped against the shared ones.
	_ = c.EnsureItem(ctx, secrets.GlobalScope(), secrets.TierApp, "lk-sh-web")
	_ = c.EnsureItem(ctx, secrets.GlobalScope(), secrets.TierApp, "lk-sh-web")
	if got := cv.ensure[vaultKey(secrets.GlobalScope(), secrets.TierApp, "lk-sh-web")]; got != 1 {
		t.Errorf("distinct per-app EnsureItem hit the backend %d times, want 1", got)
	}
}
