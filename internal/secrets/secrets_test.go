package secrets

import (
	"context"
	"testing"

	"k8s.io/client-go/kubernetes/fake"
)

// ── naming ──────────────────────────────────────────────────────────────────

// TestAppItemName_ProjectQualified pins the collision fix: app-tier items are
// project-qualified when the scope carries a project (WithProject), so a
// same-named app in a different project never collides on the shared org/env
// vault. WithProject does not affect vault selection.
func TestAppItemName_ProjectQualified(t *testing.T) {
	global := GlobalScope().WithProject("voiceai")
	env := EnvScope("production").WithProject("voiceai")
	cluster := ClusterScope("production", "eks-aws").WithProject("voiceai")

	cases := map[string]string{
		AppItemName(global, "biglysales-tts"):  "voiceai-biglysales-tts-global",
		AppItemName(env, "biglysales-tts"):     "voiceai-biglysales-tts-env-production",
		AppItemName(cluster, "biglysales-tts"): "voiceai-biglysales-tts-cluster-eks-aws",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("AppItemName = %q, want %q", got, want)
		}
	}

	// Two projects, same app name → distinct items in the same (shared) vault.
	a := AppItemName(GlobalScope().WithProject("proj-a"), "api")
	b := AppItemName(GlobalScope().WithProject("proj-b"), "api")
	if a == b {
		t.Fatalf("same-named app in different projects collided: both %q", a)
	}
	if VaultName(GlobalScope().WithProject("proj-a")) != VaultName(GlobalScope()) {
		t.Error("WithProject must not change vault selection")
	}

	// Legacy (project-less) form is retained for the migration's old-name compute.
	if got := AppItemName(GlobalScope(), "api"); got != "api-global" {
		t.Errorf("legacy AppItemName = %q, want api-global", got)
	}
	// Shared-tier names are unchanged by the project qualifier.
	if got := SharedItemName(EnvScope("production")); got != "shared-env-production" {
		t.Errorf("SharedItemName = %q, want shared-env-production", got)
	}
}

// TestMemVaultStore_CopyAndDeleteItem covers the LegacyItemMigrator primitives
// the rename migration relies on: copy-if-present (merge into the new name) and
// whole-item delete.
func TestMemVaultStore_CopyAndDeleteItem(t *testing.T) {
	m := NewMemVaultStore()
	ctx := context.Background()
	scope := EnvScope("prod")

	if err := m.Upsert(ctx, scope, TierApp, "api", map[string][]byte{"K": []byte("v")}); err != nil {
		t.Fatal(err)
	}
	legacy := AppItemName(scope, "api")                   // api-env-prod
	newer := AppItemName(scope.WithProject("proj"), "api") // proj-api-env-prod

	if err := m.CopyItem(ctx, scope, legacy, newer); err != nil {
		t.Fatal(err)
	}
	keys, _ := m.ListKeys(ctx, scope.WithProject("proj"), TierApp, "api")
	if len(keys) != 1 || keys[0].Key != "K" {
		t.Fatalf("copy failed, new item keys = %+v", keys)
	}
	// Copy-if-present: a missing source is a no-op, not an error.
	if err := m.CopyItem(ctx, scope, "does-not-exist", "whatever"); err != nil {
		t.Fatalf("copy of absent item should be a no-op: %v", err)
	}

	if err := m.DeleteItem(ctx, scope, legacy); err != nil {
		t.Fatal(err)
	}
	if old, _ := m.ListKeys(ctx, scope, TierApp, "api"); len(old) != 0 {
		t.Fatalf("legacy item still present after delete: %+v", old)
	}
	if k2, _ := m.ListKeys(ctx, scope.WithProject("proj"), TierApp, "api"); len(k2) != 1 {
		t.Fatal("new item lost when legacy was deleted")
	}
}

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

func TestProjectScopeNames(t *testing.T) {
	// Project-global: lives in the org global vault + global store.
	pg := ProjectScope("voiceai")
	if got := VaultName(pg); got != "suparship-secrets-global" {
		t.Errorf("VaultName(project) = %q, want global vault", got)
	}
	if got := SharedItemName(pg); got != "shared-project-voiceai" {
		t.Errorf("SharedItemName(project) = %q", got)
	}
	if got := StoreName(pg); got != GlobalStoreName() {
		t.Errorf("StoreName(project) = %q, want global store %q", got, GlobalStoreName())
	}
	// Project-env: lives in the env vault + env store.
	pe := ProjectEnvScope("voiceai", "staging")
	if got := VaultName(pe); got != "suparship-secrets-env-staging" {
		t.Errorf("VaultName(project-env) = %q, want env vault", got)
	}
	if got := SharedItemName(pe); got != "shared-project-voiceai-env-staging" {
		t.Errorf("SharedItemName(project-env) = %q", got)
	}
	if got := StoreName(pe); got != EnvStoreName("staging") {
		t.Errorf("StoreName(project-env) = %q, want env store %q", got, EnvStoreName("staging"))
	}
}

