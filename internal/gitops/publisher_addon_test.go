package gitops_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/gitops"
)

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
