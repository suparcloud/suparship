package gitops_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/gitops"
)

// newTestPublisher returns a Publisher with a dummy URL so it can be
// constructed without network access. Only use it for tests that call
// PublishKargoCRsForTest (which skips git entirely).
func newTestPublisher(t *testing.T) *gitops.Publisher {
	t.Helper()
	p, err := gitops.NewPublisher(gitops.PublisherConfig{
		RepoURL: "http://localhost/fake-repo.git",
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	return p
}

func TestPublishKargoCRs_WritesExpectedFiles(t *testing.T) {
	dir := t.TempDir()

	app := &domain.App{Name: "hello", ProjectName: "demo"}
	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging},
		{EnvName: "prod", EnvType: domain.AppEnvProd},
		{EnvName: "pr-42", EnvType: domain.AppEnvPreview}, // should be skipped
	}

	p := newTestPublisher(t)
	if err := p.PublishKargoCRsForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishKargoCRsForTest: %v", err)
	}

	kargoDir := filepath.Join(dir, "gitops-output", "_infra", "kargo")

	wantFiles := []string{
		"demo-project.yaml",
		"demo-hello-warehouse.yaml",
		"demo-hello-staging-stage.yaml",
		"demo-hello-prod-stage.yaml",
	}
	for _, name := range wantFiles {
		path := filepath.Join(kargoDir, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected file %q to exist, but it does not", path)
		}
	}

	// preview envs must NOT produce a Stage file
	previewStage := filepath.Join(kargoDir, "demo-hello-pr-42-stage.yaml")
	if _, err := os.Stat(previewStage); !os.IsNotExist(err) {
		t.Error("preview environment should not produce a Stage file, but demo-hello-pr-42-stage.yaml exists")
	}
}

func TestPublishKargoCRs_ProjectCRIsGenerated(t *testing.T) {
	dir := t.TempDir()
	app := &domain.App{Name: "hello", ProjectName: "demo"}
	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging},
	}

	p := newTestPublisher(t)
	if err := p.PublishKargoCRsForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishKargoCRsForTest: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(dir, "gitops-output", "_infra", "kargo", "demo-project.yaml"))
	if err != nil {
		t.Fatalf("read project file: %v", err)
	}
	body := string(content)
	if !strings.Contains(body, "kind: Project") {
		t.Errorf("project YAML missing kind:Project:\n%s", body)
	}
	if !strings.Contains(body, "kargo.akuity.io/v1alpha1") {
		t.Errorf("project YAML missing apiVersion:\n%s", body)
	}
	if !strings.Contains(body, "name: demo") {
		t.Errorf("project YAML missing name:demo:\n%s", body)
	}
	if !strings.Contains(body, "promotionPolicies") {
		t.Errorf("project YAML missing promotionPolicies:\n%s", body)
	}
}

func TestPublishKargoCRs_ProdStageHasStagingUpstream(t *testing.T) {
	dir := t.TempDir()
	app := &domain.App{Name: "hello", ProjectName: "demo"}
	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging},
		{EnvName: "prod", EnvType: domain.AppEnvProd},
	}

	p := newTestPublisher(t)
	if err := p.PublishKargoCRsForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishKargoCRsForTest: %v", err)
	}

	prodStage, err := os.ReadFile(filepath.Join(dir, "gitops-output", "_infra", "kargo", "demo-hello-prod-stage.yaml"))
	if err != nil {
		t.Fatalf("read prod stage: %v", err)
	}
	// prod Stage must reference staging as an upstream gate (not direct)
	body := string(prodStage)
	if strings.Contains(body, "direct: true") {
		t.Error("prod Stage should not have direct:true — it should gate behind staging")
	}
	if !strings.Contains(body, "staging") {
		t.Errorf("prod Stage YAML should reference 'staging' upstream:\n%s", body)
	}
	if !strings.Contains(body, "promotionMechanisms") {
		t.Errorf("prod Stage YAML should use promotionMechanisms (Kargo v0.9 webhook compat):\n%s", body)
	}
}

func TestPublishKargoCRs_StagingStageIsDirect(t *testing.T) {
	dir := t.TempDir()
	app := &domain.App{Name: "hello", ProjectName: "demo"}
	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging},
		{EnvName: "prod", EnvType: domain.AppEnvProd},
	}

	p := newTestPublisher(t)
	if err := p.PublishKargoCRsForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishKargoCRsForTest: %v", err)
	}

	stagingStage, err := os.ReadFile(filepath.Join(dir, "gitops-output", "_infra", "kargo", "demo-hello-staging-stage.yaml"))
	if err != nil {
		t.Fatalf("read staging stage: %v", err)
	}
	if !strings.Contains(string(stagingStage), "direct: true") {
		t.Errorf("staging Stage should have direct:true (receives freight directly from Warehouse):\n%s", string(stagingStage))
	}
}

func TestPublishKargoCRs_Idempotent(t *testing.T) {
	dir := t.TempDir()
	app := &domain.App{Name: "hello", ProjectName: "demo"}
	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging},
	}
	p := newTestPublisher(t)

	for i := 0; i < 3; i++ {
		if err := p.PublishKargoCRsForTest(dir, app, envs); err != nil {
			t.Fatalf("run %d: PublishKargoCRsForTest: %v", i, err)
		}
	}

	// file should exist and have stable content
	content1, _ := os.ReadFile(filepath.Join(dir, "gitops-output", "_infra", "kargo", "demo-hello-warehouse.yaml"))
	p.PublishKargoCRsForTest(dir, app, envs) //nolint:errcheck
	content2, _ := os.ReadFile(filepath.Join(dir, "gitops-output", "_infra", "kargo", "demo-hello-warehouse.yaml"))
	if string(content1) != string(content2) {
		t.Error("publishKargoCRs is not idempotent: warehouse YAML changed between runs")
	}
}
