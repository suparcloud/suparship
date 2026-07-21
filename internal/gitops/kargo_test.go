package gitops_test

import (
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/gitops"
)

func TestBuildKargoWarehouse(t *testing.T) {
	app := &domain.App{
		Name:        "hello",
		ProjectName: "demo",
	}

	wh := gitops.BuildKargoWarehouse(app, gitops.KargoBuildOptions{})

	if wh.APIVersion != "kargo.akuity.io/v1alpha1" {
		t.Errorf("APIVersion: got %q want %q", wh.APIVersion, "kargo.akuity.io/v1alpha1")
	}
	if wh.Kind != "Warehouse" {
		t.Errorf("Kind: got %q want %q", wh.Kind, "Warehouse")
	}
	if wh.Metadata.Name != "hello" {
		t.Errorf("Name: got %q want %q", wh.Metadata.Name, "hello")
	}
	if wh.Metadata.Namespace != "kargo-demo" {
		t.Errorf("Namespace: got %q want %q", wh.Metadata.Namespace, "kargo-demo")
	}
	if len(wh.Spec.Subscriptions) != 1 {
		t.Fatalf("Subscriptions: got %d want 1", len(wh.Spec.Subscriptions))
	}
	sub := wh.Spec.Subscriptions[0]
	if sub.Image == nil {
		t.Fatal("expected Image subscription, got nil")
	}
	if sub.Image.RepoURL != "ghcr.io/demo/hello" {
		t.Errorf("RepoURL: got %q want %q", sub.Image.RepoURL, "ghcr.io/demo/hello")
	}
	if sub.Image.AllowTags == "" {
		t.Error("AllowTags should be set to a default tag filter")
	}
	if wh.Spec.FreightCreationPolicy != "Automatic" {
		t.Errorf("FreightCreationPolicy: got %q want %q", wh.Spec.FreightCreationPolicy, "Automatic")
	}
}

func TestBuildKargoWarehouse_WithOverrides(t *testing.T) {
	app := &domain.App{Name: "api", ProjectName: "myproject"}

	wh := gitops.BuildKargoWarehouse(app, gitops.KargoBuildOptions{
		KargoNamespace: "kargo-myproject",
		Images: []gitops.KargoImage{{
			Repository:        "registry.example.com/myproject/api",
			TagKey:            "image.tag",
			TagPattern:        `^v\d+\.\d+\.\d+$`,
			SelectionStrategy: "NewestBuild",
		}},
	})

	if wh.Metadata.Namespace != "kargo-myproject" {
		t.Errorf("Namespace: got %q want %q", wh.Metadata.Namespace, "kargo-myproject")
	}
	if wh.Spec.Subscriptions[0].Image.RepoURL != "registry.example.com/myproject/api" {
		t.Errorf("RepoURL override not applied")
	}
	if wh.Spec.Subscriptions[0].Image.AllowTags != `^v\d+\.\d+\.\d+$` {
		t.Errorf("AllowTags override not applied: got %q", wh.Spec.Subscriptions[0].Image.AllowTags)
	}
	if wh.Spec.Subscriptions[0].Image.ImageSelectionStrategy != "NewestBuild" {
		t.Errorf("ImageSelectionStrategy: got %q want NewestBuild", wh.Spec.Subscriptions[0].Image.ImageSelectionStrategy)
	}
}

func TestBuildKargoWarehouse_MultipleImages(t *testing.T) {
	app := &domain.App{Name: "multi", ProjectName: "demo"}
	wh := gitops.BuildKargoWarehouse(app, gitops.KargoBuildOptions{
		Images: []gitops.KargoImage{
			{Repository: "acr.example.com/agent", TagKey: "agent.image.tag"},
			{Repository: "acr.example.com/caller", TagKey: "caller.image.tag"},
		},
	})
	if len(wh.Spec.Subscriptions) != 2 {
		t.Fatalf("Subscriptions: got %d want 2", len(wh.Spec.Subscriptions))
	}
	if wh.Spec.Subscriptions[0].Image.RepoURL != "acr.example.com/agent" ||
		wh.Spec.Subscriptions[1].Image.RepoURL != "acr.example.com/caller" {
		t.Errorf("per-image subscriptions not built: %+v", wh.Spec.Subscriptions)
	}
}

