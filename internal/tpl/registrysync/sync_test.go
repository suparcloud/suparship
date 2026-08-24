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
	}, nil)
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
	}, nil)
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
	}, nil)

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
	}, nil)
	if res.Err == nil {
		t.Fatal("expected error for missing repo")
	}
	if len(res.Templates) != 0 {
		t.Errorf("expected no templates on clone failure, got %v", res.Templates)
	}
}

// --- External-mode template discovery (PR3.1) ---

const externalTemplateYAML = `apiVersion: suparship.io/v1alpha1
kind: Template
metadata:
  name: valkey
  version: "1.0.0"
spec:
  title: Valkey
  description: Managed Valkey via Bitnami chart.
  category: data
  engine:
    type: helm
    chart:
      repository: oci://registry-1.docker.io/bitnamicharts
      name: valkey
      version: 1.2.3
  inputs: []
`

func TestSyncOne_ImportsExternalTemplateWithoutChartDir(t *testing.T) {
	requireGit(t)

	repoDir := t.TempDir()
	gitInit(t, repoDir)

	// Repo layout — pure external template (no sibling chart/):
	//   templates/
	//     valkey/
	//       template.yaml      (engine.chart points at OCI registry)
	writeFile(t, filepath.Join(repoDir, "templates/valkey/template.yaml"), externalTemplateYAML)
	gitCommit(t, repoDir, "initial")

	client := k8sfake.NewClientset()
	eng := &registrysync.Engine{Client: client}
	res := eng.SyncOne(context.Background(), tpl.ExternalTemplateRepo{
		Name:    "demo",
		RepoURL: repoDir,
		Ref:     "main",
		Path:    "templates",
	}, nil)
	if res.Err != nil {
		t.Fatalf("SyncOne returned error: %v", res.Err)
	}
	if len(res.Templates) != 1 || res.Templates[0] != "valkey" {
		t.Fatalf("expected [valkey], got %v", res.Templates)
	}

	// The persisted ConfigMap should NOT carry a chart.tgz bundle —
	// external-mode templates resolve at publish time via Argo's
	// repo-server. The alias and the per-version archive both exist
	// (PR1.4) so we verify the alias here.
	cm, err := client.CoreV1().ConfigMaps("suparship-system").Get(
		context.Background(), "suparship-template-valkey", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get configmap: %v", err)
	}
	if _, ok := cm.Data["template.yaml"]; !ok {
		t.Error("missing template.yaml in ConfigMap")
	}
	if len(cm.BinaryData["chart.tgz"]) != 0 {
		t.Errorf("external-mode template ConfigMap must not carry chart.tgz bytes; got %d bytes", len(cm.BinaryData["chart.tgz"]))
	}
}

func TestSyncOne_RejectsTemplateYAMLWithoutChartOrRegistryRef(t *testing.T) {
	// A template.yaml without a sibling chart/ AND without a registry
	// ref is a malformed source — there's nothing to render. Surface
	// the failure as a per-template PartialError, not a top-level error.
	requireGit(t)

	repoDir := t.TempDir()
	gitInit(t, repoDir)

	const brokenYAML = `apiVersion: suparship.io/v1alpha1
kind: Template
metadata: { name: broken, version: "1.0.0" }
spec:
  title: Broken
  category: web
  engine:
    type: helm
    # No chart field, and no sibling chart/ — neither bundled nor
    # external-mode is satisfied.
  inputs: []
`
	writeFile(t, filepath.Join(repoDir, "templates/broken/template.yaml"), brokenYAML)
	// Add a valid external template alongside to confirm it still imports.
	writeFile(t, filepath.Join(repoDir, "templates/valkey/template.yaml"), externalTemplateYAML)
	gitCommit(t, repoDir, "initial")

	client := k8sfake.NewClientset()
	eng := &registrysync.Engine{Client: client}
	res := eng.SyncOne(context.Background(), tpl.ExternalTemplateRepo{
		Name: "demo", RepoURL: repoDir, Ref: "main", Path: "templates",
	}, nil)
	// Top-level err carries the most-recent partial err — but the valid
	// template still imports.
	if res.Err == nil {
		t.Error("expected per-template error to surface on Result.Err")
	}
	if len(res.Templates) != 1 || res.Templates[0] != "valkey" {
		t.Fatalf("expected only valkey to import, got %v", res.Templates)
	}
}

