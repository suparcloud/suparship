package onepassword

import (
	"context"
	"fmt"
	"sort"

	"github.com/suparcloud/suparship/internal/secrets"
)

// VaultResolver returns the 1Password vault UUID that holds secrets for the
// given scope. It returns an error when no vault is provisioned for the scope.
type VaultResolver func(scope secrets.Scope) (string, error)

// SAVaultStore implements secrets.VaultStore using the 1Password SA client.
// Each (scope, tier, app) maps to one 1Password item (titled via
// secrets.ItemName) inside the vault the resolver returns for that scope.
type SAVaultStore struct {
	client   SAClient
	resolver VaultResolver
}

// NewSAVaultStore creates a VaultStore backed by the SA client. resolver maps
// a scope to the 1Password vault UUID it lives in.
func NewSAVaultStore(client SAClient, resolver VaultResolver) *SAVaultStore {
	return &SAVaultStore{client: client, resolver: resolver}
}

var _ secrets.VaultStore = (*SAVaultStore)(nil)

func (w *SAVaultStore) findItemID(ctx context.Context, vaultID, title string) (string, error) {
	items, err := w.client.ListItems(ctx, vaultID)
	if err != nil {
		return "", err
	}
	for _, item := range items {
		if item.Title == title {
			return item.ID, nil
		}
	}
	return "", nil
}

func (w *SAVaultStore) Upsert(ctx context.Context, scope secrets.Scope, tier secrets.Tier, app string, data map[string][]byte) error {
	vaultID, err := w.resolver(scope)
	if err != nil {
		return err
	}
	title := secrets.ItemName(scope, tier, app)

	// Merge with any existing fields so a partial upsert doesn't drop siblings.
	existing := map[string]ItemField{}
	if id, err := w.findItemID(ctx, vaultID, title); err != nil {
		return err
	} else if id != "" {
		item, err := w.client.GetItem(ctx, vaultID, id)
		if err != nil {
			return err
		}
		for _, f := range item.Fields {
			existing[f.Label] = ItemField{Label: f.Label, Value: f.Value, Type: f.Type}
		}
	}
	for k, v := range data {
		existing[k] = ItemField{Label: k, Value: string(v), Type: "concealed"}
	}

	labels := make([]string, 0, len(existing))
	for k := range existing {
		labels = append(labels, k)
	}
	sort.Strings(labels)
	fields := make([]ItemField, 0, len(labels))
	for _, k := range labels {
		fields = append(fields, existing[k])
	}

	if _, err := w.client.UpsertItem(ctx, vaultID, title, fields); err != nil {
		return fmt.Errorf("upsert item %q: %w", title, err)
	}
	return nil
}

func (w *SAVaultStore) EnsureItem(ctx context.Context, scope secrets.Scope, tier secrets.Tier, app string) error {
	vaultID, err := w.resolver(scope)
	if err != nil {
		return err
	}
	title := secrets.ItemName(scope, tier, app)
	id, err := w.findItemID(ctx, vaultID, title)
	if err != nil {
		return err
	}
	if id != "" {
		return nil // already exists — leave its fields untouched
	}
	if _, err := w.client.UpsertItem(ctx, vaultID, title, nil); err != nil {
		return fmt.Errorf("create empty item %q: %w", title, err)
	}
	return nil
}

func (w *SAVaultStore) ListKeys(ctx context.Context, scope secrets.Scope, tier secrets.Tier, app string) ([]secrets.SecretEntry, error) {
	vaultID, err := w.resolver(scope)
	if err != nil {
		return nil, err
	}
	title := secrets.ItemName(scope, tier, app)
	id, err := w.findItemID(ctx, vaultID, title)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, nil
	}
	item, err := w.client.GetItem(ctx, vaultID, id)
	if err != nil {
		return nil, err
	}
	entries := make([]secrets.SecretEntry, 0, len(item.Fields))
	for _, f := range item.Fields {
		entries = append(entries, secrets.SecretEntry{Key: f.Label})
	}
	return entries, nil
}

