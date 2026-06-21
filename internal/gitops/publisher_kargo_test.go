package gitops_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

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

	kargoDir := filepath.Join(dir, "_infra", "kargo")

	wantFiles := []string{
		"demo-project.yaml",
		"demo-projectconfig.yaml",
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

// TestPublishKargoCRs_TemplateImageMappingRoundTrip is the regression guard for
// the voiceai case: a template image mapping carrying a tag pattern + selection
// strategy must flow through to the published Warehouse subscription (allowTags
// + imageSelectionStrategy) and the Stage's helm image update (image + key).
// Without this the Warehouse silently falls back to Kargo's SemVer default and
// discovers nothing for bare-SHA tags.
func TestPublishKargoCRs_TemplateImageMappingRoundTrip(t *testing.T) {
	dir := t.TempDir()
	app := &domain.App{Name: "livekit-express-caller", ProjectName: "voiceai"}
	envs := []gitops.AppPublishEnv{
		{
			EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true,
			TemplateImages: []gitops.KargoImage{{
				Repository:        "acr.example.com/voiceai-livekit",
				TagKey:            "image.tag",
				TagPattern:        `^[0-9a-f]{7,40}$`,
				SelectionStrategy: "NewestBuild",
			}},
		},
	}

	p := newTestPublisher(t)
	if err := p.PublishKargoCRsForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishKargoCRsForTest: %v", err)
	}
	kargoDir := filepath.Join(dir, "_infra", "kargo")

	// Warehouse: subscription must carry the mapping's repo + pattern + strategy.
	var wh gitops.KargoWarehouse
	readYAMLInto(t, filepath.Join(kargoDir, "voiceai-livekit-express-caller-warehouse.yaml"), &wh)
	if len(wh.Spec.Subscriptions) != 1 || wh.Spec.Subscriptions[0].Image == nil {
		t.Fatalf("expected 1 image subscription, got %+v", wh.Spec.Subscriptions)
	}
	img := wh.Spec.Subscriptions[0].Image
	if img.RepoURL != "acr.example.com/voiceai-livekit" {
		t.Errorf("subscription repoURL = %q, want acr.example.com/voiceai-livekit", img.RepoURL)
	}
	if img.AllowTags != `^[0-9a-f]{7,40}$` {
		t.Errorf("subscription allowTags = %q, want the SHA pattern", img.AllowTags)
	}
	if img.ImageSelectionStrategy != "NewestBuild" {
		t.Errorf("subscription imageSelectionStrategy = %q, want NewestBuild", img.ImageSelectionStrategy)
	}

	// Stage: the yaml-update promotion step must target the mapped tag key.
	var stage gitops.KargoStage
	readYAMLInto(t, filepath.Join(kargoDir, "voiceai-livekit-express-caller-staging-stage.yaml"), &stage)
	if stage.Spec.PromotionTemplate == nil {
		t.Fatal("stage PromotionTemplate is nil")
	}
	var yu *gitops.PromotionStep
	for i := range stage.Spec.PromotionTemplate.Spec.Steps {
		if stage.Spec.PromotionTemplate.Spec.Steps[i].Uses == "yaml-update" {
			yu = &stage.Spec.PromotionTemplate.Spec.Steps[i]
		}
	}
	if yu == nil {
		t.Fatal("stage has no yaml-update step")
	}
	if path, _ := yu.Config["path"].(string); path != "./src/envs/staging/voiceai/livekit-express-caller/values.yaml" {
		t.Errorf("yaml-update path = %q", path)
	}
	// updates unmarshal from YAML as []any of map[string]any.
	updates, _ := yu.Config["updates"].([]any)
	if len(updates) != 1 {
		t.Fatalf("yaml-update updates = %+v, want 1", yu.Config["updates"])
	}
	u, _ := updates[0].(map[string]any)
	if u["key"] != "image.tag" {
		t.Errorf("yaml-update key = %v, want image.tag", u["key"])
	}
}

func readYAMLInto(t *testing.T, path string, out any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := yaml.Unmarshal(data, out); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
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

	content, err := os.ReadFile(filepath.Join(dir, "_infra", "kargo", "demo-project.yaml"))
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
	// Kargo v1.x: the Project CR carries NO promotionPolicies (they live on the
	// separate ProjectConfig). Emitting them on the Project gets stripped.
	if strings.Contains(body, "promotionPolicies") {
		t.Errorf("v1.x Project must NOT carry promotionPolicies (moved to ProjectConfig):\n%s", body)
	}

	// ProjectConfig holds the promotion policies (staging auto, prod manual).
	cfgContent, err := os.ReadFile(filepath.Join(dir, "_infra", "kargo", "demo-projectconfig.yaml"))
	if err != nil {
		t.Fatalf("read projectconfig file: %v", err)
	}
	cfgBody := string(cfgContent)
	if !strings.Contains(cfgBody, "kind: ProjectConfig") {
		t.Errorf("projectconfig YAML missing kind:ProjectConfig:\n%s", cfgBody)
	}
	if !strings.Contains(cfgBody, "promotionPolicies") {
		t.Errorf("projectconfig YAML missing promotionPolicies:\n%s", cfgBody)
	}
	if !strings.Contains(cfgBody, "stage: hello-staging") {
		t.Errorf("projectconfig YAML missing staging policy:\n%s", cfgBody)
	}
	if !strings.Contains(cfgBody, "autoPromotionEnabled: true") {
		t.Errorf("projectconfig YAML missing autoPromotionEnabled for first stage:\n%s", cfgBody)
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

	prodStage, err := os.ReadFile(filepath.Join(dir, "_infra", "kargo", "demo-hello-prod-stage.yaml"))
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
	if !strings.Contains(body, "promotionTemplate") {
		t.Errorf("prod Stage YAML should use promotionTemplate.spec.steps (Kargo v1.x):\n%s", body)
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

	stagingStage, err := os.ReadFile(filepath.Join(dir, "_infra", "kargo", "demo-hello-staging-stage.yaml"))
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
	content1, _ := os.ReadFile(filepath.Join(dir, "_infra", "kargo", "demo-hello-warehouse.yaml"))
	p.PublishKargoCRsForTest(dir, app, envs) //nolint:errcheck
	content2, _ := os.ReadFile(filepath.Join(dir, "_infra", "kargo", "demo-hello-warehouse.yaml"))
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

	kargoDir := filepath.Join(dir, "_infra", "kargo")

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

	kargoDir := filepath.Join(dir, "_infra", "kargo")

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

	kargoDir := filepath.Join(dir, "_infra", "kargo")

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

	kargoDir := filepath.Join(dir, "_infra", "kargo")

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
