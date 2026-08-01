package gitops_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/gitops"
)

// A composed component's values overlay may reference ((platform.component)); at
// publish it must resolve to THAT component's user-facing name (per component), not
// the app name or the chart key.
func TestComposedComponentResolvesPlatformComponentToken(t *testing.T) {
	dir := t.TempDir()
	p, err := gitops.NewPublisher(gitops.PublisherConfig{
		RepoURL:        "https://git/repo.git",
		ArgoCDRepoURL:  "https://git/repo.git",
		TemplateLoader: canonicalityLoader{"byo-chart": false}, // passthrough → overlay IS the values
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	app := &domain.App{
		Name: "bigly", ProjectName: "demo",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "byo-chart"},
			Components: []domain.ComponentSpec{
				{Name: "web", Type: domain.ComponentWeb, Enabled: true,
					Template: &domain.AppTemplateRef{Name: "byo-chart"},
					Values:   map[string]any{"label": "((platform.component))"}},
				{Name: "worker", Type: domain.ComponentWorker, Enabled: true,
					Template: &domain.AppTemplateRef{Name: "byo-chart"},
					Values:   map[string]any{"label": "svc-((platform.component))"}},
			},
		},
	}
	env := gitops.AppPublishEnv{
		EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true,
		Namespace: "bigly-staging", BaseDomain: "localhost",
		Clusters: []gitops.ClusterTarget{{Name: "c1", Server: "https://c1"}},
	}

	if err := p.PublishComposedAppEnvForTest(context.Background(), dir, app, env); err != nil {
		t.Fatalf("publish composed env: %v", err)
	}

	read := func(comp string) string {
		b, rerr := os.ReadFile(filepath.Join(dir, "envs", "staging", "demo", "bigly", "components", comp, "values.yaml"))
		if rerr != nil {
			t.Fatalf("read %s values: %v", comp, rerr)
		}
		return string(b)
	}
	if v := read("web"); !strings.Contains(v, "label: web") {
		t.Errorf("web values must resolve ((platform.component)) to its own name:\n%s", v)
	}
	if v := read("worker"); !strings.Contains(v, "label: svc-worker") {
		t.Errorf("worker values must resolve ((platform.component)) to its own name:\n%s", v)
	}
	// The token must not survive literally.
	if v := read("web"); strings.Contains(v, "((platform.component))") {
		t.Errorf("web values still contain the literal token:\n%s", v)
	}
}
