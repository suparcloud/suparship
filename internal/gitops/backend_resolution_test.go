package gitops

import (
	"testing"

	"github.com/suparcloud/suparship/internal/secrets"
)

// The bug this guards: PublisherConfig.BackendConfig was declared but never
// assigned by the server, so effectiveBackend() always answered BackendK8s. Every
// app's ExternalSecret was then rendered with the k8s store naming
// (suparship-store-global) even on a 1Password/Vault org — a store that only the
// k8s backend publishes — and for Vault the remoteRef key lost its container
// path. ESO reported "unable to validate store" and nothing pointed at the cause.
func TestEffectiveBackend_PrefersLiveFuncOverSnapshot(t *testing.T) {
	snapshot := &secrets.BackendConfig{Type: secrets.Backend1Password}
	live := secrets.BackendConfig{Type: secrets.BackendVault}

	p := &Publisher{cfg: PublisherConfig{
		BackendConfig:     snapshot,
		BackendConfigFunc: func() secrets.BackendConfig { return live },
	}}
	if got := p.effectiveBackend(); got != secrets.BackendVault {
		t.Errorf("effectiveBackend() = %q, want the live func's value %q", got, secrets.BackendVault)
	}

	// A runtime switch must be picked up without rebuilding the Publisher —
	// that is the whole reason this is a func and not a snapshot.
	live = secrets.BackendConfig{Type: secrets.BackendK8s}
	if got := p.effectiveBackend(); got != secrets.BackendK8s {
		t.Errorf("after a live switch effectiveBackend() = %q, want %q", got, secrets.BackendK8s)
	}
}

func TestEffectiveBackend_FallsBackToSnapshotThenK8s(t *testing.T) {
	// No func: the snapshot still applies, so existing callers are unaffected.
	p := &Publisher{cfg: PublisherConfig{
		BackendConfig: &secrets.BackendConfig{Type: secrets.BackendVault},
	}}
	if got := p.effectiveBackend(); got != secrets.BackendVault {
		t.Errorf("snapshot path: got %q, want %q", got, secrets.BackendVault)
	}

	// Neither set: k8s, the safe default — never guess a credentialed backend.
	empty := &Publisher{cfg: PublisherConfig{}}
	if got := empty.effectiveBackend(); got != secrets.BackendK8s {
		t.Errorf("unset path: got %q, want %q", got, secrets.BackendK8s)
	}
}

// The two things the backend actually decides, asserted end-to-end through the
// rendering the publisher does — so a regression in either shows up here rather
// than as an ESO error on a cluster.
func TestBackendDrivesStoreNameAndKeyShape(t *testing.T) {
	scope := secrets.EnvScope("staging")

	for _, tc := range []struct {
		backend   secrets.BackendType
		wantStore string
		wantKey   string
	}{
		{secrets.BackendVault, secrets.UnifiedStoreName(), "suparship-secrets-env-staging/shared-env-staging"},
		{secrets.Backend1Password, secrets.UnifiedStoreName(), "shared-env-staging"},
		{secrets.BackendK8s, secrets.StoreName(scope), "shared-env-staging"},
	} {
		p := WorkloadExternalSecretParams{Backend: tc.backend}
		if got := storeForScope(p, scope); got != tc.wantStore {
			t.Errorf("%s: store = %q, want %q", tc.backend, got, tc.wantStore)
		}
		name := secrets.ItemName(scope, secrets.TierShared, "")
		if got := itemKeyFor(p, scope, name); got != tc.wantKey {
			t.Errorf("%s: remoteRef key = %q, want %q", tc.backend, got, tc.wantKey)
		}
	}
}
