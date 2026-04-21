package onepassword

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// ProvisionConfig holds tunables for the provision orchestrator.
//
// Deprecated: The Provisioner is no longer used in the handler path.
// 1Password SA tokens cannot issue Connect tokens, so the automated
// provisioning flow is broken by design. Use the manual binding flow
// (handleAddBinding) instead. This code is retained for reference and
// for potential future use if 1Password adds SDK support for Connect
// token management.
type ProvisionConfig struct {
	// OrgName is used for vault naming: suparship-{org}-{env}.
	OrgName string

	// GroupName is the 1Password group that owns provisioned vaults.
	GroupName string

	// GroupID is the resolved 1Password group ID (looked up from GroupName).
	GroupID string

	// ConnectServerName is the name of the managed Connect server.
	ConnectServerName string

	// RotateGraceSeconds is the grace period before revoking old Connect tokens
	// after rotation. Default: 60.
	RotateGraceSeconds int
}

// ProvisionResult captures the outcome of a provision or rotate operation.
type ProvisionResult struct {
	VaultID      string
	VaultName    string
	TokenID      string
	ConnectToken string // plaintext, shown once then sealed
	Rotated      bool   // true if an existing token was rotated
	OldTokenID   string // non-empty if rotation revoked an old token
}

// RevokeResult captures the outcome of a revoke operation.
type RevokeResult struct {
	VaultID    string
	TokenID    string
	VaultKept  bool // true if vault was not deleted (only token revoked)
}

// SealPublisher abstracts the GitOps seal+publish step.
type SealPublisher interface {
	// PublishSealedConnectToken seals a Connect token and commits it to GitOps
	// along with the ClusterSecretStore and ArgoCD Application manifests.
	PublishSealedConnectToken(ctx context.Context, params SealPublishParams) error
}

// SealPublishParams contains the data needed for seal+publish.
type SealPublishParams struct {
	Env                string
	VaultID            string
	ConnectToken       string
	ClusterName        string
	TargetClusterURL   string
}

// Provisioner orchestrates the 1Password environment provisioning flow.
// Each call to Provision is idempotent: re-running provisions == rotation.
type Provisioner struct {
	client  SAClient
	cfg     ProvisionConfig
	logger  *slog.Logger
}

// NewProvisioner creates a provision orchestrator.
func NewProvisioner(client SAClient, cfg ProvisionConfig, logger *slog.Logger) *Provisioner {
	if cfg.RotateGraceSeconds <= 0 {
		cfg.RotateGraceSeconds = 60
	}
	if cfg.ConnectServerName == "" {
		cfg.ConnectServerName = "suparship-connect"
	}
	return &Provisioner{
		client: client,
		cfg:    cfg,
		logger: logger,
	}
}

// Provision executes the idempotent provisioning flow for an environment.
//
// Steps:
//  1. Ensure vault suparship-{org}-{env} exists.
//  2. Grant the configured group read/write access.
//  3. Issue a new per-env Connect token scoped to that vault.
//  4. Return the token for sealing + GitOps publishing (done by caller).
//
// If a previous token exists (rotation), it will be revoked after the grace
// period once the caller confirms the new token is published.
func (p *Provisioner) Provision(ctx context.Context, env string, existingTokenID string) (*ProvisionResult, error) {
	if env == "" {
		return nil, fmt.Errorf("provision: environment name is required")
	}

	vaultName := fmt.Sprintf("suparship-%s-%s", p.cfg.OrgName, env)

	p.logger.Info("provisioning environment",
		"env", env,
		"vault", vaultName,
		"group", p.cfg.GroupName,
	)

	// Step 1: Ensure vault exists (idempotent via title match).
	vault, err := p.ensureVault(ctx, vaultName)
	if err != nil {
		return nil, fmt.Errorf("provision: ensure vault: %w", err)
	}

	// Step 2: Grant group access.
	if p.cfg.GroupID != "" {
		if err := p.client.GrantGroupAccess(ctx, vault.ID, p.cfg.GroupID, PermReadWrite); err != nil {
			return nil, fmt.Errorf("provision: grant group access: %w", err)
		}
		p.logger.Info("granted group access", "vault", vault.ID, "group", p.cfg.GroupID)
	}

	// Step 3: Issue new Connect token.
	label := fmt.Sprintf("suparship-%s-%s", p.cfg.OrgName, env)
	tok, err := p.client.IssueConnectToken(ctx, p.cfg.ConnectServerName, []string{vault.ID}, label)
	if err != nil {
		return nil, fmt.Errorf("provision: issue connect token: %w", err)
	}
	p.logger.Info("issued connect token", "env", env, "tokenID", tok.ID)

	result := &ProvisionResult{
		VaultID:      vault.ID,
		VaultName:    vaultName,
		TokenID:      tok.ID,
		ConnectToken: tok.Token,
		Rotated:      existingTokenID != "",
		OldTokenID:   existingTokenID,
	}

	// Step 4: Schedule revocation of old token after grace period.
	if existingTokenID != "" {
		go p.revokeAfterGrace(existingTokenID, env)
	}

	return result, nil
}

// Revoke removes a provisioned environment's Connect token. The vault is kept.
func (p *Provisioner) Revoke(ctx context.Context, env string, tokenID string) (*RevokeResult, error) {
	if tokenID == "" {
		return nil, fmt.Errorf("revoke: token ID is required")
	}

	p.logger.Info("revoking connect token", "env", env, "tokenID", tokenID)

	if err := p.client.RevokeConnectToken(ctx, tokenID); err != nil {
		return nil, fmt.Errorf("revoke: %w", err)
	}

	vaultName := fmt.Sprintf("suparship-%s-%s", p.cfg.OrgName, env)
	vaultID := ""
	vaults, _ := p.client.ListVaults(ctx)
	for _, v := range vaults {
		if v.Title == vaultName {
			vaultID = v.ID
			break
		}
	}

	return &RevokeResult{
		VaultID:   vaultID,
		TokenID:   tokenID,
		VaultKept: true,
	}, nil
}

// ensureVault finds or creates a vault by title.
func (p *Provisioner) ensureVault(ctx context.Context, title string) (VaultInfo, error) {
	vaults, err := p.client.ListVaults(ctx)
	if err != nil {
		return VaultInfo{}, err
	}
	for _, v := range vaults {
		if v.Title == title {
			p.logger.Info("vault already exists", "title", title, "id", v.ID)
			return v, nil
		}
	}
	return p.client.CreateVault(ctx, title, fmt.Sprintf("Managed by suparship for environment secrets"))
}

// revokeAfterGrace waits for the grace period then revokes the old token.
func (p *Provisioner) revokeAfterGrace(tokenID, env string) {
	time.Sleep(time.Duration(p.cfg.RotateGraceSeconds) * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := p.client.RevokeConnectToken(ctx, tokenID); err != nil {
		p.logger.Error("failed to revoke old connect token after grace",
			"env", env,
			"tokenID", tokenID,
			"error", err,
		)
		return
	}
	p.logger.Info("revoked old connect token after grace", "env", env, "tokenID", tokenID)
}
