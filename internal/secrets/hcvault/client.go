// Package hcvault implements the secrets.VaultStore backend for HashiCorp
// Vault (the product — not to be confused with suparship's own "vault"
// concept, the per-scope item container named by secrets.VaultName).
//
// Layout: everything lives in ONE KV v2 mount. A suparship vault is a path
// prefix inside it and an item is the KV entry underneath, so the full path of
// an item is
//
//	{mount}/{VaultName(scope)}/{ItemName(scope, tier, app)}
//
// e.g. suparship/suparship-secrets-env-prod/acme-web-env-prod. Because the
// containers are derived paths there is nothing to provision per scope —
// unlike 1Password, where each vault is registered by an operator.
package hcvault

import (
	"context"
	"sort"
	"sync"

	"github.com/suparcloud/suparship/internal/secrets"
)

// Client is the narrow surface the store needs from a KV v2 mount. Paths are
// relative to the mount. Implemented by APIClient (real Vault) and FakeClient
// (tests).
//
// Versions drive optimistic concurrency: ReadItem reports the item's current
// version (0 = absent) and WriteItem submits it as the check-and-set
// precondition, so two concurrent read-modify-write cycles cannot silently
// drop each other's keys — the loser gets secrets.ErrStaleVersion, same as the
// k8s backend's resourceVersion conflict.
type Client interface {
	// ReadItem returns the item's data and current version. An absent item is
	// (nil, 0, nil) — not an error.
	ReadItem(ctx context.Context, path string) (data map[string][]byte, version int, err error)
	// WriteItem replaces the item's data, requiring the current version to
	// still be cas (0 = the item must not exist). A precondition failure is
	// secrets.ErrStaleVersion.
	WriteItem(ctx context.Context, path string, data map[string][]byte, cas int) error
	// DeleteItem permanently removes the item — metadata and every version.
	// No-op when absent.
	DeleteItem(ctx context.Context, path string) error
	// Probe verifies the address, token and mount are usable.
	Probe(ctx context.Context) error
}

// FakeClient is an in-memory Client for tests, with error injection and real
// check-and-set enforcement so the store's read-modify-write discipline is
// actually exercised rather than assumed.
type FakeClient struct {
	mu    sync.Mutex
	items map[string]fakeItem

	// Error injection: returned verbatim by the corresponding method.
	ReadErr   error
	WriteErr  error
	DeleteErr error
	ProbeErr  error
}

type fakeItem struct {
	data    map[string][]byte
	version int
}

// NewFakeClient creates an empty FakeClient.
func NewFakeClient() *FakeClient {
	return &FakeClient{items: map[string]fakeItem{}}
}

var _ Client = (*FakeClient)(nil)

func (f *FakeClient) ReadItem(_ context.Context, path string) (map[string][]byte, int, error) {
	if f.ReadErr != nil {
		return nil, 0, f.ReadErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.items[path]
	if !ok {
		return nil, 0, nil
	}
	out := make(map[string][]byte, len(it.data))
	for k, v := range it.data {
		out[k] = append([]byte(nil), v...)
	}
	return out, it.version, nil
}

func (f *FakeClient) WriteItem(_ context.Context, path string, data map[string][]byte, cas int) error {
	if f.WriteErr != nil {
		return f.WriteErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	cur := f.items[path] // zero value: version 0 = absent
	if cur.version != cas {
		return secrets.ErrStaleVersion
	}
	stored := make(map[string][]byte, len(data))
	for k, v := range data {
		stored[k] = append([]byte(nil), v...)
	}
	f.items[path] = fakeItem{data: stored, version: cur.version + 1}
	return nil
}

func (f *FakeClient) DeleteItem(_ context.Context, path string) error {
	if f.DeleteErr != nil {
		return f.DeleteErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.items, path)
	return nil
}

func (f *FakeClient) Probe(_ context.Context) error { return f.ProbeErr }

// Paths returns every stored item path, sorted — test helper.
func (f *FakeClient) Paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.items))
	for p := range f.items {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
