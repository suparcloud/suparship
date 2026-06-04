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
	if !strings.Contains(yaml, "name: nginx-config") {
		t.Errorf("expected per-app ConfigMap name, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, `LOG_LEVEL: "info"`) {
		t.Errorf("expected LOG_LEVEL var, got:\n%s", yaml)
	}
}

// TestPublishAppFiles_NoExternalSecretWhenNoKeys verifies the single merged
// ExternalSecret is not written when no scope has keys.
func TestPublishAppFiles_NoExternalSecretWhenNoKeys(t *testing.T) {
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
	path := filepath.Join(dir, "envs", "staging", "demo", "nginx", "external-secret.yaml")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected no external-secret.yaml when no keys present")
	}
}

// TestPublishAppFiles_WritesMergedExternalSecret verifies the single merged
// ExternalSecret targets <app>-secrets and lists all present scopes' items
// with per-entry sourceRef for the env/cluster stores.
func TestPublishAppFiles_WritesMergedExternalSecret(t *testing.T) {
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

	data, err := os.ReadFile(filepath.Join(base, "external-secret.yaml"))
	if err != nil {
		t.Fatalf("external-secret.yaml missing: %v", err)
	}
	es := string(data)
	if !strings.Contains(es, "name: nginx-secrets") {
		t.Errorf("expected merged target nginx-secrets, got:\n%s", es)
	}
	if !strings.Contains(es, "name: suparship-store-global") {
		t.Errorf("expected default global store ref, got:\n%s", es)
	}
	for _, key := range []string{`"nginx-global"`, `"shared-env-staging"`, `"nginx-env-staging"`, `"nginx-cluster-prod-eu"`} {
		if !strings.Contains(es, key) {
			t.Errorf("expected dataFrom item %s, got:\n%s", key, es)
		}
	}
	// env/cluster items must carry per-entry sourceRefs to their stores.
	if !strings.Contains(es, "name: suparship-store-env-staging") ||
		!strings.Contains(es, "name: suparship-store-cluster-prod-eu") {
		t.Errorf("expected sourceRef storeRefs for env+cluster, got:\n%s", es)
	}
	// No separate per-scope files.
	for _, scope := range []string{"global", "env", "cluster"} {
		if _, err := os.Stat(filepath.Join(base, "external-secret-"+scope+".yaml")); !os.IsNotExist(err) {
			t.Errorf("did not expect external-secret-%s.yaml", scope)
		}
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
