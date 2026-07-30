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

// TestWriteComposedAppTree_FanOut asserts a composed app deploying to MULTIPLE
// clusters in one env fans out: each cluster gets its own per-component values
// tree under _clusters/<cluster>/… (with that cluster's routing host and platform
// overlay) and its own rendered Application manifest under _targets/<cluster>/,
// mirroring the single-source fan-out. No shared components/ tree is written.
func TestWriteComposedAppTree_FanOut(t *testing.T) {
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

	// Two clusters, each with its own base domain, so the per-cluster routing host
	// must differ. A cluster-scoped platform overlay on "api" proves the right
	// cluster's overlay lands in the right file.
	envs := []gitops.AppPublishEnv{{
		EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true,
		Namespace: "bigly-staging", BaseDomain: "example.com",
		Clusters: []gitops.ClusterTarget{
			{Name: "c1", Server: "https://c1", BaseDomain: "c1.example.com"},
			{Name: "c2", Server: "https://c2", BaseDomain: "c2.example.com"},
		},
		ComponentPlatformValues: map[string]gitops.ComponentPlatformValues{
			"api": {Cluster: map[string]map[string]any{
				"c1": {"components": map[string]any{"web": map[string]any{"replicaCount": 3}}},
			}},
		},
	}}

	if err := p.WriteComposedAppTreeForTest(context.Background(), dir, app, envs); err != nil {
		t.Fatalf("WriteComposedAppTreeForTest: %v", err)
	}

	// No shared (non-fan-out) component values tree.
	shared := filepath.Join(dir, "envs", "staging", "demo", "bigly", "components")
	if _, err := os.Stat(shared); !os.IsNotExist(err) {
		t.Errorf("shared components/ tree must not exist in fan-out mode: %v", err)
	}

	type wantHost struct{ cluster, host string }
	for _, w := range []wantHost{
		{"c1", "bigly-api.staging.c1.example.com"},
		{"c2", "bigly-api.staging.c2.example.com"},
	} {
		vpath := filepath.Join(dir, "envs", "staging", "_clusters", w.cluster, "demo", "bigly", "components", "api", "values.yaml")
		raw, err := os.ReadFile(vpath)
		if err != nil {
			t.Fatalf("read %s api values.yaml: %v", w.cluster, err)
		}
		var v struct {
			Routing    struct{ Host string } `yaml:"routing"`
			Components map[string]struct {
				ReplicaCount int `yaml:"replicaCount"`
			} `yaml:"components"`
		}
		if err := yaml.Unmarshal(raw, &v); err != nil {
			t.Fatalf("unmarshal %s api values.yaml: %v", w.cluster, err)
		}
		if v.Routing.Host != w.host {
			t.Errorf("%s: routing.host = %q, want %q", w.cluster, v.Routing.Host, w.host)
		}
		// The cluster-scoped platform overlay (replicaCount:3) lands ONLY on c1.
		got := v.Components["web"].ReplicaCount
		wantReplicas := 0
		if w.cluster == "c1" {
			wantReplicas = 3
		}
		if got != wantReplicas {
			t.Errorf("%s: web.replicaCount = %d, want %d (cluster-scoped overlay)", w.cluster, got, wantReplicas)
		}

		// Worker component values also fan out per cluster.
		wp := filepath.Join(dir, "envs", "staging", "_clusters", w.cluster, "demo", "bigly", "components", "worker", "values.yaml")
		if _, err := os.Stat(wp); err != nil {
			t.Errorf("%s: worker values.yaml missing: %v", w.cluster, err)
		}

		// Per-cluster Application manifest, referencing this cluster's values tree.
		mpath := filepath.Join(dir, "_composed-apps", "staging", "demo", "bigly", "_targets", w.cluster, "application.yaml")
		mraw, err := os.ReadFile(mpath)
		if err != nil {
			t.Fatalf("read %s application.yaml: %v", w.cluster, err)
		}
		if !strings.Contains(string(mraw), "_clusters/"+w.cluster+"/demo/bigly/components/") {
			t.Errorf("%s: manifest must reference its own _clusters/%s values tree\n%s", w.cluster, w.cluster, mraw)
		}
		other := "c1"
		if w.cluster == "c1" {
			other = "c2"
		}
		if strings.Contains(string(mraw), "_clusters/"+other+"/") {
			t.Errorf("%s: manifest must not reference the other cluster %s\n%s", w.cluster, other, mraw)
		}
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

// TestComposedPerComponentConfigProjection verifies per-component env scoping: a
// component that opts out of app vars (InheritAppVars=false) points its
// platform.configMapName at a curated <app>-<component>-config (built from its
// EnvVars, selecting/renaming an app-config key), gets no app secrets
// (platform.secretName ""), while an inheriting sibling keeps <app>-config/secrets.
func TestComposedPerComponentConfigProjection(t *testing.T) {
	dir := t.TempDir()
	no := false
	app := &domain.App{
		Name:        "bigly",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "web-service"},
			Components: []domain.ComponentSpec{
				{Name: "api", Type: domain.ComponentWeb, Enabled: true,
					Template: &domain.AppTemplateRef{Name: "web-service"}},
				{Name: "db", Type: domain.ComponentWorker, Enabled: true,
					Template:       &domain.AppTemplateRef{Name: "web-service"},
					InheritAppVars: &no,
					EnvVars: []domain.ComponentEnvVar{
						{Name: "DB_URL", FromConfig: "DATABASE_URL"},
						{Name: "POOL", Value: "10"},
					}},
			},
		},
	}
	p, err := gitops.NewPublisher(gitops.PublisherConfig{
		RepoURL:        "https://git/repo.git",
		TemplateLoader: keyedTemplateLoader{"web-service": "web"},
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	envs := []gitops.AppPublishEnv{{
		EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true,
		Namespace: "bigly-staging", BaseDomain: "localhost",
		Clusters: []gitops.ClusterTarget{{Name: "c1", Server: "https://c1"}},
		EnvVars:  map[string]string{"DATABASE_URL": "postgres://x", "OTHER": "y"},
	}}
	if err := p.WriteComposedAppTreeForTest(context.Background(), dir, app, envs); err != nil {
		t.Fatalf("WriteComposedAppTreeForTest: %v", err)
	}

	readPlatform := func(comp string) (cm, sec string) {
		raw, err := os.ReadFile(filepath.Join(dir, "envs", "staging", "demo", "bigly", "components", comp, "values.yaml"))
		if err != nil {
			t.Fatalf("read %s values.yaml: %v", comp, err)
		}
		var v struct {
			Platform struct {
				ConfigMapName string `yaml:"configMapName"`
				SecretName    string `yaml:"secretName"`
			} `yaml:"platform"`
		}
		if err := yaml.Unmarshal(raw, &v); err != nil {
			t.Fatalf("unmarshal %s: %v", comp, err)
		}
		return v.Platform.ConfigMapName, v.Platform.SecretName
	}

	// Inheriting component → app-wide objects.
	if cm, sec := readPlatform("api"); cm != "bigly-config" || sec != "bigly-secrets" {
		t.Errorf("api platform = %q/%q, want bigly-config/bigly-secrets", cm, sec)
	}
	// Opt-out component → its projection ConfigMap, no app secrets.
	if cm, sec := readPlatform("db"); cm != "bigly-db-config" || sec != "" {
		t.Errorf("db platform = %q/%q, want bigly-db-config/\"\"", cm, sec)
	}
	// The projection ConfigMap has the curated, resolved keys.
	raw, err := os.ReadFile(filepath.Join(dir, "_app-resources", "staging", "demo", "bigly", "component-db-configmap.yaml"))
	if err != nil {
		t.Fatalf("read component-db-configmap: %v", err)
	}
	var cm struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal(raw, &cm); err != nil {
		t.Fatalf("unmarshal db configmap: %v", err)
	}
	if cm.Data["DB_URL"] != "postgres://x" {
		t.Errorf("db configmap DB_URL = %q, want postgres://x (renamed from DATABASE_URL)", cm.Data["DB_URL"])
	}
	if cm.Data["POOL"] != "10" {
		t.Errorf("db configmap POOL = %q, want 10 (literal)", cm.Data["POOL"])
	}
	if _, leaked := cm.Data["OTHER"]; leaked {
		t.Error("db configmap must not include unselected app var OTHER")
	}
}

// TestComposedPerComponentSecretProjection verifies the secret subset/rename: an
// opt-out component that selects app secret keys (FromSecret) points its
// platform.secretName at a curated <app>-<component>-secrets ExternalSecret whose
// data[] renames each selected key, resolved to the item that holds it.
func TestComposedPerComponentSecretProjection(t *testing.T) {
	dir := t.TempDir()
	no := false
	app := &domain.App{
		Name:        "bigly",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "web-service"},
			Components: []domain.ComponentSpec{
				{Name: "api", Type: domain.ComponentWeb, Enabled: true,
					Template: &domain.AppTemplateRef{Name: "web-service"}},
				{Name: "db", Type: domain.ComponentWorker, Enabled: true,
					Template:       &domain.AppTemplateRef{Name: "web-service"},
					InheritAppVars: &no,
					EnvVars: []domain.ComponentEnvVar{
						{Name: "DB_PASS", FromSecret: "DATABASE_PASSWORD"},
					}},
			},
		},
	}
	p, err := gitops.NewPublisher(gitops.PublisherConfig{
		RepoURL:        "https://git/repo.git",
		TemplateLoader: keyedTemplateLoader{"web-service": "web"},
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	envs := []gitops.AppPublishEnv{{
		EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true,
		Namespace: "bigly-staging", BaseDomain: "localhost",
		Clusters:        []gitops.ClusterTarget{{Name: "c1", Server: "https://c1"}},
		ScopeKeys:       gitops.ScopePresence{GlobalApp: true},
		ScopeSecretKeys: gitops.ScopeSecretKeys{GlobalApp: []string{"DATABASE_PASSWORD", "OTHER_SECRET"}},
	}}
	if err := p.WriteComposedAppTreeForTest(context.Background(), dir, app, envs); err != nil {
		t.Fatalf("WriteComposedAppTreeForTest: %v", err)
	}

	readSecretName := func(comp string) string {
		raw, err := os.ReadFile(filepath.Join(dir, "envs", "staging", "demo", "bigly", "components", comp, "values.yaml"))
		if err != nil {
			t.Fatalf("read %s values.yaml: %v", comp, err)
		}
		var v struct {
			Platform struct {
				SecretName string `yaml:"secretName"`
			} `yaml:"platform"`
		}
		if err := yaml.Unmarshal(raw, &v); err != nil {
			t.Fatalf("unmarshal %s: %v", comp, err)
		}
		return v.Platform.SecretName
	}

	if sec := readSecretName("api"); sec != "bigly-secrets" {
		t.Errorf("api platform.secretName = %q, want bigly-secrets", sec)
	}
	if sec := readSecretName("db"); sec != "bigly-db-secrets" {
		t.Errorf("db platform.secretName = %q, want bigly-db-secrets", sec)
	}

	esRaw, err := os.ReadFile(filepath.Join(dir, "_app-resources", "staging", "demo", "bigly", "component-db-externalsecret.yaml"))
	if err != nil {
		t.Fatalf("read component-db-externalsecret: %v", err)
	}
	es := string(esRaw)
	if !strings.Contains(es, "name: bigly-db-secrets") {
		t.Errorf("ExternalSecret name mismatch:\n%s", es)
	}
	if !strings.Contains(es, "  data:") {
		t.Errorf("expected data[] projection, got:\n%s", es)
	}
	if !strings.Contains(es, `secretKey: "DB_PASS"`) || !strings.Contains(es, `property: "DATABASE_PASSWORD"`) {
		t.Errorf("expected DB_PASS←DATABASE_PASSWORD rename, got:\n%s", es)
	}
	if !strings.Contains(es, `key: "demo-bigly-global"`) {
		t.Errorf("expected resolution to the app-global item, got:\n%s", es)
	}
	if strings.Contains(es, "OTHER_SECRET") {
		t.Errorf("unselected key OTHER_SECRET must not appear, got:\n%s", es)
	}
}

// TestSingleSourceOptOut verifies per-component env scoping on the single-source
// path: a 1-component app that opts out of app vars points platform.configMapName
// at its curated projection and gets no app secrets.
func TestSingleSourceOptOut(t *testing.T) {
	dir := t.TempDir()
	no := false
	app := &domain.App{
		Name:        "solo",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "web-service"},
			Components: []domain.ComponentSpec{
				{Name: "web", Type: domain.ComponentWeb, Enabled: true,
					Template:       &domain.AppTemplateRef{Name: "web-service"},
					InheritAppVars: &no,
					EnvVars: []domain.ComponentEnvVar{
						{Name: "DB_URL", FromConfig: "DATABASE_URL"},
					}},
			},
		},
	}
	envs := []gitops.AppPublishEnv{{
		EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, BaseDomain: "localhost",
		Clusters: []gitops.ClusterTarget{{Name: "c1", Server: "https://c1"}},
		EnvVars:  map[string]string{"DATABASE_URL": "postgres://x", "OTHER": "y"},
	}}
	p := newTestPublisher(t)
	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishAppFilesForTest: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "envs", "staging", "demo", "solo", "values.yaml"))
	if err != nil {
		t.Fatalf("read values.yaml: %v", err)
	}
	var v struct {
		Platform struct {
			ConfigMapName string `yaml:"configMapName"`
			SecretName    string `yaml:"secretName"`
		} `yaml:"platform"`
	}
	if err := yaml.Unmarshal(raw, &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v.Platform.ConfigMapName != "solo-web-config" || v.Platform.SecretName != "" {
		t.Errorf("platform = %q/%q, want solo-web-config/\"\"", v.Platform.ConfigMapName, v.Platform.SecretName)
	}
	cmRaw, err := os.ReadFile(filepath.Join(dir, "_app-resources", "staging", "demo", "solo", "component-web-configmap.yaml"))
	if err != nil {
		t.Fatalf("read component config: %v", err)
	}
	var cm struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal(cmRaw, &cm); err != nil {
		t.Fatalf("unmarshal cm: %v", err)
	}
	if cm.Data["DB_URL"] != "postgres://x" {
		t.Errorf("DB_URL = %q, want postgres://x", cm.Data["DB_URL"])
	}
	if _, leaked := cm.Data["OTHER"]; leaked {
		t.Error("must not include unselected app var OTHER")
	}
}

