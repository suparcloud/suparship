package onepassword

import (
	"context"
	"testing"
)

func TestFakeClient_VaultCRUD(t *testing.T) {
	ctx := context.Background()
	c := NewFakeClient()

	v, err := c.CreateVault(ctx, "test-vault", "A test vault")
	if err != nil {
		t.Fatalf("CreateVault: %v", err)
	}
	if v.ID == "" || v.Title != "test-vault" {
		t.Errorf("unexpected vault: %+v", v)
	}

	// Idempotent: second call returns same vault.
	v2, err := c.CreateVault(ctx, "test-vault", "different desc")
	if err != nil {
		t.Fatalf("CreateVault idempotent: %v", err)
	}
	if v2.ID != v.ID {
		t.Errorf("expected same ID on idempotent create, got %q vs %q", v2.ID, v.ID)
	}

	vaults, err := c.ListVaults(ctx)
	if err != nil {
		t.Fatalf("ListVaults: %v", err)
	}
	if len(vaults) != 1 {
		t.Errorf("expected 1 vault, got %d", len(vaults))
	}

	got, err := c.GetVault(ctx, v.ID)
	if err != nil {
		t.Fatalf("GetVault: %v", err)
	}
	if got.Title != "test-vault" {
		t.Errorf("GetVault title = %q, want test-vault", got.Title)
	}

	if err := c.DeleteVault(ctx, v.ID); err != nil {
		t.Fatalf("DeleteVault: %v", err)
	}

	_, err = c.GetVault(ctx, v.ID)
	if err != ErrVaultNotFound {
		t.Errorf("expected ErrVaultNotFound after delete, got %v", err)
	}
}

func TestFakeClient_GroupAccess(t *testing.T) {
	ctx := context.Background()
	c := NewFakeClient()

	v, _ := c.CreateVault(ctx, "acl-vault", "")

	if err := c.GrantGroupAccess(ctx, v.ID, "grp-1", PermReadWrite); err != nil {
		t.Fatalf("GrantGroupAccess: %v", err)
	}
	if perms := c.GroupGrants[v.ID+":grp-1"]; perms != PermReadWrite {
		t.Errorf("expected PermReadWrite, got %d", perms)
	}

	if err := c.RevokeGroupAccess(ctx, v.ID, "grp-1"); err != nil {
		t.Fatalf("RevokeGroupAccess: %v", err)
	}
	if _, ok := c.GroupGrants[v.ID+":grp-1"]; ok {
		t.Error("expected grant removed")
	}

	// Error: vault does not exist.
	if err := c.GrantGroupAccess(ctx, "nonexistent", "grp-1", PermReadItems); err != ErrVaultNotFound {
		t.Errorf("expected ErrVaultNotFound, got %v", err)
	}
}

func TestFakeClient_ConnectTokens(t *testing.T) {
	ctx := context.Background()
	c := NewFakeClient()

	tok, err := c.IssueConnectToken(ctx, "my-server", []string{"v1", "v2"}, "staging")
	if err != nil {
		t.Fatalf("IssueConnectToken: %v", err)
	}
	if tok.ID == "" || tok.Token == "" {
		t.Errorf("unexpected token: %+v", tok)
	}

	if err := c.RevokeConnectToken(ctx, tok.ID); err != nil {
		t.Fatalf("RevokeConnectToken: %v", err)
	}

	if err := c.RevokeConnectToken(ctx, tok.ID); err == nil {
		t.Error("expected error revoking already-revoked token")
	}
}

func TestFakeClient_ItemCRUD(t *testing.T) {
	ctx := context.Background()
	c := NewFakeClient()

	v, _ := c.CreateVault(ctx, "items-vault", "")

	fields := []ItemField{
		{Label: "DB_URL", Value: "postgres://localhost", Type: "concealed"},
		{Label: "APP_ENV", Value: "staging", Type: "string"},
	}

	id, err := c.UpsertItem(ctx, v.ID, "my-app-config", fields)
	if err != nil {
		t.Fatalf("UpsertItem: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty item ID")
	}

	// Idempotent: upsert with new fields updates existing.
	fields2 := []ItemField{{Label: "DB_URL", Value: "postgres://prod", Type: "concealed"}}
	id2, err := c.UpsertItem(ctx, v.ID, "my-app-config", fields2)
	if err != nil {
		t.Fatalf("UpsertItem update: %v", err)
	}
	if id2 != id {
		t.Errorf("expected same ID on upsert, got %q vs %q", id2, id)
	}

	item, err := c.GetItem(ctx, v.ID, id)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if len(item.Fields) != 1 || item.Fields[0].Value != "postgres://prod" {
		t.Errorf("unexpected item fields: %+v", item.Fields)
	}

	items, err := c.ListItems(ctx, v.ID)
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(items) != 1 || items[0].Title != "my-app-config" {
		t.Errorf("unexpected items list: %+v", items)
	}

	if err := c.DeleteItem(ctx, v.ID, id); err != nil {
		t.Fatalf("DeleteItem: %v", err)
	}
	_, err = c.GetItem(ctx, v.ID, id)
	if err != ErrItemNotFound {
		t.Errorf("expected ErrItemNotFound after delete, got %v", err)
	}
}

func TestFakeClient_Probe(t *testing.T) {
	ctx := context.Background()
	c := NewFakeClient()

	n, err := c.Probe(ctx)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 vaults, got %d", n)
	}

	c.CreateVault(ctx, "v1", "")
	c.CreateVault(ctx, "v2", "")
	n, err = c.Probe(ctx)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2 vaults, got %d", n)
	}

	c.ProbeErr = ErrTokenInvalid
	_, err = c.Probe(ctx)
	if err != ErrTokenInvalid {
		t.Errorf("expected ErrTokenInvalid, got %v", err)
	}
}

func TestFakeClient_ErrorInjection(t *testing.T) {
	ctx := context.Background()
	c := NewFakeClient()
	c.CreateErr = ErrPermissionDeny

	_, err := c.CreateVault(ctx, "should-fail", "")
	if err != ErrPermissionDeny {
		t.Errorf("expected ErrPermissionDeny, got %v", err)
	}
}
