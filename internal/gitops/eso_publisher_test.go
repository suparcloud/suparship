package gitops

import (
	"strings"
	"testing"

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

func TestBuildCollapsedExternalSecretYAML(t *testing.T) {
	cfg := ESOExternalSecretConfig{
		Name:      "web",
		Namespace: "acme-web-prod",
		StoreName: "onepassword-prod",
		ItemKeys:  []string{"org", "env-prod", "acme", "acme-web", "acme-web-prod"},
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
	stores := BuildSecretStoresForConfig(cfg, naming, "default")

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
	stores := BuildSecretStoresForConfig(cfg, secrets.ResourceNaming{}, "default")
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

	result := BuildCollapsedExternalSecretForApp(params, naming, cfg, "default")
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Name != "web-secrets" {
		t.Errorf("expected name 'web-secrets', got %q", result.Name)
	}
	if result.StoreName != "onepassword-prod" {
		t.Errorf("expected store 'onepassword-prod', got %q", result.StoreName)
	}
	if len(result.ItemKeys) != 3 {
		t.Errorf("expected 3 item keys, got %d: %v", len(result.ItemKeys), result.ItemKeys)
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

	result := BuildCollapsedExternalSecretForApp(params, naming, cfg, "default")
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

	result := BuildCollapsedExternalSecretForApp(params, naming, cfg, "myorg")
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Name != "api-prod" {
		t.Errorf("expected name 'api-prod', got %q", result.Name)
	}
	if result.StoreName != "myorg-onepassword-prod" {
		t.Errorf("expected store 'myorg-onepassword-prod', got %q", result.StoreName)
	}
	if len(result.ItemKeys) != 2 {
		t.Fatalf("expected 2 item keys, got %d", len(result.ItemKeys))
	}
	if result.ItemKeys[0] != "myorg-global" {
		t.Errorf("expected org item 'myorg-global', got %q", result.ItemKeys[0])
	}
	if result.ItemKeys[1] != "billing-api-prod-config" {
		t.Errorf("expected appenv item 'billing-api-prod-config', got %q", result.ItemKeys[1])
	}
}

// ── BuildAppConfigMapYAML ──────────────────────────────────────────────────────

func TestBuildAppConfigMapYAML_WithVars(t *testing.T) {
	yaml := BuildAppConfigMapYAML("nginx-config", "demo-nginx-staging", map[string]string{
		"LOG_LEVEL": "info",
		"APP_ENV":   "staging",
	})

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
	yaml := BuildAppConfigMapYAML("nginx-config", "demo-nginx-prod", nil)

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
	y1 := BuildAppConfigMapYAML("app-config", "ns", vars)
	y2 := BuildAppConfigMapYAML("app-config", "ns", vars)

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
