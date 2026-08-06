package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/secrets"
	"github.com/suparcloud/suparship/internal/secrets/onepassword"
)

// dynamicVaultStore is a secrets.VaultStore that picks the concrete backend
// (Kubernetes, 1Password, or HashiCorp Vault) per operation from the CURRENT
// org config. Switching the secrets backend in Settings therefore takes effect
// immediately, instead of being frozen at process startup (which left the UI
// reading/writing the old backend after a switch). The credentialed stores are
// built lazily and cached, keyed by their token + config, so a token paste or
// an address change rebuilds them.
type dynamicVaultStore struct {
	org       rbac.OrgStore
	k8s       secrets.VaultStore
	newSA     func(ctx context.Context, token string) (onepassword.SAClient, error)
	loadToken func(ctx context.Context) (string, error)
	// newHCVault builds a HashiCorp Vault store for the given org config +
	// token. Nil disables the vault backend (fake mode).
	newHCVault func(cfg secrets.HCVaultConfig, token string) (secrets.VaultStore, error)
	// loadHCVaultToken reads the suparship write token for HashiCorp Vault.
	loadHCVaultToken func(ctx context.Context) (string, error)
	logger           *slog.Logger

	mu      sync.Mutex
	opStore secrets.VaultStore
	opToken string
	hvStore secrets.VaultStore
	hvKey   string
}

func newDynamicVaultStore(
	org rbac.OrgStore,
	k8s secrets.VaultStore,
	newSA func(ctx context.Context, token string) (onepassword.SAClient, error),
	loadToken func(ctx context.Context) (string, error),
	logger *slog.Logger,
) *dynamicVaultStore {
	return &dynamicVaultStore{org: org, k8s: k8s, newSA: newSA, loadToken: loadToken, logger: logger}
}

// withHCVault wires the HashiCorp Vault backend into the dynamic store.
func (d *dynamicVaultStore) withHCVault(
	newStore func(cfg secrets.HCVaultConfig, token string) (secrets.VaultStore, error),
	loadToken func(ctx context.Context) (string, error),
) *dynamicVaultStore {
	d.newHCVault = newStore
	d.loadHCVaultToken = loadToken
	return d
}

// resolve returns the store for the SELECTED backend. When that backend cannot
// be built — no token, no address, client error — it returns the k8s store
// (never nil) TOGETHER WITH a non-nil error naming the reason.
//
// The split matters. Reads may use the fallback: an operator mid-migration still
// wants to see what is in the old backend. WRITES MUST NOT. Silently storing a
// secret in Kubernetes while the operator believes it went to Vault is worse than
// an outage — it is an undetected control failure, invisible because the request
// returns 200 and the subsequent read comes back from the same fallback. So the
// mutating methods refuse; see errBackendUnavailable.
func (d *dynamicVaultStore) resolve(ctx context.Context) (secrets.VaultStore, error) {
	if d.org == nil {
		return d.k8s, nil
	}
	org, err := d.org.GetOrg(ctx)
	if err != nil || org == nil {
		// Can't read the selection, so we can't claim it was honoured.
		return d.k8s, fmt.Errorf("reading org config: %w", err)
	}
	bc := org.SecretBackend
	switch bc.Effective() {
	case secrets.Backend1Password:
		if bc.OnePassword == nil {
			return d.k8s, d.degraded(secrets.Backend1Password, "1Password configuration is missing")
		}
		return d.activeOnePassword(ctx)
	case secrets.BackendVault:
		if bc.Vault == nil {
			return d.k8s, d.degraded(secrets.BackendVault, "vault configuration is missing")
		}
		if d.newHCVault == nil || d.loadHCVaultToken == nil {
			return d.k8s, d.degraded(secrets.BackendVault, "vault backend is not wired in this runtime mode")
		}
		return d.activeHCVault(ctx, *bc.Vault)
	default:
		return d.k8s, nil
	}
}

// active is the read path's view of resolve: the usable store, degradation
// tolerated. Never nil.
func (d *dynamicVaultStore) active(ctx context.Context) secrets.VaultStore {
	store, _ := d.resolve(ctx)
	return store
}

// degraded builds the error returned when the selected backend is unusable, and
// logs it. The message is operator-facing: it names the backend, the reason, and
// why the write was refused rather than quietly redirected.
func (d *dynamicVaultStore) degraded(backend secrets.BackendType, reason string) error {
	d.logger.Warn("selected secrets backend unavailable — reads fall back to the k8s store, writes are refused",
		"backend", backend, "reason", reason)
	return fmt.Errorf("%w: backend is set to %q but %s; refusing the write so the value is not stored in Kubernetes instead",
		errBackendUnavailable, backend, reason)
}

// errBackendUnavailable marks a refusal caused by the selected backend being
// unusable, so callers can distinguish it from a backend-level failure.
var errBackendUnavailable = errors.New("secrets backend unavailable")

