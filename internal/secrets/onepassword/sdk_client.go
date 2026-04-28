package onepassword

import (
	"context"
	"fmt"
	"strings"
	"sync"

	onepasswordsdk "github.com/1password/onepassword-sdk-go"
)

// SDKClient implements SAClient using the official 1Password Go SDK.
// It is backed by a single Service Account token.
type SDKClient struct {
	mu     sync.Mutex
	client *onepasswordsdk.Client
	token  string
}

// NewSDKClient creates an SAClient backed by the 1Password Go SDK.
// The token must be a valid Service Account token.
func NewSDKClient(ctx context.Context, token string) (*SDKClient, error) {
	if token == "" {
		return nil, ErrTokenInvalid
	}

	client, err := onepasswordsdk.NewClient(
		ctx,
		onepasswordsdk.WithServiceAccountToken(token),
		onepasswordsdk.WithIntegrationInfo("suparship", "0.1.0"),
	)
	if err != nil {
		if strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "expired") {
			return nil, ErrTokenInvalid
		}
		return nil, fmt.Errorf("onepassword: failed to initialize SDK client: %w", err)
	}

	return &SDKClient{client: client, token: token}, nil
}

func (c *SDKClient) Probe(ctx context.Context) (int, error) {
	vaults, err := c.ListVaults(ctx)
	if err != nil {
		return 0, err
	}
	return len(vaults), nil
}

func (c *SDKClient) CreateVault(ctx context.Context, title, description string) (VaultInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	desc := &description
	result, err := c.client.Vaults().Create(ctx, onepasswordsdk.VaultCreateParams{
		Title:       title,
		Description: desc,
	})
	if err != nil {
		return VaultInfo{}, fmt.Errorf("onepassword: create vault %q: %w", title, err)
	}

	return VaultInfo{
		ID:          result.ID,
		Title:       result.Title,
		Description: result.Description,
	}, nil
}

func (c *SDKClient) ListVaults(ctx context.Context) ([]VaultInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	overviews, err := c.client.Vaults().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("onepassword: list vaults: %w", err)
	}

	vaults := make([]VaultInfo, len(overviews))
	for i, v := range overviews {
		vaults[i] = VaultInfo{
			ID:    v.ID,
			Title: v.Title,
		}
	}
	return vaults, nil
}

func (c *SDKClient) GetVault(ctx context.Context, vaultID string) (VaultInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	v, err := c.client.Vaults().Get(ctx, vaultID, onepasswordsdk.VaultGetParams{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return VaultInfo{}, ErrVaultNotFound
		}
		return VaultInfo{}, fmt.Errorf("onepassword: get vault %q: %w", vaultID, err)
	}

	return VaultInfo{
		ID:          v.ID,
		Title:       v.Title,
		Description: v.Description,
	}, nil
}

func (c *SDKClient) DeleteVault(ctx context.Context, vaultID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.client.Vaults().Delete(ctx, vaultID); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return ErrVaultNotFound
		}
		return fmt.Errorf("onepassword: delete vault %q: %w", vaultID, err)
	}
	return nil
}

func (c *SDKClient) GrantGroupAccess(ctx context.Context, vaultID, groupID string, perms Permission) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	err := c.client.Vaults().GrantGroupPermissions(ctx, vaultID, []onepasswordsdk.GroupAccess{
		{
			GroupID:     groupID,
			Permissions: uint32(perms),
		},
	})
	if err != nil {
		return fmt.Errorf("onepassword: grant group access on vault %q: %w", vaultID, err)
	}
	return nil
}

func (c *SDKClient) RevokeGroupAccess(ctx context.Context, vaultID, groupID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	err := c.client.Vaults().RevokeGroupPermissions(ctx, vaultID, groupID)
	if err != nil {
		return fmt.Errorf("onepassword: revoke group access on vault %q: %w", vaultID, err)
	}
	return nil
}

// IssueConnectToken is not available via the Go SDK for service accounts.
// TODO: The 1Password REST API for Connect token issuance is not publicly
// documented for SA tokens. Fallback: shell out to `op connect token create`.
// This will be fully implemented when the API is confirmed or CLI wrapper is added.
func (c *SDKClient) IssueConnectToken(_ context.Context, _ string, _ []string, _ string) (ConnectToken, error) {
	return ConnectToken{}, fmt.Errorf("onepassword: Connect token issuance via SDK is not yet supported; requires CLI fallback (op connect token create)")
}

