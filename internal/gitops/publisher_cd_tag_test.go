package gitops_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/gitops"
	"gopkg.in/yaml.v3"
)

const (
	seedTag  = "3c52e146"
	kargoTag = "9275cbe0"
)

// rootImageMapping is the template image mapping for a chart whose tag lives at
// the root "image.tag" key (as in the voiceai-livekit chart). The publish
// adapter normally derives this from the template; tests pass it on the env.
var rootImageMapping = []gitops.KargoImage{{
	Repository: "registry.example.com/demo/hello",
	TagKey:     "image.tag",
}}

// cdManagedApp returns an app whose image tag lives at the root "image.tag"
// key, seeded to seedTag, with external-CD (Kargo) ownership toggled by managed.
func cdManagedApp(managed bool) *domain.App {
	return &domain.App{
		Name:        "hello",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "web-service"},
			RawValues: map[string]any{
				"image": map[string]any{
					"repository": "registry.example.com/demo/hello",
					"tag":        seedTag,
				},
			},
			CD: domain.CDConfig{Managed: managed},
		},
	}
}

// readRootImageTag parses a published values.yaml and returns image.tag.
func readRootImageTag(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	img, _ := m["image"].(map[string]any)
	tag, _ := img["tag"].(string)
	return tag
}

// simulateKargoCommit rewrites an already-published values.yaml as Kargo would:
// it swaps the seed tag for the promoted tag in place.
func simulateKargoCommit(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	out := strings.ReplaceAll(string(data), seedTag, kargoTag)
	if out == string(data) {
		t.Fatalf("expected seed tag %q to be present in %s before Kargo commit", seedTag, path)
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func valuesPath(dir, env string) string {
	return filepath.Join(dir, "envs", env, "demo", "hello", "values.yaml")
}

// previewValuesPath mirrors appEnvDir's preview layout, which omits the "envs/"
// prefix used by stable environments.
func previewValuesPath(dir, env string) string {
	return filepath.Join(dir, env, "demo", "hello", "values.yaml")
}

// TestPublish_CDManaged_PreservesTagOnRepublish is the core clobber-fix test:
// once Kargo has committed a tag, a republish must keep it rather than revert
// to the create-time seed.
func TestPublish_CDManaged_PreservesTagOnRepublish(t *testing.T) {
	dir := t.TempDir()
	app := cdManagedApp(true)
	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, BaseDomain: "localhost", TemplateImages: rootImageMapping},
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: true, BaseDomain: "localhost", TemplateImages: rootImageMapping},
	}
	p := newTestPublisher(t)

	// First publish writes the seed.
	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if got := readRootImageTag(t, valuesPath(dir, "staging")); got != seedTag {
		t.Fatalf("after first publish staging image.tag = %q, want seed %q", got, seedTag)
	}

	// Kargo promotes a new build into both stable envs.
	simulateKargoCommit(t, valuesPath(dir, "staging"))
	simulateKargoCommit(t, valuesPath(dir, "prod"))

	// A republish (e.g. an unrelated values save) must NOT clobber Kargo's tag.
	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("republish: %v", err)
	}
	for _, env := range []string{"staging", "prod"} {
		if got := readRootImageTag(t, valuesPath(dir, env)); got != kargoTag {
			t.Errorf("after republish %s image.tag = %q, want preserved Kargo tag %q", env, got, kargoTag)
		}
	}
}

// TestPublish_CDUnmanaged_ClobbersTagOnRepublish documents the legacy
// (platform-owned) behaviour: without CD.Managed, a republish overwrites the
// committed tag with the seed.
func TestPublish_CDUnmanaged_ClobbersTagOnRepublish(t *testing.T) {
	dir := t.TempDir()
	app := cdManagedApp(false) // CD ownership disabled
	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, BaseDomain: "localhost", TemplateImages: rootImageMapping},
	}
	p := newTestPublisher(t)

	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	simulateKargoCommit(t, valuesPath(dir, "staging"))
	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("republish: %v", err)
	}
	if got := readRootImageTag(t, valuesPath(dir, "staging")); got != seedTag {
		t.Errorf("unmanaged republish staging image.tag = %q, want seed %q (clobber expected)", got, seedTag)
	}
}

