package gitops_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	domainapp "github.com/suparcloud/suparship/internal/app"
	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/gitops"
	"github.com/suparcloud/suparship/internal/tpl"
)

// webServiceTemplate is a minimal helm template used as the app "primary" when
// creating a composed app through the real app.Create pipeline.
func webServiceTemplate() *tpl.Template {
	return &tpl.Template{
		APIVersion: tpl.CurrentAPIVersion,
		Kind:       tpl.TemplateKind,
		Metadata:   tpl.Metadata{Name: "web-service", Version: "1.0.0"},
		Spec: tpl.TemplateSpec{
			Title:  "Web Service",
			Engine: tpl.Engine{Type: tpl.EngineHelm},
		},
	}
}

// keyedTemplateLoader maps a template name to the canonical component key its
// chart reads, so composed rendering can project each component onto the key
// an off-the-shelf chart expects (web-service → "web", worker → "worker").
type keyedTemplateLoader map[string]string

func (l keyedTemplateLoader) LoadTemplate(_ context.Context, name string) (*tpl.Template, error) {
	key, ok := l[name]
	if !ok {
		return nil, nil
	}
	return &tpl.Template{
		Spec: tpl.TemplateSpec{
			Components: []tpl.TemplateComponent{{Name: key}},
		},
	}, nil
}

// composedApp is a two-component composed app: api (web-service) + worker
// (worker), each carrying its own Template — so AppSpec.IsComposed() is true.
func composedApp() *domain.App {
	return &domain.App{
		Name:        "bigly",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Components: []domain.ComponentSpec{
				{Name: "api", Type: domain.ComponentType("web"), Enabled: true,
					Template: &domain.AppTemplateRef{Name: "web-service"}},
				{Name: "worker", Type: domain.ComponentType("worker"), Enabled: true,
					Template: &domain.AppTemplateRef{Name: "worker"}},
			},
		},
	}
}

// TestBuildComposedApplication asserts the rendered multi-source Application:
// a values-ref source plus one chart source per component (name-sorted), each
// with its own release name and per-component values file, and NO bare `source:`.
func TestBuildComposedApplication(t *testing.T) {
	app := composedApp()
	manifest := gitops.BuildComposedApplication(app, gitops.ComposedBuildOptions{
		RepoURL:       "https://git/repo.git",
		AppName:       "demo-bigly-staging",
		EnvName:       "staging",
		ClusterName:   "c1",
		ClusterServer: "https://c1",
		Namespace:     "bigly-staging",
		SyncAutomated: true,
		ComponentValues: map[string]string{
			"api":    "envs/staging/demo/bigly/components/api/values.yaml",
			"worker": "envs/staging/demo/bigly/components/worker/values.yaml",
		},
	})

	if got := len(manifest.Spec.Sources); got != 3 {
		t.Fatalf("Sources len = %d, want 3 (values-ref + 2 charts)", got)
	}
	// Source 0: values ref — no path, Ref set.
	ref := manifest.Spec.Sources[0]
	if ref.Ref != "appvalues" || ref.Path != "" || ref.Chart != "" {
		t.Errorf("source[0] = %+v, want values-ref {Ref:appvalues, no path/chart}", ref)
	}
	// Sources 1..2: chart sources, name-sorted → api before worker.
	wantCharts := []struct {
		path, release, values string
	}{
		{"charts/web-service/latest", "bigly-api", "$appvalues/envs/staging/demo/bigly/components/api/values.yaml"},
		{"charts/worker/latest", "bigly-worker", "$appvalues/envs/staging/demo/bigly/components/worker/values.yaml"},
	}
	for i, wc := range wantCharts {
		src := manifest.Spec.Sources[i+1]
		if src.Path != wc.path {
			t.Errorf("chart source %d path = %q, want %q", i, src.Path, wc.path)
		}
		if src.Helm == nil {
			t.Fatalf("chart source %d has nil Helm", i)
		}
		if src.Helm.ReleaseName != wc.release {
			t.Errorf("chart source %d releaseName = %q, want %q", i, src.Helm.ReleaseName, wc.release)
		}
		if len(src.Helm.ValueFiles) != 1 || src.Helm.ValueFiles[0] != wc.values {
			t.Errorf("chart source %d valueFiles = %v, want [%q]", i, src.Helm.ValueFiles, wc.values)
		}
	}

	if manifest.Spec.Project != "demo" {
		t.Errorf("project = %q, want demo", manifest.Spec.Project)
	}
	if manifest.Spec.Destination.Namespace != "bigly-staging" {
		t.Errorf("dest namespace = %q, want bigly-staging", manifest.Spec.Destination.Namespace)
	}

	out, err := yaml.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), "source:") {
		t.Errorf("rendered Application must not carry a bare `source:` alongside `sources:`\n%s", out)
	}
}

