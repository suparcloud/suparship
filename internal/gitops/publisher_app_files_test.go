package gitops_test

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/gitops"
)

// readAppMeta reads and unmarshals a per-(app,cluster) app.yaml.
func readAppMeta(t *testing.T, path string) gitops.AppMetadata {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read app.yaml %s: %v", path, err)
	}
	var meta gitops.AppMetadata
	if err := yaml.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("unmarshal app.yaml %s: %v", path, err)
	}
	return meta
}

// TestPublishAppFiles_MultiClusterFanOut verifies that an env whose Clusters set
// has two targets writes one _targets/{cluster}/app.yaml per cluster plus a
// per-cluster values.yaml under _clusters/{cluster}/..., each app.yaml carrying
// the correct clusterName/clusterServer/valuesPath.
func TestPublishAppFiles_MultiClusterFanOut(t *testing.T) {
	dir := t.TempDir()

	app := &domain.App{
		Name:        "hello",
		ProjectName: "demo",
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "web-service"}},
	}
	envs := []gitops.AppPublishEnv{{
		EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, BaseDomain: "localhost",
		Clusters: []gitops.ClusterTarget{
			{Name: "a", Server: "https://a"},
			{Name: "b", Server: "https://b"},
		},
	}}

	p := newTestPublisher(t)
	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishAppFilesForTest: %v", err)
	}

	for _, c := range []struct{ name, server string }{{"a", "https://a"}, {"b", "https://b"}} {
		appYAML := filepath.Join(dir, "envs", "staging", "demo", "hello", "_targets", c.name, "app.yaml")
		meta := readAppMeta(t, appYAML)
		if meta.ClusterName != c.name {
			t.Errorf("cluster %s: clusterName = %q, want %q", c.name, meta.ClusterName, c.name)
		}
		if meta.ClusterServer != c.server {
			t.Errorf("cluster %s: clusterServer = %q, want %q", c.name, meta.ClusterServer, c.server)
		}
		wantValues := "envs/staging/_clusters/" + c.name + "/demo/hello/values.yaml"
		if meta.ValuesPath != wantValues {
			t.Errorf("cluster %s: valuesPath = %q, want %q", c.name, meta.ValuesPath, wantValues)
		}
		if meta.AppName != "demo-hello-"+c.name {
			t.Errorf("cluster %s: appName = %q, want demo-hello-%s", c.name, meta.AppName, c.name)
		}
		// The per-cluster values.yaml must exist where valuesPath points.
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(meta.ValuesPath))); err != nil {
			t.Errorf("cluster %s: per-cluster values.yaml missing: %v", c.name, err)
		}
	}
}

// TestPublishAppFiles_SingleNonDefaultTarget verifies that a single non-default
// cluster target writes _targets/{cluster}/app.yaml pointing at the SHARED
// (non-fan-out) env values.yaml path.
func TestPublishAppFiles_SingleNonDefaultTarget(t *testing.T) {
	dir := t.TempDir()

	app := &domain.App{
		Name:        "hello",
		ProjectName: "demo",
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "web-service"}},
	}
	envs := []gitops.AppPublishEnv{{
		EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, BaseDomain: "localhost",
		Clusters: []gitops.ClusterTarget{{Name: "b", Server: "https://b"}},
	}}

	p := newTestPublisher(t)
	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishAppFilesForTest: %v", err)
	}

	appYAML := filepath.Join(dir, "envs", "staging", "demo", "hello", "_targets", "b", "app.yaml")
	meta := readAppMeta(t, appYAML)
	if meta.ClusterName != "b" || meta.ClusterServer != "https://b" {
		t.Errorf("cluster/server = %q/%q, want b/https://b", meta.ClusterName, meta.ClusterServer)
	}
	// Single target → shared env values.yaml (no _clusters/ fan-out dir).
	if meta.ValuesPath != "envs/staging/demo/hello/values.yaml" {
		t.Errorf("valuesPath = %q, want shared envs/staging/demo/hello/values.yaml", meta.ValuesPath)
	}
	if _, err := os.Stat(filepath.Join(dir, "envs", "staging", "demo", "hello", "values.yaml")); err != nil {
		t.Errorf("shared values.yaml missing: %v", err)
	}
}