func TestPreviewScopeNames(t *testing.T) {
	// Preview band: items live in the BASE env vault (no per-preview vault),
	// named with a preview suffix.
	pv := PreviewScope("staging")
	if got := VaultName(pv); got != "suparship-secrets-env-staging" {
		t.Errorf("VaultName(preview) = %q, want base env vault", got)
	}
	if got := AppItemName(pv, "api"); got != "api-env-preview" {
		t.Errorf("AppItemName(preview) = %q, want api-env-preview", got)
	}
	if got := SharedItemName(pv); got != "shared-env-preview" {
		t.Errorf("SharedItemName(preview) = %q", got)
	}
	if got := StoreName(pv); got != EnvStoreName("staging") {
		t.Errorf("StoreName(preview) = %q, want env store %q", got, EnvStoreName("staging"))
	}
	// Per-PR override: also in the base env vault, keyed by preview name.
	pr := PreviewPRScope("staging", "pr-42")
	if got := VaultName(pr); got != "suparship-secrets-env-staging" {
		t.Errorf("VaultName(preview-pr) = %q, want base env vault", got)
	}
	if got := AppItemName(pr, "api"); got != "api-env-preview-pr-42" {
		t.Errorf("AppItemName(preview-pr) = %q", got)
	}
	if got := StoreName(pr); got != EnvStoreName("staging") {
		t.Errorf("StoreName(preview-pr) = %q, want env store", got)
	}
}