// TestPublish_CDManaged_PreviewNotPreserved verifies preview envs always take
// the rendered tag — Kargo never drives preview, so its tag must not stick.
func TestPublish_CDManaged_PreviewNotPreserved(t *testing.T) {
	dir := t.TempDir()
	app := cdManagedApp(true)
	envs := []gitops.AppPublishEnv{
		{EnvName: "pr-42", EnvType: domain.AppEnvPreview, Order: 0, Bound: true, BaseDomain: "localhost", TemplateImages: rootImageMapping},
	}
	p := newTestPublisher(t)

	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	simulateKargoCommit(t, previewValuesPath(dir, "pr-42"))
	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("republish: %v", err)
	}
	if got := readRootImageTag(t, previewValuesPath(dir, "pr-42")); got != seedTag {
		t.Errorf("preview image.tag = %q, want rendered seed %q (preview must not be preserved)", got, seedTag)
	}
}

// TestPublish_PinnedEnvWritesPinnedTag: a stable env pinned to a preview's image
// tag gets that tag in its values.yaml, overriding the create-time seed, and the
// pin holds across republish even though CD is unmanaged (pin wins over seed).
func TestPublish_PinnedEnvWritesPinnedTag(t *testing.T) {
	const pinnedTag = "pr-712-aae62d96"
	dir := t.TempDir()
	app := cdManagedApp(false) // unmanaged: seed would normally win
	app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{
		"staging": {PinnedImageTag: pinnedTag, PinnedFrom: "pr-712"},
	}
	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, BaseDomain: "localhost", TemplateImages: rootImageMapping},
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: true, BaseDomain: "localhost", TemplateImages: rootImageMapping},
	}
	p := newTestPublisher(t)
	for i := 0; i < 2; i++ { // publish twice — the pin must hold on republish
		if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
	if got := readRootImageTag(t, valuesPath(dir, "staging")); got != pinnedTag {
		t.Errorf("pinned staging image.tag = %q, want %q", got, pinnedTag)
	}
	// prod is not pinned → keeps the seed.
	if got := readRootImageTag(t, valuesPath(dir, "prod")); got != seedTag {
		t.Errorf("unpinned prod image.tag = %q, want seed %q", got, seedTag)
	}
}

// readRootMap parses a published values.yaml into its top-level map.
func readRootMap(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return m
}

// TestPublish_SuspendWritesSuspendKey: an env with Suspend=true gets the chart's
// suspend key set true in values.yaml; a non-suspended env omits the key so the
// chart default (running) applies — i.e. resume drops the flag.
func TestPublish_SuspendWritesSuspendKey(t *testing.T) {
	dir := t.TempDir()
	app := cdManagedApp(false)
	suspended := true
	app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{
		"staging": {Suspend: &suspended},
	}
	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, BaseDomain: "localhost", TemplateImages: rootImageMapping, SuspendKey: "suspend"},
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: true, BaseDomain: "localhost", TemplateImages: rootImageMapping, SuspendKey: "suspend"},
	}
	p := newTestPublisher(t)
	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if got, _ := readRootMap(t, valuesPath(dir, "staging"))["suspend"].(bool); !got {
		t.Errorf("suspended staging: suspend = %v, want true", got)
	}
	if _, present := readRootMap(t, valuesPath(dir, "prod"))["suspend"]; present {
		t.Errorf("non-suspended prod should not carry a suspend key")
	}
}

// tokenImageApp is a BYO/passthrough app whose RawValues pin the chart's image
// tag to the {platform.imageTag} token (the chart-agnostic, recommended way).
func tokenImageApp() *domain.App {
	return &domain.App{
		Name:        "hello",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "voiceai-livekit-agent"},
			RawValues: map[string]any{
				"image": map[string]any{
					"repository": "registry.example.com/demo/hello",
					"tag":        "{platform.imageTag}",
				},
			},
		},
	}
}