func TestSyncOne_MixedInlineAndExternal(t *testing.T) {
	requireGit(t)

	repoDir := t.TempDir()
	gitInit(t, repoDir)

	// Inline template + external template in the same repo.
	writeFile(t, filepath.Join(repoDir, "templates/inline/chart/Chart.yaml"),
		"apiVersion: v2\nname: inline\nversion: 1.0.0\n")
	writeFile(t, filepath.Join(repoDir, "templates/inline/chart/values.yaml"), "{}\n")
	writeFile(t, filepath.Join(repoDir, "templates/external/template.yaml"), externalTemplateYAML)
	gitCommit(t, repoDir, "initial")

	client := k8sfake.NewClientset()
	eng := &registrysync.Engine{Client: client}
	res := eng.SyncOne(context.Background(), tpl.ExternalTemplateRepo{
		Name: "demo", RepoURL: repoDir, Ref: "main", Path: "templates",
	}, nil)
	if res.Err != nil {
		t.Fatalf("SyncOne: %v", res.Err)
	}
	if len(res.Templates) != 2 {
		t.Fatalf("expected 2 templates, got %v", res.Templates)
	}

	// Inline ConfigMap carries chart.tgz; external one does not.
	inlineCM, err := client.CoreV1().ConfigMaps("suparship-system").Get(
		context.Background(), "suparship-template-inline", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get inline: %v", err)
	}
	if len(inlineCM.BinaryData["chart.tgz"]) == 0 {
		t.Error("inline ConfigMap missing chart.tgz")
	}

	externalCM, err := client.CoreV1().ConfigMaps("suparship-system").Get(
		context.Background(), "suparship-template-valkey", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get valkey: %v", err)
	}
	if len(externalCM.BinaryData["chart.tgz"]) != 0 {
		t.Errorf("external ConfigMap unexpectedly carries chart.tgz (%d bytes)", len(externalCM.BinaryData["chart.tgz"]))
	}
}

// TestSyncOne_GitCharts covers the gitcharts source type: the scan path
// defaults to charts/, a plain chart imports as passthrough/BYO, and a chart
// shipping its own template.yaml is honored as-authored (canonical).
func TestSyncOne_GitCharts(t *testing.T) {
	requireGit(t)

	repoDir := t.TempDir()
	gitInit(t, repoDir)

	// charts/foo: a plain chart, no template.yaml → passthrough.
	writeFile(t, filepath.Join(repoDir, "charts/foo/Chart.yaml"),
		"apiVersion: v2\nname: foo\nversion: 1.0.0\n")
	writeFile(t, filepath.Join(repoDir, "charts/foo/values.yaml"),
		"replicas: 1\nimage:\n  repository: foo\n")
	// charts/bar: ships its own template.yaml (canonical, declares an input).
	writeFile(t, filepath.Join(repoDir, "charts/bar/Chart.yaml"),
		"apiVersion: v2\nname: bar\nversion: 0.2.0\n")
	writeFile(t, filepath.Join(repoDir, "charts/bar/values.yaml"),
		"replicas: 2\n")
	writeFile(t, filepath.Join(repoDir, "charts/bar/template.yaml"),
		"apiVersion: suparship.io/v1alpha1\nkind: Template\n"+
			"metadata:\n  name: bar\n  version: 0.2.0\n"+
			"spec:\n  title: Bar\n  category: web\n"+
			"  engine:\n    type: helm\n    chart:\n      path: ./chart\n"+
			"  inputs:\n    - name: greeting\n      title: Greeting\n      type: string\n")
	gitCommit(t, repoDir, "initial")

	client := k8sfake.NewClientset()
	eng := &registrysync.Engine{Client: client}
	// Note: no Path set — gitcharts must default to charts/.
	res := eng.SyncOne(context.Background(), tpl.ExternalTemplateRepo{
		Name:    "mycharts",
		Type:    tpl.SourceTypeGitCharts,
		RepoURL: repoDir,
		Ref:     "main",
	}, nil)
	if res.Err != nil {
		t.Fatalf("SyncOne returned error: %v", res.Err)
	}
	if len(res.Templates) != 2 {
		t.Fatalf("expected 2 templates imported, got %d (%v)", len(res.Templates), res.Templates)
	}

	load := func(name string) *tpl.Template {
		t.Helper()
		cm, err := client.CoreV1().ConfigMaps("suparship-system").Get(
			context.Background(), "suparship-template-"+name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("template %s: %v", name, err)
		}
		parsed, err := tpl.Parse([]byte(cm.Data["template.yaml"]))
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		return parsed
	}

	// foo: chart-only import — the legacy inferred inputs/mappings are dropped
	// (they described the retired canonical schema).
	foo := load("foo")
	if len(foo.Spec.Inputs) != 0 || len(foo.Spec.Mappings) != 0 {
		t.Errorf("foo should have no inferred inputs/mappings, got %d inputs / %d mappings", len(foo.Spec.Inputs), len(foo.Spec.Mappings))
	}

	// bar: authored template.yaml honored — its input kept.
	bar := load("bar")
	if len(bar.Spec.Inputs) != 1 || bar.Spec.Inputs[0].Name != "greeting" {
		t.Errorf("bar should keep its authored input, got %+v", bar.Spec.Inputs)
	}
}