// RevokeConnectToken is not available via the Go SDK for service accounts.
// TODO: Same limitation as IssueConnectToken.
func (c *SDKClient) RevokeConnectToken(_ context.Context, _ string) error {
	return fmt.Errorf("onepassword: Connect token revocation via SDK is not yet supported; requires CLI fallback (op connect token revoke)")
}

func (c *SDKClient) UpsertItem(ctx context.Context, vaultID, title string, fields []ItemField) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	existing, err := c.findItemByTitle(ctx, vaultID, title)
	if err != nil {
		return "", err
	}

	sdkFields := make([]onepasswordsdk.ItemField, len(fields))
	for i, f := range fields {
		ft := onepasswordsdk.ItemFieldTypeText
		if f.Type == "concealed" {
			ft = onepasswordsdk.ItemFieldTypeConcealed
		}
		// 1Password requires a unique ID per field; the SDK won't generate
		// one on update (Items().Put), so leaving ID empty causes Put to
		// fail with "item contained duplicate field ids" as soon as the
		// item has more than one field. Use the label as the ID — labels
		// are unique within our merged map by construction, and we treat
		// the label as the canonical key anyway.
		sdkFields[i] = onepasswordsdk.ItemField{
			ID:        f.Label,
			Title:     f.Label,
			Value:     f.Value,
			FieldType: ft,
		}
	}

	if existing != "" {
		item, getErr := c.client.Items().Get(ctx, vaultID, existing)
		if getErr != nil {
			return "", fmt.Errorf("onepassword: get item for update %q: %w", title, getErr)
		}
		item.Fields = sdkFields
		updated, putErr := c.client.Items().Put(ctx, item)
		if putErr != nil {
			return "", fmt.Errorf("onepassword: update item %q: %w", title, putErr)
		}
		return updated.ID, nil
	}

	created, err := c.client.Items().Create(ctx, onepasswordsdk.ItemCreateParams{
		VaultID:  vaultID,
		Title:    title,
		Category: onepasswordsdk.ItemCategoryAPICredentials,
		Fields:   sdkFields,
	})
	if err != nil {
		return "", fmt.Errorf("onepassword: create item %q: %w", title, err)
	}
	return created.ID, nil
}

func (c *SDKClient) GetItem(ctx context.Context, vaultID, itemID string) (Item, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	item, err := c.client.Items().Get(ctx, vaultID, itemID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return Item{}, ErrItemNotFound
		}
		return Item{}, fmt.Errorf("onepassword: get item %q: %w", itemID, err)
	}

	fields := make([]ItemField, len(item.Fields))
	for i, f := range item.Fields {
		ft := "string"
		if f.FieldType == onepasswordsdk.ItemFieldTypeConcealed {
			ft = "concealed"
		}
		fields[i] = ItemField{
			Label: f.Title,
			Value: f.Value,
			Type:  ft,
		}
	}

	return Item{
		ID:      item.ID,
		VaultID: vaultID,
		Title:   item.Title,
		Fields:  fields,
	}, nil
}

func (c *SDKClient) ListItems(ctx context.Context, vaultID string) ([]ItemOverview, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	items, err := c.client.Items().List(ctx, vaultID)
	if err != nil {
		return nil, fmt.Errorf("onepassword: list items in vault %q: %w", vaultID, err)
	}

	out := make([]ItemOverview, len(items))
	for i, item := range items {
		out[i] = ItemOverview{
			ID:      item.ID,
			VaultID: vaultID,
			Title:   item.Title,
		}
	}
	return out, nil
}

func (c *SDKClient) DeleteItem(ctx context.Context, vaultID, itemID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.client.Items().Delete(ctx, vaultID, itemID); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return ErrItemNotFound
		}
		return fmt.Errorf("onepassword: delete item %q: %w", itemID, err)
	}
	return nil
}

func (c *SDKClient) findItemByTitle(ctx context.Context, vaultID, title string) (string, error) {
	items, err := c.client.Items().List(ctx, vaultID)
	if err != nil {
		return "", fmt.Errorf("onepassword: list items for title lookup: %w", err)
	}

	for _, item := range items {
		if item.Title == title {
			return item.ID, nil
		}
	}
	return "", nil
}

// Compile-time check.
var _ SAClient = (*SDKClient)(nil)
