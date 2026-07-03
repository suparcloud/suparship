package gitops_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/gitops"
	"github.com/suparcloud/suparship/internal/tpl"
)

// fakeTemplateLoader returns a fixed template for any name. Lets tests
// drive the publisher's external-mode resolution without standing up
// the kube store + chartimport pipeline.
type fakeTemplateLoader struct {
	template *tpl.Template
}

func (l *fakeTemplateLoader) LoadTemplate(_ context.Context, _ string) (*tpl.Template, error) {
	return l.template, nil
}

// externalTemplate returns a minimal template whose engine.chart is a
// registry ref — what triggers the external-mode publish path.
func externalTemplate(t *testing.T) *tpl.Template {
	t.Helper()
	return &tpl.Template{
		APIVersion: "suparship.io/v1alpha1",
		Kind:       "Template",
		Metadata:   tpl.Metadata{Name: "web-service", Version: "1.2.0"},
		Spec: tpl.TemplateSpec{
			Title:    "Web Service",
			Category: "web",
			Engine: tpl.Engine{
				Type: tpl.EngineHelm,
				Chart: tpl.ChartLocator{
					Ref: &tpl.ChartRef{
						Repository: "oci://ghcr.io/suparcloud/charts",
						Name:       "web-service",
						Version:    "1.2.0",
					},
				},
			},
			Inputs: []tpl.Input{},
		},
	}
}

// TestPublishAppFiles_ExternalMode_RoutesToEnvsExternal verifies that when
// the template is registry-ref, the publisher writes app.yaml + values.yaml
// to envs-external/{env}/... rather than envs/{env}/... — so the
// BuildArgoExternalAppSet's Git File generator picks them up and the
// inline AppSet's glob doesn't.
func TestPublishAppFiles_ExternalMode_RoutesToEnvsExternal(t *testing.T) {
	dir := t.TempDir()
	app := &domain.App{
		Name:        "hello",
		ProjectName: "demo",
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "web-service"}},
	}
	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, Namespace: "demo-hello-staging"},
	}

	p, err := gitops.NewPublisher(gitops.PublisherConfig{
		RepoURL:        "http://localhost/fake-repo.git",
		TemplateLoader: &fakeTemplateLoader{template: externalTemplate(t)},
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}

	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishAppFilesForTest: %v", err)
	}

	// app.yaml lands under envs-external/staging/.../_targets/{cluster}/ (per
	// target cluster; in-cluster fallback here); values.yaml stays shared.
	externalDir := filepath.Join(dir, "envs-external", "staging", "demo", "hello")
	if _, err := os.Stat(filepath.Join(externalDir, "_targets", "in-cluster", "app.yaml")); err != nil {
		t.Errorf("expected app.yaml under envs-external _targets/in-cluster; got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(externalDir, "values.yaml")); err != nil {
		t.Errorf("expected values.yaml under envs-external; got: %v", err)
	}

	// And NOT under envs/staging/... — that path belongs to the inline
	// AppSet's glob and would double-deploy.
	inlineDir := filepath.Join(dir, "envs", "staging", "demo", "hello")
	if _, err := os.Stat(filepath.Join(inlineDir, "_targets", "in-cluster", "app.yaml")); !os.IsNotExist(err) {
		t.Errorf("inline envs/ path must be empty for external-mode apps; stat err = %v", err)
	}
}

// TestPublishAppFiles_ExternalMode_AppMetadataPopulated checks that the
// generated app.yaml carries the chart locator fields BuildArgoExternalAppSet
// substitutes into the Application source.
func TestPublishAppFiles_ExternalMode_AppMetadataPopulated(t *testing.T) {
	dir := t.TempDir()
	app := &domain.App{
		Name:        "hello",
		ProjectName: "demo",
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "web-service"}},
	}
	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, Namespace: "demo-hello-staging"},
	}

	p, err := gitops.NewPublisher(gitops.PublisherConfig{
		RepoURL:        "http://localhost/fake-repo.git",
		TemplateLoader: &fakeTemplateLoader{template: externalTemplate(t)},
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishAppFilesForTest: %v", err)
	}

	appYAMLBytes, err := os.ReadFile(filepath.Join(dir, "envs-external", "staging", "demo", "hello", "_targets", "in-cluster", "app.yaml"))
	if err != nil {
		t.Fatalf("read app.yaml: %v", err)
	}
	var meta gitops.AppMetadata
	if err := yaml.Unmarshal(appYAMLBytes, &meta); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if meta.ChartType != gitops.ChartTypeExternal {
		t.Errorf("ChartType = %q, want %q", meta.ChartType, gitops.ChartTypeExternal)
	}
	if meta.ChartRepoURL != "oci://ghcr.io/suparcloud/charts" {
		t.Errorf("ChartRepoURL = %q", meta.ChartRepoURL)
	}
	if meta.ChartName != "web-service" {
		t.Errorf("ChartName = %q", meta.ChartName)
	}
	if meta.ChartVersion != "1.2.0" {
		t.Errorf("ChartVersion = %q", meta.ChartVersion)
	}
}

// TestPublishAppFiles_InlineMode_BackCompat confirms the inline path is
// unchanged when TemplateLoader is nil OR the template's engine.chart is
// the relative-path form.
func TestPublishAppFiles_InlineMode_BackCompat(t *testing.T) {
	dir := t.TempDir()
	app := &domain.App{
		Name:        "hello",
		ProjectName: "demo",
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "web-service"}},
	}
	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, Namespace: "demo-hello-staging"},
	}

	// No TemplateLoader — should default to inline mode (today's behaviour).
	p, err := gitops.NewPublisher(gitops.PublisherConfig{RepoURL: "http://localhost/fake-repo.git"})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishAppFilesForTest: %v", err)
	}

	// Files land under envs/.../_targets/{cluster}/, not envs-external/.
	if _, err := os.Stat(filepath.Join(dir, "envs", "staging", "demo", "hello", "_targets", "in-cluster", "app.yaml")); err != nil {
		t.Errorf("inline mode should write to envs/; got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "envs-external", "staging", "demo", "hello", "_targets", "in-cluster", "app.yaml")); !os.IsNotExist(err) {
		t.Errorf("inline mode must not write to envs-external/; stat err = %v", err)
	}

	// AppMetadata's chart fields are empty (omitempty drops them).
	appYAMLBytes, err := os.ReadFile(filepath.Join(dir, "envs", "staging", "demo", "hello", "_targets", "in-cluster", "app.yaml"))
	if err != nil {
		t.Fatalf("read app.yaml: %v", err)
	}
	if strings.Contains(string(appYAMLBytes), "chartType") {
		t.Errorf("inline-mode app.yaml must not carry chartType field; got:\n%s", appYAMLBytes)
	}
}