package secrets

import (
	"context"
	"errors"
	"testing"
)

func TestMemVaultWriter_UpsertAndListKeys(t *testing.T) {
	w := NewMemVaultWriter()
	ctx := context.Background()
	binding := EnvBinding{Env: "prod", VaultID: "vault-1"}
	scope := Scope{Level: LevelApp, Org: "default", Env: "prod", Project: "acme", App: "web"}

	meta, err := w.Upsert(ctx, binding, scope, "", map[string][]byte{
		"DB_HOST": []byte("db.example.com"),
		"API_KEY": []byte("secret123"),
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if meta.Version == "" {
		t.Error("expected non-empty version")
	}

	entries, meta2, err := w.ListKeys(ctx, binding, scope)
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(entries))
	}
	if entries[0].Key != "API_KEY" || entries[1].Key != "DB_HOST" {
		t.Errorf("unexpected keys: %v", entries)
	}
	if meta2.Version != meta.Version {
		t.Errorf("version mismatch: list=%q, upsert=%q", meta2.Version, meta.Version)
	}
}

func TestMemVaultWriter_MergeSemantics(t *testing.T) {
	w := NewMemVaultWriter()
	ctx := context.Background()
	binding := EnvBinding{Env: "prod", VaultID: "v1"}
	scope := Scope{Level: LevelOrg, Org: "default"}

	w.Upsert(ctx, binding, scope, "", map[string][]byte{"A": []byte("1"), "B": []byte("2")})
	w.Upsert(ctx, binding, scope, "", map[string][]byte{"B": []byte("3"), "C": []byte("4")})

	entries, _, err := w.ListKeys(ctx, binding, scope)
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 keys after merge, got %d", len(entries))
	}
}

func TestMemVaultWriter_OptimisticConcurrency(t *testing.T) {
	w := NewMemVaultWriter()
	ctx := context.Background()
	binding := EnvBinding{Env: "prod", VaultID: "v1"}
	scope := Scope{Level: LevelApp, Org: "default", Project: "p", App: "a", Env: "prod"}

	meta1, _ := w.Upsert(ctx, binding, scope, "", map[string][]byte{"K": []byte("v1")})

	// Upsert with correct version succeeds
	meta2, err := w.Upsert(ctx, binding, scope, meta1.Version, map[string][]byte{"K": []byte("v2")})
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if meta2.Version == meta1.Version {
		t.Error("version should advance after upsert")
	}

	// Upsert with stale version fails
	_, err = w.Upsert(ctx, binding, scope, meta1.Version, map[string][]byte{"K": []byte("v3")})
	if !errors.Is(err, ErrStaleVersion) {
		t.Fatalf("expected ErrStaleVersion, got: %v", err)
	}
}

func TestMemVaultWriter_DeleteKey(t *testing.T) {
	w := NewMemVaultWriter()
	ctx := context.Background()
	binding := EnvBinding{Env: "staging", VaultID: "v1"}
	scope := Scope{Level: LevelProject, Project: "proj"}

	w.Upsert(ctx, binding, scope, "", map[string][]byte{"X": []byte("1"), "Y": []byte("2")})

	_, err := w.DeleteKey(ctx, binding, scope, "X", "")
	if err != nil {
		t.Fatalf("DeleteKey: %v", err)
	}

	entries, _, _ := w.ListKeys(ctx, binding, scope)
	if len(entries) != 1 || entries[0].Key != "Y" {
		t.Errorf("unexpected keys after delete: %v", entries)
	}
}

func TestMemVaultWriter_DeleteKey_StaleVersion(t *testing.T) {
	w := NewMemVaultWriter()
	ctx := context.Background()
	binding := EnvBinding{Env: "prod", VaultID: "v1"}
	scope := Scope{Level: LevelAppEnv, Project: "p", App: "a", Env: "prod"}

	w.Upsert(ctx, binding, scope, "", map[string][]byte{"K": []byte("v")})
	// Advance version
	w.Upsert(ctx, binding, scope, "", map[string][]byte{"K2": []byte("v")})

	_, err := w.DeleteKey(ctx, binding, scope, "K", "1")
	if !errors.Is(err, ErrStaleVersion) {
		t.Fatalf("expected ErrStaleVersion, got: %v", err)
	}
}

func TestMemVaultWriter_ListKeys_EmptyItem(t *testing.T) {
	w := NewMemVaultWriter()
	ctx := context.Background()
	binding := EnvBinding{Env: "prod", VaultID: "v1"}
	scope := Scope{Level: LevelOrg, Org: "default"}

	entries, meta, err := w.ListKeys(ctx, binding, scope)
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 keys, got %d", len(entries))
	}
	if meta.Version != "" {
		t.Errorf("expected empty version for missing item, got %q", meta.Version)
	}
}

func TestMemVaultWriter_Probe(t *testing.T) {
	w := NewMemVaultWriter()
	if err := w.Probe(context.Background(), EnvBinding{}); err != nil {
		t.Fatalf("Probe: %v", err)
	}
}
