package hcvault

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sort"

	"github.com/suparcloud/suparship/internal/secrets"
)

// HCVaultStore implements secrets.VaultStore against a HashiCorp Vault KV v2
// mount. See the package doc for the path layout.
type HCVaultStore struct {
	client Client
}

// NewHCVaultStore creates a store backed by client.
func NewHCVaultStore(client Client) *HCVaultStore {
	return &HCVaultStore{client: client}
}

var (
	_ secrets.VaultStore         = (*HCVaultStore)(nil)
	_ secrets.LegacyItemMigrator = (*HCVaultStore)(nil)
	_ secrets.ItemExporter       = (*HCVaultStore)(nil)
)

// itemPath returns the mount-relative KV path for (scope, tier, app).
func itemPath(scope secrets.Scope, tier secrets.Tier, app string) string {
	return secrets.VaultName(scope) + "/" + secrets.ItemName(scope, tier, app)
}

// Upsert merges data into the item, preserving keys absent from data, as the
// VaultStore contract requires. KV v2 writes replace the whole entry, so this
// is read-modify-write — guarded by check-and-set so a concurrent writer's
// keys are never silently dropped (the loser sees secrets.ErrStaleVersion).
func (s *HCVaultStore) Upsert(ctx context.Context, scope secrets.Scope, tier secrets.Tier, app string, data map[string][]byte) error {
	path := itemPath(scope, tier, app)
	existing, ver, err := s.client.ReadItem(ctx, path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	if existing == nil {
		existing = make(map[string][]byte, len(data))
	}
	maps.Copy(existing, data)
	if err := s.client.WriteItem(ctx, path, existing, ver); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

func (s *HCVaultStore) EnsureItem(ctx context.Context, scope secrets.Scope, tier secrets.Tier, app string) error {
	path := itemPath(scope, tier, app)
	_, ver, err := s.client.ReadItem(ctx, path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	if ver != 0 {
		return nil // already exists — leave its keys untouched
	}
	// cas=0: create only. Losing the race means someone else created it, which
	// is this method's postcondition anyway.
	if err := s.client.WriteItem(ctx, path, map[string][]byte{}, 0); err != nil {
		if errors.Is(err, secrets.ErrStaleVersion) {
			return nil
		}
		return fmt.Errorf("creating %s: %w", path, err)
	}
	return nil
}

func (s *HCVaultStore) ListKeys(ctx context.Context, scope secrets.Scope, tier secrets.Tier, app string) ([]secrets.SecretEntry, error) {
	path := itemPath(scope, tier, app)
	data, _, err := s.client.ReadItem(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	entries := make([]secrets.SecretEntry, len(keys))
	for i, k := range keys {
		entries[i] = secrets.SecretEntry{Key: k}
	}
	return entries, nil
}

func (s *HCVaultStore) DeleteKey(ctx context.Context, scope secrets.Scope, tier secrets.Tier, app, key string) error {
	path := itemPath(scope, tier, app)
	data, ver, err := s.client.ReadItem(ctx, path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	if ver == 0 {
		return nil
	}
	if _, exists := data[key]; !exists {
		return nil
	}
	delete(data, key)
	if err := s.client.WriteItem(ctx, path, data, ver); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// Probe verifies the token/mount (client probe) and that the scope's derived
// path is reachable — a read of the shared item, where "absent" is success:
// per-scope containers are just paths, so existence is not a setup condition.
func (s *HCVaultStore) Probe(ctx context.Context, scope secrets.Scope) error {
	if err := s.client.Probe(ctx); err != nil {
		return err
	}
	_, _, err := s.client.ReadItem(ctx, itemPath(scope, secrets.TierShared, ""))
	return err
}

// CopyItem implements secrets.LegacyItemMigrator (merge semantics; no-op when
// the source is absent).
func (s *HCVaultStore) CopyItem(ctx context.Context, scope secrets.Scope, fromName, toName string) error {
	vault := secrets.VaultName(scope)
	src, srcVer, err := s.client.ReadItem(ctx, vault+"/"+fromName)
	if err != nil {
		return fmt.Errorf("reading %s/%s: %w", vault, fromName, err)
	}
	if srcVer == 0 {
		return nil
	}
	dstPath := vault + "/" + toName
	dst, dstVer, err := s.client.ReadItem(ctx, dstPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", dstPath, err)
	}
	if dst == nil {
		dst = make(map[string][]byte, len(src))
	}
	maps.Copy(dst, src)
	if err := s.client.WriteItem(ctx, dstPath, dst, dstVer); err != nil {
		return fmt.Errorf("writing %s: %w", dstPath, err)
	}
	return nil
}

// DeleteItem implements secrets.LegacyItemMigrator.
func (s *HCVaultStore) DeleteItem(ctx context.Context, scope secrets.Scope, itemName string) error {
	path := secrets.VaultName(scope) + "/" + itemName
	if err := s.client.DeleteItem(ctx, path); err != nil {
		return fmt.Errorf("deleting %s: %w", path, err)
	}
	return nil
}

// ExportItem implements secrets.ItemExporter: the item's full contents, for
// cross-backend migration only. An absent item is (nil, nil).
func (s *HCVaultStore) ExportItem(ctx context.Context, scope secrets.Scope, tier secrets.Tier, app string) (map[string][]byte, error) {
	path := itemPath(scope, tier, app)
	data, _, err := s.client.ReadItem(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return data, nil
}
