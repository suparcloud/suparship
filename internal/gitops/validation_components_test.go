package gitops

import (
	"context"
	"github.com/suparcloud/suparship/internal/domain"
	"gopkg.in/yaml.v3"
	"os"
	"strings"
	"testing"
)

func TestValidationSelectionRenderingAndPromotion(t *testing.T) {
	no := false
	app := &domain.App{Name: "app", ProjectName: "demo", Spec: domain.AppSpec{CD: domain.CDConfig{ReleaseValidationEnvironment: "validation"}, Components: []domain.ComponentSpec{
		{Name: "web", Template: &domain.AppTemplateRef{Name: "web"}},
		{Name: "cache", Stateful: true, Template: &domain.AppTemplateRef{Name: "cache"}, ReleaseValidationEnabled: &no},
	}}}
	root := t.TempDir()
	p, _ := NewPublisher(PublisherConfig{RepoURL: "http://gitops", SyncAutomated: true})
	for _, env := range []string{"staging", "validation", "production"} {
		if err := p.publishComposedAppFiles(context.Background(), root, app, []AppPublishEnv{{EnvName: env, Bound: true}}); err != nil {
			t.Fatal(err)
		}
		path := p.outputDir(root, composedAppsDir, env, "demo", "app", "_targets", fallbackClusterName, "application.yaml")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var manifest Application
		if err := yaml.Unmarshal(data, &manifest); err != nil {
			t.Fatal(err)
		}
		if len(manifest.Spec.Sources) != 2 {
			t.Fatal("main workload disappeared when only one component selected")
		}
		_, err = os.Stat(p.outputDir(root, composedAppsDir, env, "demo", "app", "_targets", fallbackClusterName, "cache-application.yaml"))
		if (env == "validation") != os.IsNotExist(err) {
			t.Fatalf("%s cache Application: %v", env, err)
		}
		opts := KargoBuildOptions{Composed: true, GitOpsRepoURL: "http://gitops", Images: []KargoImage{{Name: "web", Repository: "web", TagKey: "image.tag"}, {Name: "cache", Repository: "cache", TagKey: "image.tag"}}}
		stage := BuildKargoStage(app, domain.AppEnvironment{EnvName: env}, nil, opts)
		raw, err := yaml.Marshal(stage)
		if err != nil {
			t.Fatal(err)
		}
		if env == "validation" && (strings.Contains(string(raw), "cache-application") || strings.Contains(string(raw), "components/cache/") || strings.Contains(string(raw), "-cache\n")) {
			t.Fatalf("excluded component leaked into promotion: %s", raw)
		}
	}
}

func TestIdleSelectionPrunesExcludedShellAndKeepsComposedIdentity(t *testing.T) {
	no := false
	for _, excluded := range []string{"cache", "web"} {
		t.Run(excluded, func(t *testing.T) {
			app := &domain.App{Name: "app", ProjectName: "demo", Spec: domain.AppSpec{CD: domain.CDConfig{ReleaseValidationEnvironment: "validation"}, Components: []domain.ComponentSpec{
				{Name: "web", Template: &domain.AppTemplateRef{Name: "web"}},
				{Name: "cache", Stateful: true, Template: &domain.AppTemplateRef{Name: "cache"}},
			}}}
			root := t.TempDir()
			p, _ := NewPublisher(PublisherConfig{RepoURL: "http://gitops", SyncAutomated: true})
			env := AppPublishEnv{EnvName: "validation", Bound: true}
			if err := p.publishComposedAppFiles(context.Background(), root, app, []AppPublishEnv{env}); err != nil {
				t.Fatal(err)
			}
			for i := range app.Spec.Components {
				if app.Spec.Components[i].Name == excluded {
					app.Spec.Components[i].ReleaseValidationEnabled = &no
				}
			}
			app.Spec.CD.ReleaseValidationMode = "ephemeral"
			for i := 0; i < 2; i++ {
				if err := p.removeValidationWorkloads(root, app, env); err != nil {
					t.Fatal(err)
				}
			}
			for _, c := range []string{"web", "cache"} {
				file := "application.yaml"
				if c == "cache" {
					file = "cache-application.yaml"
				}
				_, err := os.Stat(p.outputDir(root, composedAppsDir, "validation", "demo", "app", "_targets", fallbackClusterName, file))
				if (c == excluded) != os.IsNotExist(err) {
					t.Fatalf("%s shell: %v", c, err)
				}
			}
		})
	}
}
