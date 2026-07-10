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
		"kargo-demo-project.yaml",
		"kargo-demo-projectconfig.yaml",
		"kargo-demo-hello-warehouse.yaml",
		"kargo-demo-hello-staging-stage.yaml",
		"kargo-demo-hello-prod-stage.yaml",
	}
	for _, name := range wantFiles {
		path := filepath.Join(kargoDir, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected file %q to exist, but it does not", path)
		}
	}

	// preview envs must NOT produce a Stage file
	previewStage := filepath.Join(kargoDir, "kargo-demo-hello-pr-42-stage.yaml")
	if _, err := os.Stat(previewStage); !os.IsNotExist(err) {
		t.Error("preview environment should not produce a Stage file, but kargo-demo-hello-pr-42-stage.yaml exists")
	}
}

// TestPublishKargoCRs_ExcludesDecommissionedEnv verifies that an env with
// Deploy=false leaves the pipeline: it gets no Stage and no promotion policy, and
// the chain re-links so the previous env becomes terminal (its stage's upstream
// no longer points at the removed env — and here staging pulls only from the
// Warehouse). Regression guard for pipeline-aware undeploy.
func TestPublishKargoCRs_ExcludesDecommissionedEnv(t *testing.T) {
	dir := t.TempDir()

	no := false
	app := &domain.App{
		Name: "hello", ProjectName: "demo",
		Spec: domain.AppSpec{
			EnvironmentDefaults: map[string]domain.EnvironmentOverride{
				"prod": {Deploy: &no},
			},
		},
	}
	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true},
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: true},
	}

	p := newTestPublisher(t)
	if err := p.PublishKargoCRsForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishKargoCRsForTest: %v", err)
	}
	kargoDir := filepath.Join(dir, "_infra", "kargo")

	// staging keeps its stage; prod's stage is not written.
	if _, err := os.Stat(filepath.Join(kargoDir, "kargo-demo-hello-staging-stage.yaml")); err != nil {
		t.Errorf("staging stage should exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(kargoDir, "kargo-demo-hello-prod-stage.yaml")); !os.IsNotExist(err) {
		t.Error("decommissioned prod stage should NOT be written")
	}

	// staging is now terminal — its stage requests freight directly from the
	// Warehouse (no upstream stage). Assert prod is not referenced anywhere.
	stagingBytes, err := os.ReadFile(filepath.Join(kargoDir, "kargo-demo-hello-staging-stage.yaml"))
	if err != nil {
		t.Fatalf("read staging stage: %v", err)
	}
	if strings.Contains(string(stagingBytes), "prod") {
		t.Errorf("staging stage should not reference the removed prod env:\n%s", stagingBytes)
	}

	// The ProjectConfig must not carry a prod promotion policy.
	pcBytes, err := os.ReadFile(filepath.Join(kargoDir, "kargo-demo-projectconfig.yaml"))
	if err != nil {
		t.Fatalf("read projectconfig: %v", err)
	}
	if strings.Contains(string(pcBytes), "hello-prod") {
		t.Errorf("projectconfig should not carry a prod policy for the app:\n%s", pcBytes)
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
	readYAMLInto(t, filepath.Join(kargoDir, "kargo-voiceai-livekit-express-caller-warehouse.yaml"), &wh)
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
	readYAMLInto(t, filepath.Join(kargoDir, "kargo-voiceai-livekit-express-caller-staging-stage.yaml"), &stage)
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

// TestPublishKargoCRs_PerProjectNamespaceAndAggregation verifies that (1) two
// projects sharing an app name "web" get isolated kargo-{project} files with no
// collision, and (2) within a project, publishing multiple apps MERGES (not
// clobbers) their policies into the project's single ProjectConfig.
func TestPublishKargoCRs_PerProjectNamespaceAndAggregation(t *testing.T) {
	dir := t.TempDir()
	p := newTestPublisher(t)

	twoEnvs := func() []gitops.AppPublishEnv {
		return []gitops.AppPublishEnv{
			{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true},
			{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: true},
		}
	}

	// Project "alpha": two apps "web" and "api" (must aggregate in one ProjectConfig).
	for _, appName := range []string{"web", "api"} {
		if err := p.PublishKargoCRsForTest(dir, &domain.App{Name: appName, ProjectName: "alpha"}, twoEnvs()); err != nil {
			t.Fatalf("publish alpha/%s: %v", appName, err)
		}
	}
	// Project "beta": also has an app "web" — must not collide with alpha's.
	if err := p.PublishKargoCRsForTest(dir, &domain.App{Name: "web", ProjectName: "beta"}, twoEnvs()); err != nil {
		t.Fatalf("publish beta/web: %v", err)
	}

	kargoDir := filepath.Join(dir, "_infra", "kargo")

	// Each project's CRs live under its kargo-{project} file prefix; alpha/web and
	// beta/web never collide.
	for _, name := range []string{
		"kargo-alpha-project.yaml", "kargo-alpha-projectconfig.yaml",
		"kargo-alpha-web-warehouse.yaml", "kargo-alpha-api-warehouse.yaml",
		"kargo-beta-project.yaml", "kargo-beta-projectconfig.yaml",
		"kargo-beta-web-warehouse.yaml",
	} {
		if _, err := os.Stat(filepath.Join(kargoDir, name)); os.IsNotExist(err) {
			t.Errorf("expected %q to exist", name)
		}
	}

	// alpha's ProjectConfig aggregates BOTH apps (4 stages), proving per-app
	// publish merges rather than clobbers.
	var alpha gitops.KargoProjectConfig
	readYAMLInto(t, filepath.Join(kargoDir, "kargo-alpha-projectconfig.yaml"), &alpha)
	got := map[string]bool{}
	for _, pol := range alpha.Spec.PromotionPolicies {
		got[pol.Stage] = true
	}
	for _, want := range []string{"web-staging", "web-prod", "api-staging", "api-prod"} {
		if !got[want] {
			t.Errorf("alpha ProjectConfig missing policy %q; got %+v", want, alpha.Spec.PromotionPolicies)
		}
	}
	if len(alpha.Spec.PromotionPolicies) != 4 {
		t.Errorf("alpha ProjectConfig has %d policies, want 4 (no clobber): %+v",
			len(alpha.Spec.PromotionPolicies), alpha.Spec.PromotionPolicies)
	}

	// beta's ProjectConfig holds only beta's app (2 stages) — isolated from alpha.
	var beta gitops.KargoProjectConfig
	readYAMLInto(t, filepath.Join(kargoDir, "kargo-beta-projectconfig.yaml"), &beta)
	if len(beta.Spec.PromotionPolicies) != 2 {
		t.Errorf("beta ProjectConfig has %d policies, want 2: %+v",
			len(beta.Spec.PromotionPolicies), beta.Spec.PromotionPolicies)
	}
}

// Auto-promote opt-in: the prod policy in the ProjectConfig flips to
// autoPromotionEnabled, while staging stays auto. Default (opted out) leaves
// prod manual.
func TestPublishKargoCRs_AutoPromoteProdPolicy(t *testing.T) {
	policyFor := func(autoPromote bool) map[string]bool {
		dir := t.TempDir()
		p := newTestPublisher(t)
		envs := []gitops.AppPublishEnv{
			{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, AutoPromote: autoPromote},
			{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: true, AutoPromote: autoPromote},
		}
		if err := p.PublishKargoCRsForTest(dir, &domain.App{Name: "web", ProjectName: "demo"}, envs); err != nil {
			t.Fatalf("publish: %v", err)
		}
		var cfg gitops.KargoProjectConfig
		readYAMLInto(t, filepath.Join(dir, "_infra", "kargo", "kargo-demo-projectconfig.yaml"), &cfg)
		auto := map[string]bool{}
		for _, pol := range cfg.Spec.PromotionPolicies {
			auto[pol.Stage] = pol.AutoPromotionEnabled
		}
		return auto
	}

	off := policyFor(false)
	if !off["web-staging"] || off["web-prod"] {
		t.Errorf("opted out: want staging auto + prod manual, got %+v", off)
	}
	on := policyFor(true)
	if !on["web-staging"] || !on["web-prod"] {
		t.Errorf("opted in: want staging + prod auto, got %+v", on)
	}
}

// Kargo pause keys on a real pin (PinnedFrom set), not on the forced tag: a real
// pin pauses staging's auto-promotion, but the transient unpin-restore write
// (PinnedImageTag set, PinnedFrom empty) must leave auto-promotion ON.
func TestPublishKargoCRs_PinPausesButRestoreDoesNot(t *testing.T) {
	stagingAuto := func(envDefaults map[string]domain.EnvironmentOverride) bool {
		dir := t.TempDir()
		p := newTestPublisher(t)
		app := &domain.App{Name: "web", ProjectName: "demo", Spec: domain.AppSpec{EnvironmentDefaults: envDefaults}}
		envs := []gitops.AppPublishEnv{
			{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true},
			{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: true},
		}
		if err := p.PublishKargoCRsForTest(dir, app, envs); err != nil {
			t.Fatalf("publish: %v", err)
		}
		var cfg gitops.KargoProjectConfig
		readYAMLInto(t, filepath.Join(dir, "_infra", "kargo", "kargo-demo-projectconfig.yaml"), &cfg)
		for _, pol := range cfg.Spec.PromotionPolicies {
			if pol.Stage == gitops.KargoStageName("web", "staging") {
				return pol.AutoPromotionEnabled
			}
		}
		t.Fatal("staging policy not found")
		return false
	}

	// Real pin → paused.
	if stagingAuto(map[string]domain.EnvironmentOverride{"staging": {PinnedImageTag: "pr-1-x", PinnedFrom: "pr-1"}}) {
		t.Error("a real pin must pause staging auto-promotion")
	}
	// Restore write (forced tag, no PinnedFrom) → still auto.
	if !stagingAuto(map[string]domain.EnvironmentOverride{"staging": {PinnedImageTag: "restore-tag"}}) {
		t.Error("an unpin-restore write must NOT pause staging auto-promotion")
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

	content, err := os.ReadFile(filepath.Join(dir, "_infra", "kargo", "kargo-demo-project.yaml"))
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
	// The per-project Project CR is named after its kargo-{project} namespace.
	if !strings.Contains(body, "name: kargo-demo") {
		t.Errorf("project YAML missing name:kargo-demo:\n%s", body)
	}
	// Kargo v1.x: the Project CR carries NO promotionPolicies (they live on the
	// separate ProjectConfig). Emitting them on the Project gets stripped.
	if strings.Contains(body, "promotionPolicies") {
		t.Errorf("v1.x Project must NOT carry promotionPolicies (moved to ProjectConfig):\n%s", body)
	}

	// ProjectConfig holds the promotion policies (staging auto, prod manual).
	cfgContent, err := os.ReadFile(filepath.Join(dir, "_infra", "kargo", "kargo-demo-projectconfig.yaml"))
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

	prodStage, err := os.ReadFile(filepath.Join(dir, "_infra", "kargo", "kargo-demo-hello-prod-stage.yaml"))
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

	stagingStage, err := os.ReadFile(filepath.Join(dir, "_infra", "kargo", "kargo-demo-hello-staging-stage.yaml"))
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
	content1, _ := os.ReadFile(filepath.Join(dir, "_infra", "kargo", "kargo-demo-hello-warehouse.yaml"))
	p.PublishKargoCRsForTest(dir, app, envs) //nolint:errcheck
	content2, _ := os.ReadFile(filepath.Join(dir, "_infra", "kargo", "kargo-demo-hello-warehouse.yaml"))
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
	devStage, err := os.ReadFile(filepath.Join(kargoDir, "kargo-demo-hello-dev-stage.yaml"))
	if err != nil {
		t.Fatalf("dev stage missing: %v", err)
	}
	if !strings.Contains(string(devStage), "direct: true") {
		t.Errorf("single-env Stage should have direct:true:\n%s", string(devStage))
	}
	// Must NOT produce a prod stage.
	if _, err := os.Stat(filepath.Join(kargoDir, "kargo-demo-hello-prod-stage.yaml")); !os.IsNotExist(err) {
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

	devStage, _ := os.ReadFile(filepath.Join(kargoDir, "kargo-demo-hello-dev-stage.yaml"))
	stagingStage, _ := os.ReadFile(filepath.Join(kargoDir, "kargo-demo-hello-staging-stage.yaml"))
	prodStage, _ := os.ReadFile(filepath.Join(kargoDir, "kargo-demo-hello-prod-stage.yaml"))

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
	if _, err := os.Stat(filepath.Join(kargoDir, "kargo-demo-hello-staging-stage.yaml")); os.IsNotExist(err) {
		t.Error("expected staging Stage file to exist for bound env")
	}

	// prod is unbound → must NOT have a Stage file
	if _, err := os.Stat(filepath.Join(kargoDir, "kargo-demo-hello-prod-stage.yaml")); !os.IsNotExist(err) {
		t.Error("unbound prod env should NOT produce a Stage file")
	}

	// staging should be the first stage (direct) since prod is not in the chain
	stagingStage, err := os.ReadFile(filepath.Join(kargoDir, "kargo-demo-hello-staging-stage.yaml"))
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
	if _, err := os.Stat(filepath.Join(kargoDir, "kargo-demo-hello-warehouse.yaml")); os.IsNotExist(err) {
		t.Error("warehouse file should exist even when all envs are unbound")
	}

	// No Stage files
	for _, envName := range []string{"staging", "prod"} {
		stageFile := filepath.Join(kargoDir, "kargo-demo-hello-"+envName+"-stage.yaml")
		if _, err := os.Stat(stageFile); !os.IsNotExist(err) {
			t.Errorf("unbound env %q should not produce a Stage file", envName)
		}
	}
}
