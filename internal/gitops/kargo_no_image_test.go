package gitops_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/gitops"
	"github.com/suparcloud/suparship/internal/tpl"
)

// A pipeline app with no image source (no bindings, no template-declared images, no
// image_repository) must NOT get a placeholder Warehouse — that unreachable
// ghcr.io/{project}/{app} subscription thrashes Kargo in a failing refresh loop.
// The Warehouse is skipped (and a stale one pruned) until an image appears.
func TestPublishKargoCRs_NoImageSkipsWarehouse(t *testing.T) {
	dir := t.TempDir()
	app := &domain.App{Name: "hello", ProjectName: "demo"}
	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true,
			ClusterRef: "c1", Clusters: []gitops.ClusterTarget{{Name: "c1", Server: "https://c1"}}},
	}
	whPath := filepath.Join(dir, "_infra", "kargo", "kargo-demo-hello-warehouse.yaml")

	p := newTestPublisher(t)
	if err := p.PublishKargoCRsForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishKargoCRsForTest: %v", err)
	}
	if _, err := os.Stat(whPath); !os.IsNotExist(err) {
		t.Errorf("no-image app must not write a Warehouse (no placeholder); got err=%v", err)
	}
	// A staging Stage still publishes — the pipeline exists, promotions just have no
	// freight until an image is set.
	stage := filepath.Join(dir, "_infra", "kargo", "kargo-demo-hello-staging-stage.yaml")
	if _, err := os.Stat(stage); err != nil {
		t.Errorf("expected staging Stage even without an image: %v", err)
	}

	// Now give the app a real image source and republish — the Warehouse materializes
	// and any stale one would be replaced.
	app.Spec.Values = map[string]any{"image_repository": "ghcr.io/demo/hello"}
	if err := p.PublishKargoCRsForTest(dir, app, envs); err != nil {
		t.Fatalf("republish with image: %v", err)
	}
	if _, err := os.Stat(whPath); err != nil {
		t.Errorf("Warehouse must appear once an image source exists: %v", err)
	}

	// And back to no image prunes the stale Warehouse.
	app.Spec.Values = nil
	if err := p.PublishKargoCRsForTest(dir, app, envs); err != nil {
		t.Fatalf("republish without image: %v", err)
	}
	if _, err := os.Stat(whPath); !os.IsNotExist(err) {
		t.Errorf("stale Warehouse must be pruned when the image source is removed; got err=%v", err)
	}
}

// SelectDeclaredKargoImages picks only the DECLARED discovered images (template
// declares a pull rule) and fills the rule, so template-declared images auto-bind to
// a healthy Warehouse with zero user config; undeclared images (sidecars) are left.
func TestSelectDeclaredKargoImages(t *testing.T) {
	discovered := []tpl.TemplateImage{
		{Name: "web", Repository: "ghcr.io/org/web", TagKey: "image.tag",
			TagPattern: "^v.*", SelectionStrategy: "SemVer", Declared: true},
		{Name: "redis", Repository: "docker.io/redis", TagKey: "sidecars.redis.image.tag"},               // undeclared sidecar
		{Name: "api", Repository: "ghcr.io/org/api", TagKey: "components.api.image.tag", Declared: true}, // declared, no rule → defaults
	}
	got := gitops.SelectDeclaredKargoImages(discovered)
	if len(got) != 2 {
		t.Fatalf("expected 2 declared images (sidecar excluded), got %d: %+v", len(got), got)
	}
	byRepo := map[string]gitops.KargoImage{}
	for _, g := range got {
		byRepo[g.Repository] = g
	}
	if w := byRepo["ghcr.io/org/web"]; w.TagPattern != "^v.*" || w.SelectionStrategy != "SemVer" {
		t.Errorf("web rule not inherited from template slot: %+v", w)
	}
	if a := byRepo["ghcr.io/org/api"]; a.TagPattern != gitops.DefaultImageTagPattern || a.SelectionStrategy != gitops.DefaultImageSelectionStrategy {
		t.Errorf("api (declared, no rule) should fall back to platform defaults: %+v", a)
	}
	if _, ok := byRepo["docker.io/redis"]; ok {
		t.Errorf("undeclared sidecar must not be auto-bound")
	}
}
