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

// cdManagedApp returns an app whose image tag lives at the root "image.tag"
// key (as in the voiceai-livekit chart), seeded to seedTag, with external-CD
// (Kargo) ownership enabled.
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
			CD: domain.CDConfig{Managed: managed, ImageTagPath: "image.tag"},
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
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, BaseDomain: "localhost"},
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: true, BaseDomain: "localhost"},
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
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, BaseDomain: "localhost"},
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
		{EnvName: "pr-42", EnvType: domain.AppEnvPreview, Order: 0, Bound: true, BaseDomain: "localhost"},
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