// TestSyncOne_CollisionGuard verifies the template-name collision guard:
// names are global, so a synced chart shadowing a disk built-in or a
// template owned by ANOTHER source is skipped (with an error naming the
// owner), while names this source already owns re-sync normally.
func TestSyncOne_CollisionGuard(t *testing.T) {
	requireGit(t)

	repoDir := t.TempDir()
	gitInit(t, repoDir)
	writeFile(t, filepath.Join(repoDir, "charts/worker/Chart.yaml"),
		"apiVersion: v2\nname: worker\nversion: 1.0.0\n")
	writeFile(t, filepath.Join(repoDir, "charts/web/Chart.yaml"),
		"apiVersion: v2\nname: web\nversion: 1.0.0\n")
	writeFile(t, filepath.Join(repoDir, "charts/mine/Chart.yaml"),
		"apiVersion: v2\nname: mine\nversion: 1.0.0\n")
	gitCommit(t, repoDir, "initial")

	client := k8sfake.NewClientset()
	eng := &registrysync.Engine{Client: client, Builtins: []string{"worker"}}
	reg := &tpl.TemplateRegistry{Sources: []tpl.TemplateSource{
		// "web" belongs to a different source → conflict.
		{Name: "web", Origin: "external", ExternalRepo: "other-source"},
		// "mine" was previously synced by THIS source → re-sync allowed.
		{Name: "mine", Origin: "external", ExternalRepo: "demo"},
	}}

	res := eng.SyncOne(context.Background(), tpl.ExternalTemplateRepo{
		Name:    "demo",
		Type:    "gitcharts",
		RepoURL: repoDir,
		Ref:     "main",
	}, reg)

	if len(res.Templates) != 1 || res.Templates[0] != "mine" {
		t.Fatalf("expected only [mine] imported, got %v", res.Templates)
	}
	if res.Err == nil {
		t.Fatal("expected a conflict error surfaced on the result")
	}
	// Skipped charts must not be persisted.
	for _, name := range []string{"worker", "web"} {
		if _, err := client.CoreV1().ConfigMaps("suparship-system").Get(
			context.Background(), "suparship-template-"+name, metav1.GetOptions{}); err == nil {
			t.Errorf("conflicting template %s was persisted; guard failed", name)
		}
	}
	if _, err := client.CoreV1().ConfigMaps("suparship-system").Get(
		context.Background(), "suparship-template-mine", metav1.GetOptions{}); err != nil {
		t.Errorf("non-conflicting template mine should be persisted: %v", err)
	}
}

// TestSyncOne_CollisionGuard_NilRegistry: a nil registry disables source
// ownership checks (nothing to consult) but built-ins stay protected.
func TestSyncOne_CollisionGuard_NilRegistry(t *testing.T) {
	requireGit(t)

	repoDir := t.TempDir()
	gitInit(t, repoDir)
	writeFile(t, filepath.Join(repoDir, "charts/cronjob/Chart.yaml"),
		"apiVersion: v2\nname: cronjob\nversion: 1.0.0\n")
	gitCommit(t, repoDir, "initial")

	client := k8sfake.NewClientset()
	eng := &registrysync.Engine{Client: client, Builtins: []string{"cronjob"}}
	res := eng.SyncOne(context.Background(), tpl.ExternalTemplateRepo{
		Name:    "demo",
		Type:    "gitcharts",
		RepoURL: repoDir,
		Ref:     "main",
	}, nil)

	if len(res.Templates) != 0 {
		t.Fatalf("expected no templates imported, got %v", res.Templates)
	}
	if res.Err == nil {
		t.Fatal("expected a conflict error for the built-in name")
	}
}