// TestBuildKargoWarehouse_DedupesSharedImage covers a composed app whose
// components share an image: several KargoImage entries carry the same Repository
// (each owning a different component's tag key). The Warehouse must emit ONE
// subscription per repo — duplicate subscriptions make Kargo's freight admission
// webhook reject every Freight with "multiple artifacts found for subscription".
func TestBuildKargoWarehouse_DedupesSharedImage(t *testing.T) {
	app := &domain.App{Name: "voiceai-lk-sh", ProjectName: "voiceai"}
	wh := gitops.BuildKargoWarehouse(app, gitops.KargoBuildOptions{
		Images: []gitops.KargoImage{
			{Name: "server", Repository: "acr.example.com/livekit", TagKey: "components.server.image.tag", TagPattern: "^[0-9a-f]{8}$"},
			{Name: "worker", Repository: "acr.example.com/livekit", TagKey: "components.worker.image.tag", TagPattern: "^[0-9a-f]{8}$"},
		},
	})
	if len(wh.Spec.Subscriptions) != 1 {
		t.Fatalf("Subscriptions: got %d want 1 (deduped by repo): %+v", len(wh.Spec.Subscriptions), wh.Spec.Subscriptions)
	}
	if wh.Spec.Subscriptions[0].Image.RepoURL != "acr.example.com/livekit" {
		t.Errorf("subscription repo = %q, want acr.example.com/livekit", wh.Spec.Subscriptions[0].Image.RepoURL)
	}
}

func TestBuildKargoWarehouse_Deterministic(t *testing.T) {
	app := &domain.App{Name: "hello", ProjectName: "demo"}
	opts := gitops.KargoBuildOptions{}

	w1 := gitops.BuildKargoWarehouse(app, opts)
	w2 := gitops.BuildKargoWarehouse(app, opts)

	if w1.Metadata.Name != w2.Metadata.Name ||
		w1.Spec.Subscriptions[0].Image.RepoURL != w2.Spec.Subscriptions[0].Image.RepoURL {
		t.Error("BuildKargoWarehouse is not deterministic")
	}
}

func TestBuildKargoStage_DirectSource(t *testing.T) {
	app := &domain.App{Name: "hello", ProjectName: "demo"}
	env := domain.AppEnvironment{
		AppName:     "hello",
		ProjectName: "demo",
		EnvName:     "staging",
		EnvType:     domain.AppEnvStaging,
		Namespace:   "demo-hello-staging",
	}

	stage := gitops.BuildKargoStage(app, env, nil, gitops.KargoBuildOptions{})

	if stage.Kind != "Stage" {
		t.Errorf("Kind: got %q want %q", stage.Kind, "Stage")
	}
	// Stages live in the per-project kargo-{project} namespace, so the Stage name
	// is just {app}-{env}.
	if stage.Metadata.Name != "hello-staging" {
		t.Errorf("Name: got %q want %q", stage.Metadata.Name, "hello-staging")
	}
	if stage.Metadata.Namespace != "kargo-demo" {
		t.Errorf("Namespace: got %q want %q", stage.Metadata.Namespace, "kargo-demo")
	}
	if len(stage.Spec.RequestedFreight) != 1 {
		t.Fatalf("RequestedFreight: got %d want 1", len(stage.Spec.RequestedFreight))
	}
	req := stage.Spec.RequestedFreight[0]
	if req.Origin.Name != "hello" {
		t.Errorf("Origin.Name: got %q want %q", req.Origin.Name, "hello")
	}
	if !req.Sources.Direct {
		t.Error("expected Direct=true for first stage (no upstream stages)")
	}
	if len(req.Sources.Stages) != 0 {
		t.Errorf("Sources.Stages: got %v want empty", req.Sources.Stages)
	}
}

func TestBuildKargoStage_UpstreamStages(t *testing.T) {
	app := &domain.App{Name: "hello", ProjectName: "demo"}
	env := domain.AppEnvironment{
		AppName:     "hello",
		ProjectName: "demo",
		EnvName:     "prod",
		EnvType:     domain.AppEnvProd,
		Namespace:   "demo-hello-prod",
	}

	stage := gitops.BuildKargoStage(app, env, []string{"staging"}, gitops.KargoBuildOptions{})

	req := stage.Spec.RequestedFreight[0]
	if req.Sources.Direct {
		t.Error("expected Direct=false for stage with upstream")
	}
	if len(req.Sources.Stages) != 1 || req.Sources.Stages[0] != "hello-staging" {
		t.Errorf("Sources.Stages: got %v want [hello-staging]", req.Sources.Stages)
	}
}

