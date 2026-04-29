package registrysync_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/suparcloud/suparship/internal/tpl"
	"github.com/suparcloud/suparship/internal/tpl/registrysync"
)

// requireGit skips the test when the system has no git on PATH. Engine
// shells out to git rather than using a Go library; without git the e2e
// path can't run.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping registrysync e2e test")
	}
}

// gitInit runs `git init` + sets identity in dir. Subsequent tests can then
// commit and clone from this directory as if it were a remote.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// gitCommit stages everything in dir and commits. Each test sets up the
// chart layout it wants and then calls this once.
func gitCommit(t *testing.T, dir, msg string) {
	t.Helper()
	for _, args := range [][]string{
		{"add", "-A"},
		{"commit", "-q", "-m", msg},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// writeFile is a tiny helper that creates parent dirs and writes content.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestSyncOne_ImportsChartsFromGitPath(t *testing.T) {
	requireGit(t)

	repoDir := t.TempDir()
	gitInit(t, repoDir)

	// Repo layout:
	//   charts/
	//     hello/
	//       Chart.yaml
	//       values.yaml
	//     world/
	//       Chart.yaml
	//       values.yaml
	writeFile(t, filepath.Join(repoDir, "charts/hello/Chart.yaml"),
		"apiVersion: v2\nname: hello\nversion: 1.0.0\n")
	writeFile(t, filepath.Join(repoDir, "charts/hello/values.yaml"),
		"replicas: 1\n")
	writeFile(t, filepath.Join(repoDir, "charts/world/Chart.yaml"),
		"apiVersion: v2\nname: world\nversion: 0.2.0\n")
	writeFile(t, filepath.Join(repoDir, "charts/world/values.yaml"),
		"replicas: 2\n")
	gitCommit(t, repoDir, "initial")

	client := k8sfake.NewClientset()
	eng := &registrysync.Engine{Client: client}
	res := eng.SyncOne(context.Background(), tpl.ExternalTemplateRepo{
		Name:    "demo",
		RepoURL: repoDir,
		Ref:     "main",
		Path:    "charts",
	})
	if res.Err != nil {
		t.Fatalf("SyncOne returned error: %v", res.Err)
	}
	if len(res.Templates) != 2 {
		t.Fatalf("expected 2 templates imported, got %d (%v)", len(res.Templates), res.Templates)
	}

	// Both ConfigMaps should now exist in suparship-system, with the chart
	// bundle attached as binaryData["chart.tgz"].
	for _, name := range []string{"hello", "world"} {
		cm, err := client.CoreV1().ConfigMaps("suparship-system").Get(
			context.Background(), "suparship-template-"+name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("template %s: %v", name, err)
		}
		if _, ok := cm.Data["template.yaml"]; !ok {
			t.Errorf("template %s: missing template.yaml", name)
		}
		if len(cm.BinaryData["chart.tgz"]) == 0 {
			t.Errorf("template %s: missing chart.tgz bundle", name)
		}
	}
}

func TestSyncOne_RootPathIsValid(t *testing.T) {
	requireGit(t)

	repoDir := t.TempDir()
	gitInit(t, repoDir)
	writeFile(t, filepath.Join(repoDir, "Chart.yaml"),
		"apiVersion: v2\nname: oneoff\nversion: 1.0.0\n")
	writeFile(t, filepath.Join(repoDir, "values.yaml"), "k: v\n")
	gitCommit(t, repoDir, "single chart at repo root")

	client := k8sfake.NewClientset()
	eng := &registrysync.Engine{Client: client}
	res := eng.SyncOne(context.Background(), tpl.ExternalTemplateRepo{
		Name:    "root",
		RepoURL: repoDir,
		Ref:     "main",
		Path:    "",
	})
	if res.Err != nil {
		t.Fatalf("SyncOne: %v", res.Err)
	}
	if len(res.Templates) != 1 || res.Templates[0] != "oneoff" {
		t.Errorf("expected [oneoff], got %v", res.Templates)
	}
}

func TestSyncOne_PartialFailureKeepsGoing(t *testing.T) {
	requireGit(t)

	repoDir := t.TempDir()
	gitInit(t, repoDir)
	// Good chart.
	writeFile(t, filepath.Join(repoDir, "charts/good/Chart.yaml"),
		"apiVersion: v2\nname: good\nversion: 1.0.0\n")
	writeFile(t, filepath.Join(repoDir, "charts/good/values.yaml"), "k: v\n")
	// Bad chart — missing required `name` field, ToTemplate will reject.
	writeFile(t, filepath.Join(repoDir, "charts/bad/Chart.yaml"),
		"apiVersion: v2\nversion: 1.0.0\n")
	writeFile(t, filepath.Join(repoDir, "charts/bad/values.yaml"), "k: v\n")
	gitCommit(t, repoDir, "one good one bad")

	client := k8sfake.NewClientset()
	eng := &registrysync.Engine{Client: client}
	res := eng.SyncOne(context.Background(), tpl.ExternalTemplateRepo{
		Name:    "mixed",
		RepoURL: repoDir,
		Ref:     "main",
		Path:    "charts",
	})

	// Partial success: the good chart should land, the bad one should be
	// surfaced via Err but not abort the loop.
	if len(res.Templates) != 1 || res.Templates[0] != "good" {
		t.Errorf("expected [good], got %v", res.Templates)
	}
	if res.Err == nil {
		t.Error("expected last-error to be populated for the bad chart")
	}
}

func TestSyncOne_BadRepoURL(t *testing.T) {
	requireGit(t)
	client := k8sfake.NewClientset()
	eng := &registrysync.Engine{Client: client, CloneDepth: 1}
	res := eng.SyncOne(context.Background(), tpl.ExternalTemplateRepo{
		Name:    "missing",
		RepoURL: "/nonexistent/path/to/repo",
		Ref:     "main",
		Path:    "charts",
	})
	if res.Err == nil {
		t.Fatal("expected error for missing repo")
	}
	if len(res.Templates) != 0 {
		t.Errorf("expected no templates on clone failure, got %v", res.Templates)
	}
}
