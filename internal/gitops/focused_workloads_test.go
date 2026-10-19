package gitops

import (
	"context"
	"github.com/suparcloud/suparship/internal/domain"
	"os"
	"testing"
)

func TestFocusedPublishLeavesValidationWorkloadsUntouched(t *testing.T) {
	p, err := NewPublisher(PublisherConfig{RepoURL: "http://localhost/gitops.git"})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	app := &domain.App{Name: "web", ProjectName: "demo"}
	app.Spec.CD.ReleaseValidationEnvironment = "validation"
	app.Spec.CD.ReleaseValidationMode = "ephemeral"
	app.Spec.Components = []domain.ComponentSpec{{Name: "redis", Enabled: true, Stateful: true, ReleaseValidationDataPolicy: "disposable", Template: &domain.AppTemplateRef{Name: "missing-chart", Version: "1"}}}
	envs := []AppPublishEnv{{EnvName: "staging", Bound: true}, {EnvName: "validation", Bound: true}, {EnvName: "production", Bound: true}}
	scoped := focusedWorkloadEnvs(envs, envs[:1])
	if len(scoped) != 3 || scoped[0].SkipWorkloads || !scoped[1].SkipWorkloads || !scoped[2].SkipWorkloads || envs[1].SkipWorkloads {
		t.Fatal("focus changed pipeline or input")
	}
	path := p.outputDir(root, composedAppsDir, "validation", "demo", "web", "_targets", "local", "app.yaml")
	if err := p.writeFile(path, []byte("existing validation manifest")); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	// A full publish would try to render the missing disposable chart; a stable
	// pin must neither validate nor remove this unrelated environment's resources.
	if err := p.publishComposedAppFiles(context.Background(), root, app, scoped[1:2]); err != nil {
		t.Fatal(err)
	}
	if err := p.pruneComposedArtifacts(root, app, scoped[1:2]); err != nil {
		t.Fatal(err)
	}
	if err := p.publishAppFiles(context.Background(), root, app, scoped[1:2]); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("unrelated validation changed")
	}
	if focusedWorkloadEnvs(envs, nil)[1].SkipWorkloads {
		t.Fatal("ordinary publish bypasses validation")
	}
}
