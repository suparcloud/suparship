package gitops_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/gitops"
)

// --- firstDeployEnvs unit tests ---

func TestFirstDeployEnvs_OnlyFirstStableEnvIncluded(t *testing.T) {
	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true},
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: true},
	}
	got := gitops.FirstDeployEnvsForTest(envs)

	if len(got) != 1 {
		t.Fatalf("expected 1 env, got %d: %v", len(got), got)
	}
	if got[0].EnvName != "staging" {
		t.Errorf("expected staging (Order=1) to be the first-deploy env, got %q", got[0].EnvName)
	}
}

func TestFirstDeployEnvs_PreviewsAlwaysIncluded(t *testing.T) {
	envs := []gitops.AppPublishEnv{
		{EnvName: "pr-1", EnvType: domain.AppEnvPreview, Order: 0},
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true},
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: true},
	}
	got := gitops.FirstDeployEnvsForTest(envs)

	if len(got) != 2 {
		t.Fatalf("expected 2 envs (preview + staging), got %d: %v", len(got), got)
	}
	names := map[string]bool{}
	for _, e := range got {
		names[e.EnvName] = true
	}
	if !names["pr-1"] {
		t.Error("expected preview env pr-1 to be included")
	}
	if !names["staging"] {
		t.Error("expected staging to be included as the first stable env")
	}
	if names["prod"] {
		t.Error("prod (Order=2) must NOT be included in the first-deploy set")
	}
}

func TestFirstDeployEnvs_SingleEnvIncluded(t *testing.T) {
	envs := []gitops.AppPublishEnv{
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 1, Bound: true},
	}
	got := gitops.FirstDeployEnvsForTest(envs)
	if len(got) != 1 || got[0].EnvName != "prod" {
		t.Errorf("single bound env should be included, got %v", got)
	}
}

func TestFirstDeployEnvs_UnboundEnvsExcluded(t *testing.T) {
	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: false}, // unbound!
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: true},
	}
	// staging is unbound, so prod (next bound env) should be selected
	got := gitops.FirstDeployEnvsForTest(envs)
	if len(got) != 1 || got[0].EnvName != "prod" {
		t.Errorf("expected prod to be first-deploy (staging unbound), got %v", got)
	}
}

func TestFirstDeployEnvs_AllUnboundReturnsEmpty(t *testing.T) {
	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: false},
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: false},
	}
	got := gitops.FirstDeployEnvsForTest(envs)
	if len(got) != 0 {
		t.Errorf("expected empty first-deploy set when all stable envs are unbound, got %v", got)
	}
}

func TestFirstDeployEnvs_ThreeEnvPicksLowestOrder(t *testing.T) {
	// Provide out-of-order to confirm sorting is used
	envs := []gitops.AppPublishEnv{
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 3, Bound: true},
		{EnvName: "dev", EnvType: domain.AppEnvStaging, Order: 1, Bound: true},
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 2, Bound: true},
	}
	got := gitops.FirstDeployEnvsForTest(envs)
	if len(got) != 1 || got[0].EnvName != "dev" {
		t.Errorf("expected dev (Order=1, lowest) to be first-deploy env, got %v", got)
	}
}

// --- PublishApp first-env-only behaviour ---

func TestPublishApp_OnlyFirstEnvGetsFiles(t *testing.T) {
	dir := t.TempDir()
	app := &domain.App{
		Name:        "hello",
		ProjectName: "demo",
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "web-service"}},
	}
	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, BaseDomain: "localhost"},
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: true, BaseDomain: "localhost"},
	}

	// firstDeployEnvs selects staging only; prod must NOT get files on create.
	p := newTestPublisher(t)
	if err := p.PublishAppFilesForTest(dir, app, gitops.FirstDeployEnvsForTest(envs)); err != nil {
		t.Fatalf("PublishAppFilesForTest: %v", err)
	}

	// staging files must exist (per-cluster in-cluster fallback target)
	if _, err := os.Stat(filepath.Join(dir, "envs", "staging", "demo", "hello", "_targets", "in-cluster", "app.yaml")); os.IsNotExist(err) {
		t.Error("expected app.yaml for staging to exist after initial publish")
	}

	// prod files must NOT exist (publish-on-promote, not on create)
	if _, err := os.Stat(filepath.Join(dir, "envs", "prod", "demo", "hello", "_targets", "in-cluster", "app.yaml")); !os.IsNotExist(err) {
		t.Error("prod app.yaml must NOT exist after initial publish — it should only be written on promotion")
	}
}

