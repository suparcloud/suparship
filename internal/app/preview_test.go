package app

import (
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/gitops"
)

// --- shared fixtures ---

func previewApp() *domain.App {
	return &domain.App{
		Name:        "hello",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "web-service"},
			Values: map[string]any{
				"image_repository": "ghcr.io/org/hello",
				"image_tag":        "latest",
			},
			Components: []domain.ComponentSpec{
				{Name: "web", Type: domain.ComponentWeb, Enabled: true, Expose: true, PreviewEnabled: true},
				{Name: "worker", Type: domain.ComponentWorker, Enabled: true, PreviewEnabled: false},
			},
		},
	}
}

func workerOnlyApp() *domain.App {
	return &domain.App{
		Name:        "jobs",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Template:   domain.AppTemplateRef{Name: "worker"},
			Components: []domain.ComponentSpec{
				{Name: "worker", Type: domain.ComponentWorker, Enabled: true, PreviewEnabled: false},
			},
		},
	}
}

func defaultBuildOpts() gitops.BuildOptions {
	return gitops.BuildOptions{RepoURL: "https://github.com/org/gitops"}
}

// --- CreatePreview error paths ---

func TestCreatePreview_NilApp(t *testing.T) {
	_, err := CreatePreview(PreviewRequest{App: nil, PreviewName: "pr-1", BuildOpts: defaultBuildOpts()})
	if err == nil {
		t.Fatal("expected error for nil app")
	}
}

func TestCreatePreview_EmptyPreviewName(t *testing.T) {
	_, err := CreatePreview(PreviewRequest{App: previewApp(), PreviewName: "", BuildOpts: defaultBuildOpts()})
	if err == nil {
		t.Fatal("expected error for empty preview name")
	}
}

func TestCreatePreview_NoPreviewEnabledComponents(t *testing.T) {
	_, err := CreatePreview(PreviewRequest{App: workerOnlyApp(), PreviewName: "pr-1", BuildOpts: defaultBuildOpts()})
	if err == nil {
		t.Fatal("expected error when no preview-enabled components")
	}
}

// --- CreatePreview happy path ---

func TestCreatePreview_EnvironmentInstance(t *testing.T) {
	result, err := CreatePreview(PreviewRequest{
		App:         previewApp(),
		PreviewName: "pr-42",
		BuildOpts:   defaultBuildOpts(),
	})
	if err != nil {
		t.Fatalf("CreatePreview returned error: %v", err)
	}

	inst := result.Instance
	if inst == nil {
		t.Fatal("Instance is nil")
	}
	if inst.EnvType != domain.AppEnvPreview {
		t.Errorf("EnvType = %q, want preview", inst.EnvType)
	}
	if inst.EnvName != "pr-42" {
		t.Errorf("EnvName = %q, want pr-42", inst.EnvName)
	}
	if inst.AppName != "hello" {
		t.Errorf("AppName = %q, want hello", inst.AppName)
	}
	if inst.ProjectName != "demo" {
		t.Errorf("ProjectName = %q, want demo", inst.ProjectName)
	}
	if inst.Status.Phase != domain.StatusNotDeployed {
		t.Errorf("Status.Phase = %q, want %q", inst.Status.Phase, domain.StatusNotDeployed)
	}
}