// canonicalityLoader returns a template whose values mode is keyed by name:
// true = canonical (suparship-common), false = BYO/passthrough. The component key
// is the template name itself.
type canonicalityLoader map[string]bool

func (l canonicalityLoader) LoadTemplate(_ context.Context, name string) (*tpl.Template, error) {
	canonical, ok := l[name]
	if !ok {
		return nil, nil
	}
	spec := tpl.TemplateSpec{Components: []tpl.TemplateComponent{{Name: name}}}
	if !canonical {
		no := false
		spec.InjectCanonicalValues = &no
	}
	return &tpl.Template{Spec: spec}, nil
}

// TestComposedPassthroughComponentOmitsCanonicalSchema is the core BYO guarantee:
// a composed component whose template is passthrough (InjectCanonicalValues:false)
// gets ONLY its own overlay in values.yaml — with ((platform.*)) tokens resolved —
// and NONE of the assumed canonical schema (app/components/routing/platform/
// containerPort/envLayers/suparship). A canonical sibling still gets the full doc.
func TestComposedPassthroughComponentOmitsCanonicalSchema(t *testing.T) {
	dir := t.TempDir()
	app := &domain.App{
		Name: "bigly", ProjectName: "demo",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "web-service"},
			Components: []domain.ComponentSpec{
				{Name: "api", Type: domain.ComponentWeb, Enabled: true,
					Template: &domain.AppTemplateRef{Name: "web-service"}},
				{Name: "byo", Type: domain.ComponentWeb, Enabled: true,
					Template: &domain.AppTemplateRef{Name: "byo-chart"},
					Values: map[string]any{
						"image":  map[string]any{"repository": "nginx", "tag": "latest"},
						"envCfg": "((platform.configMapName))",
					}},
			},
		},
	}
	p, err := gitops.NewPublisher(gitops.PublisherConfig{
		RepoURL:        "https://git/repo.git",
		TemplateLoader: canonicalityLoader{"web-service": true, "byo-chart": false},
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

	read := func(comp string) string {
		raw, err := os.ReadFile(filepath.Join(dir, "envs", "staging", "demo", "bigly", "components", comp, "values.yaml"))
		if err != nil {
			t.Fatalf("read %s values.yaml: %v", comp, err)
		}
		return string(raw)
	}

	// BYO component: ONLY its overlay, with the platform token resolved. No
	// injected canonical/platform schema.
	byo := read("byo")
	var byoDoc map[string]any
	if err := yaml.Unmarshal([]byte(byo), &byoDoc); err != nil {
		t.Fatalf("unmarshal byo values: %v", err)
	}
	for _, k := range []string{"app", "components", "routing", "platform", "containerPort", "envLayers", "suparship"} {
		if _, present := byoDoc[k]; present {
			t.Errorf("BYO passthrough values must NOT contain %q, got:\n%s", k, byo)
		}
	}
	if byoDoc["envCfg"] != "bigly-config" {
		t.Errorf("expected ((platform.configMapName)) resolved to bigly-config, got %v", byoDoc["envCfg"])
	}
	if img, _ := byoDoc["image"].(map[string]any); img == nil || img["repository"] != "nginx" {
		t.Errorf("expected the overlay image to survive, got:\n%s", byo)
	}

	// Canonical sibling still gets the full doc.
	api := read("api")
	if !strings.Contains(api, "components:") || !strings.Contains(api, "platform:") {
		t.Errorf("canonical component should keep the full schema, got:\n%s", api)
	}
}

// TestComposedPerEnvComponentValues verifies a component's base overlay is merged
// with the per-env override (EnvironmentDefaults[env].ComponentValues[name]) — the
// env override wins on key collisions and base keys survive.
func TestComposedPerEnvComponentValues(t *testing.T) {
	dir := t.TempDir()
	app := &domain.App{
		Name: "bigly", ProjectName: "demo",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "byo-chart"},
			Components: []domain.ComponentSpec{
				{Name: "web", Type: domain.ComponentWeb, Enabled: true,
					Template: &domain.AppTemplateRef{Name: "byo-chart"},
					Values: map[string]any{
						"replicaCount": 2,
						"image":        map[string]any{"repository": "nginx", "tag": "latest"},
					}},
			},
			// Override on the published (first) env so the merge is observable.
			EnvironmentDefaults: map[string]domain.EnvironmentOverride{
				"staging": {ComponentValues: map[string]map[string]any{
					"web": {"replicaCount": 5},
				}},
			},
		},
	}
	p, err := gitops.NewPublisher(gitops.PublisherConfig{
		RepoURL:        "https://git/repo.git",
		TemplateLoader: canonicalityLoader{"byo-chart": false},
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true,
			Namespace: "bigly-staging", BaseDomain: "localhost",
			Clusters: []gitops.ClusterTarget{{Name: "c1", Server: "https://c1"}}},
	}
	if err := p.WriteComposedAppTreeForTest(context.Background(), dir, app, envs); err != nil {
		t.Fatalf("WriteComposedAppTreeForTest: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "envs", "staging", "demo", "bigly", "components", "web", "values.yaml"))
	if err != nil {
		t.Fatalf("read web values: %v", err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["replicaCount"] != 5 {
		t.Errorf("replicaCount = %v, want 5 (per-env override wins over base 2)", m["replicaCount"])
	}
	// Base keys not overridden survive.
	img, _ := m["image"].(map[string]any)
	if img == nil || img["repository"] != "nginx" {
		t.Errorf("base image must survive the merge, got %v", m["image"])
	}
}

// TestComposedComponentIncludesPlatformOverrides verifies a composed component's
// rendered values include its PE-authored template overlays (env.ComponentPlatform
// Values: Default + Env[env] + Cluster[activeCluster]) layered BENEATH the
// component's own Values (which win) and a per-env override.
func TestComposedComponentIncludesPlatformOverrides(t *testing.T) {
	dir := t.TempDir()
	p, err := gitops.NewPublisher(gitops.PublisherConfig{
		RepoURL:        "https://git/repo.git",
		TemplateLoader: canonicalityLoader{"byo": false}, // passthrough → values.yaml IS the overlay
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	app := &domain.App{
		Name: "bigly", ProjectName: "demo",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "byo"},
			Components: []domain.ComponentSpec{
				// api overrides replicaCount → its own value wins over the platform layers.
				{Name: "api", Type: domain.ComponentWeb, Enabled: true,
					Template: &domain.AppTemplateRef{Name: "byo"},
					Values:   map[string]any{"replicaCount": 5}},
				// worker has no override → the platform Env[staging] wins over Default.
				{Name: "worker", Type: domain.ComponentWorker, Enabled: true,
					Template: &domain.AppTemplateRef{Name: "byo"}},
			},
		},
	}
	// Platform overlays (as the server adapter would thread them from the template +
	// org TemplateOverride) — same for both components here.
	pv := gitops.ComponentPlatformValues{
		Default: map[string]any{
			"envConfigMapName": "((platform.configMapName))",
			"replicaCount":     1,
		},
		Env:     map[string]any{"replicaCount": 3},
		Cluster: map[string]map[string]any{"c1": {"nodeSelector": map[string]any{"zone": "scus"}}},
	}
	envs := []gitops.AppPublishEnv{{
		EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true,
		Namespace: "bigly-staging", BaseDomain: "localhost",
		Clusters: []gitops.ClusterTarget{{Name: "c1", Server: "https://c1"}},
		ComponentPlatformValues: map[string]gitops.ComponentPlatformValues{
			"api": pv, "worker": pv,
		},
	}}
	if err := p.WriteComposedAppTreeForTest(context.Background(), dir, app, envs); err != nil {
		t.Fatalf("WriteComposedAppTreeForTest: %v", err)
	}
	read := func(comp string) map[string]any {
		raw, err := os.ReadFile(filepath.Join(dir, "envs", "staging", "demo", "bigly", "components", comp, "values.yaml"))
		if err != nil {
			t.Fatalf("read %s values: %v", comp, err)
		}
		var m map[string]any
		if err := yaml.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal %s: %v", comp, err)
		}
		return m
	}

	api := read("api")
	// Platform Default reaches the component (token resolved by the passthrough marshal).
	if api["envConfigMapName"] == nil {
		t.Errorf("api must include the platform override envConfigMapName, got %v", api)
	}
	// Per-cluster overlay for the active cluster is applied.
	if ns, _ := api["nodeSelector"].(map[string]any); ns == nil || ns["zone"] != "scus" {
		t.Errorf("api must include the per-cluster overlay, got %v", api["nodeSelector"])
	}
	// The component's own Values win over Default+Env.
	if api["replicaCount"] != 5 {
		t.Errorf("api replicaCount = %v, want 5 (component Values wins over platform layers)", api["replicaCount"])
	}
	worker := read("worker")
	if worker["replicaCount"] != 3 {
		t.Errorf("worker replicaCount = %v, want 3 (platform Env wins over Default when no component override)", worker["replicaCount"])
	}
}

// TestStatefulComponentSeparateApplication verifies the addon/stateful primitive:
// a stateful component is EXCLUDED from the composed multi-source Application and
// rendered as its own prune-disabled Application (BuildComponentApplication).
func TestStatefulComponentSeparateApplication(t *testing.T) {
	app := &domain.App{
		Name: "bigly", ProjectName: "demo",
		Spec: domain.AppSpec{
			Components: []domain.ComponentSpec{
				{Name: "web", Type: domain.ComponentWeb, Enabled: true,
					Template: &domain.AppTemplateRef{Name: "web-service", Version: "1.0.0"}},
				{Name: "db", Type: domain.ComponentWorker, Enabled: true, Stateful: true,
					Template: &domain.AppTemplateRef{Name: "cnpg", Version: "0.1.0"}},
			},
		},
	}
	opts := gitops.ComposedBuildOptions{
		RepoURL: "https://git/repo.git", AppName: "demo-bigly-staging",
		EnvName: "staging", ClusterName: "c1", ClusterServer: "https://c1",
		Namespace: "bigly-staging", SyncAutomated: true,
		ComponentValues: map[string]string{
			"web": "envs/staging/demo/bigly/components/web/values.yaml",
			"db":  "envs/staging/demo/bigly/components/db/values.yaml",
		},
	}

	// Composed app: only the non-stateful web source (+ appvalues ref), Prune:true.
	composed := gitops.BuildComposedApplication(app, opts)
	if got := len(composed.Spec.Sources); got != 2 {
		t.Fatalf("composed Sources = %d, want 2 (appvalues + web only)", got)
	}
	for _, s := range composed.Spec.Sources {
		if s.Helm != nil && s.Helm.ReleaseName == "bigly-db" {
			t.Error("stateful db must NOT be a source in the composed Application")
		}
	}
	if composed.Spec.SyncPolicy.Automated == nil || !composed.Spec.SyncPolicy.Automated.Prune {
		t.Error("composed Application should keep Prune:true")
	}

	// Stateful db: its own Application, Prune:false, no Kargo annotation.
	dbApp := gitops.BuildComponentApplication(app, app.Spec.StatefulComponents()[0], opts)
	if dbApp.Metadata.Name != "demo-bigly-staging-db" {
		t.Errorf("db Application name = %q, want demo-bigly-staging-db", dbApp.Metadata.Name)
	}
	if len(dbApp.Spec.Sources) != 2 {
		t.Fatalf("db Application Sources = %d, want 2 (appvalues + chart)", len(dbApp.Spec.Sources))
	}
	if dbApp.Spec.SyncPolicy.Automated == nil || dbApp.Spec.SyncPolicy.Automated.Prune {
		t.Error("stateful db Application must have Prune:false")
	}
	if _, ok := dbApp.Metadata.Annotations["kargo.akuity.io/authorized-stage"]; ok {
		t.Error("stateful db Application must carry no Kargo annotation")
	}
}

// TestComposedPublishesStatefulComponentManifest is the integration counterpart:
// publishing a web+stateful-db app writes the composed application.yaml (web only)
// and a separate db-application.yaml (Prune:false) under the same _targets dir.
func TestComposedPublishesStatefulComponentManifest(t *testing.T) {
	dir := t.TempDir()
	p, err := gitops.NewPublisher(gitops.PublisherConfig{
		RepoURL:        "https://git/repo.git",
		ArgoCDRepoURL:  "https://git/repo.git",
		SyncAutomated:  true,
		TemplateLoader: canonicalityLoader{"byo-chart": false},
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	app := &domain.App{
		Name: "bigly", ProjectName: "demo",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "byo-chart"},
			Components: []domain.ComponentSpec{
				{Name: "web", Type: domain.ComponentWeb, Enabled: true,
					Template: &domain.AppTemplateRef{Name: "byo-chart"}},
				{Name: "db", Type: domain.ComponentWorker, Enabled: true, Stateful: true,
					Template: &domain.AppTemplateRef{Name: "byo-chart"}},
			},
		},
	}
	envs := []gitops.AppPublishEnv{{
		EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true,
		Namespace: "bigly-staging", BaseDomain: "localhost",
		Clusters: []gitops.ClusterTarget{{Name: "c1", Server: "https://c1"}},
	}}
	if err := p.WriteAppTreeForTest(context.Background(), dir, app, envs); err != nil {
		t.Fatalf("WriteAppTreeForTest: %v", err)
	}
	tdir := filepath.Join(dir, "_composed-apps", "staging", "demo", "bigly", "_targets", "c1")
	main, err := os.ReadFile(filepath.Join(tdir, "application.yaml"))
	if err != nil {
		t.Fatalf("read composed application.yaml: %v", err)
	}
	if strings.Contains(string(main), "bigly-db") {
		t.Error("composed application.yaml must not reference the stateful db release")
	}
	dbManifest, err := os.ReadFile(filepath.Join(tdir, "db-application.yaml"))
	if err != nil {
		t.Fatalf("read db-application.yaml: %v", err)
	}
	if !strings.Contains(string(dbManifest), "prune: false") {
		t.Errorf("db Application must have prune: false, got:\n%s", dbManifest)
	}
	// The db still gets its per-component values file.
	if _, err := os.Stat(filepath.Join(dir, "envs", "staging", "demo", "bigly", "components", "db", "values.yaml")); err != nil {
		t.Errorf("stateful db should still get its component values: %v", err)
	}
}

// TestComposedWarehouseSubscribesAllComponentImages verifies the Phase-1 image
// foundation: each composed component's declared image binding is collected and a
// single app Warehouse subscribes to every component's repository.
func TestComposedWarehouseSubscribesAllComponentImages(t *testing.T) {
	app := &domain.App{
		Name: "bigly", ProjectName: "demo",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "web"},
			Components: []domain.ComponentSpec{
				{Name: "web-1", Type: domain.ComponentWeb, Enabled: true,
					Template: &domain.AppTemplateRef{Name: "web"},
					Images:   []domain.ComponentImage{{Repository: "ghcr.io/bigly/web", TagKey: "image.tag"}}},
				{Name: "web-2", Type: domain.ComponentWeb, Enabled: true,
					Template: &domain.AppTemplateRef{Name: "web"},
					Images:   []domain.ComponentImage{{Repository: "ghcr.io/bigly/web2", TagKey: "image.tag"}}},
			},
		},
	}
	// resolved=nil → the explicit-repository legacy fallback path emits each image.
	imgs := gitops.CollectComponentImagesForTest(app, nil)
	if len(imgs) != 2 {
		t.Fatalf("collected %d images, want 2: %+v", len(imgs), imgs)
	}
	// Each image carries its owning component (for per-component promotion later).
	if imgs[0].Name != "web-1" || imgs[1].Name != "web-2" {
		t.Errorf("images must carry the owning component name, got %q/%q", imgs[0].Name, imgs[1].Name)
	}

	wh := gitops.BuildKargoWarehouse(app, gitops.KargoBuildOptions{
		Images:         imgs,
		KargoNamespace: "kargo-demo",
	})
	if len(wh.Spec.Subscriptions) != 2 {
		t.Fatalf("warehouse has %d subscriptions, want 2", len(wh.Spec.Subscriptions))
	}
	got := map[string]bool{}
	for _, s := range wh.Spec.Subscriptions {
		if s.Image != nil {
			got[s.Image.RepoURL] = true
		}
	}
	for _, repo := range []string{"ghcr.io/bigly/web", "ghcr.io/bigly/web2"} {
		if !got[repo] {
			t.Errorf("warehouse must subscribe to %q, got %v", repo, got)
		}
	}
}