// TestWriteComposedAppTree_RendersFiles exercises the publisher's composed path
// end-to-end (no git): per-component values files under the canonical key, the
// rendered Application manifest, and the per-env composed App-of-Apps.
func TestWriteComposedAppTree_RendersFiles(t *testing.T) {
	dir := t.TempDir()
	app := composedApp()

	p, err := gitops.NewPublisher(gitops.PublisherConfig{
		RepoURL:       "https://git/repo.git",
		SyncAutomated: true,
		TemplateLoader: keyedTemplateLoader{
			"web-service": "web",
			"worker":      "worker",
		},
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}

	envs := []gitops.AppPublishEnv{{
		EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true,
		Namespace: "bigly-staging", BaseDomain: "localhost",
		Clusters: []gitops.ClusterTarget{{Name: "c1", Server: "https://c1"}},
	}}

	if err := p.WriteComposedAppTreeForTest(context.Background(), dir, app, envs); err != nil {
		t.Fatalf("WriteComposedAppTreeForTest: %v", err)
	}

	// Per-component values: emitted under each chart's canonical key, with a
	// per-component fullname (app.name = {app}-{component}).
	cases := []struct {
		comp, key, fullname string
	}{
		{"api", "web", "bigly-api"},
		{"worker", "worker", "bigly-worker"},
	}
	for _, c := range cases {
		vpath := filepath.Join(dir, "envs", "staging", "demo", "bigly", "components", c.comp, "values.yaml")
		raw, err := os.ReadFile(vpath)
		if err != nil {
			t.Fatalf("read %s values.yaml: %v", c.comp, err)
		}
		var v struct {
			App        struct{ Name string } `yaml:"app"`
			Components map[string]any         `yaml:"components"`
		}
		if err := yaml.Unmarshal(raw, &v); err != nil {
			t.Fatalf("unmarshal %s values.yaml: %v", c.comp, err)
		}
		if v.App.Name != c.fullname {
			t.Errorf("%s: app.name = %q, want %q", c.comp, v.App.Name, c.fullname)
		}
		if _, ok := v.Components[c.key]; !ok {
			t.Errorf("%s: values.components missing canonical key %q, got keys %v", c.comp, c.key, keysOf(v.Components))
		}
	}

	// Rendered Application manifest for the (app, cluster) target.
	manifestPath := filepath.Join(dir, "_composed-apps", "staging", "demo", "bigly", "_targets", "c1", "application.yaml")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read application.yaml: %v", err)
	}
	if strings.Contains(string(raw), "\nsource:") {
		t.Errorf("application.yaml must not carry a bare `source:`\n%s", raw)
	}
	var manifest gitops.Application
	if err := yaml.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("unmarshal application.yaml: %v", err)
	}
	if len(manifest.Spec.Sources) != 3 {
		t.Errorf("rendered Application Sources = %d, want 3", len(manifest.Spec.Sources))
	}
	if manifest.Metadata.Name != "demo-bigly-staging-c1" && manifest.Metadata.Name != "demo-bigly-c1" {
		t.Logf("rendered Application name = %q", manifest.Metadata.Name)
	}

	// Per-env composed App-of-Apps.
	rootPath := filepath.Join(dir, "_infra", "staging-composed-appset.yaml")
	rootRaw, err := os.ReadFile(rootPath)
	if err != nil {
		t.Fatalf("read composed App-of-Apps: %v", err)
	}
	var root gitops.Application
	if err := yaml.Unmarshal(rootRaw, &root); err != nil {
		t.Fatalf("unmarshal composed App-of-Apps: %v", err)
	}
	if root.Spec.Source.Directory == nil || !root.Spec.Source.Directory.Recurse {
		t.Errorf("composed App-of-Apps must be a recursive directory source, got %+v", root.Spec.Source)
	}
	if !strings.HasSuffix(root.Spec.Source.Path, "_composed-apps/staging") {
		t.Errorf("composed App-of-Apps path = %q, want …/_composed-apps/staging", root.Spec.Source.Path)
	}
}

