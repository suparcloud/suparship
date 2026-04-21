// Package onepassword provides the 1Password Service Account client for
// vault lifecycle management, ACL grants, Connect token issuance, and item CRUD.
package onepassword

import (
	"context"
	"fmt"
	"time"
)

// Permission is a bitmask of 1Password vault permissions.
type Permission uint32

const (
	PermReadItems    Permission = 32
	PermCreateItems  Permission = 128
	PermRevealPW     Permission = 16
	PermUpdateItems  Permission = 64
	PermArchiveItems Permission = 256
	PermDeleteItems  Permission = 512
	PermManageVault  Permission = 2

	// PermReadWrite is the minimum set needed for ESO Connect access.
	PermReadWrite = PermReadItems | PermRevealPW | PermCreateItems | PermUpdateItems | PermDeleteItems | PermArchiveItems
)

// VaultInfo represents a 1Password vault.
type VaultInfo struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"createdAt,omitempty"`
}

// ItemField is a single field within a 1Password item.
type ItemField struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Type  string `json:"type,omitempty"` // "concealed", "string", etc.
}

// Item is a 1Password vault item.
type Item struct {
	ID      string      `json:"id"`
	VaultID string      `json:"vaultId"`
	Title   string      `json:"title"`
	Fields  []ItemField `json:"fields"`
}

// ItemOverview is a summary of a vault item (no field values).
type ItemOverview struct {
	ID      string `json:"id"`
	VaultID string `json:"vaultId"`
	Title   string `json:"title"`
}

// ConnectToken represents an issued Connect server access token.
type ConnectToken struct {
	ID        string `json:"id"`
	Token     string `json:"token"`
	ServerID  string `json:"serverId"`
	ExpiresAt string `json:"expiresAt,omitempty"`
}

// SAClient abstracts administrative 1Password operations that require a
// privileged Service Account token. All operations are idempotent where noted.
type SAClient interface {
	// ── Vault CRUD ──────────────────────────────────────────────────────────

	// CreateVault creates a vault with the given title. Idempotent: returns
	// existing vault if title already exists within the configured group.
	CreateVault(ctx context.Context, title, description string) (VaultInfo, error)

	// ListVaults returns all vaults visible to the SA token.
	ListVaults(ctx context.Context) ([]VaultInfo, error)

	// GetVault returns vault details by ID.
	GetVault(ctx context.Context, vaultID string) (VaultInfo, error)

	// DeleteVault permanently deletes a vault. Irreversible.
	DeleteVault(ctx context.Context, vaultID string) error

	// ── ACL / Group management ──────────────────────────────────────────────

	// GrantGroupAccess grants the specified permission bitmask to a group on a
	// vault. Idempotent: safe to call repeatedly.
	GrantGroupAccess(ctx context.Context, vaultID, groupID string, perms Permission) error

	// RevokeGroupAccess removes a group's access to a vault.
	RevokeGroupAccess(ctx context.Context, vaultID, groupID string) error

	// ── Connect token management ────────────────────────────────────────────
	//
	// IMPORTANT: 1Password Service Account tokens CANNOT issue Connect server
	// tokens or grant Connect servers access to vaults. These methods exist
	// for interface forward-compatibility if 1Password adds SDK support in
	// the future. Current implementations MUST return ErrConnectNotReady.
	// See: https://1password.community/discussion/167592

	// IssueConnectToken is NOT SUPPORTED via SA tokens. Always returns
	// ErrConnectNotReady. Admins must issue Connect tokens manually via the
	// 1Password web console and provide them through the suparShip binding flow.
	IssueConnectToken(ctx context.Context, serverName string, vaultIDs []string, label string) (ConnectToken, error)

	// RevokeConnectToken is NOT SUPPORTED via SA tokens. Always returns
	// ErrConnectNotReady. Admins must revoke Connect tokens manually.
	RevokeConnectToken(ctx context.Context, tokenID string) error

	// ── Item CRUD (SA writes directly to 1Password) ─────────────────────────

	// UpsertItem creates or updates an item by title. Returns item ID.
	UpsertItem(ctx context.Context, vaultID, title string, fields []ItemField) (string, error)

	// GetItem retrieves an item by ID from a vault.
	GetItem(ctx context.Context, vaultID, itemID string) (Item, error)

	// ListItems returns item overviews for a vault.
	ListItems(ctx context.Context, vaultID string) ([]ItemOverview, error)

	// DeleteItem permanently removes an item from a vault.
	DeleteItem(ctx context.Context, vaultID, itemID string) error

	// ── Health ──────────────────────────────────────────────────────────────

	// Probe validates that the SA token is functional and returns the number
	// of accessible vaults.
	Probe(ctx context.Context) (int, error)
}

// Errors returned by SAClient implementations.
var (
	ErrTokenInvalid    = fmt.Errorf("onepassword: service account token is invalid or expired")
	ErrTokenScope      = fmt.Errorf("onepassword: token scope violation — visible vaults outside configured group")
	ErrVaultNotFound   = fmt.Errorf("onepassword: vault not found")
	ErrItemNotFound    = fmt.Errorf("onepassword: item not found")
	ErrPermissionDeny  = fmt.Errorf("onepassword: insufficient permissions")
	ErrConnectNotReady = fmt.Errorf("onepassword: connect server not available for token issuance")
)