// findStep returns the first promotion step with the given `uses`.
func findStep(t *testing.T, steps []gitops.PromotionStep, uses string) gitops.PromotionStep {
	t.Helper()
	for _, s := range steps {
		if s.Uses == uses {
			return s
		}
	}
	t.Fatalf("no promotion step with uses=%q in %+v", uses, steps)
	return gitops.PromotionStep{}
}

func TestBuildKargoStage_PromotionTemplateSyncsArgoApp(t *testing.T) {
	app := &domain.App{Name: "hello", ProjectName: "demo"}
	env := domain.AppEnvironment{EnvName: "staging", EnvType: domain.AppEnvStaging}

	stage := gitops.BuildKargoStage(app, env, nil, gitops.KargoBuildOptions{
		Images:        []gitops.KargoImage{{Repository: "r", TagKey: "image.tag"}},
		GitOpsRepoURL: "http://gitops.example.com/gitops.git",
		// Default pattern {project}-{app}-{cluster}; refresh one app per cluster.
		Clusters: []gitops.ClusterTarget{{Name: "staging-eastus"}, {Name: "staging-westus"}},
	})

	if stage.Spec.PromotionTemplate == nil {
		t.Fatal("PromotionTemplate is nil")
	}
	steps := stage.Spec.PromotionTemplate.Spec.Steps
	argo := findStep(t, steps, "argocd-update")
	apps, _ := argo.Config["apps"].([]map[string]any)
	if len(apps) != 2 {
		t.Fatalf("argocd-update apps: got %+v, want one per cluster", apps)
	}
	if apps[0]["name"] != "demo-hello-staging-eastus" || apps[0]["namespace"] != "argocd" {
		t.Errorf("apps[0] = %+v, want demo-hello-staging-eastus in argocd", apps[0])
	}
	if apps[1]["name"] != "demo-hello-staging-westus" {
		t.Errorf("apps[1] = %+v, want demo-hello-staging-westus", apps[1])
	}
}

// TestBuildKargoStage_ArgoUpdateFallback covers the no-cluster fallback: the
// argocd-update target uses the in-cluster cluster token.
func TestBuildKargoStage_ArgoUpdateFallback(t *testing.T) {
	app := &domain.App{Name: "hello", ProjectName: "demo"}
	env := domain.AppEnvironment{EnvName: "staging", EnvType: domain.AppEnvStaging}
	stage := gitops.BuildKargoStage(app, env, nil, gitops.KargoBuildOptions{
		Images:        []gitops.KargoImage{{Repository: "r", TagKey: "image.tag"}},
		GitOpsRepoURL: "http://gitops.example.com/gitops.git",
	})
	apps, _ := findStep(t, stage.Spec.PromotionTemplate.Spec.Steps, "argocd-update").Config["apps"].([]map[string]any)
	if len(apps) != 1 || apps[0]["name"] != "demo-hello-in-cluster" {
		t.Errorf("fallback argocd-update apps = %+v, want demo-hello-in-cluster", apps)
	}
}