// TestPublishPreview_TokenImageTagResolvesToPR is the core fix: a per-PR image
// tag must be exposed as {platform.imageTag} so a passthrough chart's
// `image.tag: "{platform.imageTag}"` override renders the PR build — regardless
// of the template's image-mapping shape.
func TestPublishPreview_TokenImageTagResolvesToPR(t *testing.T) {
	dir := t.TempDir()
	p := newTestPublisher(t)
	const prTag = "pr-42-abc1234"

	spec := gitops.PreviewPublishSpec{
		PreviewName:       "pr-42",
		BaseEnv:           "staging",
		ClusterServer:     "https://kubernetes.default.svc",
		Namespace:         "demo-hello-preview-pr-42",
		BaseDomain:        "localhost",
		ImageTag:          prTag,
		SkipCanonicalBase: true, // BYO/passthrough
	}
	if err := p.PublishPreviewForTest(dir, tokenImageApp(), spec); err != nil {
		t.Fatalf("publish preview: %v", err)
	}
	path := filepath.Join(dir, "previews", "staging", "demo", "pr-42", "hello", "values.yaml")
	if got := readRootImageTag(t, path); got != prTag {
		t.Errorf("preview image.tag = %q, want %q ({platform.imageTag} must resolve to the per-PR tag)", got, prTag)
	}
}

// TestDeletePreview_RemovesPlatformResources guards the dangling
// previews-platform Application: delete must remove BOTH the preview chart tree
// and its _app-resources platform tree, else the previews-platform AppSet keeps
// generating the {project}-{name}-preview-platform Application.
func TestDeletePreview_RemovesPlatformResources(t *testing.T) {
	dir := t.TempDir()
	p := newTestPublisher(t)

	spec := gitops.PreviewPublishSpec{
		PreviewName:       "pr-42",
		BaseEnv:           "staging",
		ClusterServer:     "https://kubernetes.default.svc",
		Namespace:         "demo-hello-preview-pr-42",
		BaseDomain:        "localhost",
		SkipCanonicalBase: true,
	}
	if err := p.PublishPreviewForTest(dir, tokenImageApp(), spec); err != nil {
		t.Fatalf("publish preview: %v", err)
	}
	chartTree := filepath.Join(dir, "previews", "staging", "demo", "pr-42", "hello")
	platformTree := filepath.Join(dir, "_app-resources", "previews", "staging", "demo", "pr-42", "hello")
	for _, d := range []string{chartTree, platformTree} {
		if _, err := os.Stat(d); err != nil {
			t.Fatalf("expected %s to exist after publish: %v", d, err)
		}
	}

	removed, err := p.DeletePreviewForTest(dir, "demo", "pr-42", "hello", "staging")
	if err != nil {
		t.Fatalf("delete preview: %v", err)
	}
	if !removed {
		t.Error("DeletePreview reported nothing removed")
	}
	for _, d := range []string{chartTree, platformTree} {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("%s should be removed after delete (err=%v)", d, err)
		}
	}
}

