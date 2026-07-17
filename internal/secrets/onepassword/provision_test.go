package onepassword

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestProvisioner_Provision_NewEnv(t *testing.T) {
	ctx := context.Background()
	client := NewFakeClient()
	p := NewProvisioner(client, ProvisionConfig{
		OrgName:            "acme",
		GroupName:          "Suparship",
		GroupID:            "grp-001",
		ConnectServerName:  "suparship-connect",
		RotateGraceSeconds: 1,
	}, testLogger())

	result, err := p.Provision(ctx, "staging", "")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	if result.VaultName != "suparship-acme-staging" {
		t.Errorf("VaultName = %q, want suparship-acme-staging", result.VaultName)
	}
	if result.VaultID == "" {
		t.Error("expected non-empty VaultID")
	}
	if result.ConnectToken == "" {
		t.Error("expected non-empty ConnectToken")
	}
	if result.TokenID == "" {
		t.Error("expected non-empty TokenID")
	}
	if result.Rotated {
		t.Error("expected Rotated=false for first provision")
	}

	// Verify vault was created.
	if len(client.Vaults) != 1 {
		t.Errorf("expected 1 vault, got %d", len(client.Vaults))
	}

	// Verify group access was granted.
	key := result.VaultID + ":grp-001"
	if perms, ok := client.GroupGrants[key]; !ok {
		t.Error("expected group access grant")
	} else if perms != PermReadWrite {
		t.Errorf("expected PermReadWrite, got %d", perms)
	}
}

func TestProvisioner_Provision_Idempotent(t *testing.T) {
	ctx := context.Background()
	client := NewFakeClient()
	p := NewProvisioner(client, ProvisionConfig{
		OrgName:            "acme",
		GroupName:          "Suparship",
		GroupID:            "grp-001",
		RotateGraceSeconds: 1,
	}, testLogger())

	// First provision.
	r1, err := p.Provision(ctx, "prod", "")
	if err != nil {
		t.Fatalf("first Provision: %v", err)
	}

	// Second provision (rotation).
	r2, err := p.Provision(ctx, "prod", r1.TokenID)
	if err != nil {
		t.Fatalf("second Provision: %v", err)
	}

	if r2.VaultID != r1.VaultID {
		t.Errorf("vault ID changed: %q -> %q", r1.VaultID, r2.VaultID)
	}
	if !r2.Rotated {
		t.Error("expected Rotated=true on second provision")
	}
	if r2.OldTokenID != r1.TokenID {
		t.Errorf("OldTokenID = %q, want %q", r2.OldTokenID, r1.TokenID)
	}
	if r2.TokenID == r1.TokenID {
		t.Error("expected new token ID on rotation")
	}

	// Wait for grace period to expire and old token to be revoked. Read through
	// the thread-safe accessor — the revoke runs in a background goroutine.
	time.Sleep(2 * time.Second)
	if client.HasConnectToken(r1.TokenID) {
		t.Error("expected old token to be revoked after grace period")
	}
}

func TestProvisioner_Provision_EmptyEnv(t *testing.T) {
	ctx := context.Background()
	client := NewFakeClient()
	p := NewProvisioner(client, ProvisionConfig{
		OrgName: "acme",
	}, testLogger())

	_, err := p.Provision(ctx, "", "")
	if err == nil {
		t.Fatal("expected error for empty env")
	}
}

func TestProvisioner_Provision_VaultCreateError(t *testing.T) {
	ctx := context.Background()
	client := NewFakeClient()
	client.CreateErr = fmt.Errorf("simulated create failure")
	p := NewProvisioner(client, ProvisionConfig{
		OrgName: "acme",
	}, testLogger())

	_, err := p.Provision(ctx, "staging", "")
	if err == nil {
		t.Fatal("expected error when vault creation fails")
	}
}

func TestProvisioner_Provision_GrantAccessError(t *testing.T) {
	ctx := context.Background()
	client := NewFakeClient()
	client.GrantErr = fmt.Errorf("simulated grant failure")
	p := NewProvisioner(client, ProvisionConfig{
		OrgName: "acme",
		GroupID: "grp-001",
	}, testLogger())

	_, err := p.Provision(ctx, "staging", "")
	if err == nil {
		t.Fatal("expected error when grant fails")
	}
}

func TestProvisioner_Provision_IssueTokenError(t *testing.T) {
	ctx := context.Background()
	client := NewFakeClient()
	client.IssueErr = ErrConnectNotReady
	p := NewProvisioner(client, ProvisionConfig{
		OrgName: "acme",
	}, testLogger())

	_, err := p.Provision(ctx, "staging", "")
	if err == nil {
		t.Fatal("expected error when token issuance fails")
	}
}

func TestProvisioner_Revoke(t *testing.T) {
	ctx := context.Background()
	client := NewFakeClient()
	p := NewProvisioner(client, ProvisionConfig{
		OrgName: "acme",
	}, testLogger())

	// First provision to get a token.
	r, err := p.Provision(ctx, "staging", "")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	// Revoke.
	result, err := p.Revoke(ctx, "staging", r.TokenID)
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	if !result.VaultKept {
		t.Error("expected VaultKept=true")
	}
	if result.TokenID != r.TokenID {
		t.Errorf("TokenID = %q, want %q", result.TokenID, r.TokenID)
	}

	// Token should be gone.
	if client.HasConnectToken(r.TokenID) {
		t.Error("expected token to be removed after revoke")
	}

	// Vault should still exist.
	if len(client.Vaults) != 1 {
		t.Errorf("expected vault to remain, got %d vaults", len(client.Vaults))
	}
}

func TestProvisioner_Revoke_EmptyTokenID(t *testing.T) {
	ctx := context.Background()
	client := NewFakeClient()
	p := NewProvisioner(client, ProvisionConfig{
		OrgName: "acme",
	}, testLogger())

	_, err := p.Revoke(ctx, "staging", "")
	if err == nil {
		t.Fatal("expected error for empty token ID")
	}
}

func TestProvisioner_Revoke_TokenNotFound(t *testing.T) {
	ctx := context.Background()
	client := NewFakeClient()
	p := NewProvisioner(client, ProvisionConfig{
		OrgName: "acme",
	}, testLogger())

	_, err := p.Revoke(ctx, "staging", "nonexistent-token")
	if err == nil {
		t.Fatal("expected error for nonexistent token")
	}
}

func TestProvisioner_DefaultConfig(t *testing.T) {
	client := NewFakeClient()
	p := NewProvisioner(client, ProvisionConfig{}, testLogger())

	if p.cfg.RotateGraceSeconds != 60 {
		t.Errorf("default RotateGraceSeconds = %d, want 60", p.cfg.RotateGraceSeconds)
	}
	if p.cfg.ConnectServerName != "suparship-connect" {
		t.Errorf("default ConnectServerName = %q, want suparship-connect", p.cfg.ConnectServerName)
	}
}
