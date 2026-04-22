package onepassword

import (
	"context"
	"testing"

	"github.com/suparcloud/suparship/internal/secrets"
)

func TestSAVaultWriter_Upsert(t *testing.T) {
	ctx := context.Background()
	client := NewFakeClient()
	client.CreateVault(ctx, "test-vault", "")
	vaults, _ := client.ListVaults(ctx)
	vaultID := vaults[0].ID

	writer := NewSAVaultWriter(client)
	binding := secrets.EnvBinding{Env: "prod", VaultID: vaultID}
	scope := secrets.Scope{Level: "app", Org: "acme", Project: "web", App: "api", Env: "prod"}

	data := map[string][]byte{
		"DB_URL": []byte("postgres://localhost/db"),
		"SECRET": []byte("hunter2"),
	}

	meta, err := writer.Upsert(ctx, binding, scope, "", data)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if meta.Version == "" {
		t.Error("expected non-empty version")
	}
}

func TestSAVaultWriter_ListKeys(t *testing.T) {
	ctx := context.Background()
	client := NewFakeClient()
	client.CreateVault(ctx, "test-vault", "")
	vaults, _ := client.ListVaults(ctx)
	vaultID := vaults[0].ID

	writer := NewSAVaultWriter(client)
	binding := secrets.EnvBinding{Env: "prod", VaultID: vaultID}
	scope := secrets.Scope{Level: "app", Org: "acme", Project: "web", App: "api", Env: "prod"}

	// No item yet → empty list.
	keys, _, err := writer.ListKeys(ctx, binding, scope)
	if err != nil {
		t.Fatalf("ListKeys empty: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("expected 0 keys, got %d", len(keys))
	}

	// Upsert, then list.
	writer.Upsert(ctx, binding, scope, "", map[string][]byte{
		"A": []byte("1"),
		"B": []byte("2"),
	})

	keys, meta, err := writer.ListKeys(ctx, binding, scope)
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("expected 2 keys, got %d", len(keys))
	}
	if meta.Version == "" {
		t.Error("expected non-empty version")
	}
}

func TestSAVaultWriter_DeleteKey(t *testing.T) {
	ctx := context.Background()
	client := NewFakeClient()
	client.CreateVault(ctx, "test-vault", "")
	vaults, _ := client.ListVaults(ctx)
	vaultID := vaults[0].ID

	writer := NewSAVaultWriter(client)
	binding := secrets.EnvBinding{Env: "prod", VaultID: vaultID}
	scope := secrets.Scope{Level: "app", Org: "acme", Env: "prod"}

	writer.Upsert(ctx, binding, scope, "", map[string][]byte{
		"KEEP": []byte("keep"),
		"DROP": []byte("drop"),
	})

	_, err := writer.DeleteKey(ctx, binding, scope, "DROP", "")
	if err != nil {
		t.Fatalf("DeleteKey: %v", err)
	}

	keys, _, err := writer.ListKeys(ctx, binding, scope)
	if err != nil {
		t.Fatalf("ListKeys after delete: %v", err)
	}
	if len(keys) != 1 {
		t.Errorf("expected 1 key after delete, got %d", len(keys))
	}
	if keys[0].Key != "KEEP" {
		t.Errorf("expected key KEEP, got %q", keys[0].Key)
	}
}

func TestSAVaultWriter_Probe(t *testing.T) {
	ctx := context.Background()
	client := NewFakeClient()
	v, _ := client.CreateVault(ctx, "test-vault", "")

	writer := NewSAVaultWriter(client)

	if err := writer.Probe(ctx, secrets.EnvBinding{VaultID: v.ID}); err != nil {
		t.Fatalf("Probe existing: %v", err)
	}

	if err := writer.Probe(ctx, secrets.EnvBinding{VaultID: "nonexistent"}); err == nil {
		t.Fatal("expected error for nonexistent vault")
	}
}

func TestSAVaultWriter_EmptyVaultID(t *testing.T) {
	ctx := context.Background()
	client := NewFakeClient()
	writer := NewSAVaultWriter(client)
	binding := secrets.EnvBinding{VaultID: ""}
	scope := secrets.Scope{Level: "org"}

	if _, err := writer.Upsert(ctx, binding, scope, "", map[string][]byte{"k": []byte("v")}); err == nil {
		t.Error("expected error for empty vault ID")
	}
	if _, _, err := writer.ListKeys(ctx, binding, scope); err == nil {
		t.Error("expected error for empty vault ID")
	}
	if _, err := writer.DeleteKey(ctx, binding, scope, "k", ""); err == nil {
		t.Error("expected error for empty vault ID")
	}
	if err := writer.Probe(ctx, binding); err == nil {
		t.Error("expected error for empty vault ID")
	}
}
