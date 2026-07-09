package gitops

import (
	"strings"
	"testing"

	"github.com/suparcloud/suparship/internal/branding"
	"github.com/suparcloud/suparship/internal/secrets"
)

func TestBuildClusterSecretStoreYAML_K8s(t *testing.T) {
	yaml := BuildClusterSecretStoreYAML(ESOSecretStoreConfig{
		Scope:       secrets.EnvScope("staging"),
		BackendType: secrets.BackendK8s,
	})
	if !strings.Contains(yaml, "name: suparship-store-env-staging") {
		t.Errorf("expected scope store name, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, "remoteNamespace: suparship-secrets-env-staging") {
		t.Errorf("expected vault namespace as remoteNamespace, got:\n%s", yaml)
	}
}

func TestBuildUnifiedClusterSecretStoreYAML(t *testing.T) {
	yaml := BuildUnifiedClusterSecretStoreYAML(UnifiedStoreConfig{
		VaultIDs: []string{"v-global", "v-env-staging"},
	})
	if !strings.Contains(yaml, "name: "+secrets.UnifiedStoreName()) {
		t.Errorf("expected fixed unified store name, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, "connectHost: "+DefaultConnectEndpoint) {
		t.Errorf("expected in-cluster connectHost, got:\n%s", yaml)
	}
	// One store lists every vault the cluster reads, in order.
	if !strings.Contains(yaml, "v-global: 1") || !strings.Contains(yaml, "v-env-staging: 2") {
		t.Errorf("expected ordered multi-vault mapping, got:\n%s", yaml)
	}
	// Auth references the cluster's single sealed Connect token.
	if !strings.Contains(yaml, "name: "+secrets.ConnectTokenSecretName) {
		t.Errorf("expected unified auth secret name, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, "namespace: "+secrets.OnePasswordRemoteNamespace) {
		t.Errorf("expected auth secret namespace, got:\n%s", yaml)
	}
}

func TestBuildExternalSecretYAML(t *testing.T) {
	yaml := BuildExternalSecretYAML(ESOExternalSecretConfig{
		Name:      "web-secrets",
		Namespace: "acme-web-prod",
		StoreName: "suparship-store-global", // default store
		Items: []ESOItemRef{
			{Key: "shared-global", StoreName: "suparship-store-global"},
			{Key: "web-global", StoreName: "suparship-store-global"},
			{Key: "shared-env-prod", StoreName: "suparship-store-env-prod"},
			{Key: "web-env-prod", StoreName: "suparship-store-env-prod"},
		},
	})
	if !strings.Contains(yaml, "name: web-secrets") {
		t.Errorf("expected merged target name, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, "name: suparship-store-global") {
		t.Errorf("expected default store ref, got:\n%s", yaml)
	}
	// Global items use the default store → no sourceRef; env items differ → sourceRef.
	gi := strings.Index(yaml, `"web-global"`)
	ei := strings.Index(yaml, `"web-env-prod"`)
	if gi < 0 || ei < 0 || gi > ei {
		t.Errorf("expected global items before env items, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, "sourceRef:") || !strings.Contains(yaml, "name: suparship-store-env-prod") {
		t.Errorf("expected per-entry sourceRef for the env store, got:\n%s", yaml)
	}
	// The global store is the default, so it must not appear as a sourceRef.
	if strings.Contains(yaml, "name: suparship-store-global\n        kind: ClusterSecretStore") {
		t.Errorf("did not expect a sourceRef for the default (global) store, got:\n%s", yaml)
	}
}

func TestBuildSecretStoresForConfig_K8s(t *testing.T) {
	cfg := secrets.BackendConfig{Type: secrets.BackendK8s}
	stores := BuildSecretStoresForConfig(cfg, []string{"staging", "prod"}, branding.Config{})
	// global + 2 envs; clusters get no store of their own (cluster items live
	// in the env vault).
	if len(stores) != 3 {
		t.Fatalf("expected 3 stores, got %d", len(stores))
	}
	names := map[string]bool{}
	for _, s := range stores {
		names[s.Name()] = true
		if s.BackendType != secrets.BackendK8s {
			t.Errorf("expected k8s backend, got %q", s.BackendType)
		}
	}
	for _, want := range []string{"suparship-store-global", "suparship-store-env-staging", "suparship-store-env-prod"} {
		if !names[want] {
			t.Errorf("missing store %q", want)
		}
	}
}

func TestBuildSecretStoresForConfig_1PasswordEmitsNoInfraStores(t *testing.T) {
	// 1Password stores are per-workload-cluster (published via the sealing
	// flow), never to _infra/secret-stores/. The _infra builder must return
	// nothing for the 1Password backend.
	cfg := secrets.BackendConfig{Type: secrets.Backend1Password}
	cfg.UpsertVault(secrets.GlobalScope(), secrets.VaultRef{VaultID: "g1"})
	stores := BuildSecretStoresForConfig(cfg, []string{"staging", "prod"}, branding.Config{})
	if len(stores) != 0 {
		t.Fatalf("expected 0 _infra stores for 1Password, got %d", len(stores))
	}
}

func TestBuildAppExternalSecret_PresenceDriven(t *testing.T) {
	cfg := BuildAppExternalSecret(WorkloadExternalSecretParams{
		App:       "web",
		Namespace: "acme-web-prod",
		Env:       "prod",
		Cluster:   "c1",
		Presence: ScopePresence{
			GlobalApp: true,
			EnvShared: true, EnvApp: true,
			// no cluster keys
		},
	})
	if cfg == nil {
		t.Fatal("expected a config")
	}
	if cfg.Name != "web-secrets" {
		t.Errorf("expected merged target web-secrets, got %q", cfg.Name)
	}
	// Ordered: global.app, env.shared, env.app (global.shared absent).
	wantKeys := []string{"web-global", "shared-env-prod", "web-env-prod"}
	if len(cfg.Items) != len(wantKeys) {
		t.Fatalf("expected %d items, got %d: %+v", len(wantKeys), len(cfg.Items), cfg.Items)
	}
	for i, k := range wantKeys {
		if cfg.Items[i].Key != k {
			t.Errorf("item %d: got %q, want %q", i, cfg.Items[i].Key, k)
		}
	}
	// Global item uses the default store; env items carry the env store.
	if cfg.Items[0].StoreName != "suparship-store-global" {
		t.Errorf("global item store = %q", cfg.Items[0].StoreName)
	}
	if cfg.Items[1].StoreName != "suparship-store-env-prod" {
		t.Errorf("env item store = %q", cfg.Items[1].StoreName)
	}
}

func TestBuildAppExternalSecret_ProjectItemsOrderedWithinBands(t *testing.T) {
	cfg := BuildAppExternalSecret(WorkloadExternalSecretParams{
		App:       "web",
		Namespace: "acme-web-prod",
		Env:       "prod",
		Project:   "acme",
		Presence: ScopePresence{
			GlobalShared: true, GlobalApp: true, ProjectShared: true,
			EnvShared: true, EnvApp: true, ProjectEnvShared: true,
		},
	})
	if cfg == nil {
		t.Fatal("expected a config")
	}
	// Within each band: org-shared → project-shared → app.
	wantKeys := []string{
		"shared-global", "shared-project-acme", "acme-web-global",
		"shared-env-prod", "shared-project-acme-env-prod", "acme-web-env-prod",
	}
	if len(cfg.Items) != len(wantKeys) {
		t.Fatalf("expected %d items, got %d: %+v", len(wantKeys), len(cfg.Items), cfg.Items)
	}
	for i, k := range wantKeys {
		if cfg.Items[i].Key != k {
			t.Errorf("item %d: got %q, want %q", i, cfg.Items[i].Key, k)
		}
	}
	// project-global resolves to the global store; project-env to the env store.
	if cfg.Items[1].StoreName != "suparship-store-global" {
		t.Errorf("project-global store = %q, want global store", cfg.Items[1].StoreName)
	}
	if cfg.Items[4].StoreName != "suparship-store-env-prod" {
		t.Errorf("project-env store = %q, want env store", cfg.Items[4].StoreName)
	}
}

// Project presence is ignored when no project is set (e.g. legacy callers).
func TestBuildAppExternalSecret_NoProjectSkipsProjectItems(t *testing.T) {
	cfg := BuildAppExternalSecret(WorkloadExternalSecretParams{
		App: "web", Namespace: "ns", Env: "prod",
		Presence: ScopePresence{GlobalApp: true, ProjectShared: true, ProjectEnvShared: true},
	})
	if cfg == nil || len(cfg.Items) != 1 || cfg.Items[0].Key != "web-global" {
		t.Fatalf("expected only the app-global item, got %+v", cfg)
	}
}

func TestBuildAppExternalSecret_ClusterItemsUseEnvStore(t *testing.T) {
	cfg := BuildAppExternalSecret(WorkloadExternalSecretParams{
		App:       "web",
		Namespace: "acme-web-prod",
		Env:       "prod",
		Cluster:   "c1",
		Presence: ScopePresence{
			EnvApp:        true,
			ClusterShared: true, ClusterApp: true,
		},
	})
	if cfg == nil {
		t.Fatal("expected a config")
	}
	// Cluster items keep their cluster-suffixed names but extract from the ENV
	// store — the items live inside the env vault.
	wantKeys := []string{"web-env-prod", "shared-cluster-c1", "web-cluster-c1"}
	if len(cfg.Items) != len(wantKeys) {
		t.Fatalf("expected %d items, got %d: %+v", len(wantKeys), len(cfg.Items), cfg.Items)
	}
	for i, k := range wantKeys {
		if cfg.Items[i].Key != k {
			t.Errorf("item %d: got %q, want %q", i, cfg.Items[i].Key, k)
		}
		if cfg.Items[i].StoreName != "suparship-store-env-prod" {
			t.Errorf("item %q store = %q, want env store", k, cfg.Items[i].StoreName)
		}
	}
}

// TestBuildAppExternalSecret_PreviewReusesBaseEnvVault is the core guarantee of
// the preview design: a preview reads the BASE env store (not a per-preview
// vault), layering base env → preview band → per-PR items in precedence order.
func TestBuildAppExternalSecret_PreviewReusesBaseEnvVault(t *testing.T) {
	cfg := BuildAppExternalSecret(WorkloadExternalSecretParams{
		App:         "web",
		Namespace:   "web-pr-42",
		Env:         "staging", // BASE env — the preview clones it
		IsPreview:   true,
		PreviewName: "pr-42",
		Presence: ScopePresence{
			EnvApp:          true,
			PreviewShared:   true,
			PreviewApp:      true,
			PreviewPRShared: true,
			PreviewPRApp:    true,
		},
	})
	if cfg == nil {
		t.Fatal("expected a config")
	}
	// Order: base env app → preview band (shared, app) → per-PR (shared, app).
	wantKeys := []string{
		"web-env-staging",
		"shared-env-preview", "web-env-preview",
		"shared-env-preview-pr-42", "web-env-preview-pr-42",
	}
	if len(cfg.Items) != len(wantKeys) {
		t.Fatalf("expected %d items, got %d: %+v", len(wantKeys), len(cfg.Items), cfg.Items)
	}
	for i, k := range wantKeys {
		if cfg.Items[i].Key != k {
			t.Errorf("item %d: got %q, want %q", i, cfg.Items[i].Key, k)
		}
		// Every item — including the preview bands — must read from the BASE env
		// store. No per-preview store/vault may appear.
		if cfg.Items[i].StoreName != "suparship-store-env-staging" {
			t.Errorf("item %q store = %q, want base env store", k, cfg.Items[i].StoreName)
		}
		if strings.Contains(cfg.Items[i].StoreName, "pr-42") {
			t.Errorf("item %q must not reference a per-preview store, got %q", k, cfg.Items[i].StoreName)
		}
	}
}

// TestBuildAppExternalSecret_PreviewWithoutPRItems covers the common case: a
// preview band exists but no per-PR override has been written out-of-band.
func TestBuildAppExternalSecret_PreviewWithoutPRItems(t *testing.T) {
	cfg := BuildAppExternalSecret(WorkloadExternalSecretParams{
		App:         "web",
		Namespace:   "web-pr-7",
		Env:         "staging",
		IsPreview:   true,
		PreviewName: "pr-7",
		Presence:    ScopePresence{EnvApp: true, PreviewApp: true},
	})
	if cfg == nil {
		t.Fatal("expected a config")
	}
	wantKeys := []string{"web-env-staging", "web-env-preview"}
	if len(cfg.Items) != len(wantKeys) {
		t.Fatalf("expected %d items, got %d: %+v", len(wantKeys), len(cfg.Items), cfg.Items)
	}
	for i, k := range wantKeys {
		if cfg.Items[i].Key != k {
			t.Errorf("item %d: got %q, want %q", i, cfg.Items[i].Key, k)
		}
	}
}

func TestBuildAppExternalSecret_UnifiedStore(t *testing.T) {
	cfg := BuildAppExternalSecret(WorkloadExternalSecretParams{
		App:          "web",
		Namespace:    "acme-web-prod",
		Env:          "prod",
		Cluster:      "c1",
		UnifiedStore: true,
		Presence: ScopePresence{
			GlobalShared: true,
			EnvApp:       true,
			ClusterApp:   true,
		},
	})
	if cfg == nil {
		t.Fatal("expected a config")
	}
	// 1Password: every item extracts from the single per-cluster store.
	if cfg.StoreName != secrets.UnifiedStoreName() {
		t.Errorf("default store = %q, want %q", cfg.StoreName, secrets.UnifiedStoreName())
	}
	for _, it := range cfg.Items {
		if it.StoreName != secrets.UnifiedStoreName() {
			t.Errorf("item %q store = %q, want unified store", it.Key, it.StoreName)
		}
	}
	// No per-entry sourceRef should be rendered (item store == default store).
	yaml := BuildExternalSecretYAML(*cfg)
	if strings.Contains(yaml, "sourceRef:") {
		t.Errorf("expected no sourceRef in unified-store mode, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, "name: "+secrets.UnifiedStoreName()) {
		t.Errorf("expected unified store as secretStoreRef, got:\n%s", yaml)
	}
}

func TestBuildAppExternalSecret_OmitsClusterWhenUnbound(t *testing.T) {
	cfg := BuildAppExternalSecret(WorkloadExternalSecretParams{
		App:       "web",
		Namespace: "acme-web-staging",
		Env:       "staging",
		Cluster:   "", // unbound
		Presence:  ScopePresence{EnvApp: true, ClusterApp: true},
	})
	if cfg == nil {
		t.Fatal("expected a config")
	}
	for _, it := range cfg.Items {
		if strings.Contains(it.Key, "cluster-") || strings.Contains(it.StoreName, "cluster-") {
			t.Errorf("expected no cluster item when unbound, got %+v", it)
		}
	}
}

func TestBuildAppExternalSecret_NilWhenNoKeys(t *testing.T) {
	if cfg := BuildAppExternalSecret(WorkloadExternalSecretParams{App: "web", Env: "prod"}); cfg != nil {
		t.Errorf("expected nil when no scope has keys, got %+v", cfg)
	}
}

// ── BuildAppConfigMapYAML (unchanged behavior) ───────────────────────────────

func TestBuildAppConfigMapYAML_WithVars(t *testing.T) {
	yaml := BuildAppConfigMapYAML("nginx-config", "demo-nginx-staging", map[string]string{
		"LOG_LEVEL": "info",
		"APP_ENV":   "staging",
	}, branding.Config{})
	if !strings.Contains(yaml, "name: nginx-config") {
		t.Error("expected name 'nginx-config'")
	}
	if !strings.Contains(yaml, `APP_ENV: "staging"`) {
		t.Error("expected APP_ENV var")
	}
}

func TestBuildAppConfigMapYAML_Empty(t *testing.T) {
	yaml := BuildAppConfigMapYAML("nginx-config", "demo-nginx-prod", nil, branding.Config{})
	if !strings.Contains(yaml, "data:\n  {}\n") {
		t.Errorf("expected empty data block, got:\n%s", yaml)
	}
}

func TestBuildExternalSecretYAML_RefreshInterval(t *testing.T) {
	// Configured value is emitted.
	yaml := BuildExternalSecretYAML(ESOExternalSecretConfig{
		Name: "web-secrets", Namespace: "ns", StoreName: "s",
		Items:           []ESOItemRef{{Key: "web-global", StoreName: "s"}},
		RefreshInterval: "30s",
	})
	if !strings.Contains(yaml, "refreshInterval: 30s") {
		t.Errorf("expected refreshInterval: 30s, got:\n%s", yaml)
	}
	// Empty falls back to the 1m default.
	yaml = BuildExternalSecretYAML(ESOExternalSecretConfig{
		Name: "web-secrets", Namespace: "ns", StoreName: "s",
		Items: []ESOItemRef{{Key: "web-global", StoreName: "s"}},
	})
	if !strings.Contains(yaml, "refreshInterval: 1m") {
		t.Errorf("expected default refreshInterval: 1m, got:\n%s", yaml)
	}
}

func TestBuildAppExternalSecret_PassesRefreshInterval(t *testing.T) {
	cfg := BuildAppExternalSecret(WorkloadExternalSecretParams{
		App: "web", Namespace: "ns", Env: "prod",
		Presence:        ScopePresence{GlobalApp: true},
		RefreshInterval: "45s",
	})
	if cfg == nil || cfg.RefreshInterval != "45s" {
		t.Fatalf("expected RefreshInterval threaded to config, got %+v", cfg)
	}
}

func TestBuildAppExternalSecret_StackItemsAfterProject(t *testing.T) {
	cfg := BuildAppExternalSecret(WorkloadExternalSecretParams{
		App: "web", Namespace: "ns", Env: "prod", Project: "proj", Stack: "voiceai",
		Presence: ScopePresence{
			GlobalShared: true, ProjectShared: true, StackShared: true, GlobalApp: true,
			EnvShared: true, ProjectEnvShared: true, StackEnvShared: true, EnvApp: true,
		},
	})
	if cfg == nil {
		t.Fatal("expected a config")
	}
	want := []string{
		"shared-global", "shared-project-proj", "shared-stack-proj-voiceai", "proj-web-global",
		"shared-env-prod", "shared-project-proj-env-prod", "shared-stack-proj-voiceai-env-prod", "proj-web-env-prod",
	}
	if len(cfg.Items) != len(want) {
		t.Fatalf("expected %d items, got %d: %+v", len(want), len(cfg.Items), cfg.Items)
	}
	for i, k := range want {
		if cfg.Items[i].Key != k {
			t.Errorf("item %d = %q, want %q", i, cfg.Items[i].Key, k)
		}
	}
}