func TestPublishApp_PreviewEnvPublishedOnCreate(t *testing.T) {
	dir := t.TempDir()
	app := &domain.App{
		Name:        "hello",
		ProjectName: "demo",
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "web-service"}},
	}
	envs := []gitops.AppPublishEnv{
		{EnvName: "pr-1", EnvType: domain.AppEnvPreview, Order: 0, Bound: true, BaseDomain: "localhost"},
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, BaseDomain: "localhost"},
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: true, BaseDomain: "localhost"},
	}

	p := newTestPublisher(t)
	if err := p.PublishAppFilesForTest(dir, app, gitops.FirstDeployEnvsForTest(envs)); err != nil {
		t.Fatalf("PublishAppFilesForTest: %v", err)
	}

	// preview env always gets files on create — previews keep the flat layout
	// (no _targets/), so this path is unchanged.
	if _, err := os.Stat(filepath.Join(dir, "pr-1", "demo", "hello", "app.yaml")); os.IsNotExist(err) {
		t.Error("expected preview env pr-1 app.yaml to exist after create")
	}
	// first stable env gets files on create (per-cluster in-cluster fallback target)
	if _, err := os.Stat(filepath.Join(dir, "envs", "staging", "demo", "hello", "_targets", "in-cluster", "app.yaml")); os.IsNotExist(err) {
		t.Error("expected staging app.yaml to exist after create")
	}
	// higher stable env must NOT have files yet
	if _, err := os.Stat(filepath.Join(dir, "envs", "prod", "demo", "hello", "_targets", "in-cluster", "app.yaml")); !os.IsNotExist(err) {
		t.Error("prod app.yaml must NOT exist after create — only written on promotion")
	}
}

// --- PublishAppEnv (promotion publish) ---

func TestPublishAppEnv_WritesTargetEnv(t *testing.T) {
	dir := t.TempDir()
	app := &domain.App{
		Name:        "hello",
		ProjectName: "demo",
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "web-service"}},
	}
	env := gitops.AppPublishEnv{
		EnvName:    "prod",
		EnvType:    domain.AppEnvProd,
		Order:      2,
		Bound:      true,
		BaseDomain: "localhost",
	}

	p := newTestPublisher(t)
	if err := p.PublishAppEnvForTest(dir, app, env); err != nil {
		t.Fatalf("PublishAppEnvForTest: %v", err)
	}

	// prod files must now exist (simulate promotion; per-cluster in-cluster target)
	if _, err := os.Stat(filepath.Join(dir, "envs", "prod", "demo", "hello", "_targets", "in-cluster", "app.yaml")); os.IsNotExist(err) {
		t.Error("expected prod app.yaml to exist after PublishAppEnv")
	}
	if _, err := os.Stat(filepath.Join(dir, "envs", "prod", "demo", "hello", "values.yaml")); os.IsNotExist(err) {
		t.Error("expected prod values.yaml to exist after PublishAppEnv")
	}
}

// --- republish of already-promoted envs ---