func TestStackScopeNames(t *testing.T) {
	// Stack-global: lives in the org global vault + global store.
	sg := StackScope("voiceproj", "voiceai")
	if got := VaultName(sg); got != "suparship-secrets-global" {
		t.Errorf("VaultName(stack) = %q, want global vault", got)
	}
	if got := SharedItemName(sg); got != "shared-stack-voiceproj-voiceai" {
		t.Errorf("SharedItemName(stack) = %q", got)
	}
	if got := StoreName(sg); got != GlobalStoreName() {
		t.Errorf("StoreName(stack) = %q, want global store", got)
	}
	// Stack-env: lives in the env vault + env store.
	se := StackEnvScope("voiceproj", "voiceai", "staging")
	if got := VaultName(se); got != "suparship-secrets-env-staging" {
		t.Errorf("VaultName(stack-env) = %q, want env vault", got)
	}
	if got := SharedItemName(se); got != "shared-stack-voiceproj-voiceai-env-staging" {
		t.Errorf("SharedItemName(stack-env) = %q", got)
	}
	if got := StoreName(se); got != EnvStoreName("staging") {
		t.Errorf("StoreName(stack-env) = %q, want env store", got)
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
		ScopeKeys{Shared: []string{"A"}, App: []string{"B"}}, // global
		ScopeKeys{},                                          // projectGlobal
		ScopeKeys{},                                          // stackGlobal
		ScopeKeys{Shared: []string{"B"}, App: []string{"C"}}, // env
		ScopeKeys{},                                          // projectEnv
		ScopeKeys{},                                          // stackEnv
		ScopeKeys{App: []string{"A"}},                        // cluster
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

// Project layers sit between org-shared and app within each band: a project
// secret overrides the org-shared value, but the app's own value still wins.
func TestResolveScopes_ProjectPrecedence(t *testing.T) {
	got := ResolveScopes(
		ScopeKeys{Shared: []string{"K", "P"}}, // global-shared: K, P
		ScopeKeys{Shared: []string{"K"}},      // project-global: K (overrides global K)
		ScopeKeys{},                           // stackGlobal
		ScopeKeys{},                           // env
		ScopeKeys{Shared: []string{"P"}},      // project-env: P (overrides global P)
		ScopeKeys{},                           // stackEnv
		ScopeKeys{App: []string{"K"}},         // cluster-app: K (overrides everything)
	)
	// K: global → project-global → cluster-app; cluster wins.
	if got["K"].Source != SourceCluster {
		t.Errorf("K = %+v, want cluster (highest)", got["K"])
	}
	// P: global-shared → project-env-shared; project wins (no app/cluster override).
	if got["P"].Source != SourceProject || got["P"].Tier != string(TierShared) {
		t.Errorf("P = %+v, want project/shared", got["P"])
	}
}

// Stack secrets sit between project-shared and app within each band: a stack
// secret overrides project + org, but the app's own value still wins.
func TestResolveScopes_StackPrecedence(t *testing.T) {
	got := ResolveScopes(
		ScopeKeys{Shared: []string{"X", "Y"}}, // global-shared: X, Y
		ScopeKeys{Shared: []string{"X"}},      // project-global: X
		ScopeKeys{Shared: []string{"X", "Y"}}, // stack-global: X, Y (overrides project+global)
		ScopeKeys{},                           // env
		ScopeKeys{},                           // projectEnv
		ScopeKeys{},                           // stackEnv
		ScopeKeys{App: []string{"X"}},         // cluster-app: X
	)
	// X: ... → stack-global → cluster-app; cluster wins.
	if got["X"].Source != SourceCluster {
		t.Errorf("X = %+v, want cluster (highest)", got["X"])
	}
	// Y: global-shared → stack-global-shared; stack wins.
	if got["Y"].Source != SourceStack || got["Y"].Tier != string(TierShared) {
		t.Errorf("Y = %+v, want stack/shared", got["Y"])
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

	// Project-global is env-agnostic → global vault. Project-env is env-bound →
	// the env vault (regression: it previously fell through to the global vault).
	if id, err := c.VaultIDForScope(ProjectScope("voiceai")); err != nil || id != "g1" {
		t.Errorf("project-global vault id = %q, %v (want global vault g1)", id, err)
	}
	if id, err := c.VaultIDForScope(ProjectEnvScope("voiceai", "staging")); err != nil || id != "e1" {
		t.Errorf("project-env vault id = %q, %v (want env vault e1)", id, err)
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

// TestEnsureItem covers the baseline-item guarantee: an app with no secrets
// still gets an item so its ExternalSecret resolves, and EnsureItem never
// clobbers an existing item's keys.
func TestEnsureItem(t *testing.T) {
	stores := map[string]VaultStore{
		"mem": NewMemVaultStore(),
		"k8s": NewK8sVaultStore(fake.NewSimpleClientset()),
	}
	ctx := context.Background()
	for name, store := range stores {
		t.Run(name, func(t *testing.T) {
			scope := GlobalScope()
			// Create on first call.
			if err := store.EnsureItem(ctx, scope, TierApp, "api"); err != nil {
				t.Fatalf("ensure: %v", err)
			}
			if keys, _ := store.ListKeys(ctx, scope, TierApp, "api"); len(keys) != 0 {
				t.Errorf("baseline item should be empty, got %+v", keys)
			}
			// Add a key, then ensure again — must not wipe it (idempotent).
			if err := store.Upsert(ctx, scope, TierApp, "api", map[string][]byte{"K": []byte("v")}); err != nil {
				t.Fatalf("upsert: %v", err)
			}
			if err := store.EnsureItem(ctx, scope, TierApp, "api"); err != nil {
				t.Fatalf("ensure again: %v", err)
			}
			if keys, _ := store.ListKeys(ctx, scope, TierApp, "api"); len(keys) != 1 || keys[0].Key != "K" {
				t.Errorf("EnsureItem clobbered existing keys: %+v", keys)
			}
		})
	}
}

func TestExternalSecretSettings_EffectiveRefreshInterval(t *testing.T) {
	if got := (ExternalSecretSettings{}).EffectiveRefreshInterval(); got != "1m" {
		t.Errorf("empty refresh interval = %q, want 1m (default)", got)
	}
	if got := (ExternalSecretSettings{RefreshInterval: "  "}).EffectiveRefreshInterval(); got != "1m" {
		t.Errorf("blank refresh interval = %q, want 1m (default)", got)
	}
	if got := (ExternalSecretSettings{RefreshInterval: "30s"}).EffectiveRefreshInterval(); got != "30s" {
		t.Errorf("custom refresh interval = %q, want 30s", got)
	}
	// Reachable via BackendConfig (round-trips through the org secret-backend API).
	bc := BackendConfig{ExternalSecrets: ExternalSecretSettings{RefreshInterval: "2m"}}
	if got := bc.ExternalSecrets.EffectiveRefreshInterval(); got != "2m" {
		t.Errorf("backend refresh interval = %q, want 2m", got)
	}
}
