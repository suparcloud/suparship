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
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true},
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: true},
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
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Bound: true},
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
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true},
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: true},
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
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true},
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: true},
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
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Bound: true},
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

// TestPublishKargoCRs_SingleEnvNoUpstream verifies that when only one stable
// env is registered the resulting Stage has direct:true and no upstream stages.
func TestPublishKargoCRs_SingleEnvNoUpstream(t *testing.T) {
	dir := t.TempDir()
	app := &domain.App{Name: "hello", ProjectName: "demo"}
	envs := []gitops.AppPublishEnv{
		{EnvName: "dev", EnvType: domain.AppEnvStaging, Order: 1, Bound: true},
	}

	p := newTestPublisher(t)
	if err := p.PublishKargoCRsForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishKargoCRsForTest: %v", err)
	}

	kargoDir := filepath.Join(dir, "gitops-output", "_infra", "kargo")

	// Only one stage file and no prod stage.
	devStage, err := os.ReadFile(filepath.Join(kargoDir, "demo-hello-dev-stage.yaml"))
	if err != nil {
		t.Fatalf("dev stage missing: %v", err)
	}
	if !strings.Contains(string(devStage), "direct: true") {
		t.Errorf("single-env Stage should have direct:true:\n%s", string(devStage))
	}
	// Must NOT produce a prod stage.
	if _, err := os.Stat(filepath.Join(kargoDir, "demo-hello-prod-stage.yaml")); !os.IsNotExist(err) {
		t.Error("single-env publish should not produce a prod stage file")
	}
}

// TestPublishKargoCRs_ThreeEnvChain verifies that a three-env pipeline
// (dev → staging → prod) chains each stage's upstream correctly.
func TestPublishKargoCRs_ThreeEnvChain(t *testing.T) {
	dir := t.TempDir()
	app := &domain.App{Name: "hello", ProjectName: "demo"}
	// Intentionally provide envs out of Order-sort sequence to confirm sorting.
	envs := []gitops.AppPublishEnv{
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 3, Bound: true},
		{EnvName: "dev", EnvType: domain.AppEnvStaging, Order: 1, Bound: true},
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 2, Bound: true},
	}

	p := newTestPublisher(t)
	if err := p.PublishKargoCRsForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishKargoCRsForTest: %v", err)
	}

	kargoDir := filepath.Join(dir, "gitops-output", "_infra", "kargo")

	devStage, _ := os.ReadFile(filepath.Join(kargoDir, "demo-hello-dev-stage.yaml"))
	stagingStage, _ := os.ReadFile(filepath.Join(kargoDir, "demo-hello-staging-stage.yaml"))
	prodStage, _ := os.ReadFile(filepath.Join(kargoDir, "demo-hello-prod-stage.yaml"))

	if !strings.Contains(string(devStage), "direct: true") {
		t.Errorf("dev Stage (Order=1, first) should have direct:true:\n%s", string(devStage))
	}
	if !strings.Contains(string(stagingStage), "hello-dev") {
		t.Errorf("staging Stage should reference 'hello-dev' as upstream:\n%s", string(stagingStage))
	}
	if !strings.Contains(string(prodStage), "hello-staging") {
		t.Errorf("prod Stage should reference 'hello-staging' as upstream:\n%s", string(prodStage))
	}
}

func TestPublishKargoCRs_UnboundEnvSkipped(t *testing.T) {
	dir := t.TempDir()
	app := &domain.App{Name: "hello", ProjectName: "demo"}
	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true},
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: false}, // unbound
	}

	p := newTestPublisher(t)
	if err := p.PublishKargoCRsForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishKargoCRsForTest: %v", err)
	}

	kargoDir := filepath.Join(dir, "gitops-output", "_infra", "kargo")

	// staging is bound → should have a Stage file
	if _, err := os.Stat(filepath.Join(kargoDir, "demo-hello-staging-stage.yaml")); os.IsNotExist(err) {
		t.Error("expected staging Stage file to exist for bound env")
	}

	// prod is unbound → must NOT have a Stage file
	if _, err := os.Stat(filepath.Join(kargoDir, "demo-hello-prod-stage.yaml")); !os.IsNotExist(err) {
		t.Error("unbound prod env should NOT produce a Stage file")
	}

	// staging should be the first stage (direct) since prod is not in the chain
	stagingStage, err := os.ReadFile(filepath.Join(kargoDir, "demo-hello-staging-stage.yaml"))
	if err != nil {
		t.Fatalf("read staging stage: %v", err)
	}
	if !strings.Contains(string(stagingStage), "direct: true") {
		t.Errorf("staging Stage should be direct (only bound stage):\n%s", string(stagingStage))
	}
}

func TestPublishKargoCRs_AllUnboundProducesWarehouseOnly(t *testing.T) {
	dir := t.TempDir()
	app := &domain.App{Name: "hello", ProjectName: "demo"}
	// Both envs are unbound — only Warehouse + Project CR should be written, no Stage files.
	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: false},
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: false},
	}

	p := newTestPublisher(t)
	if err := p.PublishKargoCRsForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishKargoCRsForTest: %v", err)
	}

	kargoDir := filepath.Join(dir, "gitops-output", "_infra", "kargo")

	// Warehouse should still be written (it's env-independent)
	if _, err := os.Stat(filepath.Join(kargoDir, "demo-hello-warehouse.yaml")); os.IsNotExist(err) {
		t.Error("warehouse file should exist even when all envs are unbound")
	}

	// No Stage files
	for _, envName := range []string{"staging", "prod"} {
		stageFile := filepath.Join(kargoDir, "demo-hello-"+envName+"-stage.yaml")
		if _, err := os.Stat(stageFile); !os.IsNotExist(err) {
			t.Errorf("unbound env %q should not produce a Stage file", envName)
		}
	}
}
