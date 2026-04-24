package onepassword

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/suparcloud/suparship/internal/secrets"
)

// SAVaultWriter implements secrets.VaultWriter using the 1Password SA client.
// It writes secrets as 1Password items (one item per scope) in the env-bound vault.
type SAVaultWriter struct {
	client SAClient
}

// NewSAVaultWriter creates a VaultWriter backed by the SA client.
func NewSAVaultWriter(client SAClient) *SAVaultWriter {
	return &SAVaultWriter{client: client}
}

func itemTitle(scope secrets.Scope) string {
	parts := []string{}
	if scope.Org != "" {
		parts = append(parts, scope.Org)
	}
	if scope.Env != "" {
		parts = append(parts, scope.Env)
	}
	if scope.Project != "" {
		parts = append(parts, scope.Project)
	}
	if scope.App != "" {
		parts = append(parts, scope.App)
	}
	if len(parts) == 0 {
		return scope.Level
	}
	return strings.Join(parts, "/")
}

func (w *SAVaultWriter) Upsert(ctx context.Context, binding secrets.EnvBinding, scope secrets.Scope, _ string, data map[string][]byte) (secrets.ItemMeta, error) {
	if binding.VaultID == "" {
		return secrets.ItemMeta{}, fmt.Errorf("vault ID is empty")
	}

	title := itemTitle(scope)
	fields := make([]ItemField, 0, len(data))
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fields = append(fields, ItemField{
			Label: k,
			Value: string(data[k]),
			Type:  "concealed",
		})
	}

	id, err := w.client.UpsertItem(ctx, binding.VaultID, title, fields)
	if err != nil {
		return secrets.ItemMeta{}, fmt.Errorf("upsert item %q: %w", title, err)
	}
	return secrets.ItemMeta{Version: id}, nil
}

func (w *SAVaultWriter) ListKeys(ctx context.Context, binding secrets.EnvBinding, scope secrets.Scope) ([]secrets.SecretEntry, secrets.ItemMeta, error) {
	if binding.VaultID == "" {
		return nil, secrets.ItemMeta{}, fmt.Errorf("vault ID is empty")
	}

	title := itemTitle(scope)
	items, err := w.client.ListItems(ctx, binding.VaultID)
	if err != nil {
		return nil, secrets.ItemMeta{}, err
	}

	// Find item by title.
	var itemID string
	for _, item := range items {
		if item.Title == title {
			itemID = item.ID
			break
		}
	}
	if itemID == "" {
		return nil, secrets.ItemMeta{}, nil
	}

	item, err := w.client.GetItem(ctx, binding.VaultID, itemID)
	if err != nil {
		return nil, secrets.ItemMeta{}, err
	}

	entries := make([]secrets.SecretEntry, len(item.Fields))
	for i, f := range item.Fields {
		entries[i] = secrets.SecretEntry{Key: f.Label}
	}
	return entries, secrets.ItemMeta{Version: item.ID}, nil
}

func (w *SAVaultWriter) DeleteKey(ctx context.Context, binding secrets.EnvBinding, scope secrets.Scope, key, _ string) (secrets.ItemMeta, error) {
	if binding.VaultID == "" {
		return secrets.ItemMeta{}, fmt.Errorf("vault ID is empty")
	}

	title := itemTitle(scope)
	items, err := w.client.ListItems(ctx, binding.VaultID)
	if err != nil {
		return secrets.ItemMeta{}, err
	}

	var itemID string
	for _, item := range items {
		if item.Title == title {
			itemID = item.ID
			break
		}
	}
	if itemID == "" {
		return secrets.ItemMeta{}, nil
	}

	item, err := w.client.GetItem(ctx, binding.VaultID, itemID)
	if err != nil {
		return secrets.ItemMeta{}, err
	}

	// Rebuild fields without the deleted key.
	fields := make([]ItemField, 0, len(item.Fields))
	for _, f := range item.Fields {
		if f.Label != key {
			fields = append(fields, ItemField{
				Label: f.Label,
				Value: f.Value,
				Type:  f.Type,
			})
		}
	}

	id, err := w.client.UpsertItem(ctx, binding.VaultID, title, fields)
	if err != nil {
		return secrets.ItemMeta{}, fmt.Errorf("delete key %q: %w", key, err)
	}
	return secrets.ItemMeta{Version: id}, nil
}

func (w *SAVaultWriter) Probe(ctx context.Context, binding secrets.EnvBinding) error {
	if binding.VaultID == "" {
		return fmt.Errorf("vault ID is empty")
	}
	_, err := w.client.GetVault(ctx, binding.VaultID)
	return err
}

// DeleteItem permanently removes the Item for scope from the vault identified
// by binding. It is a no-op when the item does not exist.
func (w *SAVaultWriter) DeleteItem(ctx context.Context, binding secrets.EnvBinding, scope secrets.Scope) error {
	if binding.VaultID == "" {
		return fmt.Errorf("vault ID is empty")
	}

	title := itemTitle(scope)
	items, err := w.client.ListItems(ctx, binding.VaultID)
	if err != nil {
		return err
	}

	var itemID string
	for _, item := range items {
		if item.Title == title {
			itemID = item.ID
			break
		}
	}
	if itemID == "" {
		return nil // already gone
	}

	return w.client.DeleteItem(ctx, binding.VaultID, itemID)
}

// Compile-time check.
var _ secrets.VaultWriter = (*SAVaultWriter)(nil)
