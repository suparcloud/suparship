package gitops_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/envconfig"
	"github.com/suparcloud/suparship/internal/gitops"
	"github.com/suparcloud/suparship/internal/secrets"
)

// TestPublishAppFiles_WritesConfigMap verifies that publishAppFiles writes
// env-configmap.yaml with the correct name and namespace for each bound env.
//
// The publisher writes env.EnvVars verbatim — the adapter is responsible for
// merging the six-scope hierarchy before publishing. Tests pass the merged map
// directly via EnvVars to mirror that contract.
func TestPublishAppFiles_WritesConfigMap(t *testing.T) {
	dir := t.TempDir()

	app := &domain.App{
		Name:        "nginx",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "web-service"},
		},
	}
	envs := []gitops.AppPublishEnv{
		{
			EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true,
			Namespace: "demo-nginx-staging",
			EnvVars:   map[string]string{"LOG_LEVEL": "info"},
		},
		{
			EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: true,
			Namespace: "demo-nginx-prod",
			EnvVars:   map[string]string{"LOG_LEVEL": "info"},
		},
	}

	p := newTestPublisher(t)
	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishAppFilesForTest: %v", err)
	}

	for _, envName := range []string{"staging", "prod"} {
		ns := "demo-nginx-" + envName
		cmPath := filepath.Join(dir, "gitops-output", envName, "demo", "nginx", "env-configmap.yaml")
		data, err := os.ReadFile(cmPath)
		if err != nil {
			t.Fatalf("env-configmap.yaml missing for env %q: %v", envName, err)
		}
		yaml := string(data)

		if !strings.Contains(yaml, "name: nginx-config") {
			t.Errorf("[%s] expected name 'nginx-config' in ConfigMap, got:\n%s", envName, yaml)
		}
		if !strings.Contains(yaml, "namespace: "+ns) {
			t.Errorf("[%s] expected namespace %q in ConfigMap, got:\n%s", envName, ns, yaml)
		}
		if !strings.Contains(yaml, `LOG_LEVEL: "info"`) {
			t.Errorf("[%s] expected LOG_LEVEL var in ConfigMap, got:\n%s", envName, yaml)
		}
	}
}

// TestPublishAppFiles_NoExternalSecretWithoutStoreName verifies that no
// external-secret.yaml is written when StoreName is empty (K8s backend default).
func TestPublishAppFiles_NoExternalSecretWithoutStoreName(t *testing.T) {
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

	esPath := filepath.Join(dir, "gitops-output", "staging", "demo", "nginx", "external-secret.yaml")
	if _, err := os.Stat(esPath); !os.IsNotExist(err) {
		t.Error("expected no external-secret.yaml when StoreName is empty")
	}
}

// TestPublishAppFiles_WritesExternalSecretWhenStoreNameSet verifies that
// external-secret.yaml is written when StoreName is set.
func TestPublishAppFiles_WritesExternalSecretWhenStoreNameSet(t *testing.T) {
	dir := t.TempDir()

	app := &domain.App{
		Name:        "nginx",
		ProjectName: "demo",
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "web-service"}},
	}
	envs := []gitops.AppPublishEnv{
		{
			EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true,
			Namespace:      "demo-nginx-staging",
			StoreName:      "onepassword-staging",
			VaultItemTitle: "demo-nginx-staging",
		},
	}

	p := newTestPublisher(t)
	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishAppFilesForTest: %v", err)
	}

	esPath := filepath.Join(dir, "gitops-output", "staging", "demo", "nginx", "external-secret.yaml")
	data, err := os.ReadFile(esPath)
	if err != nil {
		t.Fatalf("external-secret.yaml missing: %v", err)
	}
	yaml := string(data)

	if !strings.Contains(yaml, "name: nginx-secrets") {
		t.Errorf("expected name 'nginx-secrets' (default AppResource), got:\n%s", yaml)
	}
	if !strings.Contains(yaml, "name: onepassword-staging") {
		t.Errorf("expected store ref 'onepassword-staging', got:\n%s", yaml)
	}
	if !strings.Contains(yaml, `"demo-nginx-staging"`) {
		t.Errorf("expected vault item title in dataFrom, got:\n%s", yaml)
	}
}

// TestPublishAppFiles_CustomNamingPatterns verifies that ResourceNaming patterns
// on PublisherConfig are honoured in the generated ConfigMap and ExternalSecret.
func TestPublishAppFiles_CustomNamingPatterns(t *testing.T) {
	dir := t.TempDir()

	app := &domain.App{
		Name:        "api",
		ProjectName: "billing",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "web-service"},
			EnvConfig: envconfig.EnvConfig{Vars: map[string]string{"ENV": "prod"}},
		},
	}
	envs := []gitops.AppPublishEnv{
		{
			EnvName: "prod", EnvType: domain.AppEnvProd, Order: 1, Bound: true,
			Namespace: "billing-api-prod", StoreName: "op-prod",
			VaultItemTitle: "billing-api-prod",
		},
	}

	// Build publisher with custom naming.
	p := newTestPublisher(t)
	p.SetOrgConfig("myorg", secrets.ResourceNaming{
		AppResource:  "{app}-env-secrets",
		AppConfigMap: "{app}-env-config",
	}, nil)

	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishAppFilesForTest: %v", err)
	}

	cmPath := filepath.Join(dir, "gitops-output", "prod", "billing", "api", "env-configmap.yaml")
	cmData, err := os.ReadFile(cmPath)
	if err != nil {
		t.Fatalf("env-configmap.yaml missing: %v", err)
	}
	if !strings.Contains(string(cmData), "name: api-env-config") {
		t.Errorf("expected custom ConfigMap name 'api-env-config', got:\n%s", cmData)
	}

	esPath := filepath.Join(dir, "gitops-output", "prod", "billing", "api", "external-secret.yaml")
	esData, err := os.ReadFile(esPath)
	if err != nil {
		t.Fatalf("external-secret.yaml missing: %v", err)
	}
	if !strings.Contains(string(esData), "name: api-env-secrets") {
		t.Errorf("expected custom ExternalSecret name 'api-env-secrets', got:\n%s", esData)
	}
}

// TestPublishAppFiles_PreviewWritesPlatformResources verifies that preview envs
// also write env-configmap.yaml and external-secret.yaml when StoreName is set.
// This test uses PublishAppFilesForTest with a preview-type env (simulating the
// preview path via a writable dir).
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

	// Nothing should be written for an unbound env.
	prodDir := filepath.Join(dir, "gitops-output", "prod")
	if _, err := os.Stat(prodDir); !os.IsNotExist(err) {
		t.Error("expected no output for unbound prod env")
	}
}
