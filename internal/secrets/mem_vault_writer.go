package secrets

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"sync"
)

// MemVaultWriter implements VaultWriter in-memory for development and testing.
type MemVaultWriter struct {
	mu      sync.Mutex
	items   map[string]*memItem // keyed by "binding.VaultID/scopeKey"
	version int64
}

type memItem struct {
	data    map[string][]byte
	version string
}

// NewMemVaultWriter creates an empty in-memory vault writer.
func NewMemVaultWriter() *MemVaultWriter {
	return &MemVaultWriter{items: make(map[string]*memItem)}
}

func scopeKey(scope Scope) string {
	return fmt.Sprintf("%s/%s/%s/%s/%s", scope.Level, scope.Org, scope.Env, scope.Project, scope.App)
}

func itemKey(binding EnvBinding, scope Scope) string {
	return binding.VaultID + "/" + scopeKey(scope)
}

func (w *MemVaultWriter) nextVersion() string {
	w.version++
	return strconv.FormatInt(w.version, 10)
}

func (w *MemVaultWriter) Upsert(_ context.Context, binding EnvBinding, scope Scope, expectedVersion string, data map[string][]byte) (ItemMeta, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	key := itemKey(binding, scope)
	item, exists := w.items[key]
	if exists && expectedVersion != "" && item.version != expectedVersion {
		return ItemMeta{}, ErrStaleVersion
	}

	if !exists {
		item = &memItem{data: make(map[string][]byte)}
		w.items[key] = item
	}
	for k, v := range data {
		item.data[k] = v
	}
	item.version = w.nextVersion()
	return ItemMeta{Version: item.version}, nil
}

func (w *MemVaultWriter) ListKeys(_ context.Context, binding EnvBinding, scope Scope) ([]SecretEntry, ItemMeta, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	key := itemKey(binding, scope)
	item, exists := w.items[key]
	if !exists {
		return nil, ItemMeta{}, nil
	}

	keys := make([]string, 0, len(item.data))
	for k := range item.data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	entries := make([]SecretEntry, len(keys))
	for i, k := range keys {
		entries[i] = SecretEntry{Key: k}
	}
	return entries, ItemMeta{Version: item.version}, nil
}

func (w *MemVaultWriter) DeleteKey(_ context.Context, binding EnvBinding, scope Scope, key, expectedVersion string) (ItemMeta, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	ikey := itemKey(binding, scope)
	item, exists := w.items[ikey]
	if !exists {
		return ItemMeta{}, nil
	}
	if expectedVersion != "" && item.version != expectedVersion {
		return ItemMeta{}, ErrStaleVersion
	}
	delete(item.data, key)
	item.version = w.nextVersion()
	return ItemMeta{Version: item.version}, nil
}

func (w *MemVaultWriter) Probe(_ context.Context, _ EnvBinding) error {
	return nil
}