// TestCollectComponentImages_DiscoveredAndFallback verifies the two resolution
// paths: a component whose selection was resolved from discovery (repository
// derived, in the env map) is emitted as-is; a component with an explicit legacy
// repository that discovery did NOT resolve falls back to watching that repo
// directly (with default tag pattern/strategy).
func TestCollectComponentImages_DiscoveredAndFallback(t *testing.T) {
	app := &domain.App{
		Name: "bigly", ProjectName: "demo",
		Spec: domain.AppSpec{
			Components: []domain.ComponentSpec{
				{Name: "api", Type: domain.ComponentWeb, Enabled: true,
					Template: &domain.AppTemplateRef{Name: "web"},
					Images:   []domain.ComponentImage{{TagKey: "components.web.image.tag"}}},
				{Name: "legacy", Type: domain.ComponentWeb, Enabled: true,
					Template: &domain.AppTemplateRef{Name: "web"},
					Images:   []domain.ComponentImage{{Repository: "ghcr.io/bigly/legacy", TagKey: "image.tag"}}},
			},
		},
	}
	// Only "api" was resolved from discovery; "legacy" has no resolved entry.
	resolved := map[string][]gitops.KargoImage{
		"api": {{Name: "api", Repository: "acr.io/api", TagKey: "components.web.image.tag",
			TagPattern: "^[0-9a-f]{7}$", SelectionStrategy: "NewestBuild"}},
	}
	got := gitops.CollectComponentImagesForTest(app, resolved)
	if len(got) != 2 {
		t.Fatalf("got %d images, want 2: %+v", len(got), got)
	}
	if got[0].Name != "api" || got[0].Repository != "acr.io/api" {
		t.Errorf("discovered image = %+v, want api/acr.io/api", got[0])
	}
	// Fallback: explicit legacy repo, defaults applied.
	if got[1].Name != "legacy" || got[1].Repository != "ghcr.io/bigly/legacy" {
		t.Errorf("fallback image = %+v, want legacy/ghcr.io/bigly/legacy", got[1])
	}
	if got[1].TagPattern != gitops.DefaultImageTagPattern || got[1].SelectionStrategy != gitops.DefaultImageSelectionStrategy {
		t.Errorf("fallback image defaults = %q/%q, want %q/%q", got[1].TagPattern, got[1].SelectionStrategy, gitops.DefaultImageTagPattern, gitops.DefaultImageSelectionStrategy)
	}
}

