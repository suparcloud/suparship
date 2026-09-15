package gitops_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/gitops"
)

func gitInitCommit(t *testing.T, dir string, msg string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		run("init", "-q", "-b", "main")
	}
	run("add", "-A")
	run("commit", "-q", "--allow-empty", "-m", msg)
}

// DetectAppDrift renders from the store into the checkout, reports what git
// sees as changed, and leaves the working tree exactly as it found it.
func TestDetectAppDrift(t *testing.T) {
	dir := t.TempDir()
	p := newTestPublisher(t)
	app := &domain.App{
		Name: "solo", ProjectName: "demo",
		Spec: domain.AppSpec{
			Template:  domain.AppTemplateRef{Name: "web-service"},
			RawValues: map[string]any{"replicaCount": 1},
		},
	}
	envs := []gitops.AppPublishEnv{{
		EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, BaseDomain: "localhost",
		Namespace: "solo-staging",
		Clusters:  []gitops.ClusterTarget{{Name: "c1", Server: "https://c1"}},
	}}
	// Publish once (the same full tree the drift check renders) and commit:
	// repo == store → no drift.
	if err := p.WriteAppTreeForTest(context.Background(), dir, app, envs); err != nil {
		t.Fatalf("publish: %v", err)
	}
	gitInitCommit(t, dir, "publish")
	files, err := p.DetectAppDriftInRepo(context.Background(), dir, app, envs)
	if err != nil {
		t.Fatalf("drift (clean): %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("expected no drift right after publish, got %v", files)
	}

	// Someone reverts the values in the repo behind suparship's back.
	valuesPath := filepath.Join(dir, "envs", "staging", "demo", "solo", "values.yaml")
	orig, err := os.ReadFile(valuesPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(valuesPath, []byte("# hand edited\nreplicaCount: 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInitCommit(t, dir, "manual revert")

	files, err = p.DetectAppDriftInRepo(context.Background(), dir, app, envs)
	if err != nil {
		t.Fatalf("drift (reverted): %v", err)
	}
	if len(files) != 1 || files[0] != "envs/staging/demo/solo/values.yaml" {
		t.Errorf("drift files = %v, want just the reverted values.yaml", files)
	}
	// The check must not leave the render behind: the committed (hand-edited)
	// content is still what the working tree holds.
	after, _ := os.ReadFile(valuesPath)
	if string(after) != "# hand edited\nreplicaCount: 99\n" {
		t.Errorf("drift check must discard its render; values.yaml now:\n%s", after)
	}
	out, _ := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	if len(out) != 0 {
		t.Errorf("working tree must be clean after a drift check, got:\n%s", out)
	}
	_ = orig
}
