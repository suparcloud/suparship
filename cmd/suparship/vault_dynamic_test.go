package main

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/secrets"
	"github.com/suparcloud/suparship/internal/secrets/onepassword"
)

type fakeOrgStore struct{ org *rbac.Org }

func (f *fakeOrgStore) GetOrg(context.Context) (*rbac.Org, error)    { return f.org, nil }
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

// Same contract for the HashiCorp Vault backend: dispatch when configured,
// degrade to k8s when not, and rebuild the client when the config changes.
func TestDynamicVaultStore_DispatchHCVault(t *testing.T) {
	ctx := context.Background()
	k8s := secrets.NewMemVaultStore()
	noSA := func(context.Context, string) (onepassword.SAClient, error) { return nil, nil }
	noToken := func(context.Context) (string, error) { return "", nil }

	hv := secrets.NewMemVaultStore()
	builds := 0
	newHV := func(cfg secrets.HCVaultConfig, token string) (secrets.VaultStore, error) {
		builds++
		return hv, nil
	}
	vaultToken := "root"
	loadHVToken := func(context.Context) (string, error) { return vaultToken, nil }

	org := &fakeOrgStore{org: &rbac.Org{SecretBackend: secrets.BackendConfig{
		Type:  secrets.BackendVault,
		Vault: &secrets.HCVaultConfig{Address: "http://vault:8200"},
	}}}
	d := newDynamicVaultStore(org, k8s, noSA, noToken, slog.Default()).withHCVault(newHV, loadHVToken)

	if got := d.active(ctx); got != secrets.VaultStore(hv) {
		t.Fatal("vault backend should dispatch to the hcvault store")
	}
	// Cached: a second resolve must not rebuild.
	_ = d.active(ctx)
	if builds != 1 {
		t.Errorf("builds = %d after cached resolve, want 1", builds)
	}
	// A config change (address edit) rebuilds.
	org.org.SecretBackend.Vault.Address = "http://vault-2:8200"
	_ = d.active(ctx)
	if builds != 2 {
		t.Errorf("builds = %d after address change, want 2", builds)
	}
	// A token rotation rebuilds.
	vaultToken = "rotated"
	_ = d.active(ctx)
	if builds != 3 {
		t.Errorf("builds = %d after token rotation, want 3", builds)
	}

	// No address → fallback to k8s.
	org.org.SecretBackend.Vault = &secrets.HCVaultConfig{}
	if got := d.active(ctx); got != secrets.VaultStore(k8s) {
		t.Fatal("vault without an address should fall back to the k8s store")
	}
	// No token → fallback to k8s.
	org.org.SecretBackend.Vault = &secrets.HCVaultConfig{Address: "http://vault:8200"}
	d2 := newDynamicVaultStore(org, k8s, noSA, noToken, slog.Default()).
		withHCVault(newHV, func(context.Context) (string, error) { return "", nil })
	if got := d2.active(ctx); got != secrets.VaultStore(k8s) {
		t.Fatal("vault without a token should fall back to the k8s store")
	}
	// Vault selected but not wired (fake mode) → fallback, not panic.
	d3 := newDynamicVaultStore(org, k8s, noSA, noToken, slog.Default())
	if got := d3.active(ctx); got != secrets.VaultStore(k8s) {
		t.Fatal("vault without wiring should fall back to the k8s store")
	}

	// Switching backends at runtime moves the dispatch immediately.
	org.org.SecretBackend = secrets.BackendConfig{Type: secrets.BackendK8s}
	if got := d.active(ctx); got != secrets.VaultStore(k8s) {
		t.Fatal("switch back to k8s should dispatch to the k8s store")
	}
}

