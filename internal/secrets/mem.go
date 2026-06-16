package secrets

import (
	"context"
	"sort"
	"sync"
)

// MemVaultStore is an in-memory VaultStore for development and testing.
// All data is lost when the process exits. Items are keyed by
// VaultName(scope)/ItemName(scope, tier, app), giving the same per-scope,
// per-tier, per-app isolation as the real backends.
type MemVaultStore struct {
	mu    sync.Mutex
	items map[string]map[string][]byte // "<vault>/<item>" -> key -> value
}

// NewMemVaultStore creates an empty in-memory vault store.
func NewMemVaultStore() *MemVaultStore {
	return &MemVaultStore{items: make(map[string]map[string][]byte)}
}

var _ VaultStore = (*MemVaultStore)(nil)

func memItemKey(scope Scope, tier Tier, app string) string {
	return VaultName(scope) + "/" + ItemName(scope, tier, app)
}

func (m *MemVaultStore) Upsert(_ context.Context, scope Scope, tier Tier, app string, data map[string][]byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := memItemKey(scope, tier, app)
	if m.items[key] == nil {
		m.items[key] = make(map[string][]byte)
	}
	for k, v := range data {
		m.items[key][k] = v
	}
	return nil
}

func (m *MemVaultStore) EnsureItem(_ context.Context, scope Scope, tier Tier, app string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := memItemKey(scope, tier, app)
	if m.items[key] == nil {
		m.items[key] = make(map[string][]byte)
	}
	return nil
}

func (m *MemVaultStore) ListKeys(_ context.Context, scope Scope, tier Tier, app string) ([]SecretEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item := m.items[memItemKey(scope, tier, app)]
	keys := make([]string, 0, len(item))
	for k := range item {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	entries := make([]SecretEntry, len(keys))
	for i, k := range keys {
		entries[i] = SecretEntry{Key: k}
	}
	return entries, nil
}

func (m *MemVaultStore) DeleteKey(_ context.Context, scope Scope, tier Tier, app, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if item := m.items[memItemKey(scope, tier, app)]; item != nil {
		delete(item, key)
	}
	return nil
}

func (m *MemVaultStore) Probe(_ context.Context, _ Scope) error { return nil }
