package secrets

import (
	"context"
	"testing"

	"k8s.io/client-go/kubernetes/fake"
)

// ── naming ──────────────────────────────────────────────────────────────────

func TestVaultName(t *testing.T) {
	cases := map[Scope]string{
		GlobalScope():       "suparship-secrets-global",
		EnvScope("staging"): "suparship-secrets-env-staging",
		// Cluster overrides live in the env vault — no per-cluster vault.
		ClusterScope("prod", "eu-1"): "suparship-secrets-env-prod",
	}
	for scope, want := range cases {
		if got := VaultName(scope); got != want {
			t.Errorf("VaultName(%+v) = %q, want %q", scope, got, want)
		}
	}
}

func TestItemAndStoreAndWorkloadNames(t *testing.T) {
	env := EnvScope("staging")
	if got := SharedItemName(env); got != "shared-env-staging" {
		t.Errorf("SharedItemName = %q", got)
	}
	if got := AppItemName(env, "api"); got != "api-env-staging" {
		t.Errorf("AppItemName = %q", got)
	}
	if got := ItemName(env, TierShared, "ignored"); got != "shared-env-staging" {
		t.Errorf("ItemName shared = %q", got)
	}
	if got := StoreName(env); got != "suparship-store-env-staging" {
		t.Errorf("StoreName = %q", got)
	}
	// Cluster scope: items keep the cluster suffix, but vault/store resolve to
	// the env vault/store (cluster overrides live inside the env vault).
	cluster := ClusterScope("staging", "eu-1")
	if got := SharedItemName(cluster); got != "shared-cluster-eu-1" {
		t.Errorf("SharedItemName(cluster) = %q", got)
	}
	if got := AppItemName(cluster, "api"); got != "api-cluster-eu-1" {
		t.Errorf("AppItemName(cluster) = %q", got)
	}
	if got := StoreName(cluster); got != "suparship-store-env-staging" {
		t.Errorf("StoreName(cluster) = %q, want env store", got)
	}
	if got := AppSecretName("api"); got != "api-secrets" {
		t.Errorf("AppSecretName = %q", got)
	}
	if got := AppConfigMapName("api"); got != "api-config" {
		t.Errorf("AppConfigMapName = %q", got)
	}
}

// ── resolve ─────────────────────────────────────────────────────────────────

func TestResolveScopes(t *testing.T) {
	got := ResolveScopes(
		ScopeKeys{Shared: []string{"A"}, App: []string{"B"}},
		ScopeKeys{Shared: []string{"B"}, App: []string{"C"}},
		ScopeKeys{App: []string{"A"}},
	)
	// A: set by global-shared, overwritten by cluster-app → cluster/app.
	if got["A"].Source != SourceCluster || got["A"].Tier != string(TierApp) {
		t.Errorf("A resolved to %+v, want cluster/app", got["A"])
	}
	// B: global-app then env-shared → env wins (env-shared is later).
	if got["B"].Source != SourceEnv || got["B"].Tier != string(TierShared) {
		t.Errorf("B resolved to %+v, want env/shared", got["B"])
	}
	// C: only env-app.
	if got["C"].Source != SourceEnv || got["C"].Tier != string(TierApp) {
		t.Errorf("C resolved to %+v, want env/app", got["C"])
	}
}

// ── backend vault refs ──────────────────────────────────────────────────────

func TestBackendConfig_VaultRefs(t *testing.T) {
	c := &BackendConfig{Type: Backend1Password}
	c.UpsertVault(GlobalScope(), VaultRef{VaultID: "g1"})
	c.UpsertVault(EnvScope("staging"), VaultRef{VaultID: "e1"})

	if id, err := c.VaultIDForScope(GlobalScope()); err != nil || id != "g1" {
		t.Errorf("global vault id = %q, %v", id, err)
	}
	if id, err := c.VaultIDForScope(EnvScope("staging")); err != nil || id != "e1" {
		t.Errorf("env vault id = %q, %v", id, err)
	}
	// Cluster scope delegates to the env vault.
	if ref := c.FindVault(ClusterScope("staging", "c1")); ref == nil || ref.VaultID != "e1" {
		t.Errorf("FindVault(cluster) = %+v, want env vault e1", ref)
	}
	if id, err := c.VaultIDForScope(ClusterScope("staging", "c1")); err != nil || id != "e1" {
		t.Errorf("cluster vault id = %q, %v (want env vault e1)", id, err)
	}
	if _, err := c.VaultIDForScope(EnvScope("unknown")); err == nil {
		t.Error("expected error for unprovisioned env vault")
	}

	c.RemoveVault(EnvScope("staging"))
	if c.FindVault(EnvScope("staging")) != nil {
		t.Error("expected env vault removed")
	}
}

// ── mem + k8s vault stores ──────────────────────────────────────────────────

func TestVaultStores_Isolation(t *testing.T) {
	stores := map[string]VaultStore{
		"mem": NewMemVaultStore(),
		"k8s": NewK8sVaultStore(fake.NewSimpleClientset()),
	}
	ctx := context.Background()
	for name, store := range stores {
		t.Run(name, func(t *testing.T) {
			scope := EnvScope("staging")
			if err := store.Upsert(ctx, scope, TierShared, "", map[string][]byte{"S": []byte("1")}); err != nil {
				t.Fatalf("upsert shared: %v", err)
			}
			if err := store.Upsert(ctx, scope, TierApp, "api", map[string][]byte{"A": []byte("1")}); err != nil {
				t.Fatalf("upsert app: %v", err)
			}
			// app api sees only its own key; another app sees nothing.
			if keys, _ := store.ListKeys(ctx, scope, TierApp, "api"); len(keys) != 1 || keys[0].Key != "A" {
				t.Errorf("app api keys = %+v", keys)
			}
			if keys, _ := store.ListKeys(ctx, scope, TierApp, "web"); len(keys) != 0 {
				t.Errorf("app web should be empty, got %+v", keys)
			}
			// delete + merge semantics.
			_ = store.Upsert(ctx, scope, TierApp, "api", map[string][]byte{"B": []byte("2")})
			_ = store.DeleteKey(ctx, scope, TierApp, "api", "A")
			if keys, _ := store.ListKeys(ctx, scope, TierApp, "api"); len(keys) != 1 || keys[0].Key != "B" {
				t.Errorf("after delete, app api keys = %+v", keys)
			}
		})
	}
}