// TestPublishAppFiles_RepublishPrunesDeselectedCluster verifies that publishing
// with two targets and then re-publishing with one removes the de-selected
// cluster's _targets/{cluster}/app.yaml so it stops generating an Application.
func TestPublishAppFiles_RepublishPrunesDeselectedCluster(t *testing.T) {
	dir := t.TempDir()

	app := &domain.App{
		Name:        "hello",
		ProjectName: "demo",
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "web-service"}},
	}
	twoTargets := []gitops.AppPublishEnv{{
		EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, BaseDomain: "localhost",
		Clusters: []gitops.ClusterTarget{
			{Name: "a", Server: "https://a"},
			{Name: "b", Server: "https://b"},
		},
	}}

	p := newTestPublisher(t)
	if err := p.PublishAppFilesForTest(dir, app, twoTargets); err != nil {
		t.Fatalf("initial PublishAppFilesForTest: %v", err)
	}

	bTarget := filepath.Join(dir, "envs", "staging", "demo", "hello", "_targets", "b", "app.yaml")
	if _, err := os.Stat(bTarget); err != nil {
		t.Fatalf("expected cluster b app.yaml after initial publish: %v", err)
	}

	// Re-publish with only cluster a — b must be pruned.
	oneTarget := []gitops.AppPublishEnv{{
		EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, BaseDomain: "localhost",
		Clusters: []gitops.ClusterTarget{{Name: "a", Server: "https://a"}},
	}}
	if err := p.PublishAppFilesForTest(dir, app, oneTarget); err != nil {
		t.Fatalf("republish PublishAppFilesForTest: %v", err)
	}

	if _, err := os.Stat(bTarget); !os.IsNotExist(err) {
		t.Errorf("de-selected cluster b app.yaml should be pruned, stat err = %v", err)
	}
	aTarget := filepath.Join(dir, "envs", "staging", "demo", "hello", "_targets", "a", "app.yaml")
	if _, err := os.Stat(aTarget); err != nil {
		t.Errorf("retained cluster a app.yaml should still exist: %v", err)
	}
}

func TestPublishAppFiles_BoundEnvsWriteFiles(t *testing.T) {
	dir := t.TempDir()

	app := &domain.App{
		Name:        "hello",
		ProjectName: "demo",
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "web-service"}},
	}
	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, BaseDomain: "localhost"},
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: true, BaseDomain: "localhost"},
	}

	p := newTestPublisher(t)
	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishAppFilesForTest: %v", err)
	}

	for _, envName := range []string{"staging", "prod"} {
		// Stable-env app.yaml is now per target cluster; with no Clusters/ClusterRef
		// the single target is the in-cluster fallback. values.yaml stays shared.
		appYAML := filepath.Join(dir, "envs", envName, "demo", "hello", "_targets", "in-cluster", "app.yaml")
		valuesYAML := filepath.Join(dir, "envs", envName, "demo", "hello", "values.yaml")
		if _, err := os.Stat(appYAML); os.IsNotExist(err) {
			t.Errorf("expected app.yaml for env %q to exist", envName)
		}
		if _, err := os.Stat(valuesYAML); os.IsNotExist(err) {
			t.Errorf("expected values.yaml for env %q to exist", envName)
		}
	}
}

func TestPublishAppFiles_UnboundEnvSkipped(t *testing.T) {
	dir := t.TempDir()

	app := &domain.App{
		Name:        "hello",
		ProjectName: "demo",
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "web-service"}},
	}
	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, BaseDomain: "localhost"},
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: false}, // unbound
	}

	p := newTestPublisher(t)
	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishAppFilesForTest: %v", err)
	}

	// staging is bound → files must exist (per-cluster in-cluster fallback target)
	stagingApp := filepath.Join(dir, "envs", "staging", "demo", "hello", "_targets", "in-cluster", "app.yaml")
	if _, err := os.Stat(stagingApp); os.IsNotExist(err) {
		t.Error("expected app.yaml for bound staging env to exist")
	}

	// prod is unbound → files must NOT exist
	prodApp := filepath.Join(dir, "envs", "prod", "demo", "hello", "_targets", "in-cluster", "app.yaml")
	if _, err := os.Stat(prodApp); !os.IsNotExist(err) {
		t.Error("unbound prod env should NOT produce app.yaml")
	}
	prodValues := filepath.Join(dir, "envs", "prod", "demo", "hello", "values.yaml")
	if _, err := os.Stat(prodValues); !os.IsNotExist(err) {
		t.Error("unbound prod env should NOT produce values.yaml")
	}
}

func TestPublishAppFiles_AllUnboundWritesNothing(t *testing.T) {
	dir := t.TempDir()

	app := &domain.App{
		Name:        "hello",
		ProjectName: "demo",
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "web-service"}},
	}
	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: false},
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: false},
	}

	p := newTestPublisher(t)
	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishAppFilesForTest: %v", err)
	}

	// gitops-output directory should not have been created for any env
	for _, envName := range []string{"staging", "prod"} {
		envDir := filepath.Join(dir, envName)
		if _, err := os.Stat(envDir); !os.IsNotExist(err) {
			t.Errorf("unbound env %q should not have produced any output directory", envName)
		}
	}
}