// TestPublishPreview_MultipleAppsSamePreviewNoCollision guards the bug where all
// apps in one PR wrote to previews/{project}/{preview}/ and overwrote each other.
// Each app's preview must live under its own app-scoped subdirectory.
func TestPublishPreview_MultipleAppsSamePreviewNoCollision(t *testing.T) {
	dir := t.TempDir()
	p := newTestPublisher(t)

	spec := gitops.PreviewPublishSpec{
		PreviewName:       "pr-42",
		BaseEnv:           "staging",
		ClusterServer:     "https://kubernetes.default.svc",
		Namespace:         "demo-preview-pr-42",
		BaseDomain:        "localhost",
		SkipCanonicalBase: true,
	}
	for _, name := range []string{"web", "worker"} {
		app := &domain.App{
			Name: name, ProjectName: "demo",
			Spec: domain.AppSpec{Template: domain.AppTemplateRef{Name: "voiceai-livekit-agent"}},
		}
		if err := p.PublishPreviewForTest(dir, app, spec); err != nil {
			t.Fatalf("publish preview for %s: %v", name, err)
		}
	}

	// Both apps' files coexist under the base-env-scoped path — neither overwrote
	// the other.
	for _, name := range []string{"web", "worker"} {
		for _, f := range []string{"app.yaml", "values.yaml"} {
			path := filepath.Join(dir, "previews", "staging", "demo", "pr-42", name, f)
			if _, err := os.Stat(path); err != nil {
				t.Errorf("expected %s for app %s: %v", f, name, err)
			}
		}
	}

	// Deleting one app's preview leaves the other intact.
	if _, err := p.DeletePreviewForTest(dir, "demo", "pr-42", "web", "staging"); err != nil {
		t.Fatalf("delete web preview: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "previews", "staging", "demo", "pr-42", "web")); !os.IsNotExist(err) {
		t.Error("web preview dir should be removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "previews", "staging", "demo", "pr-42", "worker", "values.yaml")); err != nil {
		t.Errorf("worker preview must survive deletion of web: %v", err)
	}
}

// TestPublishPreview_TemplateDefaultsBelowAppBand verifies template-level default
// preview values apply to a preview but the app's own preview band wins, and the
// template default overrides the app's base-env value for previews.
func TestPublishPreview_TemplateDefaultsBelowAppBand(t *testing.T) {
	dir := t.TempDir()
	p := newTestPublisher(t)

	app := &domain.App{
		Name: "hello", ProjectName: "demo",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "voiceai-livekit-agent"},
			// App's base-env (staging) value — the template preview default should
			// override it for previews.
			EnvironmentDefaults: map[string]domain.EnvironmentOverride{
				"staging": {RawValues: map[string]any{"replicas": 5}},
				// App preview band: wins over the template default.
				domain.PreviewOverrideKey: {RawValues: map[string]any{"appWins": "yes"}},
			},
		},
	}
	spec := gitops.PreviewPublishSpec{
		PreviewName:       "pr-1",
		BaseEnv:           "staging",
		ClusterServer:     "https://kubernetes.default.svc",
		Namespace:         "demo-preview-pr-1",
		BaseDomain:        "localhost",
		SkipCanonicalBase: true,
		TemplatePreviewValues: map[string]any{
			"replicas":     1,     // overrides app base-env (5) for previews
			"templateOnly": "t",   // survives
			"appWins":      "no",  // app band overrides this
		},
	}
	if err := p.PublishPreviewForTest(dir, app, spec); err != nil {
		t.Fatalf("publish preview: %v", err)
	}
	m := readPreviewValues(t, filepath.Join(dir, "previews", "staging", "demo", "pr-1", "hello", "values.yaml"))
	if m["replicas"] != 1 {
		t.Errorf("replicas = %v, want 1 (template preview default over app base-env)", m["replicas"])
	}
	if m["templateOnly"] != "t" {
		t.Errorf("templateOnly = %v, want t (template default survives)", m["templateOnly"])
	}
	if m["appWins"] != "yes" {
		t.Errorf("appWins = %v, want yes (app preview band wins over template default)", m["appWins"])
	}
}

// readPreviewValues parses a preview's published values.yaml into a map.
func readPreviewValues(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return m
}

// platformNameApp wires its env CM/Secret and fullname to platform tokens — the
// pattern used for a shared preview namespace.
func platformNameApp() *domain.App {
	return &domain.App{
		Name:        "hello",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "voiceai-livekit-agent"},
			RawValues: map[string]any{
				"envConfigMapName": "{platform.configMapName}",
				"envSecretName":    "{platform.secretName}",
				"fullnameOverride": "{platform.app}-{platform.previewName}",
			},
		},
	}
}