func (w *SAVaultStore) DeleteKey(ctx context.Context, scope secrets.Scope, tier secrets.Tier, app, key string) error {
	vaultID, err := w.resolver(scope)
	if err != nil {
		return err
	}
	title := secrets.ItemName(scope, tier, app)
	id, err := w.findItemID(ctx, vaultID, title)
	if err != nil {
		return err
	}
	if id == "" {
		return nil
	}
	item, err := w.client.GetItem(ctx, vaultID, id)
	if err != nil {
		return err
	}
	fields := make([]ItemField, 0, len(item.Fields))
	for _, f := range item.Fields {
		if f.Label != key {
			fields = append(fields, ItemField{Label: f.Label, Value: f.Value, Type: f.Type})
		}
	}
	if _, err := w.client.UpsertItem(ctx, vaultID, title, fields); err != nil {
		return fmt.Errorf("delete key %q: %w", key, err)
	}
	return nil
}

var _ secrets.LegacyItemMigrator = (*SAVaultStore)(nil)

// CopyItem implements secrets.LegacyItemMigrator: read the source item's fields
// and upsert them into a new-titled item in the same vault (merge). No-op when
// the source item is absent.
func (w *SAVaultStore) CopyItem(ctx context.Context, scope secrets.Scope, fromName, toName string) error {
	vaultID, err := w.resolver(scope)
	if err != nil {
		return err
	}
	fromID, err := w.findItemID(ctx, vaultID, fromName)
	if err != nil {
		return err
	}
	if fromID == "" {
		return nil // nothing to copy
	}
	src, err := w.client.GetItem(ctx, vaultID, fromID)
	if err != nil {
		return err
	}
	// Merge onto any existing destination fields so a re-run is safe.
	merged := map[string]ItemField{}
	if toID, err := w.findItemID(ctx, vaultID, toName); err != nil {
		return err
	} else if toID != "" {
		dst, err := w.client.GetItem(ctx, vaultID, toID)
		if err != nil {
			return err
		}
		for _, f := range dst.Fields {
			merged[f.Label] = ItemField{Label: f.Label, Value: f.Value, Type: f.Type}
		}
	}
	for _, f := range src.Fields {
		merged[f.Label] = ItemField{Label: f.Label, Value: f.Value, Type: f.Type}
	}
	labels := make([]string, 0, len(merged))
	for k := range merged {
		labels = append(labels, k)
	}
	sort.Strings(labels)
	fields := make([]ItemField, 0, len(labels))
	for _, k := range labels {
		fields = append(fields, merged[k])
	}
	if _, err := w.client.UpsertItem(ctx, vaultID, toName, fields); err != nil {
		return fmt.Errorf("copy item %q → %q: %w", fromName, toName, err)
	}
	return nil
}

// DeleteItem implements secrets.LegacyItemMigrator: remove the whole item by
// title. No-op when absent.
func (w *SAVaultStore) DeleteItem(ctx context.Context, scope secrets.Scope, itemName string) error {
	vaultID, err := w.resolver(scope)
	if err != nil {
		return err
	}
	id, err := w.findItemID(ctx, vaultID, itemName)
	if err != nil {
		return err
	}
	if id == "" {
		return nil
	}
	if err := w.client.DeleteItem(ctx, vaultID, id); err != nil {
		return fmt.Errorf("delete item %q: %w", itemName, err)
	}
	return nil
}

var _ secrets.ItemExporter = (*SAVaultStore)(nil)

// ExportItem implements secrets.ItemExporter: the item's fields as key/value
// pairs, for cross-backend migration only. Absent item → (nil, nil).
func (w *SAVaultStore) ExportItem(ctx context.Context, scope secrets.Scope, tier secrets.Tier, app string) (map[string][]byte, error) {
	vaultID, err := w.resolver(scope)
	if err != nil {
		return nil, err
	}
	title := secrets.ItemName(scope, tier, app)
	id, err := w.findItemID(ctx, vaultID, title)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, nil
	}
	item, err := w.client.GetItem(ctx, vaultID, id)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]byte, len(item.Fields))
	for _, f := range item.Fields {
		out[f.Label] = []byte(f.Value)
	}
	return out, nil
}

func (w *SAVaultStore) Probe(ctx context.Context, scope secrets.Scope) error {
	vaultID, err := w.resolver(scope)
	if err != nil {
		return err
	}
	_, err = w.client.GetVault(ctx, vaultID)
	return err
}