// TestComposedPublishesKargoCRsAndAnnotation verifies Phase-2 wiring: a composed
// pipeline app publishes a Kargo Warehouse + per-env Stage, and its rendered
// Application carries the authorized-stage annotation so the Stage may sync it.
func TestComposedPublishesKargoCRsAndAnnotation(t *testing.T) {
	dir := t.TempDir()
	p, err := gitops.NewPublisher(gitops.PublisherConfig{
		RepoURL:        "https://git/repo.git",
		ArgoCDRepoURL:  "https://git/repo.git",
		TemplateLoader: canonicalityLoader{"byo-chart": false},
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	app := &domain.App{
		Name: "bigly", ProjectName: "demo",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "byo-chart"},
			Components: []domain.ComponentSpec{
				{Name: "web-1", Type: domain.ComponentWeb, Enabled: true,
					Template: &domain.AppTemplateRef{Name: "byo-chart"},
					Images:   []domain.ComponentImage{{Repository: "ghcr.io/bigly/web", TagKey: "image.tag"}}},
				{Name: "web-2", Type: domain.ComponentWeb, Enabled: true,
					Template: &domain.AppTemplateRef{Name: "byo-chart"},
					Images:   []domain.ComponentImage{{Repository: "ghcr.io/bigly/web2", TagKey: "image.tag"}}},
			},
		},
	}
	envs := []gitops.AppPublishEnv{{
		EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true,
		Namespace: "bigly-staging", BaseDomain: "localhost",
		Clusters: []gitops.ClusterTarget{{Name: "c1", Server: "https://c1"}},
	}}
	if err := p.WriteAppTreeForTest(context.Background(), dir, app, envs); err != nil {
		t.Fatalf("WriteAppTreeForTest: %v", err)
	}

	kargoDir := filepath.Join(dir, "_infra", "kargo")
	if m, _ := filepath.Glob(filepath.Join(kargoDir, "*bigly-warehouse.yaml")); len(m) != 1 {
		t.Errorf("expected a composed Kargo Warehouse, got %v", m)
	}
	if m, _ := filepath.Glob(filepath.Join(kargoDir, "*bigly-staging-stage.yaml")); len(m) != 1 {
		t.Errorf("expected a staging Kargo Stage, got %v", m)
	}

	appManifest := filepath.Join(dir, "_composed-apps", "staging", "demo", "bigly", "_targets", "c1", "application.yaml")
	raw, err := os.ReadFile(appManifest)
	if err != nil {
		t.Fatalf("read composed application: %v", err)
	}
	if !strings.Contains(string(raw), "kargo.akuity.io/authorized-stage") {
		t.Errorf("composed Application must carry the authorized-stage annotation, got:\n%s", raw)
	}
	// The annotation MUST be the project-qualified stage reference
	// "<kargo-project>:<stage>"; an unqualified stage name makes Kargo reject the
	// argocd-update with "…is not authorized".
	wantStage := "kargo-demo:bigly-staging"
	if !strings.Contains(string(raw), wantStage) {
		t.Errorf("composed Application authorized-stage must be project-qualified %q, got:\n%s", wantStage, raw)
	}
}

