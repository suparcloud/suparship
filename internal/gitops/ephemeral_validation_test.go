package gitops

import (
	"context"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
)

func TestEphemeralDormancySurvivesRepublish(t *testing.T) {
	root := t.TempDir()
	p, err := NewPublisher(PublisherConfig{RepoURL: "http://localhost/gitops.git"})
	if err != nil {
		t.Fatal(err)
	}
	app := &domain.App{Name: "web", ProjectName: "demo"}
	app.Spec.CD.ReleaseValidationEnvironment = "validation"
	app.Spec.CD.ReleaseValidationMode = "ephemeral"
	env := AppPublishEnv{EnvName: "validation", Bound: true}

	if dormant, err := p.validationDormant(root, app, env); err != nil || !dormant {
		t.Fatalf("new environment must start idle: %v %v", dormant, err)
	}
	for _, tree := range []string{"envs", "envs-external"} {
		for _, name := range []string{"validation", "prod"} {
			if err := p.writeFile(p.outputDir(root, tree, name, "demo", "web", "_targets", "local", "app.yaml"), []byte("name: web\nproject: demo\n")); err != nil {
				t.Fatal(err)
			}
		}
	}
	stage := p.outputDir(root, "_infra", "kargo", "stage.yaml")
	if err := p.writeFile(stage, []byte("name: web\nproject: demo\n")); err != nil {
		t.Fatal(err)
	}
	if dormant, err := p.validationDormant(root, app, env); err != nil || dormant {
		t.Fatalf("existing deployment removed before scheduler handoff: %v %v", dormant, err)
	}
	if err := p.writeFile(filepath.Join(root, validationStatePath("", "demo", "web", "validation")), []byte("dormant: true\n")); err != nil {
		t.Fatal(err)
	}
	if err := p.publishAppFiles(context.Background(), root, app, []AppPublishEnv{env}); err != nil {
		t.Fatal(err)
	}
	for _, tree := range []string{"envs", "envs-external"} {
		data, err := os.ReadFile(p.outputDir(root, tree, "validation", "demo", "web", "_targets", "local", "app.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var meta AppMetadata
		if err := yaml.Unmarshal(data, &meta); err != nil {
			t.Fatal(err)
		}
		if meta.ValidationIdle != "true" || meta.ValidationEmptyPath == "" {
			t.Fatal("idle shell not published")
		}
		if _, err := os.Stat(p.outputDir(root, tree, "prod", "demo", "web", "_targets")); err != nil {
			t.Fatal("production touched")
		}
	}
	if _, err := os.Stat(stage); err != nil {
		t.Fatal("stage history removed")
	}
	state := filepath.Join(root, validationStatePath("", "demo", "web", "validation"))
	if err := p.writeFile(state, []byte("dormant: false\n")); err != nil {
		t.Fatal(err)
	}
	if dormant, err := p.validationDormant(root, app, env); err != nil || dormant {
		t.Fatal("active state lost")
	}
	if err := p.writeFile(state, []byte("dormant: [invalid]\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.validationDormant(root, app, env); err == nil {
		t.Fatal("corrupt marker accepted")
	}
	app.Spec.CD.ReleaseValidationMode = "persistent"
	if dormant, err := p.validationDormant(root, app, env); err != nil || dormant {
		t.Fatal("persistent mode depended on ephemeral marker")
	}
}

func TestValidationCleanupPreservesEmptyComposedRoot(t *testing.T) {
	for _, sub := range []string{"", "gitops"} {
		t.Run(sub, func(t *testing.T) {
			root := t.TempDir()
			p, err := NewPublisher(PublisherConfig{RepoURL: "http://localhost/gitops.git", SubPath: sub})
			if err != nil {
				t.Fatal(err)
			}
			app := &domain.App{Name: "web", ProjectName: "demo"}
			env := AppPublishEnv{EnvName: "validation"}
			parent := BuildComposedRootApp(env.EnvName, "http://localhost/gitops.git", AppSetOptions{SyncAutomated: true, SubPath: sub})
			data, err := yaml.Marshal(parent)
			if err != nil {
				t.Fatal(err)
			}
			parentPath := p.outputDir(root, "_infra", "validation-composed-appset.yaml")
			if err := p.writeFile(parentPath, data); err != nil {
				t.Fatal(err)
			}
			child := p.outputDir(root, composedAppsDir, "validation", "demo", "web", "_targets", "local", "app.yaml")
			if err := p.writeFile(child, []byte("name: web\nproject: demo\n")); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if err := p.removeValidationWorkloads(root, app, env); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := os.Stat(child); !os.IsNotExist(err) {
				t.Fatal("workload input retained")
			}
			if _, err := os.Stat(p.outputDir(root, composedAppsDir, "validation", ".gitkeep")); err != nil {
				t.Fatal(err)
			}
			data, err = os.ReadFile(parentPath)
			if err != nil {
				t.Fatal(err)
			}
			var got Application
			if err := yaml.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if !got.Spec.SyncPolicy.Automated.AllowEmpty || got.Spec.Source.Path != parent.Spec.Source.Path {
				t.Fatal("cleanup root cannot reconcile empty state")
			}
			stable := BuildComposedRootApp("production", "http://localhost/gitops.git", AppSetOptions{SyncAutomated: true})
			if stable.Spec.SyncPolicy.Automated.AllowEmpty {
				t.Fatal("stable environment empty protection changed")
			}
		})
	}
}

func TestParkComposedValidationRestoresMissingApplications(t *testing.T) {
	root := t.TempDir()
	p, _ := NewPublisher(PublisherConfig{RepoURL: "http://gitops", SubPath: "output"})
	app := &domain.App{Name: "web", ProjectName: "demo", Spec: domain.AppSpec{
		CD:         domain.CDConfig{ReleaseValidationEnvironment: "validation", ReleaseValidationMode: "ephemeral"},
		Components: []domain.ComponentSpec{{Name: "web", Template: &domain.AppTemplateRef{Name: "web"}}, {Name: "cache", Stateful: true, ReleaseValidationDataPolicy: "disposable", Template: &domain.AppTemplateRef{Name: "cache"}}},
	}}
	env := AppPublishEnv{EnvName: "validation", Clusters: []ClusterTarget{{Name: "east"}, {Name: "west"}}}
	for _, cluster := range env.Clusters {
		for _, file := range []string{"application.yaml", "cache-application.yaml"} {
			manifest := &Application{APIVersion: argoAPIVersion, Kind: argoKind, Metadata: ObjectMeta{Name: cluster.Name + "-" + file, Namespace: "argocd"}, Spec: ApplicationSpec{Project: "demo", Sources: []ApplicationSource{{RepoURL: "http://chart", Chart: "cache", TargetRevision: "1.0"}}, Destination: ApplicationDestination{Namespace: "validation", Server: "https://" + cluster.Name}}}
			if err := p.saveActiveApplication(root, app, env, cluster.Name+"-"+file, manifest); err != nil {
				t.Fatal(err)
			}
		}
	}
	// No _targets exists: this is an already-cleaned run from the previous version.
	for i := 0; i < 2; i++ {
		if err := p.removeValidationWorkloads(root, app, env); err != nil {
			t.Fatal(err)
		}
	}
	for _, cluster := range env.Clusters {
		for _, file := range []string{"application.yaml", "cache-application.yaml"} {
			data, err := os.ReadFile(p.outputDir(root, composedAppsDir, "validation", "demo", "web", "_targets", cluster.Name, file))
			if err != nil {
				t.Fatal(err)
			}
			var shell Application
			if err := yaml.Unmarshal(data, &shell); err != nil {
				t.Fatal(err)
			}
			if shell.Spec.Source.Path != "output/.suparship/validation-empty" || len(shell.Spec.Sources) != 0 || shell.Metadata.Annotations[validationIdleAnnotation] != "true" || !shell.Spec.SyncPolicy.Automated.Prune || !shell.Spec.SyncPolicy.Automated.AllowEmpty {
				t.Fatalf("unsafe shell: %+v", shell)
			}
			if shell.Spec.Destination.Server != "https://"+cluster.Name {
				t.Fatal("cluster changed")
			}
			original, err := os.ReadFile(filepath.Join(root, activeApplicationPath("output", "demo", "web", "validation", cluster.Name+"-"+file)))
			if err != nil {
				t.Fatal(err)
			}
			var active Application
			if err := yaml.Unmarshal(original, &active); err != nil {
				t.Fatal(err)
			}
			if len(active.Spec.Sources) != 1 || active.Metadata.Annotations[validationIdleAnnotation] != "false" {
				t.Fatal("cleanup overwrote activation template")
			}
		}
	}
}
