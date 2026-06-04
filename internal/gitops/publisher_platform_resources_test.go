package gitops_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/gitops"
)

// TestPublishAppFiles_WritesConfigMap verifies env-configmap.yaml is written
// with the deterministic per-app name and the merged EnvVars.
func TestPublishAppFiles_WritesConfigMap(t *testing.T) {
	dir := t.TempDir()
	app := &domain.App{
		Name:        "nginx",
		ProjectName: "demo",
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "web-service"}},
	}
	envs := []gitops.AppPublishEnv{
		{
			EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true,
			Namespace: "demo-nginx-staging",
			EnvVars:   map[string]string{"LOG_LEVEL": "info"},
		},
	}

	p := newTestPublisher(t)
	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishAppFilesForTest: %v", err)
	}

	cmPath := filepath.Join(dir, "envs", "staging", "demo", "nginx", "env-configmap.yaml")
	data, err := os.ReadFile(cmPath)
	if err != nil {
		t.Fatalf("env-configmap.yaml missing: %v", err)
	}
	yaml := string(data)
	if !strings.Contains(yaml, "name: suparship-config-demo-nginx-staging") {
		t.Errorf("expected per-app ConfigMap name, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, `LOG_LEVEL: "info"`) {
		t.Errorf("expected LOG_LEVEL var, got:\n%s", yaml)
	}
}

// TestPublishAppFiles_NoExternalSecretsWhenNoKeys verifies that no
// external-secret-*.yaml files are written when no scope has keys.
func TestPublishAppFiles_NoExternalSecretsWhenNoKeys(t *testing.T) {
	dir := t.TempDir()
	app := &domain.App{
		Name:        "nginx",
		ProjectName: "demo",
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "web-service"}},
	}
	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, Namespace: "demo-nginx-staging"},
	}

	p := newTestPublisher(t)
	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishAppFilesForTest: %v", err)
	}
	for _, scope := range []string{"global", "env", "cluster"} {
		path := filepath.Join(dir, "envs", "staging", "demo", "nginx", "external-secret-"+scope+".yaml")
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("expected no external-secret-%s.yaml when no keys present", scope)
		}
	}
}

// TestPublishAppFiles_WritesScopedExternalSecrets verifies that per-scope
// ExternalSecrets are written for scopes with keys, targeting the
// <app>-global/-env/-cluster Secrets.
func TestPublishAppFiles_WritesScopedExternalSecrets(t *testing.T) {
	dir := t.TempDir()
	app := &domain.App{
		Name:        "nginx",
		ProjectName: "demo",
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "web-service"}},
	}
	envs := []gitops.AppPublishEnv{
		{
			EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true,
			Namespace:  "demo-nginx-staging",
			ClusterRef: "prod-eu",
			ScopeKeys: gitops.ScopePresence{
				GlobalApp: true,
				EnvShared: true, EnvApp: true,
				ClusterApp: true,
			},
		},
	}

	p := newTestPublisher(t)
	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishAppFilesForTest: %v", err)
	}
	base := filepath.Join(dir, "envs", "staging", "demo", "nginx")

	global, err := os.ReadFile(filepath.Join(base, "external-secret-global.yaml"))
	if err != nil {
		t.Fatalf("external-secret-global.yaml missing: %v", err)
	}
	if !strings.Contains(string(global), "name: nginx-global") {
		t.Errorf("expected target nginx-global, got:\n%s", global)
	}
	if !strings.Contains(string(global), "name: suparship-store-global") {
		t.Errorf("expected global store ref, got:\n%s", global)
	}

	env, err := os.ReadFile(filepath.Join(base, "external-secret-env.yaml"))
	if err != nil {
		t.Fatalf("external-secret-env.yaml missing: %v", err)
	}
	// env scope has both shared and app items, app listed last (wins).
	if !strings.Contains(string(env), `"shared-env-staging"`) || !strings.Contains(string(env), `"nginx-env-staging"`) {
		t.Errorf("expected shared+app dataFrom keys, got:\n%s", env)
	}

	if _, err := os.Stat(filepath.Join(base, "external-secret-cluster.yaml")); err != nil {
		t.Errorf("expected external-secret-cluster.yaml: %v", err)
	}
}

// TestPublishAppFiles_UnboundEnvSkipsPlatformResources verifies an unbound env
// produces no output.
func TestPublishAppFiles_UnboundEnvSkipsPlatformResources(t *testing.T) {
	dir := t.TempDir()
	app := &domain.App{
		Name:        "nginx",
		ProjectName: "demo",
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "web-service"}},
	}
	envs := []gitops.AppPublishEnv{
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 1, Bound: false},
	}

	p := newTestPublisher(t)
	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishAppFilesForTest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "envs", "prod")); !os.IsNotExist(err) {
		t.Error("expected no output for unbound prod env")
	}
}