// TestComposedTagPreservedOnRepublish verifies per-component CD-tag preservation:
// with CD.Managed, a Kargo-committed tag in a component's values.yaml survives a
// republish instead of being reset to the overlay seed.
func TestComposedTagPreservedOnRepublish(t *testing.T) {
	dir := t.TempDir()
	p, err := gitops.NewPublisher(gitops.PublisherConfig{
		RepoURL:        "https://git/repo.git",
		ArgoCDRepoURL:  "https://git/repo.git",
		TemplateLoader: canonicalityLoader{"byo-chart": false},
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	app := &domain.App{
		Name: "bigly", ProjectName: "demo",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "byo-chart"},
			CD:       domain.CDConfig{Managed: true},
			Components: []domain.ComponentSpec{
				{Name: "web", Type: domain.ComponentWeb, Enabled: true,
					Template: &domain.AppTemplateRef{Name: "byo-chart"},
					Values:   map[string]any{"image": map[string]any{"tag": "seed"}},
					Images:   []domain.ComponentImage{{Repository: "ghcr.io/bigly/web", TagKey: "image.tag"}}},
			},
		},
	}
	envs := []gitops.AppPublishEnv{{
		EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true,
		Namespace: "bigly-staging", BaseDomain: "localhost",
		Clusters: []gitops.ClusterTarget{{Name: "c1", Server: "https://c1"}},
	}}
	vpath := filepath.Join(dir, "envs", "staging", "demo", "bigly", "components", "web", "values.yaml")

	if err := p.WriteComposedAppTreeForTest(context.Background(), dir, app, envs); err != nil {
		t.Fatalf("initial publish: %v", err)
	}
	readTag := func() string {
		raw, err := os.ReadFile(vpath)
		if err != nil {
			t.Fatalf("read values: %v", err)
		}
		var m map[string]any
		if err := yaml.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		img, _ := m["image"].(map[string]any)
		if img == nil {
			return ""
		}
		s, _ := img["tag"].(string)
		return s
	}
	if got := readTag(); got != "seed" {
		t.Fatalf("initial image.tag = %q, want seed", got)
	}

	// Simulate a Kargo promotion committing a new tag into the values file.
	if err := os.WriteFile(vpath, []byte("image:\n  tag: promoted-abc123\n"), 0o644); err != nil {
		t.Fatalf("simulate promote: %v", err)
	}

	// Republish — the promoted tag must survive (not reset to the "seed" overlay).
	if err := p.WriteComposedAppTreeForTest(context.Background(), dir, app, envs); err != nil {
		t.Fatalf("republish: %v", err)
	}
	if got := readTag(); got != "promoted-abc123" {
		t.Errorf("republish reset the tag to %q, want promoted-abc123 preserved", got)
	}
}

