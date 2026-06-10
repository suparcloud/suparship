package gitops_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/gitops"
)

// TestSyncAddonCharts_MaterialisesWrapperChart is the regression test for the
// addon chart not being synced: the addon's wrapper chart ("valkey") must be
// materialised under charts/<chart>/latest/ so the addon Application (which
// sources charts/{{chartPath}}) resolves.
func TestSyncAddonCharts_MaterialisesWrapperChart(t *testing.T) {
	repoDir := t.TempDir()
	tgz := buildPackagedChart(t, "valkey", map[string]string{
		"Chart.yaml": "apiVersion: v2\nname: valkey\nversion: 0.1.0\n",
	})
	fetcher := &fakeChartFetcher{templateName: "valkey", data: tgz}
	p, err := gitops.NewPublisher(gitops.PublisherConfig{
		RepoURL:      "http://localhost/fake.git",
		ChartFetcher: fetcher,
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}

	app := &domain.App{
		Name:        "hello",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "web-service"},
			Addons:   []domain.AddonSpec{{Name: "cache", Type: "redis"}},
		},
	}
	envs := []gitops.AppPublishEnv{{
		EnvName: "staging", EnvType: domain.AppEnvStaging, Bound: true,
		AddonProfiles: domain.AddonProfiles{
			"redis": {Type: "redis", Provider: "valkey-operator", Chart: "valkey"},
		},
	}}

	if err := p.SyncAddonChartsForTest(context.Background(), repoDir, app, envs); err != nil {
		t.Fatalf("SyncAddonChartsForTest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "charts", "valkey", "latest", "Chart.yaml")); err != nil {
		t.Errorf("addon wrapper chart should be synced to charts/valkey/latest/: %v", err)
	}
}

// TestPublishAppFiles_WithAddon_WritesPerAddonValues asserts the
// publisher renders a parallel app.yaml + values.yaml per addon claim
// under {appDir}/addons/{addon-name}/, using the resolved
// AddonProfile's Chart as the wrapper template.
func TestPublishAppFiles_WithAddon_WritesPerAddonValues(t *testing.T) {
	dir := t.TempDir()

	app := &domain.App{
		Name:        "hello",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "web-service"},
			Components: []domain.ComponentSpec{
				{Name: "web", Type: domain.ComponentWeb},
			},
			Addons: []domain.AddonSpec{
				{Name: "cache", Type: "redis", Size: "small"},
			},
		},
	}
	envs := []gitops.AppPublishEnv{
		{
			EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1,
			Bound: true, BaseDomain: "localhost",
			AddonProfiles: domain.AddonProfiles{
				"redis": {Type: "redis", Provider: "valkey-operator", Chart: "valkey"},
			},
		},
	}

	p := newTestPublisher(t)
	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishAppFilesForTest: %v", err)
	}

	addonDir := filepath.Join(dir, "envs", "staging", "demo", "hello", "addons", "cache")
	for _, fn := range []string{"app.yaml", "values.yaml"} {
		if _, err := os.Stat(filepath.Join(addonDir, fn)); os.IsNotExist(err) {
			t.Errorf("missing %s under %s", fn, addonDir)
		}
	}

	// app.yaml must point at the resolved wrapper template name.
	appYAML, _ := os.ReadFile(filepath.Join(addonDir, "app.yaml"))
	if !strings.Contains(string(appYAML), "template: valkey") {
		t.Errorf("addon app.yaml should reference template=valkey, got:\n%s", appYAML)
	}
	if !strings.Contains(string(appYAML), "name: hello-addon-cache") {
		t.Errorf("addon app.yaml should name the release hello-addon-cache, got:\n%s", appYAML)
	}
	// Regression: the addon app.yaml must carry chartPath so the shared
	// ApplicationSet (sourcing charts/{{chartPath}}) resolves the addon's
	// wrapper chart. Addons are unpinned → the "latest" dir.
	if !strings.Contains(string(appYAML), "chartPath: valkey/latest") {
		t.Errorf("addon app.yaml should set chartPath=valkey/latest, got:\n%s", appYAML)
	}

	// values.yaml must carry the AddonInstanceValues shape with the
	// publisher-computed Secret name.
	valuesYAML, _ := os.ReadFile(filepath.Join(addonDir, "values.yaml"))
	var hv struct {
		App   map[string]string `yaml:"app"`
		Addon map[string]any    `yaml:"addon"`
	}
	if err := yaml.Unmarshal(valuesYAML, &hv); err != nil {
		t.Fatalf("unmarshal values.yaml: %v", err)
	}
	if hv.App["name"] != "hello" {
		t.Errorf("addon values.yaml app.name = %q, want hello", hv.App["name"])
	}
	if hv.Addon["secretName"] != "hello-addon-cache-conn" {
		t.Errorf("addon values.yaml addon.secretName = %q, want hello-addon-cache-conn", hv.Addon["secretName"])
	}
	if hv.Addon["type"] != "redis" {
		t.Errorf("addon values.yaml addon.type = %q, want redis", hv.Addon["type"])
	}

	// Consumer's main values.yaml must list the addon Secret in
	// suparship.envFromSecrets[] so its components pick it up.
	mainValues, _ := os.ReadFile(filepath.Join(dir, "envs", "staging", "demo", "hello", "values.yaml"))
	if !strings.Contains(string(mainValues), "hello-addon-cache-conn") {
		t.Errorf("consumer values.yaml should envFrom the addon Secret, got:\n%s", mainValues)
	}
}

// TestPublishAppFiles_AddonWithoutProfile_Skipped asserts the
// publisher logs and skips an addon when its type isn't configured —
// publish must not fail because of an org-config gap.
func TestPublishAppFiles_AddonWithoutProfile_Skipped(t *testing.T) {
	dir := t.TempDir()

	app := &domain.App{
		Name:        "hello",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "web-service"},
			Addons: []domain.AddonSpec{
				{Name: "cache", Type: "redis"}, // no profile configured
			},
		},
	}
	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, BaseDomain: "localhost"},
	}

	p := newTestPublisher(t)
	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishAppFilesForTest: %v", err)
	}

	// Main app files still produced …
	mainApp := filepath.Join(dir, "envs", "staging", "demo", "hello", "app.yaml")
	if _, err := os.Stat(mainApp); os.IsNotExist(err) {
		t.Error("main app.yaml should exist even when addon profile is missing")
	}
	// … but the addon directory is absent.
	addonDir := filepath.Join(dir, "envs", "staging", "demo", "hello", "addons")
	if _, err := os.Stat(addonDir); !os.IsNotExist(err) {
		t.Errorf("addons dir should NOT exist when no addon resolves; got entries at %s", addonDir)
	}
}
