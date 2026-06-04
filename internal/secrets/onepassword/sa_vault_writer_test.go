package onepassword

import (
	"context"
	"fmt"
	"testing"

	"github.com/suparcloud/suparship/internal/secrets"
)

// resolverFor returns a VaultResolver that maps every scope to vaultID.
func resolverFor(vaultID string) VaultResolver {
	return func(secrets.Scope) (string, error) {
		if vaultID == "" {
			return "", fmt.Errorf("no vault")
		}
		return vaultID, nil
	}
}

func TestSAVaultStore_Upsert(t *testing.T) {
	ctx := context.Background()
	client := NewFakeClient()
	client.CreateVault(ctx, "test-vault", "")
	vaults, _ := client.ListVaults(ctx)
	vaultID := vaults[0].ID

	store := NewSAVaultStore(client, resolverFor(vaultID))
	scope := secrets.EnvScope("prod")

	if err := store.Upsert(ctx, scope, secrets.TierApp, "api", map[string][]byte{
		"DB_URL": []byte("postgres://localhost/db"),
		"SECRET": []byte("hunter2"),
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	keys, err := store.ListKeys(ctx, scope, secrets.TierApp, "api")
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("expected 2 keys, got %d", len(keys))
	}
}

func TestSAVaultStore_TierAndAppIsolation(t *testing.T) {
	ctx := context.Background()
	client := NewFakeClient()
	client.CreateVault(ctx, "test-vault", "")
	vaults, _ := client.ListVaults(ctx)
	store := NewSAVaultStore(client, resolverFor(vaults[0].ID))
	scope := secrets.GlobalScope()

	_ = store.Upsert(ctx, scope, secrets.TierShared, "", map[string][]byte{"S": []byte("1")})
	_ = store.Upsert(ctx, scope, secrets.TierApp, "api", map[string][]byte{"A": []byte("1")})
	_ = store.Upsert(ctx, scope, secrets.TierApp, "web", map[string][]byte{"W": []byte("1")})

	// app "api" must not see shared or web keys.
	keys, _ := store.ListKeys(ctx, scope, secrets.TierApp, "api")
	if len(keys) != 1 || keys[0].Key != "A" {
		t.Fatalf("app api isolation broken: %+v", keys)
	}
	shared, _ := store.ListKeys(ctx, scope, secrets.TierShared, "")
	if len(shared) != 1 || shared[0].Key != "S" {
		t.Fatalf("shared isolation broken: %+v", shared)
	}
}

func TestSAVaultStore_DeleteKey(t *testing.T) {
	ctx := context.Background()
	client := NewFakeClient()
	client.CreateVault(ctx, "test-vault", "")
	vaults, _ := client.ListVaults(ctx)
	store := NewSAVaultStore(client, resolverFor(vaults[0].ID))
	scope := secrets.EnvScope("prod")

	_ = store.Upsert(ctx, scope, secrets.TierApp, "api", map[string][]byte{
		"KEEP": []byte("keep"),
		"DROP": []byte("drop"),
	})
	if err := store.DeleteKey(ctx, scope, secrets.TierApp, "api", "DROP"); err != nil {
		t.Fatalf("DeleteKey: %v", err)
	}
	keys, _ := store.ListKeys(ctx, scope, secrets.TierApp, "api")
	if len(keys) != 1 || keys[0].Key != "KEEP" {
		t.Errorf("expected only KEEP, got %+v", keys)
	}
}

func TestSAVaultStore_Probe(t *testing.T) {
	ctx := context.Background()
	client := NewFakeClient()
	v, _ := client.CreateVault(ctx, "test-vault", "")

	ok := NewSAVaultStore(client, resolverFor(v.ID))
	if err := ok.Probe(ctx, secrets.GlobalScope()); err != nil {
		t.Fatalf("Probe existing: %v", err)
	}
	bad := NewSAVaultStore(client, resolverFor("nonexistent"))
	if err := bad.Probe(ctx, secrets.GlobalScope()); err == nil {
		t.Fatal("expected error for nonexistent vault")
	}
}

func TestSAVaultStore_NoVaultResolved(t *testing.T) {
	ctx := context.Background()
	store := NewSAVaultStore(NewFakeClient(), resolverFor(""))
	scope := secrets.GlobalScope()
	if err := store.Upsert(ctx, scope, secrets.TierShared, "", map[string][]byte{"k": []byte("v")}); err == nil {
		t.Error("expected error when no vault resolved")
	}
}