// TestComposedPromoteMaterializesEnv verifies the composed promote path writes a
// target env's component values + rendered Application (so a higher env exists).
func TestComposedPromoteMaterializesEnv(t *testing.T) {
	dir := t.TempDir()
	p, err := gitops.NewPublisher(gitops.PublisherConfig{
		RepoURL:        "https://git/repo.git",
		ArgoCDRepoURL:  "https://git/repo.git",
		TemplateLoader: canonicalityLoader{"byo-chart": false},
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	app := &domain.App{
		Name: "bigly", ProjectName: "demo",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "byo-chart"},
			Components: []domain.ComponentSpec{
				{Name: "web", Type: domain.ComponentWeb, Enabled: true,
					Template: &domain.AppTemplateRef{Name: "byo-chart"},
					Images:   []domain.ComponentImage{{Repository: "ghcr.io/bigly/web", TagKey: "image.tag"}}},
			},
		},
	}
	// Promote to prod (a non-base env that the initial pipeline publish skipped).
	prod := gitops.AppPublishEnv{
		EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: true,
		Namespace: "bigly-prod", BaseDomain: "localhost",
		Clusters: []gitops.ClusterTarget{{Name: "c2", Server: "https://c2"}},
	}
	if err := p.PublishComposedAppEnvForTest(context.Background(), dir, app, prod); err != nil {
		t.Fatalf("publish composed env: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "envs", "prod", "demo", "bigly", "components", "web", "values.yaml")); err != nil {
		t.Errorf("expected prod component values: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "_composed-apps", "prod", "demo", "bigly", "_targets", "c2", "application.yaml")); err != nil {
		t.Errorf("expected prod composed Application: %v", err)
	}
}

// TestComposedPromotionTemplatePerComponentFiles verifies the composed promotion
// template writes each component's promoted tag into its OWN
// components/<name>/values.yaml — one yaml-update step per component.
func TestComposedPromotionTemplatePerComponentFiles(t *testing.T) {
	app := &domain.App{Name: "bigly", ProjectName: "demo"}
	env := domain.AppEnvironment{AppName: "bigly", ProjectName: "demo", EnvName: "staging", EnvType: domain.AppEnvStaging}
	stage := gitops.BuildKargoStage(app, env, nil, gitops.KargoBuildOptions{
		Composed:      true,
		GitOpsRepoURL: "https://git/repo.git",
		Images: []gitops.KargoImage{
			{Name: "web-1", Repository: "ghcr.io/bigly/web", TagKey: "image.tag"},
			{Name: "web-2", Repository: "ghcr.io/bigly/web2", TagKey: "image.tag"},
		},
	})
	if stage.Spec.PromotionTemplate == nil {
		t.Fatal("expected a promotion template")
	}
	// Collect yaml-update step paths.
	paths := map[string]bool{}
	for _, step := range stage.Spec.PromotionTemplate.Spec.Steps {
		if step.Uses != "yaml-update" {
			continue
		}
		if p, ok := step.Config["path"].(string); ok {
			paths[p] = true
		}
	}
	// Each per-component tag value must be quote()-wrapped so a numeric-looking
	// tag is written as a string, not an int/float (akuity/kargo #3743).
	for _, step := range stage.Spec.PromotionTemplate.Spec.Steps {
		if step.Uses != "yaml-update" {
			continue
		}
		ups, _ := step.Config["updates"].([]map[string]any)
		for _, u := range ups {
			got, _ := u["value"].(string)
			if got != `${{ quote(imageFrom("ghcr.io/bigly/web").Tag) }}` &&
				got != `${{ quote(imageFrom("ghcr.io/bigly/web2").Tag) }}` {
				t.Errorf("composed yaml-update value not quote()-wrapped: %v", got)
			}
		}
	}
	want := []string{
		"./src/envs/staging/demo/bigly/components/web-1/values.yaml",
		"./src/envs/staging/demo/bigly/components/web-2/values.yaml",
	}
	if len(paths) != len(want) {
		t.Fatalf("expected %d per-component yaml-update steps, got %d: %v", len(want), len(paths), paths)
	}
	for _, w := range want {
		if !paths[w] {
			t.Errorf("missing yaml-update for %s, got %v", w, paths)
		}
	}
	// Base stage pulls direct from the Warehouse.
	if !stage.Spec.RequestedFreight[0].Sources.Direct {
		t.Error("base stage should pull direct from the warehouse")
	}
}

// TestComposedPromotionTemplateFanOut verifies that when a composed app fans out
// to several clusters, the promotion writes each component's tag into EVERY
// cluster's per-cluster values file (_clusters/<cluster>/…), matching where the
// publisher wrote them — so a promoted tag reaches all target clusters.
func TestComposedPromotionTemplateFanOut(t *testing.T) {
	app := &domain.App{Name: "bigly", ProjectName: "demo"}
	env := domain.AppEnvironment{AppName: "bigly", ProjectName: "demo", EnvName: "staging", EnvType: domain.AppEnvStaging}
	stage := gitops.BuildKargoStage(app, env, nil, gitops.KargoBuildOptions{
		Composed:      true,
		GitOpsRepoURL: "https://git/repo.git",
		Clusters: []gitops.ClusterTarget{
			{Name: "c1", Server: "https://c1"},
			{Name: "c2", Server: "https://c2"},
		},
		Images: []gitops.KargoImage{
			{Name: "api", Repository: "ghcr.io/bigly/api", TagKey: "image.tag"},
			{Name: "worker", Repository: "ghcr.io/bigly/worker", TagKey: "image.tag"},
		},
	})
	if stage.Spec.PromotionTemplate == nil {
		t.Fatal("expected a promotion template")
	}
	paths := map[string]bool{}
	for _, step := range stage.Spec.PromotionTemplate.Spec.Steps {
		if step.Uses != "yaml-update" {
			continue
		}
		if p, ok := step.Config["path"].(string); ok {
			paths[p] = true
		}
	}
	// One yaml-update per (cluster, component) = 2 × 2 = 4 steps.
	want := []string{
		"./src/envs/staging/_clusters/c1/demo/bigly/components/api/values.yaml",
		"./src/envs/staging/_clusters/c1/demo/bigly/components/worker/values.yaml",
		"./src/envs/staging/_clusters/c2/demo/bigly/components/api/values.yaml",
		"./src/envs/staging/_clusters/c2/demo/bigly/components/worker/values.yaml",
	}
	if len(paths) != len(want) {
		t.Fatalf("expected %d per-cluster×component yaml-update steps, got %d: %v", len(want), len(paths), paths)
	}
	for _, w := range want {
		if !paths[w] {
			t.Errorf("missing yaml-update for %s, got %v", w, paths)
		}
	}
}

// TestSingleToComposedTransitionPrunesTree verifies edit-to-composed cleanup: an
// app first published single-source (one chart values.yaml/app.yaml) that gains a
// second component becomes composed on republish, and the stale single-source
// files are pruned so ArgoCD drops the orphaned single-chart Application.
func TestSingleToComposedTransitionPrunesTree(t *testing.T) {
	dir := t.TempDir()
	p, err := gitops.NewPublisher(gitops.PublisherConfig{
		RepoURL:        "https://git/repo.git",
		TemplateLoader: canonicalityLoader{"byo-chart": false},
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	envs := []gitops.AppPublishEnv{{
		EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true,
		Namespace: "bigly-staging", BaseDomain: "localhost",
		Clusters: []gitops.ClusterTarget{{Name: "c1", Server: "https://c1"}},
	}}

	// 1) Single-source app (one component → single-chart render).
	single := &domain.App{
		Name: "bigly", ProjectName: "demo",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "byo-chart", Version: "0.1.0"},
			Components: []domain.ComponentSpec{
				{Name: "web", Type: domain.ComponentWeb, Enabled: true,
					Template: &domain.AppTemplateRef{Name: "byo-chart", Version: "0.1.0"}},
			},
		},
	}
	if err := p.WriteAppTreeForTest(context.Background(), dir, single, envs); err != nil {
		t.Fatalf("publish single: %v", err)
	}
	valuesPath := filepath.Join(dir, "envs", "staging", "demo", "bigly", "values.yaml")
	if _, err := os.Stat(valuesPath); err != nil {
		t.Fatalf("expected single-source values.yaml after single publish: %v", err)
	}

	// 2) Add a second component → composed. Republish into the same dir.
	composed := &domain.App{
		Name: "bigly", ProjectName: "demo",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "byo-chart", Version: "0.1.0"},
			Components: []domain.ComponentSpec{
				{Name: "web", Type: domain.ComponentWeb, Enabled: true,
					Template: &domain.AppTemplateRef{Name: "byo-chart", Version: "0.1.0"}},
				{Name: "worker", Type: domain.ComponentWorker, Enabled: true,
					Template: &domain.AppTemplateRef{Name: "byo-chart", Version: "0.1.0"}},
			},
		},
	}
	if err := p.WriteAppTreeForTest(context.Background(), dir, composed, envs); err != nil {
		t.Fatalf("publish composed: %v", err)
	}
	// Stale single-source values.yaml/app.yaml must be gone.
	if _, err := os.Stat(valuesPath); !os.IsNotExist(err) {
		t.Errorf("stale single-source values.yaml must be pruned after becoming composed (err=%v)", err)
	}
	// Composed per-component values now exist.
	for _, comp := range []string{"web", "worker"} {
		cp := filepath.Join(dir, "envs", "staging", "demo", "bigly", "components", comp, "values.yaml")
		if _, err := os.Stat(cp); err != nil {
			t.Errorf("expected composed component values for %s: %v", comp, err)
		}
	}
}