// componentImageValues builds a per-component Values overlay setting the image
// under the chart's canonical component key ("web").
func componentImageValues(repo, tag string) map[string]any {
	return map[string]any{
		"components": map[string]any{
			"web": map[string]any{
				"image": map[string]any{"repository": repo, "tag": tag},
			},
		},
	}
}

// TestCreateThenRender_ComposedPerComponentImage closes the loop: build a
// composed app through the real app.Create ingest pipeline (two web-service
// components with DIFFERENT images set via their Values overlay) and render it
// with the publisher. Each component's values.yaml must carry its OWN image under
// the chart's canonical key — proving per-component config flows create → render.
func TestCreateThenRender_ComposedPerComponentImage(t *testing.T) {
	res, err := domainapp.Create(domainapp.CreateRequest{
		ProjectName: "demo",
		AppName:     "bigly",
		Template:    webServiceTemplate(), // app "primary"
		ExplicitComponents: []domain.ComponentSpec{
			{Name: "api", Type: domain.ComponentWeb, Enabled: true,
				Template: &domain.AppTemplateRef{Name: "web-service", Version: "1.0.0"},
				Values:   componentImageValues("ghcr.io/acme/api", "v1")},
			{Name: "frontend", Type: domain.ComponentWeb, Enabled: true,
				Template: &domain.AppTemplateRef{Name: "web-service", Version: "1.0.0"},
				Values:   componentImageValues("ghcr.io/acme/frontend", "v2")},
		},
	})
	if err != nil {
		t.Fatalf("app.Create: %v", err)
	}
	if !res.App.Spec.IsComposed() {
		t.Fatal("created app is not composed")
	}

	dir := t.TempDir()
	p, err := gitops.NewPublisher(gitops.PublisherConfig{
		RepoURL:        "https://git/repo.git",
		SyncAutomated:  true,
		TemplateLoader: keyedTemplateLoader{"web-service": "web"},
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	envs := []gitops.AppPublishEnv{{
		EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true,
		Namespace: "bigly-staging", BaseDomain: "localhost",
		Clusters: []gitops.ClusterTarget{{Name: "c1", Server: "https://c1"}},
	}}
	if err := p.WriteComposedAppTreeForTest(context.Background(), dir, res.App, envs); err != nil {
		t.Fatalf("WriteComposedAppTreeForTest: %v", err)
	}

	// Each component projects onto the chart's canonical key "web" but keeps its
	// own image.
	for _, tc := range []struct{ comp, repo, tag string }{
		{"api", "ghcr.io/acme/api", "v1"},
		{"frontend", "ghcr.io/acme/frontend", "v2"},
	} {
		vpath := filepath.Join(dir, "envs", "staging", "demo", "bigly", "components", tc.comp, "values.yaml")
		raw, err := os.ReadFile(vpath)
		if err != nil {
			t.Fatalf("read %s values.yaml: %v", tc.comp, err)
		}
		var v struct {
			Components map[string]struct {
				Image struct{ Repository, Tag string } `yaml:"image"`
			} `yaml:"components"`
		}
		if err := yaml.Unmarshal(raw, &v); err != nil {
			t.Fatalf("unmarshal %s values.yaml: %v", tc.comp, err)
		}
		got := v.Components["web"].Image
		if got.Repository != tc.repo || got.Tag != tc.tag {
			t.Errorf("%s: components.web.image = %s:%s, want %s:%s", tc.comp, got.Repository, got.Tag, tc.repo, tc.tag)
		}
	}
}

// TestSingleSourceAppliesComponentValues verifies the unified model's single-
// source path: a 1-component app (not composed) still applies its component's
// Values overlay onto the rendered values.yaml.
func TestSingleSourceAppliesComponentValues(t *testing.T) {
	dir := t.TempDir()
	app := &domain.App{
		Name:        "hello",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "web-service"},
			Components: []domain.ComponentSpec{
				{Name: "web", Type: domain.ComponentWeb, Enabled: true,
					Template: &domain.AppTemplateRef{Name: "web-service"},
					Values: map[string]any{
						"components": map[string]any{
							"web": map[string]any{"image": map[string]any{"tag": "v9"}},
						},
					}},
			},
		},
	}
	if app.Spec.IsComposed() {
		t.Fatal("1-component app must not be composed")
	}
	envs := []gitops.AppPublishEnv{{
		EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, BaseDomain: "localhost",
		Clusters: []gitops.ClusterTarget{{Name: "c1", Server: "https://c1"}},
	}}
	p := newTestPublisher(t)
	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishAppFilesForTest: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "envs", "staging", "demo", "hello", "values.yaml"))
	if err != nil {
		t.Fatalf("read values.yaml: %v", err)
	}
	var v struct {
		Components map[string]struct {
			Image struct{ Tag string } `yaml:"image"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &v); err != nil {
		t.Fatalf("unmarshal values.yaml: %v", err)
	}
	if got := v.Components["web"].Image.Tag; got != "v9" {
		t.Errorf("components.web.image.tag = %q, want v9 (from the component Values overlay)", got)
	}
}

// TestSingleSourceRemapsComponentKey verifies a 1-component app whose component
// is named differently from the chart's canonical key still renders under the
// canonical key (web-service reads components.web) — so renaming the sole
// component never breaks single-source rendering.
func TestSingleSourceRemapsComponentKey(t *testing.T) {
	dir := t.TempDir()
	app := &domain.App{
		Name:        "hello",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "web-service"},
			Components: []domain.ComponentSpec{
				{Name: "api", Type: domain.ComponentWeb, Enabled: true, // renamed from "web"
					Template: &domain.AppTemplateRef{Name: "web-service"}},
			},
		},
	}
	if app.Spec.IsComposed() {
		t.Fatal("1-component app must not be composed")
	}
	p, err := gitops.NewPublisher(gitops.PublisherConfig{
		RepoURL:        "https://git/repo.git",
		TemplateLoader: keyedTemplateLoader{"web-service": "web"},
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	envs := []gitops.AppPublishEnv{{
		EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, BaseDomain: "localhost",
		Clusters: []gitops.ClusterTarget{{Name: "c1", Server: "https://c1"}},
	}}
	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishAppFilesForTest: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "envs", "staging", "demo", "hello", "values.yaml"))
	if err != nil {
		t.Fatalf("read values.yaml: %v", err)
	}
	var v struct {
		Components map[string]any `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := v.Components["web"]; !ok {
		t.Errorf("values.components missing canonical key %q; got %v", "web", keysOf(v.Components))
	}
	if _, ok := v.Components["api"]; ok {
		t.Error("values.components must not use the source component name 'api'")
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
