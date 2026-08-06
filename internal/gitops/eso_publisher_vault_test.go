package gitops

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/suparcloud/suparship/internal/branding"
	"github.com/suparcloud/suparship/internal/secrets"
)

// The Vault backend's dataFrom keys must be full KV v2 paths — a bare item
// name would be looked up at the mount root, resolve to nothing, and leave the
// ExternalSecret silently NotReady. This pins the exact key format against the
// same {VaultName}/{ItemName} layout the hcvault store writes.
func TestBuildAppExternalSecret_VaultPathQualifiedKeys(t *testing.T) {
	cfg := BuildAppExternalSecret(WorkloadExternalSecretParams{
		App:       "web",
		Namespace: "acme-web-prod",
		Env:       "prod",
		Project:   "acme",
		Cluster:   "c1",
		Backend:   secrets.BackendVault,
		Presence: ScopePresence{
			GlobalShared: true,
			EnvApp:       true,
			ClusterApp:   true,
		},
	})
	if cfg == nil {
		t.Fatal("expected a config")
	}
	wantKeys := []string{
		"suparship-secrets-global/shared-global",
		"suparship-secrets-env-prod/acme-web-env-prod",
		"suparship-secrets-env-prod/acme-web-cluster-c1",
	}
	if len(cfg.Items) != len(wantKeys) {
		t.Fatalf("items = %+v, want %d keys", cfg.Items, len(wantKeys))
	}
	for i, k := range wantKeys {
		if cfg.Items[i].Key != k {
			t.Errorf("item %d: got %q, want %q", i, cfg.Items[i].Key, k)
		}
	}
	// Vault uses the single per-cluster store — no per-entry sourceRef.
	if cfg.StoreName != secrets.UnifiedStoreName() {
		t.Errorf("default store = %q, want %q", cfg.StoreName, secrets.UnifiedStoreName())
	}
	yaml := BuildExternalSecretYAML(*cfg)
	if strings.Contains(yaml, "sourceRef:") {
		t.Errorf("expected no sourceRef for the vault backend, got:\n%s", yaml)
	}
}

// The other backends keep bare item names — path qualification is vault-only.
func TestBuildAppExternalSecret_BareKeysForOtherBackends(t *testing.T) {
	for _, backend := range []secrets.BackendType{secrets.BackendK8s, secrets.Backend1Password} {
		cfg := BuildAppExternalSecret(WorkloadExternalSecretParams{
			App: "web", Namespace: "ns", Env: "prod", Project: "acme",
			Backend:  backend,
			Presence: ScopePresence{EnvApp: true},
		})
		if cfg == nil {
			t.Fatalf("%s: expected a config", backend)
		}
		if got := cfg.Items[0].Key; got != "acme-web-env-prod" {
			t.Errorf("%s: key = %q, want bare item name", backend, got)
		}
	}
}

// The per-component data[] projection resolves through the same items, so its
// ItemKey must be path-qualified for vault too.
func TestBuildComponentExternalSecret_VaultQualifiedItemKey(t *testing.T) {
	cfg := BuildComponentExternalSecret(WorkloadExternalSecretParams{
		App: "web", Namespace: "ns", Env: "prod", Project: "acme",
		Backend:    secrets.BackendVault,
		Presence:   ScopePresence{EnvApp: true},
		SecretKeys: ScopeSecretKeys{EnvApp: []string{"DB_URL"}},
	}, "web-worker-secrets", map[string]string{"DATABASE_URL": "DB_URL"})
	if cfg == nil {
		t.Fatal("expected a config")
	}
	if got := cfg.Data[0].ItemKey; got != "suparship-secrets-env-prod/acme-web-env-prod" {
		t.Errorf("ItemKey = %q, want path-qualified", got)
	}
	if cfg.Data[0].Property != "DB_URL" {
		t.Errorf("Property = %q", cfg.Data[0].Property)
	}
}

func TestBuildVaultClusterSecretStoreYAML(t *testing.T) {
	yaml := BuildVaultClusterSecretStoreYAML(VaultStoreConfig{
		Address: "https://vault.example.com:8200",
		Mount:   "suparship",
	})
	for _, want := range []string{
		"name: " + secrets.UnifiedStoreName(),
		"vault:",
		"server: https://vault.example.com:8200",
		"path: suparship",
		"version: v2",
		"tokenSecretRef:",
		"name: " + secrets.VaultTokenClusterSecretName,
		"key: " + secrets.VaultTokenSecretKey,
		"namespace: external-secrets",
	} {
		if !strings.Contains(yaml, want) {
			t.Errorf("store YAML missing %q:\n%s", want, yaml)
		}
	}
	// Optional stanzas absent when unset.
	for _, absent := range []string{"caBundle:", "namespace: \n"} {
		if strings.Contains(yaml, absent+"\n      auth") {
			t.Errorf("unexpected %q in minimal store YAML:\n%s", absent, yaml)
		}
	}

	// Enterprise namespace + private CA render when set; CA is base64.
	pem := "-----BEGIN CERTIFICATE-----\nabc\n-----END CERTIFICATE-----\n"
	yaml = BuildVaultClusterSecretStoreYAML(VaultStoreConfig{
		Address:   "https://vault.internal:8200",
		Namespace: "team-a",
		CACertPEM: pem,
	})
	if !strings.Contains(yaml, "namespace: team-a") {
		t.Errorf("enterprise namespace missing:\n%s", yaml)
	}
	if !strings.Contains(yaml, "caBundle: "+base64.StdEncoding.EncodeToString([]byte(pem))) {
		t.Errorf("caBundle missing or not base64:\n%s", yaml)
	}
	if !strings.Contains(yaml, "path: "+secrets.DefaultVaultMount) {
		t.Errorf("mount default missing:\n%s", yaml)
	}
}

// The k8s-only guard on the tooling-cluster store set must hold for vault:
// vault stores go through the per-cluster publishing path, and emitting them
// into _infra/ would recreate the k8s backend's hub-only bug.
func TestBuildSecretStoresForConfig_VaultEmitsNone(t *testing.T) {
	stores := BuildSecretStoresForConfig(
		secrets.BackendConfig{Type: secrets.BackendVault},
		[]string{"staging", "prod"},
		branding.Config{},
	)
	if len(stores) != 0 {
		t.Errorf("vault backend emitted %d tooling-cluster stores, want 0", len(stores))
	}
}
