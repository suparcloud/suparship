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

var _ ItemExporter = (*MemVaultStore)(nil)

// ExportItem implements ItemExporter. Absent item → (nil, nil).
func (m *MemVaultStore) ExportItem(_ context.Context, scope Scope, tier Tier, app string) (map[string][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item := m.items[memItemKey(scope, tier, app)]
	if item == nil {
		return nil, nil
	}
	out := make(map[string][]byte, len(item))
	for k, v := range item {
		out[k] = append([]byte(nil), v...)
	}
	return out, nil
}

var _ LegacyItemMigrator = (*MemVaultStore)(nil)

// CopyItem implements LegacyItemMigrator.
func (m *MemVaultStore) CopyItem(_ context.Context, scope Scope, fromName, toName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	src := m.items[VaultName(scope)+"/"+fromName]
	if src == nil {
		return nil
	}
	dstKey := VaultName(scope) + "/" + toName
	if m.items[dstKey] == nil {
		m.items[dstKey] = make(map[string][]byte)
	}
	for k, v := range src {
		m.items[dstKey][k] = v
	}
	return nil
}

// DeleteItem implements LegacyItemMigrator.
func (m *MemVaultStore) DeleteItem(_ context.Context, scope Scope, itemName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, VaultName(scope)+"/"+itemName)
	return nil
}
