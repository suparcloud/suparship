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

// Pin-to-env for a COMPOSED app must publish the composed tree (per-component
// values under components/, a multi-source Application) with the pinned tag frozen
// onto each component's image — NOT single-source app.yaml/values.yaml using the
// app's "primary" template. The pinned env reaches the publisher as a focus env,
// which routes through the same per-env writer (publishEnvFiles).
func TestComposedPinnedFocusEnvPublishesComposedWithPinnedTag(t *testing.T) {
	dir := t.TempDir()
	p, err := gitops.NewPublisher(gitops.PublisherConfig{
		RepoURL:        "https://git/repo.git",
		ArgoCDRepoURL:  "https://git/repo.git",
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}

	const pinned = "pr-42-abc123"
	app := &domain.App{
		Name: "bigly", ProjectName: "demo",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "byo-chart"},
			Components: []domain.ComponentSpec{
				{Name: "web", Type: domain.ComponentWeb, Enabled: true,
					Template: &domain.AppTemplateRef{Name: "byo-chart"},
					Images:   []domain.ComponentImage{{Repository: "ghcr.io/bigly/web", TagKey: "image.tag"}}},
				{Name: "worker", Type: domain.ComponentWorker, Enabled: true,
					Template: &domain.AppTemplateRef{Name: "byo-chart"},
					Images:   []domain.ComponentImage{{Repository: "ghcr.io/bigly/worker", TagKey: "image.tag"}}},
			},
			// prod is pinned to a promoted preview's tag.
			EnvironmentDefaults: map[string]domain.EnvironmentOverride{
				"prod": {PinnedImageTag: pinned, PinnedFrom: "pr-42"},
			},
		},
	}
	prod := gitops.AppPublishEnv{
		EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: true,
		Namespace: "bigly-prod", BaseDomain: "localhost",
		Clusters: []gitops.ClusterTarget{{Name: "c2", Server: "https://c2"}},
	}

	if err := p.PublishEnvFilesForTest(context.Background(), dir, app, prod); err != nil {
		t.Fatalf("publish pinned focus env: %v", err)
	}

	// Composed per-component values exist and carry the pinned tag.
	for _, comp := range []string{"web", "worker"} {
		vp := filepath.Join(dir, "envs", "prod", "demo", "bigly", "components", comp, "values.yaml")
		data, err := os.ReadFile(vp)
		if err != nil {
			t.Fatalf("expected composed component values for %s: %v", comp, err)
		}
		if !strings.Contains(string(data), pinned) {
			t.Errorf("component %s values must contain the pinned tag %q:\n%s", comp, pinned, data)
		}
	}

	// The single-source artifacts must NOT be written for this composed app.
	for _, stale := range []string{"values.yaml", "app.yaml", "_targets"} {
		path := filepath.Join(dir, "envs", "prod", "demo", "bigly", stale)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("composed pin must not write single-source %s (err=%v)", stale, err)
		}
	}
}
