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
	if wh.Metadata.Namespace != "demo" {
		t.Errorf("Namespace: got %q want %q", wh.Metadata.Namespace, "demo")
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
		ImageRepoURL:   "registry.example.com/myproject/api",
		ImageTagPattern: `^v\d+\.\d+\.\d+$`,
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
	// Kargo Stage name = {app}-{env}; project is encoded in the
	// namespace (Stage.Metadata.Namespace), so it doesn't need to
	// repeat in the name. Distinct from ArgoCD Applications, which all
	// share the argocd namespace and thus need {project}-{app}-{env}.
	if stage.Metadata.Name != "hello-staging" {
		t.Errorf("Name: got %q want %q", stage.Metadata.Name, "hello-staging")
	}
	if stage.Metadata.Namespace != "demo" {
		t.Errorf("Namespace: got %q want %q", stage.Metadata.Namespace, "demo")
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

func TestBuildKargoStage_PromotionMechanismsIncludesArgoApp(t *testing.T) {
	app := &domain.App{Name: "hello", ProjectName: "demo"}
	env := domain.AppEnvironment{EnvName: "staging", EnvType: domain.AppEnvStaging}

	stage := gitops.BuildKargoStage(app, env, nil, gitops.KargoBuildOptions{})

	if stage.Spec.PromotionMechanisms == nil {
		t.Fatal("PromotionMechanisms is nil")
	}
	updates := stage.Spec.PromotionMechanisms.ArgoCDAppUpdates
	if len(updates) == 0 {
		t.Fatal("PromotionMechanisms has no ArgoCDAppUpdates")
	}
	if updates[0].AppName != "demo-hello-staging" {
		t.Errorf("ArgoCDAppUpdates[0].AppName: got %q want %q", updates[0].AppName, "demo-hello-staging")
	}
	if updates[0].AppNamespace != "argocd" {
		t.Errorf("ArgoCDAppUpdates[0].AppNamespace: got %q want %q", updates[0].AppNamespace, "argocd")
	}
}

func TestBuildKargoStage_GitRepoUpdates(t *testing.T) {
	app := &domain.App{
		Name:        "color-app",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Values: map[string]any{
				"image_repository": "kind-registry:5000/demo/color-app",
			},
		},
	}
	env := domain.AppEnvironment{
		AppName:     "color-app",
		ProjectName: "demo",
		EnvName:     "staging",
		EnvType:     domain.AppEnvStaging,
	}

	opts := gitops.KargoBuildOptions{
		ImageRepoURL:   "kind-registry:5000/demo/color-app",
		GitOpsRepoURL:  "http://gitea-http.gitea.svc:3000/gitops/gitops.git",
		GitOpsRepoInsecure: true,
	}
	stage := gitops.BuildKargoStage(app, env, nil, opts)

	pm := stage.Spec.PromotionMechanisms
	if pm == nil {
		t.Fatal("PromotionMechanisms is nil")
	}
	if len(pm.GitRepoUpdates) != 1 {
		t.Fatalf("GitRepoUpdates: got %d want 1", len(pm.GitRepoUpdates))
	}
	gru := pm.GitRepoUpdates[0]
	if gru.RepoURL != "http://gitea-http.gitea.svc:3000/gitops/gitops.git" {
		t.Errorf("RepoURL: got %q", gru.RepoURL)
	}
	if gru.ReadBranch != "main" {
		t.Errorf("ReadBranch: got %q want %q", gru.ReadBranch, "main")
	}
	if gru.WriteBranch != "main" {
		t.Errorf("WriteBranch: got %q want %q", gru.WriteBranch, "main")
	}
	if !gru.InsecureSkipTLSVerify {
		t.Error("expected InsecureSkipTLSVerify=true")
	}
	if gru.Helm == nil || len(gru.Helm.Images) != 1 {
		t.Fatal("expected 1 Helm image update")
	}
	img := gru.Helm.Images[0]
	if img.Image != "kind-registry:5000/demo/color-app" {
		t.Errorf("Image: got %q", img.Image)
	}
	if img.ValuesFilePath != "envs/staging/demo/color-app/values.yaml" {
		t.Errorf("ValuesFilePath: got %q", img.ValuesFilePath)
	}
	if img.Key != "components.web.image.tag" {
		t.Errorf("Key: got %q", img.Key)
	}
	if img.Value != "Tag" {
		t.Errorf("Value: got %q want %q", img.Value, "Tag")
	}
}

func TestBuildKargoStage_ImageTagKeyFromCDConfig(t *testing.T) {
	// An app whose chart keeps the tag at the root "image.tag" key (e.g. the
	// voiceai-livekit chart) must have Kargo write that same key — otherwise
	// the promotion edits a non-existent path and the publisher preserves a
	// different one. CD.ImageTagPath is the single source of truth for both.
	app := &domain.App{
		Name:        "livekit-express-caller",
		ProjectName: "voiceai",
		Spec:        domain.AppSpec{CD: domain.CDConfig{Managed: true, ImageTagPath: "image.tag"}},
	}
	env := domain.AppEnvironment{EnvName: "staging", EnvType: domain.AppEnvStaging}
	opts := gitops.KargoBuildOptions{
		ImageRepoURL:  "acr.example.com/voiceai-livekit",
		GitOpsRepoURL: "http://gitops.example.com/gitops.git",
	}

	stage := gitops.BuildKargoStage(app, env, nil, opts)
	img := stage.Spec.PromotionMechanisms.GitRepoUpdates[0].Helm.Images[0]
	if img.Key != "image.tag" {
		t.Errorf("Key: got %q want %q", img.Key, "image.tag")
	}
}

func TestDetectImageTagKey(t *testing.T) {
	cases := []struct {
		name   string
		values map[string]any
		want   string
	}{
		{
			name:   "root image block",
			values: map[string]any{"image": map[string]any{"repository": "r", "tag": "t"}},
			want:   "image.tag",
		},
		{
			name: "web component image",
			values: map[string]any{"components": map[string]any{
				"web": map[string]any{"image": map[string]any{"tag": "t"}},
			}},
			want: "components.web.image.tag",
		},
		{
			name: "non-web component falls to lexically first",
			values: map[string]any{"components": map[string]any{
				"worker": map[string]any{"image": map[string]any{"tag": "t"}},
				"api":    map[string]any{"image": map[string]any{"tag": "t"}},
			}},
			want: "components.api.image.tag",
		},
		{
			name: "web preferred over other components",
			values: map[string]any{"components": map[string]any{
				"api": map[string]any{"image": map[string]any{"tag": "t"}},
				"web": map[string]any{"image": map[string]any{"tag": "t"}},
			}},
			want: "components.web.image.tag",
		},
		{
			name:   "no image block",
			values: map[string]any{"replicas": 2},
			want:   "",
		},
		{
			name:   "image is a string, not a map",
			values: map[string]any{"image": "repo:tag"},
			want:   "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := gitops.DetectImageTagKey(tc.values); got != tc.want {
				t.Errorf("DetectImageTagKey = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildKargoStage_NoGitRepoUpdatesWithoutRepoURL(t *testing.T) {
	app := &domain.App{Name: "hello", ProjectName: "demo"}
	env := domain.AppEnvironment{EnvName: "staging", EnvType: domain.AppEnvStaging}
	stage := gitops.BuildKargoStage(app, env, nil, gitops.KargoBuildOptions{})

	if len(stage.Spec.PromotionMechanisms.GitRepoUpdates) != 0 {
		t.Errorf("expected no GitRepoUpdates without GitOpsRepoURL, got %d", len(stage.Spec.PromotionMechanisms.GitRepoUpdates))
	}
}

func TestKargoNamespaceForProject(t *testing.T) {
	got := gitops.KargoNamespaceForProject("demo")
	if got != "demo" {
		t.Errorf("got %q want %q", got, "demo")
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