// The bug this guards: Vault selected but unusable (no write token, no address,
// client error) used to redirect WRITES to the k8s store. The request returned
// 200, the read came back from that same fallback, and the operator saw their
// secret "saved" while Vault stayed empty — an undetected control failure. Writes
// must now refuse.
func TestDynamicVaultStore_WritesFailClosedWhenVaultUnusable(t *testing.T) {
	ctx := context.Background()
	noSA := func(context.Context, string) (onepassword.SAClient, error) { return nil, nil }
	noToken := func(context.Context) (string, error) { return "", nil }
	scope := secrets.EnvScope("prod")
	clusterScope := secrets.ClusterScope("prod", "eu-1")

	cases := []struct {
		label string
		build func(k8s secrets.VaultStore) *dynamicVaultStore
	}{
		{"no write token", func(k8s secrets.VaultStore) *dynamicVaultStore {
			org := &fakeOrgStore{org: &rbac.Org{SecretBackend: secrets.BackendConfig{
				Type:  secrets.BackendVault,
				Vault: &secrets.HCVaultConfig{Address: "http://vault:8200"},
			}}}
			return newDynamicVaultStore(org, k8s, noSA, noToken, slog.Default()).withHCVault(
				func(secrets.HCVaultConfig, string) (secrets.VaultStore, error) {
					return secrets.NewMemVaultStore(), nil
				},
				func(context.Context) (string, error) { return "", nil },
			)
		}},
		{"no address", func(k8s secrets.VaultStore) *dynamicVaultStore {
			org := &fakeOrgStore{org: &rbac.Org{SecretBackend: secrets.BackendConfig{
				Type:  secrets.BackendVault,
				Vault: &secrets.HCVaultConfig{},
			}}}
			return newDynamicVaultStore(org, k8s, noSA, noToken, slog.Default()).withHCVault(
				func(secrets.HCVaultConfig, string) (secrets.VaultStore, error) {
					return secrets.NewMemVaultStore(), nil
				},
				func(context.Context) (string, error) { return "root", nil },
			)
		}},
		{"client init fails", func(k8s secrets.VaultStore) *dynamicVaultStore {
			org := &fakeOrgStore{org: &rbac.Org{SecretBackend: secrets.BackendConfig{
				Type:  secrets.BackendVault,
				Vault: &secrets.HCVaultConfig{Address: "http://vault:8200"},
			}}}
			return newDynamicVaultStore(org, k8s, noSA, noToken, slog.Default()).withHCVault(
				func(secrets.HCVaultConfig, string) (secrets.VaultStore, error) {
					return nil, errors.New("bad CA bundle")
				},
				func(context.Context) (string, error) { return "root", nil },
			)
		}},
		{"vault config missing", func(k8s secrets.VaultStore) *dynamicVaultStore {
			org := &fakeOrgStore{org: &rbac.Org{SecretBackend: secrets.BackendConfig{
				Type: secrets.BackendVault,
			}}}
			return newDynamicVaultStore(org, k8s, noSA, noToken, slog.Default()).withHCVault(
				func(secrets.HCVaultConfig, string) (secrets.VaultStore, error) {
					return secrets.NewMemVaultStore(), nil
				},
				func(context.Context) (string, error) { return "root", nil },
			)
		}},
		{"backend not wired (fake mode)", func(k8s secrets.VaultStore) *dynamicVaultStore {
			org := &fakeOrgStore{org: &rbac.Org{SecretBackend: secrets.BackendConfig{
				Type:  secrets.BackendVault,
				Vault: &secrets.HCVaultConfig{Address: "http://vault:8200"},
			}}}
			return newDynamicVaultStore(org, k8s, noSA, noToken, slog.Default())
		}},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			k8s := secrets.NewMemVaultStore()
			d := tc.build(k8s)

			// Every mutating op refuses, and identifies itself as a backend
			// availability problem rather than a store-level failure.
			err := d.Upsert(ctx, scope, secrets.TierShared, "", map[string][]byte{"K": []byte("v")})
			if err == nil {
				t.Fatal("Upsert must fail rather than write to the k8s fallback")
			}
			if !errors.Is(err, errBackendUnavailable) {
				t.Errorf("Upsert error not marked unavailable: %v", err)
			}
			// The reported bug was on cluster scope specifically — same refusal.
			if err := d.Upsert(ctx, clusterScope, secrets.TierShared, "", map[string][]byte{"K": []byte("v")}); err == nil {
				t.Error("cluster-scope Upsert must fail too")
			}
			if err := d.EnsureItem(ctx, scope, secrets.TierShared, ""); err == nil {
				t.Error("EnsureItem must fail")
			}
			if err := d.DeleteKey(ctx, scope, secrets.TierShared, "", "K"); err == nil {
				t.Error("DeleteKey must fail — deleting the fallback reports a false success")
			}
			if err := d.CopyItem(ctx, scope, "a", "b"); err == nil {
				t.Error("CopyItem must fail")
			}
			if err := d.DeleteItem(ctx, scope, "a"); err == nil {
				t.Error("DeleteItem must fail")
			}
			// Probe is the connection test: it must report the real problem, not
			// answer "healthy" about a backend nobody selected.
			if err := d.Probe(ctx, scope); err == nil {
				t.Error("Probe must surface the degradation")
			}

			// Nothing leaked into the fallback.
			for _, s := range []secrets.Scope{scope, clusterScope} {
				if keys, _ := k8s.ListKeys(ctx, s, secrets.TierShared, ""); len(keys) != 0 {
					t.Errorf("value written to the k8s fallback for %v: %v", s.Kind, keys)
				}
			}

			// Reads still degrade gracefully: an operator mid-migration needs to
			// see what the old backend holds.
			if _, err := d.ListKeys(ctx, scope, secrets.TierShared, ""); err != nil {
				t.Errorf("ListKeys should read through the fallback, got %v", err)
			}
		})
	}
}