func TestBuildKargoStage_PromotionStepsUpdateValues(t *testing.T) {
	app := &domain.App{Name: "color-app", ProjectName: "demo"}
	env := domain.AppEnvironment{
		AppName: "color-app", ProjectName: "demo",
		EnvName: "staging", EnvType: domain.AppEnvStaging,
	}
	opts := gitops.KargoBuildOptions{
		Images: []gitops.KargoImage{{
			Repository: "kind-registry:5000/demo/color-app",
			TagKey:     "components.web.image.tag",
		}},
		GitOpsRepoURL: "http://gitea-http.gitea.svc:3000/gitops/gitops.git",
	}
	stage := gitops.BuildKargoStage(app, env, nil, opts)
	if stage.Spec.PromotionTemplate == nil {
		t.Fatal("PromotionTemplate is nil")
	}
	steps := stage.Spec.PromotionTemplate.Spec.Steps

	// git-clone targets the gitops repo at ./src
	clone := findStep(t, steps, "git-clone")
	if clone.Config["repoURL"] != "http://gitea-http.gitea.svc:3000/gitops/gitops.git" {
		t.Errorf("git-clone repoURL: got %v", clone.Config["repoURL"])
	}

	// yaml-update writes the env values.yaml under ./src with the imageFrom expr
	yu := findStep(t, steps, "yaml-update")
	if yu.Config["path"] != "./src/envs/staging/demo/color-app/values.yaml" {
		t.Errorf("yaml-update path: got %v", yu.Config["path"])
	}
	updates, _ := yu.Config["updates"].([]map[string]any)
	if len(updates) != 1 || updates[0]["key"] != "components.web.image.tag" {
		t.Fatalf("yaml-update updates: got %+v", updates)
	}
	if got := updates[0]["value"]; got != `${{ imageFrom("kind-registry:5000/demo/color-app").Tag }}` {
		t.Errorf("yaml-update value: got %v", got)
	}

	// commit + push present (push checks the chain completes)
	findStep(t, steps, "git-commit")
	findStep(t, steps, "git-push")
}

func TestBuildKargoStage_MultipleImageKeys(t *testing.T) {
	// A multi-service chart writes one yaml-update entry per service, each with
	// that service's tag key + imageFrom expression.
	app := &domain.App{Name: "multi", ProjectName: "demo"}
	env := domain.AppEnvironment{EnvName: "staging", EnvType: domain.AppEnvStaging}
	opts := gitops.KargoBuildOptions{
		Images: []gitops.KargoImage{
			{Repository: "acr.example.com/agent", TagKey: "agent.image.tag"},
			{Repository: "acr.example.com/caller", TagKey: "caller.image.tag"},
		},
		GitOpsRepoURL: "http://gitops.example.com/gitops.git",
	}
	stage := gitops.BuildKargoStage(app, env, nil, opts)
	yu := findStep(t, stage.Spec.PromotionTemplate.Spec.Steps, "yaml-update")
	updates, _ := yu.Config["updates"].([]map[string]any)
	if len(updates) != 2 {
		t.Fatalf("yaml-update updates: got %d want 2", len(updates))
	}
	if updates[0]["key"] != "agent.image.tag" || updates[1]["key"] != "caller.image.tag" {
		t.Errorf("per-service keys not written: %+v", updates)
	}
}

func TestBuildKargoStage_NoPromotionTemplateWithoutRepoURL(t *testing.T) {
	app := &domain.App{Name: "hello", ProjectName: "demo"}
	env := domain.AppEnvironment{EnvName: "staging", EnvType: domain.AppEnvStaging}
	stage := gitops.BuildKargoStage(app, env, nil, gitops.KargoBuildOptions{})

	if stage.Spec.PromotionTemplate != nil {
		t.Errorf("expected nil PromotionTemplate without GitOpsRepoURL, got %+v", stage.Spec.PromotionTemplate)
	}
}

func TestKargoNamespaceForProject(t *testing.T) {
	if got := gitops.KargoNamespaceForProject("demo"); got != "kargo-demo" {
		t.Errorf("KargoNamespaceForProject = %q, want kargo-demo", got)
	}
}

func TestKargoStageName(t *testing.T) {
	if got := gitops.KargoStageName("hello", "staging"); got != "hello-staging" {
		t.Errorf("KargoStageName = %q, want hello-staging", got)
	}
}

func TestMergeKargoPromotionPolicies(t *testing.T) {
	// Within a project namespace, multiple apps share one ProjectConfig; a per-app
	// publish must merge (not clobber) other apps' policies.
	existing := []gitops.KargoPromotionPolicy{
		{Stage: "web-staging", AutoPromotionEnabled: true},
		{Stage: "web-prod"},
		{Stage: "api-staging", AutoPromotionEnabled: true},
	}
	// Re-publish web with only one env: its old prod policy must drop, api
	// must be untouched.
	merged := gitops.MergeKargoPromotionPolicies(existing, "web", []gitops.KargoPromotionPolicy{
		{Stage: "web-staging", AutoPromotionEnabled: true},
	})
	want := []string{"api-staging", "web-staging"} // sorted by Stage
	if len(merged) != len(want) {
		t.Fatalf("merged = %+v, want stages %v", merged, want)
	}
	for i, w := range want {
		if merged[i].Stage != w {
			t.Errorf("merged[%d].Stage = %q, want %q", i, merged[i].Stage, w)
		}
	}

	// Teardown: empty appPolicies removes web entirely.
	removed := gitops.MergeKargoPromotionPolicies(existing, "web", nil)
	for _, p := range removed {
		if p.Stage == "web-staging" || p.Stage == "web-prod" {
			t.Errorf("web policy %q should have been removed", p.Stage)
		}
	}
}

