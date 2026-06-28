package main

import (
	"context"
	"log/slog"
	"testing"

	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/secrets"
	"github.com/suparcloud/suparship/internal/secrets/onepassword"
)

type fakeOrgStore struct{ org *rbac.Org }

func (f *fakeOrgStore) GetOrg(context.Context) (*rbac.Org, error)  { return f.org, nil }
func (f *fakeOrgStore) SaveOrg(_ context.Context, o *rbac.Org) error { f.org = o; return nil }

// The dynamic vault store must dispatch to the backend the CURRENT org config
// selects — so switching the backend at runtime takes effect immediately.
func TestDynamicVaultStore_Dispatch(t *testing.T) {
	k8s := secrets.NewMemVaultStore()
	noSA := func(context.Context, string) (onepassword.SAClient, error) { return nil, nil }

	// Default org → k8s backend → k8s store.
	org := &fakeOrgStore{org: &rbac.Org{}}
	d := newDynamicVaultStore(org, k8s, noSA, func(context.Context) (string, error) { return "tok", nil }, slog.Default())
	if got := d.active(context.Background()); got != secrets.VaultStore(k8s) {
		t.Fatalf("k8s backend should dispatch to the k8s store")
	}

	// Switch to 1Password (with a token) → SA store, not k8s.
	org.org.SecretBackend = secrets.BackendConfig{Type: secrets.Backend1Password, OnePassword: &secrets.OnePasswordConfig{}}
	if got := d.active(context.Background()); got == secrets.VaultStore(k8s) {
		t.Fatalf("1Password backend should dispatch to the 1Password store")
	}

	// 1Password selected but no token → safe fallback to k8s.
	org2 := &fakeOrgStore{org: &rbac.Org{SecretBackend: secrets.BackendConfig{
		Type: secrets.Backend1Password, OnePassword: &secrets.OnePasswordConfig{},
	}}}
	d2 := newDynamicVaultStore(org2, k8s, noSA, func(context.Context) (string, error) { return "", nil }, slog.Default())
	if got := d2.active(context.Background()); got != secrets.VaultStore(k8s) {
		t.Fatalf("1Password without a token should fall back to the k8s store")
	}
}