func TestCreatePreview_Namespace(t *testing.T) {
	result, err := CreatePreview(PreviewRequest{
		App: previewApp(), PreviewName: "pr-42", BuildOpts: defaultBuildOpts(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantNS := domain.GenerateNamespace("hello", "pr-42", domain.AppEnvPreview)
	if result.Instance.Namespace != wantNS {
		t.Errorf("Namespace = %q, want %q", result.Instance.Namespace, wantNS)
	}
	// Concrete expected value from the convention: "<app>-<envName>"
	if result.Instance.Namespace != "hello-pr-42" {
		t.Errorf("Namespace = %q, want hello-pr-42", result.Instance.Namespace)
	}
}

func TestCreatePreview_URL(t *testing.T) {
	result, err := CreatePreview(PreviewRequest{
		App: previewApp(), PreviewName: "pr-42", BuildOpts: defaultBuildOpts(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantURL := domain.GenerateURL("hello", "pr-42", domain.AppEnvPreview)
	if result.Instance.URL != wantURL {
		t.Errorf("URL = %q, want %q", result.Instance.URL, wantURL)
	}
	// Concrete expected value: "http://pr-42.hello.preview.localhost"
	if result.Instance.URL != "http://pr-42.hello.preview.localhost" {
		t.Errorf("URL = %q, want http://pr-42.hello.preview.localhost", result.Instance.URL)
	}
}

// --- CreatePreview Helm values: preview_enabled components only ---

func TestCreatePreview_HelmValues_PreviewEnabledOnly(t *testing.T) {
	result, err := CreatePreview(PreviewRequest{
		App: previewApp(), PreviewName: "pr-42", BuildOpts: defaultBuildOpts(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hv := result.HelmValues
	webComp, ok := hv.Components["web"]
	if !ok {
		t.Fatal("web component missing from HelmValues")
	}
	if !webComp.Enabled {
		t.Error("web component should be enabled in preview (PreviewEnabled=true)")
	}

	workerComp, ok := hv.Components["worker"]
	if !ok {
		t.Fatal("worker component missing from HelmValues")
	}
	if workerComp.Enabled {
		t.Error("worker component should be disabled in preview (PreviewEnabled=false)")
	}
}

func TestCreatePreview_HelmValues_AppContext(t *testing.T) {
	result, err := CreatePreview(PreviewRequest{
		App: previewApp(), PreviewName: "pr-42", BuildOpts: defaultBuildOpts(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.HelmValues.App.Name != "hello" {
		t.Errorf("HelmValues.App.Name = %q, want hello", result.HelmValues.App.Name)
	}
	if result.HelmValues.App.Env != "pr-42" {
		t.Errorf("HelmValues.App.Env = %q, want pr-42", result.HelmValues.App.Env)
	}
}

func TestCreatePreview_HelmValues_RoutingHost(t *testing.T) {
	result, err := CreatePreview(PreviewRequest{
		App: previewApp(), PreviewName: "pr-42", BuildOpts: defaultBuildOpts(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Routing host should be the preview hostname without scheme.
	wantHost := "pr-42.hello.preview.localhost"
	if result.HelmValues.Routing.Host != wantHost {
		t.Errorf("Routing.Host = %q, want %q", result.HelmValues.Routing.Host, wantHost)
	}
}

func TestCreatePreview_HelmValues_PreviewReplicas(t *testing.T) {
	result, err := CreatePreview(PreviewRequest{
		App: previewApp(), PreviewName: "pr-1", BuildOpts: defaultBuildOpts(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	webComp := result.HelmValues.Components["web"]
	// Preview environments use 1 replica by default.
	if webComp.Replicas != 1 {
		t.Errorf("web replicas = %d, want 1 (preview default)", webComp.Replicas)
	}
}

// --- CreatePreview ArgoCD application ---

func TestCreatePreview_ArgoApp(t *testing.T) {
	result, err := CreatePreview(PreviewRequest{
		App:         previewApp(),
		PreviewName: "pr-42",
		BuildOpts:   defaultBuildOpts(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	argoApp := result.ArgoApp
	if argoApp == nil {
		t.Fatal("ArgoApp is nil")
	}
	if argoApp.Metadata.Name != "demo-hello-pr-42" {
		t.Errorf("ArgoApp.Name = %q, want hello-pr-42", argoApp.Metadata.Name)
	}
	if argoApp.Spec.Destination.Namespace != "hello-pr-42" {
		t.Errorf("ArgoApp.Destination.Namespace = %q, want hello-pr-42", argoApp.Spec.Destination.Namespace)
	}
	if argoApp.Metadata.Labels["suparship.io/env-type"] != "preview" {
		t.Errorf("ArgoApp env-type label = %q, want preview", argoApp.Metadata.Labels["suparship.io/env-type"])
	}
}

func TestCreatePreview_ArgoApp_DefaultRepoPath(t *testing.T) {
	result, err := CreatePreview(PreviewRequest{
		App: previewApp(), PreviewName: "pr-42", BuildOpts: defaultBuildOpts(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantPath := "demo/hello/pr-42"
	if result.ArgoApp.Spec.Source.Path != wantPath {
		t.Errorf("ArgoApp Source.Path = %q, want %q", result.ArgoApp.Spec.Source.Path, wantPath)
	}
}

func TestCreatePreview_ArgoApp_SyncAutomated(t *testing.T) {
	result, err := CreatePreview(PreviewRequest{
		App:         previewApp(),
		PreviewName: "pr-42",
		BuildOpts: gitops.BuildOptions{
			RepoURL:       "https://github.com/org/gitops",
			SyncAutomated: true,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ArgoApp.Spec.SyncPolicy == nil {
		t.Fatal("SyncPolicy is nil, want automated sync")
	}
	if result.ArgoApp.Spec.SyncPolicy.Automated == nil {
		t.Fatal("SyncPolicy.Automated is nil")
	}
}

// --- Determinism ---

func TestCreatePreview_Determinism(t *testing.T) {
	req := PreviewRequest{
		App:         previewApp(),
		PreviewName: "pr-42",
		BuildOpts:   defaultBuildOpts(),
	}

	a, err := CreatePreview(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, err := CreatePreview(req)
	if err != nil {
		t.Fatalf("unexpected error on second call: %v", err)
	}

	if a.Instance.Namespace != b.Instance.Namespace {
		t.Error("CreatePreview is non-deterministic: namespaces differ")
	}
	if a.Instance.URL != b.Instance.URL {
		t.Error("CreatePreview is non-deterministic: URLs differ")
	}
	if a.ArgoApp.Metadata.Name != b.ArgoApp.Metadata.Name {
		t.Error("CreatePreview is non-deterministic: ArgoApp names differ")
	}
}

// --- Multi-preview name variants ---

func TestCreatePreview_PreviewNameVariants(t *testing.T) {
	tests := []struct {
		previewName   string
		wantNS        string
		wantURLPrefix string
	}{
		{"pr-1", "hello-pr-1", "http://pr-1.hello.preview.localhost"},
		{"feature-login", "hello-feature-login", "http://feature-login.hello.preview.localhost"},
		{"hotfix-123", "hello-hotfix-123", "http://hotfix-123.hello.preview.localhost"},
	}

	for _, tc := range tests {
		t.Run(tc.previewName, func(t *testing.T) {
			result, err := CreatePreview(PreviewRequest{
				App:         previewApp(),
				PreviewName: tc.previewName,
				BuildOpts:   defaultBuildOpts(),
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Instance.Namespace != tc.wantNS {
				t.Errorf("Namespace = %q, want %q", result.Instance.Namespace, tc.wantNS)
			}
			if result.Instance.URL != tc.wantURLPrefix {
				t.Errorf("URL = %q, want %q", result.Instance.URL, tc.wantURLPrefix)
			}
		})
	}
}
