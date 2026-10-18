package gitops

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
)

func TestDisposableStorageInspection(t *testing.T) {
	for _, tc := range []struct {
		name, manifest string
		safe           bool
	}{
		{"temporary", "kind: Deployment\nspec:\n  template:\n    spec:\n      volumes:\n      - name: data\n        emptyDir: {}\n      - name: config\n        configMap: {name: app-config}\n", true},
		{"retained-pvc", "kind: PersistentVolumeClaim\nmetadata:\n  annotations: {argocd.argoproj.io/sync-options: 'Prune=false,Delete=false'}\n", false},
		{"existing-claim", "kind: Pod\nspec:\n  volumes:\n  - name: data\n    persistentVolumeClaim: {claimName: staging-data}\n", false},
		{"vct", "kind: StatefulSet\nspec:\n  volumeClaimTemplates:\n  - metadata: {name: data}\n", false},
		{"host", "kind: Pod\nspec:\n  volumes:\n  - name: data\n    hostPath: {path: /data}\n", false},
		{"list", "kind: List\nitems:\n- kind: PersistentVolumeClaim\n", false},
		{"docs", "kind: Service\n---\nkind: PersistentVolume\n", false},
		{"bad-yaml", "kind: [", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkDisposableManifests([]byte(tc.manifest)); (err == nil) != tc.safe {
				t.Fatalf("safe=%t error=%v", tc.safe, err)
			}
		})
	}
}
func TestDisposableValidationPruneIsEnvironmentScoped(t *testing.T) {
	c := domain.ComponentSpec{Name: "redis", Stateful: true, ReleaseValidationDataPolicy: "disposable", Template: &domain.AppTemplateRef{Name: "redis"}}
	app := &domain.App{Spec: domain.AppSpec{Components: []domain.ComponentSpec{c}, CD: domain.CDConfig{ReleaseValidationEnvironment: "validation", ReleaseValidationMode: "ephemeral"}}}
	for _, env := range []string{"staging", "prod", "validation"} {
		manifest := BuildComponentApplication(app, c, ComposedBuildOptions{EnvName: env, SyncAutomated: true})
		if got := manifest.Spec.SyncPolicy.Automated.Prune; got != (env == "validation") {
			t.Fatalf("%s prune=%t", env, got)
		}
	}
	app.Spec.CD.ReleaseValidationMode = "persistent"
	if BuildComponentApplication(app, c, ComposedBuildOptions{EnvName: "validation", SyncAutomated: true}).Spec.SyncPolicy.Automated.Prune {
		t.Fatal("persistent validation lost protection")
	}
}
func TestDisposableRenderChecksChartDefaults(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is needed for the real chart rendering test")
	}
	repo := t.TempDir()
	chart := filepath.Join(repo, "charts", "redis", "latest")
	if err := os.MkdirAll(filepath.Join(chart, "templates"), 0755); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string]string{
		"Chart.yaml":             "apiVersion: v2\nname: redis\nversion: 1.0.0\n",
		"values.yaml":            "persistence:\n  enabled: true\n",
		"templates/storage.yaml": "{{ if .Values.persistence.enabled }}\nkind: PersistentVolumeClaim\napiVersion: v1\nmetadata: {name: redis}\n{{ end }}\n",
	} {
		if err := os.WriteFile(filepath.Join(chart, path), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	p, _ := NewPublisher(PublisherConfig{RepoURL: "http://localhost/git.git"})
	c := domain.ComponentSpec{Name: "redis", Stateful: true, ReleaseValidationDataPolicy: "disposable", Template: &domain.AppTemplateRef{Name: "redis"}}
	app := &domain.App{Name: "app", Spec: domain.AppSpec{CD: domain.CDConfig{ReleaseValidationEnvironment: "validation", ReleaseValidationMode: "ephemeral"}}}
	env := AppPublishEnv{EnvName: "validation", Namespace: "app-validation"}
	values := filepath.Join(repo, "values.yaml")
	if err := os.WriteFile(values, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := p.checkDisposableValidation(context.Background(), repo, app, env, c, values); err == nil {
		t.Fatal("chart's default PVC was not rejected")
	}
	if err := os.WriteFile(values, []byte("persistence:\n  enabled: false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := p.checkDisposableValidation(context.Background(), repo, app, env, c, values); err != nil {
		t.Fatal(err)
	}
}

func TestDisposableIdlePreflightNeverCreatesWorkloadInputs(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is needed for preflight")
	}
	root := t.TempDir()
	chart := filepath.Join(root, "charts", "cache", "latest")
	if err := os.MkdirAll(filepath.Join(chart, "templates"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"Chart.yaml": "apiVersion: v2\nname: cache\nversion: 1.0.0\n", "values.yaml": "{}", "templates/deployment.yaml": "kind: Deployment\napiVersion: apps/v1\nmetadata: {name: cache}\n"} {
		if err := os.WriteFile(filepath.Join(chart, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	p, _ := NewPublisher(PublisherConfig{RepoURL: "http://localhost/git.git"})
	app := &domain.App{Name: "app", ProjectName: "demo", Spec: domain.AppSpec{CD: domain.CDConfig{ReleaseValidationEnvironment: "validation", ReleaseValidationMode: "ephemeral"}, Components: []domain.ComponentSpec{
		{Name: "web", Template: &domain.AppTemplateRef{Name: "cache"}},
		{Name: "redis", Stateful: true, ReleaseValidationDataPolicy: "disposable", Template: &domain.AppTemplateRef{Name: "cache"}},
	}}}
	env := AppPublishEnv{EnvName: "validation", Bound: true, Namespace: "app-validation"}
	if err := p.publishComposedAppFiles(context.Background(), root, app, []AppPublishEnv{env}); err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{"envs", composedAppsDir} {
		if _, err := os.Stat(filepath.Join(root, base, "validation", "demo", "app", "_targets")); !os.IsNotExist(err) {
			t.Fatalf("idle preflight published %s workloads: %v", base, err)
		}
	}
}
