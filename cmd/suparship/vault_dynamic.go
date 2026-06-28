package main

import (
	"context"
	"log/slog"
	"sync"

	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/secrets"
	"github.com/suparcloud/suparship/internal/secrets/onepassword"
)

// dynamicVaultStore is a secrets.VaultStore that picks the concrete backend
// (Kubernetes vs 1Password) per operation from the CURRENT org config. Switching
// the secrets backend in Settings therefore takes effect immediately, instead of
// being frozen at process startup (which left the UI reading/writing the old
// backend after a switch). The 1Password SA store is built lazily and cached,
// keyed by the SA token, so a token change rebuilds it.
type dynamicVaultStore struct {
	org       rbac.OrgStore
	k8s       secrets.VaultStore
	newSA     func(ctx context.Context, token string) (onepassword.SAClient, error)
	loadToken func(ctx context.Context) (string, error)
	logger    *slog.Logger

	mu      sync.Mutex
	opStore secrets.VaultStore
	opToken string
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

// active resolves the backend for the current org config. It falls back to the
// k8s store (never nil) when 1Password is selected but not usable yet (no token,
// SA client error) so secret operations degrade rather than panic.
func (d *dynamicVaultStore) active(ctx context.Context) secrets.VaultStore {
	if d.org == nil {
		return d.k8s
	}
	org, err := d.org.GetOrg(ctx)
	if err != nil || org == nil {
		return d.k8s
	}
	bc := org.SecretBackend
	if bc.Effective() != secrets.Backend1Password || bc.OnePassword == nil {
		return d.k8s
	}
	token, err := d.loadToken(ctx)
	if err != nil || token == "" {
		d.logger.Warn("1Password backend selected but SA token unavailable — using k8s vault store", "error", err)
		return d.k8s
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.opStore != nil && d.opToken == token {
		return d.opStore
	}
	saClient, err := d.newSA(ctx, token)
	if err != nil {
		d.logger.Warn("1Password SA client init failed — using k8s vault store", "error", err)
		return d.k8s
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
	return d.opStore
}

func (d *dynamicVaultStore) Upsert(ctx context.Context, scope secrets.Scope, tier secrets.Tier, app string, data map[string][]byte) error {
	return d.active(ctx).Upsert(ctx, scope, tier, app, data)
}

func (d *dynamicVaultStore) EnsureItem(ctx context.Context, scope secrets.Scope, tier secrets.Tier, app string) error {
	return d.active(ctx).EnsureItem(ctx, scope, tier, app)
}

func (d *dynamicVaultStore) ListKeys(ctx context.Context, scope secrets.Scope, tier secrets.Tier, app string) ([]secrets.SecretEntry, error) {
	return d.active(ctx).ListKeys(ctx, scope, tier, app)
}

func (d *dynamicVaultStore) DeleteKey(ctx context.Context, scope secrets.Scope, tier secrets.Tier, app, key string) error {
	return d.active(ctx).DeleteKey(ctx, scope, tier, app, key)
}

func (d *dynamicVaultStore) Probe(ctx context.Context, scope secrets.Scope) error {
	return d.active(ctx).Probe(ctx, scope)
}
