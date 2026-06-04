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

func TestBuildClusterSecretStoreYAML_1Password(t *testing.T) {
	yaml := BuildClusterSecretStoreYAML(ESOSecretStoreConfig{
		Scope:       secrets.EnvScope("prod"),
		BackendType: secrets.Backend1Password,
		VaultID:     "v1",
	})
	if !strings.Contains(yaml, "connectHost: "+DefaultConnectEndpoint) {
		t.Errorf("expected in-cluster connectHost, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, "name: op-connect-token-env-prod") {
		t.Errorf("expected scope-derived auth secret, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, "namespace: "+secrets.OnePasswordRemoteNamespace) {
		t.Errorf("expected auth secret namespace, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, "v1: 1") {
		t.Errorf("expected single vault id mapping, got:\n%s", yaml)
	}
}

func TestBuildExternalSecretYAML(t *testing.T) {
	yaml := BuildExternalSecretYAML(ESOExternalSecretConfig{
		Name:      "web-env",
		Namespace: "acme-web-prod",
		StoreName: "suparship-store-env-prod",
		ItemKeys:  []string{"shared-env-prod", "web-env-prod"},
	})
	if !strings.Contains(yaml, "name: web-env") {
		t.Errorf("expected target name, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, "name: suparship-store-env-prod") {
		t.Errorf("expected store ref, got:\n%s", yaml)
	}
	// Shared listed before app so the app's keys win in ESO's ordered extract.
	si := strings.Index(yaml, `"shared-env-prod"`)
	ai := strings.Index(yaml, `"web-env-prod"`)
	if si < 0 || ai < 0 || si > ai {
		t.Errorf("expected shared item before app item, got:\n%s", yaml)
	}
}

func TestBuildSecretStoresForConfig_K8s(t *testing.T) {
	cfg := secrets.BackendConfig{Type: secrets.BackendK8s}
	stores := BuildSecretStoresForConfig(cfg, []string{"staging", "prod"}, []string{"c1"}, branding.Config{})
	// global + 2 envs + 1 cluster.
	if len(stores) != 4 {
		t.Fatalf("expected 4 stores, got %d", len(stores))
	}
	names := map[string]bool{}
	for _, s := range stores {
		names[s.Name()] = true
		if s.BackendType != secrets.BackendK8s {
			t.Errorf("expected k8s backend, got %q", s.BackendType)
		}
	}
	for _, want := range []string{"suparship-store-global", "suparship-store-env-staging", "suparship-store-env-prod", "suparship-store-cluster-c1"} {
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
	stores := BuildSecretStoresForConfig(cfg, []string{"staging", "prod"}, []string{"c1"}, branding.Config{})
	if len(stores) != 0 {
		t.Fatalf("expected 0 _infra stores for 1Password, got %d", len(stores))
	}
}

func TestBuildWorkloadExternalSecrets_PresenceDriven(t *testing.T) {
	cfgs := BuildWorkloadExternalSecrets(WorkloadExternalSecretParams{
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
	if len(cfgs) != 2 {
		t.Fatalf("expected 2 configs (global, env), got %d", len(cfgs))
	}
	byName := map[string]ESOExternalSecretConfig{}
	for _, c := range cfgs {
		byName[c.Name] = c
	}
	g, ok := byName["web-global"]
	if !ok || len(g.ItemKeys) != 1 || g.ItemKeys[0] != "web-global" {
		t.Errorf("global config wrong: %+v", g)
	}
	e, ok := byName["web-env"]
	if !ok || len(e.ItemKeys) != 2 || e.ItemKeys[0] != "shared-env-prod" || e.ItemKeys[1] != "web-env-prod" {
		t.Errorf("env config wrong: %+v", e)
	}
}

func TestBuildWorkloadExternalSecrets_OmitsClusterWhenUnbound(t *testing.T) {
	cfgs := BuildWorkloadExternalSecrets(WorkloadExternalSecretParams{
		App:       "web",
		Namespace: "acme-web-staging",
		Env:       "staging",
		Cluster:   "", // unbound
		Presence:  ScopePresence{EnvApp: true, ClusterApp: true},
	})
	for _, c := range cfgs {
		if strings.HasSuffix(c.Name, "-cluster") {
			t.Errorf("expected no cluster ExternalSecret when unbound, got %+v", c)
		}
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
