package gitops

import (
	"context"
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
	for _, tree := range []string{"envs", "envs-external", composedAppsDir} {
		for _, name := range []string{"validation", "prod"} {
			if err := p.writeFile(p.outputDir(root, tree, name, "demo", "web", "_targets", "local", "app.yaml"), []byte("test")); err != nil {
				t.Fatal(err)
			}
		}
	}
	stage := p.outputDir(root, "_infra", "kargo", "stage.yaml")
	if err := p.writeFile(stage, []byte("test")); err != nil {
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
	for _, tree := range []string{"envs", "envs-external", composedAppsDir} {
		if _, err := os.Stat(p.outputDir(root, tree, "validation", "demo", "web", "_targets")); !os.IsNotExist(err) {
			t.Fatal("idle workload resurrected")
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