// The healthy path must be unaffected — writes land on the selected backend.
func TestDynamicVaultStore_WritesSucceedWhenVaultUsable(t *testing.T) {
	ctx := context.Background()
	k8s := secrets.NewMemVaultStore()
	hv := secrets.NewMemVaultStore()
	noSA := func(context.Context, string) (onepassword.SAClient, error) { return nil, nil }
	noToken := func(context.Context) (string, error) { return "", nil }

	org := &fakeOrgStore{org: &rbac.Org{SecretBackend: secrets.BackendConfig{
		Type:  secrets.BackendVault,
		Vault: &secrets.HCVaultConfig{Address: "http://vault:8200"},
	}}}
	d := newDynamicVaultStore(org, k8s, noSA, noToken, slog.Default()).withHCVault(
		func(secrets.HCVaultConfig, string) (secrets.VaultStore, error) { return hv, nil },
		func(context.Context) (string, error) { return "root", nil },
	)

	scope := secrets.ClusterScope("prod", "eu-1")
	if err := d.Upsert(ctx, scope, secrets.TierShared, "", map[string][]byte{"K": []byte("v")}); err != nil {
		t.Fatalf("Upsert on a usable vault backend: %v", err)
	}
	if keys, _ := hv.ListKeys(ctx, scope, secrets.TierShared, ""); len(keys) != 1 {
		t.Errorf("value did not land on the vault store: %v", keys)
	}
	if keys, _ := k8s.ListKeys(ctx, scope, secrets.TierShared, ""); len(keys) != 0 {
		t.Errorf("value leaked into the k8s store: %v", keys)
	}
}

// The migrations type-assert LegacyItemMigrator/ItemExporter on the store they
// are handed; when that store is the dynamic wrapper the assertion must hold
// and delegate to the active backend.
func TestDynamicVaultStore_MigratorDelegation(t *testing.T) {
	ctx := context.Background()
	k8s := secrets.NewMemVaultStore()
	noSA := func(context.Context, string) (onepassword.SAClient, error) { return nil, nil }
	org := &fakeOrgStore{org: &rbac.Org{}}
	d := newDynamicVaultStore(org, k8s, noSA, func(context.Context) (string, error) { return "", nil }, slog.Default())

	scope := secrets.GlobalScope()
	if err := d.Upsert(ctx, scope, secrets.TierApp, "api", map[string][]byte{"K": []byte("v")}); err != nil {
		t.Fatal(err)
	}
	data, err := d.ExportItem(ctx, scope, secrets.TierApp, "api")
	if err != nil || string(data["K"]) != "v" {
		t.Errorf("export through wrapper = %v, %v", data, err)
	}
	src := secrets.ItemName(scope, secrets.TierApp, "api")
	if err := d.CopyItem(ctx, scope, src, "renamed"); err != nil {
		t.Errorf("copy through wrapper: %v", err)
	}
	if err := d.DeleteItem(ctx, scope, src); err != nil {
		t.Errorf("delete through wrapper: %v", err)
	}
	if keys, _ := d.ListKeys(ctx, scope, secrets.TierApp, "api"); len(keys) != 0 {
		t.Errorf("item survived delete: %v", keys)
	}
}