// TestComposedProjectionPrunedOnRevert verifies that when a component reverts
// from opt-out (curated config + secret projections) back to inheriting the app
// vars, a republish removes the now-orphaned component-*-configmap.yaml /
// component-*-externalsecret.yaml so no stale ConfigMap/ExternalSecret lingers.
func TestComposedProjectionPrunedOnRevert(t *testing.T) {
	dir := t.TempDir()
	no := false
	yes := true
	mkApp := func(inherit bool) *domain.App {
		db := domain.ComponentSpec{
			Name: "db", Type: domain.ComponentWorker, Enabled: true,
			Template: &domain.AppTemplateRef{Name: "web-service"},
		}
		if inherit {
			db.InheritAppVars = &yes
		} else {
			db.InheritAppVars = &no
			db.EnvVars = []domain.ComponentEnvVar{
				{Name: "POOL", Value: "10"},
				{Name: "DB_PASS", FromSecret: "DATABASE_PASSWORD"},
			}
		}
		return &domain.App{
			Name: "bigly", ProjectName: "demo",
			Spec: domain.AppSpec{
				Template: domain.AppTemplateRef{Name: "web-service"},
				Components: []domain.ComponentSpec{
					{Name: "api", Type: domain.ComponentWeb, Enabled: true,
						Template: &domain.AppTemplateRef{Name: "web-service"}},
					db,
				},
			},
		}
	}
	envs := []gitops.AppPublishEnv{{
		EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true,
		Namespace: "bigly-staging", BaseDomain: "localhost",
		Clusters:        []gitops.ClusterTarget{{Name: "c1", Server: "https://c1"}},
		ScopeKeys:       gitops.ScopePresence{GlobalApp: true},
		ScopeSecretKeys: gitops.ScopeSecretKeys{GlobalApp: []string{"DATABASE_PASSWORD"}},
	}}
	p, err := gitops.NewPublisher(gitops.PublisherConfig{
		RepoURL:        "https://git/repo.git",
		TemplateLoader: keyedTemplateLoader{"web-service": "web"},
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}

	cm := filepath.Join(dir, "_app-resources", "staging", "demo", "bigly", "component-db-configmap.yaml")
	es := filepath.Join(dir, "_app-resources", "staging", "demo", "bigly", "component-db-externalsecret.yaml")

	// 1) Opt-out publish writes both projections.
	if err := p.WriteComposedAppTreeForTest(context.Background(), dir, mkApp(false), envs); err != nil {
		t.Fatalf("publish opt-out: %v", err)
	}
	for _, f := range []string{cm, es} {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("expected %s after opt-out publish: %v", filepath.Base(f), err)
		}
	}

	// 2) Republish with the component reverted to inherit — projections must go.
	if err := p.WriteComposedAppTreeForTest(context.Background(), dir, mkApp(true), envs); err != nil {
		t.Fatalf("publish inherit: %v", err)
	}
	for _, f := range []string{cm, es} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("expected %s pruned after revert to inherit (err=%v)", filepath.Base(f), err)
		}
	}
	// The app-wide env-configmap must survive the prune.
	if _, err := os.Stat(filepath.Join(dir, "_app-resources", "staging", "demo", "bigly", "env-configmap.yaml")); err != nil {
		t.Errorf("app-wide env-configmap should survive prune: %v", err)
	}
}

// TestPreviewOfOptOutAppIsAppLevel is the safety check for the deliberate
// "previews are app-level, not per-component" decision: a preview of a component
// that opts out of app vars in stable envs must still reference the APP-WIDE
// config/secret (which the preview publish writes), never a per-component
// projection object (which is not written in preview scope) — so the preview
// self-resolves and can't dangle on a missing ConfigMap/Secret.
func TestPreviewOfOptOutAppIsAppLevel(t *testing.T) {
	dir := t.TempDir()
	no := false
	app := &domain.App{
		Name: "hello", ProjectName: "demo",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "web-service"},
			Components: []domain.ComponentSpec{
				{Name: "web", Type: domain.ComponentWeb, Enabled: true,
					Template:       &domain.AppTemplateRef{Name: "web-service"},
					InheritAppVars: &no,
					EnvVars: []domain.ComponentEnvVar{
						{Name: "DB_URL", FromConfig: "DATABASE_URL"},
						{Name: "TOKEN", FromSecret: "API_TOKEN"},
					}},
			},
		},
	}
	p := newTestPublisher(t)
	spec := gitops.PreviewPublishSpec{
		PreviewName:   "pr-42",
		BaseEnv:       "staging",
		ClusterServer: "https://kubernetes.default.svc",
		Namespace:     "demo-hello-preview-pr-42", // contains the preview name → resBase stays "hello"
		BaseDomain:    "localhost",
		ScopeKeys:     gitops.ScopePresence{PreviewApp: true},
	}
	if err := p.PublishPreviewForTest(dir, app, spec); err != nil {
		t.Fatalf("publish preview: %v", err)
	}

	// values.yaml points at the app-wide objects, NOT hello-web-config/secrets.
	raw, err := os.ReadFile(filepath.Join(dir, "previews", "staging", "demo", "pr-42", "hello", "values.yaml"))
	if err != nil {
		t.Fatalf("read preview values.yaml: %v", err)
	}
	var v struct {
		Platform struct {
			ConfigMapName string `yaml:"configMapName"`
			SecretName    string `yaml:"secretName"`
		} `yaml:"platform"`
	}
	if err := yaml.Unmarshal(raw, &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v.Platform.ConfigMapName != "hello-config" || v.Platform.SecretName != "hello-secrets" {
		t.Errorf("preview platform = %q/%q, want hello-config/hello-secrets (app-level, not the per-component projection)",
			v.Platform.ConfigMapName, v.Platform.SecretName)
	}

	// The app-wide ConfigMap the preview references is actually written.
	if _, err := os.Stat(filepath.Join(dir, "_app-resources", "previews", "staging", "demo", "pr-42", "hello", "env-configmap.yaml")); err != nil {
		t.Errorf("app-wide preview env-configmap missing (preview would dangle): %v", err)
	}

	// No per-component projection files leak into either preview tree.
	for _, root := range []string{
		filepath.Join(dir, "previews", "staging", "demo", "pr-42", "hello"),
		filepath.Join(dir, "_app-resources", "previews", "staging", "demo", "pr-42", "hello"),
	} {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err == nil && info != nil && !info.IsDir() && strings.HasPrefix(info.Name(), "component-") {
				t.Errorf("unexpected per-component projection in preview: %s", path)
			}
			return nil
		})
	}
}

// TestSingleSourceProjectionPrunedOnRevert is the single-source counterpart of
// TestComposedProjectionPrunedOnRevert: a 1-component opt-out app writes its
// projections, and reverting to inherit prunes them on republish.
func TestSingleSourceProjectionPrunedOnRevert(t *testing.T) {
	dir := t.TempDir()
	no := false
	yes := true
	mkApp := func(inherit bool) *domain.App {
		web := domain.ComponentSpec{
			Name: "web", Type: domain.ComponentWeb, Enabled: true,
			Template: &domain.AppTemplateRef{Name: "web-service"},
		}
		if inherit {
			web.InheritAppVars = &yes
		} else {
			web.InheritAppVars = &no
			web.EnvVars = []domain.ComponentEnvVar{{Name: "TOKEN", FromSecret: "API_TOKEN"}}
		}
		return &domain.App{
			Name: "solo", ProjectName: "demo",
			Spec: domain.AppSpec{
				Template:   domain.AppTemplateRef{Name: "web-service"},
				Components: []domain.ComponentSpec{web},
			},
		}
	}
	envs := []gitops.AppPublishEnv{{
		EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, BaseDomain: "localhost",
		Clusters:        []gitops.ClusterTarget{{Name: "c1", Server: "https://c1"}},
		ScopeKeys:       gitops.ScopePresence{EnvApp: true},
		ScopeSecretKeys: gitops.ScopeSecretKeys{EnvApp: []string{"API_TOKEN"}},
	}}
	p := newTestPublisher(t)

	cm := filepath.Join(dir, "_app-resources", "staging", "demo", "solo", "component-web-configmap.yaml")
	es := filepath.Join(dir, "_app-resources", "staging", "demo", "solo", "component-web-externalsecret.yaml")

	if err := p.PublishAppFilesForTest(dir, mkApp(false), envs); err != nil {
		t.Fatalf("publish opt-out: %v", err)
	}
	for _, f := range []string{cm, es} {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("expected %s after opt-out publish: %v", filepath.Base(f), err)
		}
	}
	if err := p.PublishAppFilesForTest(dir, mkApp(true), envs); err != nil {
		t.Fatalf("publish inherit: %v", err)
	}
	for _, f := range []string{cm, es} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("expected %s pruned after revert (err=%v)", filepath.Base(f), err)
		}
	}
}