// Once a promotion has materialized a higher env, a full republish (the
// values-save / app-edit path) must rewrite that env's files too. A prod
// values edit used to produce no git change until the next promotion, while
// the same edit on the base env committed immediately.
func TestWriteAppTree_RepublishesPromotedEnv(t *testing.T) {
	dir := t.TempDir()
	app := &domain.App{
		Name:        "hello",
		ProjectName: "demo",
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "web-service"}},
	}
	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, BaseDomain: "localhost"},
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: true, BaseDomain: "localhost"},
	}
	p := newTestPublisher(t)

	// Create: only staging materializes.
	if err := p.WriteAppTreeForTest(context.Background(), dir, app, envs); err != nil {
		t.Fatalf("WriteAppTreeForTest (create): %v", err)
	}
	prodValues := filepath.Join(dir, "envs", "prod", "demo", "hello", "values.yaml")
	if _, err := os.Stat(prodValues); !os.IsNotExist(err) {
		t.Fatal("prod must not be materialized before its first promotion")
	}

	// Promote: prod materializes via the per-env publish.
	if err := p.PublishAppEnvForTest(dir, app, envs[1]); err != nil {
		t.Fatalf("PublishAppEnvForTest: %v", err)
	}
	if _, err := os.Stat(prodValues); err != nil {
		t.Fatalf("prod values.yaml should exist after promotion publish: %v", err)
	}

	// Save prod values → full republish must rewrite prod's values.yaml.
	app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{
		"prod": {RawValues: map[string]any{"replicaCount": 5}},
	}
	if err := p.WriteAppTreeForTest(context.Background(), dir, app, envs); err != nil {
		t.Fatalf("WriteAppTreeForTest (republish): %v", err)
	}
	data, err := os.ReadFile(prodValues)
	if err != nil {
		t.Fatalf("read prod values.yaml: %v", err)
	}
	if !strings.Contains(string(data), "replicaCount: 5") {
		t.Errorf("prod values.yaml must carry the saved override after republish, got:\n%s", data)
	}
}

// A decommissioned env (Deploy=false) stays untouched by a republish even if
// its files are still in the repo — mirroring enabledDeployEnvs for direct apps.
func TestWriteAppTree_SkipsDecommissionedPromotedEnv(t *testing.T) {
	dir := t.TempDir()
	app := &domain.App{
		Name:        "hello",
		ProjectName: "demo",
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "web-service"}},
	}
	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, BaseDomain: "localhost"},
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: true, BaseDomain: "localhost"},
	}
	p := newTestPublisher(t)
	if err := p.PublishAppEnvForTest(dir, app, envs[1]); err != nil {
		t.Fatalf("PublishAppEnvForTest: %v", err)
	}
	prodValues := filepath.Join(dir, "envs", "prod", "demo", "hello", "values.yaml")
	before, err := os.ReadFile(prodValues)
	if err != nil {
		t.Fatalf("read prod values.yaml: %v", err)
	}

	deploy := false
	app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{
		"prod": {Deploy: &deploy, RawValues: map[string]any{"replicaCount": 5}},
	}
	if err := p.WriteAppTreeForTest(context.Background(), dir, app, envs); err != nil {
		t.Fatalf("WriteAppTreeForTest: %v", err)
	}
	after, err := os.ReadFile(prodValues)
	if err != nil {
		t.Fatalf("read prod values.yaml after republish: %v", err)
	}
	if string(before) != string(after) {
		t.Error("a decommissioned env's files must not be rewritten by a republish")
	}
}

func TestPublishAppEnv_UnboundEnvWritesNothing(t *testing.T) {
	dir := t.TempDir()
	app := &domain.App{
		Name:        "hello",
		ProjectName: "demo",
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "web-service"}},
	}
	env := gitops.AppPublishEnv{
		EnvName: "prod",
		EnvType: domain.AppEnvProd,
		Order:   2,
		Bound:   false, // unbound — should be skipped
	}

	p := newTestPublisher(t)
	if err := p.PublishAppEnvForTest(dir, app, env); err != nil {
		t.Fatalf("PublishAppEnvForTest: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "envs", "prod", "demo", "hello", "_targets", "in-cluster", "app.yaml")); !os.IsNotExist(err) {
		t.Error("unbound env should NOT produce app.yaml even when explicitly promoted")
	}
}
