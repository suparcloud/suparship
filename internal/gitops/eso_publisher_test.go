package gitops

import (
	"strings"
	"testing"

	"github.com/suparcloud/suparship/internal/branding"
	"github.com/suparcloud/suparship/internal/secrets"
)

func TestBuildClusterSecretStoreYAML_K8s(t *testing.T) {
	cfg := ESOSecretStoreConfig{
		Name:        "k8s-default",
		BackendType: secrets.BackendK8s,
	}
	yaml := BuildClusterSecretStoreYAML(cfg)
	if !strings.Contains(yaml, "name: k8s-default") {
		t.Error("expected store name in YAML")
	}
	if !strings.Contains(yaml, "app.kubernetes.io/managed-by: suparship") {
		t.Error("expected managed-by label")
	}
	if !strings.Contains(yaml, "remoteNamespace: suparship-system") {
		t.Error("expected k8s provider config")
	}
}

func TestBuildClusterSecretStoreYAML_1Password(t *testing.T) {
	cfg := ESOSecretStoreConfig{
		Name:        "onepassword-prod",
		BackendType: secrets.Backend1Password,
		Binding:     secrets.EnvBinding{Env: "prod", VaultID: "v1"},
	}
	yaml := BuildClusterSecretStoreYAML(cfg)
	if !strings.Contains(yaml, "connectHost: "+DefaultConnectEndpoint) {
		t.Errorf("expected in-cluster connectHost, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, "name: op-connect-token-prod") {
		t.Errorf("expected derived auth secret, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, "namespace: "+secrets.OnePasswordRemoteNamespace) {
		t.Errorf("expected auth secret namespace, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, "v1: 1") {
		t.Errorf("expected vault id mapping, got:\n%s", yaml)
	}
}

func TestBuildClusterSecretStoreYAML_1Password_WithPlatformVault(t *testing.T) {
	cfg := ESOSecretStoreConfig{
		Name:            "onepassword-prod",
		BackendType:     secrets.Backend1Password,
		Binding:         secrets.EnvBinding{Env: "prod", VaultID: "v-env"},
		PlatformVaultID: "v-platform",
	}
	yaml := BuildClusterSecretStoreYAML(cfg)
	if !strings.Contains(yaml, "v-env: 1") {
		t.Errorf("expected env vault as priority 1, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, "v-platform: 2") {
		t.Errorf("expected platform vault as priority 2, got:\n%s", yaml)
	}
}

func TestBuildSecretStoresForConfig_IncludesPlatformVault(t *testing.T) {
	cfg := secrets.BackendConfig{
		Type: secrets.Backend1Password,
		OnePassword: &secrets.OnePasswordConfig{
			PlatformVaultID: "v-platform",
			Bindings: []secrets.EnvBinding{
				{Env: "prod", VaultID: "v-prd", Provisioned: true},
			},
		},
	}
	stores := BuildSecretStoresForConfig(cfg, secrets.ResourceNaming{}, "default", branding.Config{})
	if len(stores) != 1 {
		t.Fatalf("expected 1 store, got %d", len(stores))
	}
	if stores[0].PlatformVaultID != "v-platform" {
		t.Errorf("expected PlatformVaultID propagated to store config, got %q", stores[0].PlatformVaultID)
	}
}

func TestBuildCollapsedExternalSecretYAML(t *testing.T) {
	cfg := ESOExternalSecretConfig{
		Name:      "web",
		Namespace: "acme-web-prod",
		StoreName: "onepassword-prod",
		Items: []ESOItemRef{
			{Key: "org", StoreName: "onepassword-prod"},
			{Key: "env-prod", StoreName: "onepassword-prod"},
			{Key: "acme", StoreName: "onepassword-prod"},
			{Key: "acme-web", StoreName: "onepassword-prod"},
			{Key: "acme-web-prod", StoreName: "onepassword-prod"},
		},
	}
	yaml := BuildCollapsedExternalSecretYAML(cfg)

	if !strings.Contains(yaml, "name: web") {
		t.Error("expected resource name 'web'")
	}
	if !strings.Contains(yaml, "namespace: acme-web-prod") {
		t.Error("expected namespace")
	}
	if !strings.Contains(yaml, "name: onepassword-prod") {
		t.Error("expected store ref")
	}
	for _, key := range []string{"org", "env-prod", "acme", "acme-web", "acme-web-prod"} {
		if !strings.Contains(yaml, key) {
			t.Errorf("expected dataFrom key %q", key)
		}
	}
	// All entries share the same store as the top-level secretStoreRef, so
	// no per-entry sourceRef should be emitted (single-store output stays terse).
	if strings.Contains(yaml, "sourceRef:") {
		t.Errorf("did not expect sourceRef when items share the default store, got:\n%s", yaml)
	}
}

func TestBuildCollapsedExternalSecretYAML_PerEntryStoreRef(t *testing.T) {
	cfg := ESOExternalSecretConfig{
		Name:      "web",
		Namespace: "acme-web-prod",
		StoreName: "onepassword-prod",
		Items: []ESOItemRef{
			{Key: "org", StoreName: "platform-shared"},      // platform vault
			{Key: "acme-web-prod", StoreName: "onepassword-prod"}, // env vault
		},
	}
	yaml := BuildCollapsedExternalSecretYAML(cfg)

	// Org entry should carry an explicit per-entry storeRef pointing at the
	// platform-shared store.
	if !strings.Contains(yaml, "sourceRef:") {
		t.Errorf("expected sourceRef block for cross-store item, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, "name: platform-shared") {
		t.Errorf("expected platform-shared store override, got:\n%s", yaml)
	}
}

func TestBuildSecretStoresForConfig_PerEnvBinding(t *testing.T) {
	cfg := secrets.BackendConfig{
		Type: secrets.Backend1Password,
		OnePassword: &secrets.OnePasswordConfig{
			GroupName: "Suparship",
			Bindings: []secrets.EnvBinding{
				{Env: "staging", VaultID: "v-stg", Provisioned: true},
				{Env: "prod", VaultID: "v-prd", Provisioned: true},
			},
		},
	}
	naming := secrets.ResourceNaming{}
	stores := BuildSecretStoresForConfig(cfg, naming, "default", branding.Config{})

	if len(stores) != 2 {
		t.Fatalf("expected 2 stores (one per env), got %d", len(stores))
	}
	names := make(map[string]bool)
	for _, s := range stores {
		names[s.Name] = true
	}
	if !names["onepassword-staging"] || !names["onepassword-prod"] {
		t.Errorf("unexpected store names: %v", names)
	}
}

func TestBuildSecretStoresForConfig_SkipsUnprovisioned(t *testing.T) {
	cfg := secrets.BackendConfig{
		Type: secrets.Backend1Password,
		OnePassword: &secrets.OnePasswordConfig{
			Bindings: []secrets.EnvBinding{
				{Env: "staging", VaultID: "v-stg", Provisioned: false},
				{Env: "prod", VaultID: "v-prd", Provisioned: true},
			},
		},
	}
	stores := BuildSecretStoresForConfig(cfg, secrets.ResourceNaming{}, "default", branding.Config{})
	if len(stores) != 1 {
		t.Fatalf("expected 1 store (only provisioned), got %d", len(stores))
	}
	if stores[0].Binding.Env != "prod" {
		t.Errorf("expected prod binding, got %q", stores[0].Binding.Env)
	}
}

func TestBuildCollapsedExternalSecretForApp(t *testing.T) {
	cfg := secrets.BackendConfig{
		Type: secrets.Backend1Password,
	}
	naming := secrets.ResourceNaming{}
	params := AppEnvPublishParams{
		Project:   "acme",
		App:       "web",
		Env:       "prod",
		Namespace: "acme-web-prod",
		ScopeKeys: map[string]bool{
			secrets.LevelOrg:     true,
			secrets.LevelProject: true,
			secrets.LevelAppEnv:  true,
		},
	}

	result := BuildCollapsedExternalSecretForApp(params, naming, cfg, "default", branding.Config{})
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Name != "web-secrets" {
		t.Errorf("expected name 'web-secrets', got %q", result.Name)
	}
	if result.StoreName != "onepassword-prod" {
		t.Errorf("expected store 'onepassword-prod', got %q", result.StoreName)
	}
	if len(result.Items) != 3 {
		t.Errorf("expected 3 items, got %d: %+v", len(result.Items), result.Items)
	}
}

func TestBuildCollapsedExternalSecretForApp_SkipsMissingScopes(t *testing.T) {
	cfg := secrets.BackendConfig{
		Type: secrets.Backend1Password,
	}
	naming := secrets.ResourceNaming{}
	params := AppEnvPublishParams{
		Project:   "acme",
		App:       "api",
		Env:       "staging",
		Namespace: "acme-api-staging",
		ScopeKeys: map[string]bool{},
	}

	result := BuildCollapsedExternalSecretForApp(params, naming, cfg, "default", branding.Config{})
	if result != nil {
		t.Error("expected nil result when no scopes have keys")
	}
}

func TestBuildCollapsedExternalSecretForApp_CustomNaming(t *testing.T) {
	cfg := secrets.BackendConfig{
		Type: secrets.Backend1Password,
	}
	naming := secrets.ResourceNaming{
		AppResource:        "{app}-{env}",
		ClusterSecretStore: "{org}-{provider}-{env}",
		VaultItem: secrets.ItemNaming{
			Org:    "{org}-global",
			AppEnv: "{project}-{app}-{env}-config",
		},
	}
	params := AppEnvPublishParams{
		Project:   "billing",
		App:       "api",
		Env:       "prod",
		Namespace: "billing-api-prod",
		ScopeKeys: map[string]bool{
			secrets.LevelOrg:    true,
			secrets.LevelAppEnv: true,
		},
	}

	result := BuildCollapsedExternalSecretForApp(params, naming, cfg, "myorg", branding.Config{})
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Name != "api-prod" {
		t.Errorf("expected name 'api-prod', got %q", result.Name)
	}
	if result.StoreName != "myorg-onepassword-prod" {
		t.Errorf("expected store 'myorg-onepassword-prod', got %q", result.StoreName)
	}
	if len(result.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(result.Items))
	}
	if result.Items[0].Key != "myorg-global" {
		t.Errorf("expected org item 'myorg-global', got %q", result.Items[0].Key)
	}
	if result.Items[1].Key != "billing-api-prod-config" {
		t.Errorf("expected appenv item 'billing-api-prod-config', got %q", result.Items[1].Key)
	}
}

func TestBuildCollapsedExternalSecretForApp_PlatformStoreRoutesOrgAndProject(t *testing.T) {
	cfg := secrets.BackendConfig{Type: secrets.Backend1Password}
	naming := secrets.ResourceNaming{}
	params := AppEnvPublishParams{
		Project:           "acme",
		App:               "web",
		Env:               "prod",
		Namespace:         "acme-web-prod",
		Cluster:           "kind-prod",
		PlatformStoreName: "platform-shared",
		ScopeKeys: map[string]bool{
			secrets.LevelOrg:         true,
			secrets.LevelEnvironment: true,
			secrets.LevelProject:     true,
			secrets.LevelApp:         true,
			secrets.LevelAppEnv:      true,
			secrets.LevelCluster:     true,
		},
	}

	result := BuildCollapsedExternalSecretForApp(params, naming, cfg, "default", branding.Config{})
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.Items) != 6 {
		t.Fatalf("expected 6 items (one per scope), got %d: %+v", len(result.Items), result.Items)
	}

	wantStores := map[string]string{
		"org":                  "platform-shared",
		"env-prod":             "onepassword-prod",
		"acme":                 "platform-shared",
		"acme-web":             "onepassword-prod",
		"acme-web-prod":        "onepassword-prod",
		"cluster-kind-prod":    "onepassword-prod",
	}
	for _, item := range result.Items {
		if got, want := item.StoreName, wantStores[item.Key]; got != want {
			t.Errorf("item %q: store = %q, want %q", item.Key, got, want)
		}
	}
}

func TestBuildCollapsedExternalSecretForApp_OmitsClusterWhenUnbound(t *testing.T) {
	cfg := secrets.BackendConfig{Type: secrets.Backend1Password}
	naming := secrets.ResourceNaming{}
	params := AppEnvPublishParams{
		Project:   "acme",
		App:       "web",
		Env:       "staging",
		Namespace: "acme-web-staging",
		Cluster:   "", // unbound
		ScopeKeys: map[string]bool{
			secrets.LevelAppEnv:  true,
			secrets.LevelCluster: true, // ScopeKeys says yes, but Cluster is empty
		},
	}

	result := BuildCollapsedExternalSecretForApp(params, naming, cfg, "default", branding.Config{})
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	for _, item := range result.Items {
		if strings.HasPrefix(item.Key, "cluster-") {
			t.Errorf("expected cluster scope to be omitted when Cluster is empty, got item %q", item.Key)
		}
	}
}

// ── BuildAppConfigMapYAML ──────────────────────────────────────────────────────

func TestBuildAppConfigMapYAML_WithVars(t *testing.T) {
	yaml := BuildAppConfigMapYAML("nginx-config", "demo-nginx-staging", map[string]string{
		"LOG_LEVEL": "info",
		"APP_ENV":   "staging",
	}, branding.Config{})

	if !strings.Contains(yaml, "name: nginx-config") {
		t.Error("expected name 'nginx-config'")
	}
	if !strings.Contains(yaml, "namespace: demo-nginx-staging") {
		t.Error("expected namespace in output")
	}
	if !strings.Contains(yaml, "app.kubernetes.io/managed-by: suparship") {
		t.Error("expected managed-by label")
	}
	if !strings.Contains(yaml, `APP_ENV: "staging"`) {
		t.Error("expected APP_ENV var")
	}
	if !strings.Contains(yaml, `LOG_LEVEL: "info"`) {
		t.Error("expected LOG_LEVEL var")
	}
}

func TestBuildAppConfigMapYAML_Empty(t *testing.T) {
	yaml := BuildAppConfigMapYAML("nginx-config", "demo-nginx-prod", nil, branding.Config{})

	if !strings.Contains(yaml, "name: nginx-config") {
		t.Error("expected name in output")
	}
	if !strings.Contains(yaml, "data:\n  {}\n") {
		t.Errorf("expected empty data block, got:\n%s", yaml)
	}
}

func TestBuildAppConfigMapYAML_Deterministic(t *testing.T) {
	vars := map[string]string{
		"Z_VAR": "z",
		"A_VAR": "a",
		"M_VAR": "m",
	}
	y1 := BuildAppConfigMapYAML("app-config", "ns", vars, branding.Config{})
	y2 := BuildAppConfigMapYAML("app-config", "ns", vars, branding.Config{})

	if y1 != y2 {
		t.Error("expected deterministic output — two calls with same input should produce identical YAML")
	}

	// Keys should appear in sorted order.
	aIdx := strings.Index(y1, "A_VAR")
	mIdx := strings.Index(y1, "M_VAR")
	zIdx := strings.Index(y1, "Z_VAR")
	if aIdx > mIdx || mIdx > zIdx {
		t.Errorf("expected sorted key order A < M < Z, got positions a=%d m=%d z=%d", aIdx, mIdx, zIdx)
	}
}