// TestSingleSourceSecretProjection verifies secret subset/rename on the
// single-source path: a 1-component opt-out app that selects app secret keys
// points platform.secretName at its curated <app>-<component>-secrets and writes
// the data[] ExternalSecret.
func TestSingleSourceSecretProjection(t *testing.T) {
	dir := t.TempDir()
	no := false
	app := &domain.App{
		Name:        "solo",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "web-service"},
			Components: []domain.ComponentSpec{
				{Name: "web", Type: domain.ComponentWeb, Enabled: true,
					Template:       &domain.AppTemplateRef{Name: "web-service"},
					InheritAppVars: &no,
					EnvVars: []domain.ComponentEnvVar{
						{Name: "TOKEN", FromSecret: "API_TOKEN"},
					}},
			},
		},
	}
	envs := []gitops.AppPublishEnv{{
		EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, BaseDomain: "localhost",
		Clusters:        []gitops.ClusterTarget{{Name: "c1", Server: "https://c1"}},
		ScopeKeys:       gitops.ScopePresence{EnvApp: true},
		ScopeSecretKeys: gitops.ScopeSecretKeys{EnvApp: []string{"API_TOKEN"}},
	}}
	p := newTestPublisher(t)
	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishAppFilesForTest: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "envs", "staging", "demo", "solo", "values.yaml"))
	if err != nil {
		t.Fatalf("read values.yaml: %v", err)
	}
	var v struct {
		Platform struct {
			SecretName string `yaml:"secretName"`
		} `yaml:"platform"`
	}
	if err := yaml.Unmarshal(raw, &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v.Platform.SecretName != "solo-web-secrets" {
		t.Errorf("platform.secretName = %q, want solo-web-secrets", v.Platform.SecretName)
	}
	esRaw, err := os.ReadFile(filepath.Join(dir, "_app-resources", "staging", "demo", "solo", "component-web-externalsecret.yaml"))
	if err != nil {
		t.Fatalf("read component externalsecret: %v", err)
	}
	es := string(esRaw)
	if !strings.Contains(es, "  data:") || !strings.Contains(es, `secretKey: "TOKEN"`) || !strings.Contains(es, `property: "API_TOKEN"`) {
		t.Errorf("expected TOKEN←API_TOKEN data[] projection, got:\n%s", es)
	}
	// env-app item under project demo, app solo, env staging.
	if !strings.Contains(es, `key: "demo-solo-env-staging"`) {
		t.Errorf("expected resolution to the env-app item, got:\n%s", es)
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestComposedPreview_OnlyEnabledComponents verifies composed preview publishing:
// only components with preview enabled (web/worker by default; stateful + job/cron
// off) render into the preview — their per-component values + the multi-source
// Application manifest — while disabled components (a stateful DB, a migration job)
// are omitted entirely.
func TestComposedPreview_OnlyEnabledComponents(t *testing.T) {
	dir := t.TempDir()
	p, err := gitops.NewPublisher(gitops.PublisherConfig{
		RepoURL:       "https://git/repo.git",
		ArgoCDRepoURL: "https://git/repo.git",
		SyncAutomated: true,
		TemplateLoader: keyedTemplateLoader{
			"web-service": "web",
			"worker":      "worker",
			"valkey":      "valkey",
			"job":         "job",
		},
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}

	app := &domain.App{
		Name: "telephony", ProjectName: "voiceai",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "web-service"},
			Components: []domain.ComponentSpec{
				{Name: "backend", Type: domain.ComponentWeb, Enabled: true,
					Template: &domain.AppTemplateRef{Name: "web-service"}},
				{Name: "worker", Type: domain.ComponentType("worker"), Enabled: true,
					Template: &domain.AppTemplateRef{Name: "worker"}},
				{Name: "cache", Type: domain.ComponentWeb, Enabled: true, Stateful: true,
					Template: &domain.AppTemplateRef{Name: "valkey"}},
				{Name: "migration", Type: domain.ComponentJob, Enabled: true,
					Template: &domain.AppTemplateRef{Name: "job"}},
			},
		},
	}

	spec := gitops.PreviewPublishSpec{
		PreviewName:   "pr-42",
		BaseEnv:       "staging",
		ClusterServer: "https://kubernetes.default.svc",
		Namespace:     "voiceai-telephony-preview-pr-42",
		BaseDomain:    "localhost",
		ImageTag:      "abc1234",
		ScopeKeys:     gitops.ScopePresence{PreviewApp: true},
		// The worker component's template carries a preview-defaults overlay (set by
		// a platform engineer) that must land in the component's rendered preview.
		ComponentPlatformValues: map[string]gitops.ComponentPlatformValues{
			"worker": {Preview: map[string]any{"previewFlag": "on"}},
		},
	}
	if err := p.PublishPreviewForTest(dir, app, spec); err != nil {
		t.Fatalf("publish composed preview: %v", err)
	}

	compDir := filepath.Join(dir, "previews", "staging", "voiceai", "pr-42", "telephony", "components")
	// Enabled (web/worker) rendered.
	for _, c := range []string{"backend", "worker"} {
		if _, err := os.Stat(filepath.Join(compDir, c, "values.yaml")); err != nil {
			t.Errorf("enabled component %q must render preview values: %v", c, err)
		}
	}
	// The component template's preview-defaults overlay is applied to the worker.
	workerRaw, err := os.ReadFile(filepath.Join(compDir, "worker", "values.yaml"))
	if err != nil {
		t.Fatalf("read worker preview values: %v", err)
	}
	if !strings.Contains(string(workerRaw), "previewFlag:") {
		t.Errorf("worker preview values must include the template preview default (previewFlag):\n%s", workerRaw)
	}
	// Disabled-by-default (stateful cache, one-shot migration) omitted.
	for _, c := range []string{"cache", "migration"} {
		if _, err := os.Stat(filepath.Join(compDir, c, "values.yaml")); !os.IsNotExist(err) {
			t.Errorf("component %q must NOT render in preview (default off): err=%v", c, err)
		}
	}

	// The composed preview Application manifest exists with sources only for the
	// enabled components (appvalues ref + backend + worker = 3).
	manifestPath := filepath.Join(dir, "_composed-apps", "_previews", "staging", "voiceai", "pr-42", "telephony", "application.yaml")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read composed preview Application: %v", err)
	}
	var manifest gitops.Application
	if err := yaml.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("unmarshal Application: %v", err)
	}
	if len(manifest.Spec.Sources) != 3 {
		t.Errorf("preview Application Sources = %d, want 3 (appvalues + backend + worker)", len(manifest.Spec.Sources))
	}
	if manifest.Spec.Destination.Namespace != "voiceai-telephony-preview-pr-42" {
		t.Errorf("preview destination namespace = %q, want the preview ns", manifest.Spec.Destination.Namespace)
	}
	// No Kargo authorized-stage annotation (previews are pinned, not promoted).
	if _, ok := manifest.Metadata.Annotations["kargo.akuity.io/authorized-stage"]; ok {
		t.Error("preview Application must NOT carry a Kargo authorized-stage annotation")
	}

	// The static previews-composed root app is written for discovery.
	if _, err := os.Stat(filepath.Join(dir, "_infra", "previews-composed-appset.yaml")); err != nil {
		t.Errorf("previews-composed root app missing: %v", err)
	}
	// App-wide preview platform ConfigMap is written (shared by components).
	if _, err := os.Stat(filepath.Join(dir, "_app-resources", "previews", "staging", "voiceai", "pr-42", "telephony", "env-configmap.yaml")); err != nil {
		t.Errorf("app-wide preview env-configmap missing: %v", err)
	}
}