func TestDefaultImageRepoURL(t *testing.T) {
	got := gitops.DefaultImageRepoURL("demo", "hello")
	if got != "ghcr.io/demo/hello" {
		t.Errorf("got %q want %q", got, "ghcr.io/demo/hello")
	}
}

func TestBuildKargoProjectNamespace(t *testing.T) {
	ns := gitops.BuildKargoProjectNamespace("demo")

	if ns.APIVersion != "v1" {
		t.Errorf("APIVersion: got %q want %q", ns.APIVersion, "v1")
	}
	if ns.Kind != "Namespace" {
		t.Errorf("Kind: got %q want %q", ns.Kind, "Namespace")
	}
	if ns.Metadata.Name != "demo" {
		t.Errorf("Name: got %q want %q", ns.Metadata.Name, "demo")
	}
	if ns.Metadata.Labels["kargo.akuity.io/project"] != "true" {
		t.Errorf("missing kargo.akuity.io/project label, labels: %v", ns.Metadata.Labels)
	}
}

func TestBuildKargoProjectNamespace_Deterministic(t *testing.T) {
	n1 := gitops.BuildKargoProjectNamespace("myproject")
	n2 := gitops.BuildKargoProjectNamespace("myproject")
	if n1.Metadata.Name != n2.Metadata.Name || n1.Metadata.Labels["kargo.akuity.io/project"] != n2.Metadata.Labels["kargo.akuity.io/project"] {
		t.Error("BuildKargoProjectNamespace is not deterministic")
	}
}

// Auto-promote: staging always auto-promotes; prod auto-promotes only when the
// app opts in (CD.AutoPromote → KargoProjectEnv.AutoPromote on every env).
func TestBuildKargoPromotionPolicies_AutoPromote(t *testing.T) {
	stagingProd := func(autoPromote bool) []gitops.KargoProjectEnv {
		return []gitops.KargoProjectEnv{
			{AppName: "web", EnvName: "staging", IsFirstStage: true, AutoPromote: autoPromote},
			{AppName: "web", EnvName: "prod", IsFirstStage: false, AutoPromote: autoPromote},
		}
	}

	// Default (opted out): staging auto, prod manual — today's behavior.
	off := gitops.BuildKargoPromotionPolicies(stagingProd(false))
	if len(off) != 2 || !off[0].AutoPromotionEnabled || off[1].AutoPromotionEnabled {
		t.Fatalf("opted out: want staging auto + prod manual, got %+v", off)
	}

	// Opted in: prod auto-promotes too, staging still auto.
	on := gitops.BuildKargoPromotionPolicies(stagingProd(true))
	if !on[0].AutoPromotionEnabled || !on[1].AutoPromotionEnabled {
		t.Fatalf("opted in: want staging+prod auto, got %+v", on)
	}
	if on[1].Stage != gitops.KargoStageName("web", "prod") {
		t.Errorf("prod policy stage = %q", on[1].Stage)
	}
}

// A pinned stage has auto-promotion forced off — even staging (the first stage),
// which is otherwise hardwired to auto-promote from the Warehouse.
func TestBuildKargoPromotionPolicies_PinnedDisablesAuto(t *testing.T) {
	envs := []gitops.KargoProjectEnv{
		{AppName: "web", EnvName: "staging", IsFirstStage: true, Pinned: true},
		{AppName: "web", EnvName: "prod", IsFirstStage: false, AutoPromote: true, Pinned: true},
	}
	for _, p := range gitops.BuildKargoPromotionPolicies(envs) {
		if p.AutoPromotionEnabled {
			t.Errorf("pinned stage %q must have autoPromotionEnabled=false", p.Stage)
		}
	}
}