func (d *dynamicVaultStore) activeOnePassword(ctx context.Context) (secrets.VaultStore, error) {
	token, err := d.loadToken(ctx)
	if err != nil || token == "" {
		return d.k8s, d.degraded(secrets.Backend1Password,
			"the Service Account token in "+secrets.SATokenSecretName+" is missing or unreadable")
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.opStore != nil && d.opToken == token {
		return d.opStore, nil
	}
	saClient, err := d.newSA(ctx, token)
	if err != nil {
		return d.k8s, d.degraded(secrets.Backend1Password, "the Service Account client failed to initialise: "+err.Error())
	}
	// Resolve vault IDs from the live org config so vault selections made after
	// the client was built still take effect; cluster scope reads its env vault.
	resolver := func(scope secrets.Scope) (string, error) {
		o, gerr := d.org.GetOrg(context.Background())
		if gerr != nil {
			return "", gerr
		}
		return o.SecretBackend.VaultIDForScope(scope)
	}
	d.opStore = onepassword.NewSAVaultStore(saClient, resolver)
	d.opToken = token
	d.logger.Info("1Password vault store active (resolved from current org config)")
	return d.opStore, nil
}

func (d *dynamicVaultStore) activeHCVault(ctx context.Context, cfg secrets.HCVaultConfig) (secrets.VaultStore, error) {
	if cfg.Address == "" {
		return d.k8s, d.degraded(secrets.BackendVault, "no server address is configured")
	}
	token, err := d.loadHCVaultToken(ctx)
	if err != nil || token == "" {
		return d.k8s, d.degraded(secrets.BackendVault,
			"the write token in "+secrets.VaultTokenSecretName+" is missing or unreadable")
	}

	// Rebuild on any input change: token rotation, address/mount/namespace/CA
	// edits. The key deliberately contains the full config, not just the token.
	key := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s", token, cfg.Address, cfg.EffectiveMount(), cfg.Namespace, cfg.CACert)

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.hvStore != nil && d.hvKey == key {
		return d.hvStore, nil
	}
	store, err := d.newHCVault(cfg, token)
	if err != nil {
		return d.k8s, d.degraded(secrets.BackendVault, "the client failed to initialise: "+err.Error())
	}
	d.hvStore = store
	d.hvKey = key
	d.logger.Info("HashiCorp Vault store active", "address", cfg.Address, "mount", cfg.EffectiveMount())
	return d.hvStore, nil
}

// ── Mutating operations: fail closed ────────────────────────────────────────
// Each refuses when the selected backend could not be built, rather than writing
// to (or deleting from) the k8s fallback the operator did not choose.

func (d *dynamicVaultStore) Upsert(ctx context.Context, scope secrets.Scope, tier secrets.Tier, app string, data map[string][]byte) error {
	store, err := d.resolve(ctx)
	if err != nil {
		return err
	}
	return store.Upsert(ctx, scope, tier, app, data)
}

func (d *dynamicVaultStore) EnsureItem(ctx context.Context, scope secrets.Scope, tier secrets.Tier, app string) error {
	store, err := d.resolve(ctx)
	if err != nil {
		return err
	}
	return store.EnsureItem(ctx, scope, tier, app)
}

// DeleteKey fails closed too: deleting from the fallback would report success
// while leaving the real value in place on the selected backend.
func (d *dynamicVaultStore) DeleteKey(ctx context.Context, scope secrets.Scope, tier secrets.Tier, app, key string) error {
	store, err := d.resolve(ctx)
	if err != nil {
		return err
	}
	return store.DeleteKey(ctx, scope, tier, app, key)
}

// ── Read operations: degradation tolerated ──────────────────────────────────

// ListKeys reads through the fallback when the selection is unusable — an
// operator mid-migration still needs to see what the old backend holds. Key
// names only, never values.
func (d *dynamicVaultStore) ListKeys(ctx context.Context, scope secrets.Scope, tier secrets.Tier, app string) ([]secrets.SecretEntry, error) {
	return d.active(ctx).ListKeys(ctx, scope, tier, app)
}

// Probe is the connection test, so it reports degradation rather than hiding it:
// probing the fallback would answer "healthy" about a backend nobody selected.
func (d *dynamicVaultStore) Probe(ctx context.Context, scope secrets.Scope) error {
	store, err := d.resolve(ctx)
	if err != nil {
		return err
	}
	return store.Probe(ctx, scope)
}

// The item-rename migration and the cross-backend migration CLI type-assert
// LegacyItemMigrator / ItemExporter on the store they are handed. When that
// store is this dynamic wrapper, the assertions must keep working — otherwise
// wiring the wrapper in would silently disable both migrations. Delegate with
// a per-call assertion on the resolved backend (every concrete store
// implements both; the error path is a safety net, not an expected case).

var (
	_ secrets.LegacyItemMigrator = (*dynamicVaultStore)(nil)
	_ secrets.ItemExporter       = (*dynamicVaultStore)(nil)
)

// CopyItem and DeleteItem mutate, so they fail closed like Upsert: the item
// rename migration must not rewrite the fallback's items under the impression it
// is fixing up the selected backend's.

func (d *dynamicVaultStore) CopyItem(ctx context.Context, scope secrets.Scope, fromName, toName string) error {
	store, err := d.resolve(ctx)
	if err != nil {
		return err
	}
	m, ok := store.(secrets.LegacyItemMigrator)
	if !ok {
		return fmt.Errorf("active secret backend does not support item copy")
	}
	return m.CopyItem(ctx, scope, fromName, toName)
}

func (d *dynamicVaultStore) DeleteItem(ctx context.Context, scope secrets.Scope, itemName string) error {
	store, err := d.resolve(ctx)
	if err != nil {
		return err
	}
	m, ok := store.(secrets.LegacyItemMigrator)
	if !ok {
		return fmt.Errorf("active secret backend does not support item delete")
	}
	return m.DeleteItem(ctx, scope, itemName)
}

func (d *dynamicVaultStore) ExportItem(ctx context.Context, scope secrets.Scope, tier secrets.Tier, app string) (map[string][]byte, error) {
	e, ok := d.active(ctx).(secrets.ItemExporter)
	if !ok {
		return nil, fmt.Errorf("active secret backend does not support item export")
	}
	return e.ExportItem(ctx, scope, tier, app)
}