func TestPublishPreview_SharedNamespaceSuffixesPlatformNames(t *testing.T) {
	dir := t.TempDir()
	p := newTestPublisher(t)
	spec := gitops.PreviewPublishSpec{
		PreviewName:       "pr-42",
		BaseEnv:           "staging",
		ClusterServer:     "https://kubernetes.default.svc",
		Namespace:         "demo-previews", // shared: no preview name in it
		BaseDomain:        "localhost",
		SkipCanonicalBase: true,
	}
	if err := p.PublishPreviewForTest(dir, platformNameApp(), spec); err != nil {
		t.Fatalf("publish preview: %v", err)
	}
	m := readPreviewValues(t, filepath.Join(dir, "previews", "staging", "demo", "pr-42", "hello", "values.yaml"))
	for key, want := range map[string]string{
		"envConfigMapName": "hello-pr-42-config",
		"envSecretName":    "hello-pr-42-secrets",
		"fullnameOverride": "hello-pr-42",
	} {
		if got, _ := m[key].(string); got != want {
			t.Errorf("%s = %q, want %q (suffixed per preview in a shared namespace)", key, got, want)
		}
	}
}

func TestPublishPreview_PerPreviewNamespaceNoSuffix(t *testing.T) {
	dir := t.TempDir()
	p := newTestPublisher(t)
	spec := gitops.PreviewPublishSpec{
		PreviewName:       "pr-42",
		BaseEnv:           "staging",
		ClusterServer:     "https://kubernetes.default.svc",
		Namespace:         "demo-hello-preview-pr-42", // carries the preview name
		BaseDomain:        "localhost",
		SkipCanonicalBase: true,
	}
	if err := p.PublishPreviewForTest(dir, platformNameApp(), spec); err != nil {
		t.Fatalf("publish preview: %v", err)
	}
	m := readPreviewValues(t, filepath.Join(dir, "previews", "staging", "demo", "pr-42", "hello", "values.yaml"))
	if got, _ := m["envConfigMapName"].(string); got != "hello-config" {
		t.Errorf("envConfigMapName = %q, want hello-config (no suffix when namespace is per-preview)", got)
	}
}

// TestPublishPreview_InheritsBaseEnvOverlay is the core fix: a preview must
// inherit the base env's template/org platform value overrides and app values —
// not just the preview band — so e.g. envConfigMapName resolves to the platform
// ConfigMap instead of the chart default. The preview band layers on top.
func TestPublishPreview_InheritsBaseEnvOverlay(t *testing.T) {
	dir := t.TempDir()
	p := newTestPublisher(t)
	app := &domain.App{
		Name:        "hello",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Template:  domain.AppTemplateRef{Name: "voiceai-livekit-agent"},
			RawValues: map[string]any{"appKey": "appVal"},
			EnvironmentDefaults: map[string]domain.EnvironmentOverride{
				domain.PreviewOverrideKey: {RawValues: map[string]any{
					"previewKey":   "previewVal",
					"fromTemplate": "overridden-by-band", // band wins over template default
				}},
			},
		},
	}
	spec := gitops.PreviewPublishSpec{
		PreviewName:       "pr-42",
		BaseEnv:           "staging",
		ClusterServer:     "https://kubernetes.default.svc",
		Namespace:         "demo-hello-preview-pr-42",
		BaseDomain:        "localhost",
		SkipCanonicalBase: true,
		// The base env's resolved template/org platform overrides — the layer the
		// preview was dropping.
		PlatformDefaultValues: map[string]any{
			"envConfigMapName": "{platform.configMapName}",
			"fromTemplate":     "yes",
		},
		PlatformEnvValues: map[string]any{"fromStagingTemplate": "yes"},
	}
	if err := p.PublishPreviewForTest(dir, app, spec); err != nil {
		t.Fatalf("publish preview: %v", err)
	}
	m := readPreviewValues(t, filepath.Join(dir, "previews", "staging", "demo", "pr-42", "hello", "values.yaml"))
	checks := map[string]string{
		"envConfigMapName":    "hello-config",       // inherited template override, token resolved
		"fromTemplate":        "overridden-by-band", // preview band wins over the template default
		"fromStagingTemplate": "yes",                // PlatformEnvValues inherited
		"appKey":              "appVal",             // app RawValues inherited
		"previewKey":          "previewVal",         // preview band applied
	}
	for k, want := range checks {
		if got, _ := m[k].(string); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}
