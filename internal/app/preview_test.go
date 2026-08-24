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
				{Name: "web", Type: domain.ComponentWeb, Enabled: true, ExposeMode: domain.ExposeExternal},
				{Name: "worker", Type: domain.ComponentWorker, Enabled: true},
			},
			PreviewsEnabled: true,
		},
	}
}

func workerOnlyApp() *domain.App {
	return &domain.App{
		Name:        "jobs",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "worker"},
			Components: []domain.ComponentSpec{
				{Name: "worker", Type: domain.ComponentWorker, Enabled: true},
			},
			PreviewsEnabled: true,
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

func TestCreatePreview_PreviewsDisabled(t *testing.T) {
	app := previewApp()
	app.Spec.PreviewsEnabled = false
	_, err := CreatePreview(PreviewRequest{App: app, PreviewName: "pr-1", BuildOpts: defaultBuildOpts()})
	if err == nil {
		t.Fatal("expected error when previews are disabled for the app")
	}
}

func TestCreatePreview_NoEnabledComponents(t *testing.T) {
	// Previews are an app-level concept: with PreviewsEnabled set, the app
	// previews as a whole regardless of per-component enablement (the component
	// gate is gone). The preview renders exactly what the base env renders.
	app := previewApp()
	for i := range app.Spec.Components {
		app.Spec.Components[i].Enabled = false
	}
	result, err := CreatePreview(PreviewRequest{App: app, PreviewName: "pr-1", BuildOpts: defaultBuildOpts()})
	if err != nil {
		t.Fatalf("preview should succeed for an app with previews enabled: %v", err)
	}
	if result == nil || result.Instance == nil {
		t.Fatal("expected a preview result")
	}
}

func TestCreatePreview_WorkerOnlyApp(t *testing.T) {
	// A worker-only app (PreviewsEnabled, one enabled worker) now produces a
	// valid preview — the per-component preview gate is gone.
	result, err := CreatePreview(PreviewRequest{App: workerOnlyApp(), PreviewName: "pr-1", BuildOpts: defaultBuildOpts()})
	if err != nil {
		t.Fatalf("unexpected error for worker-only app: %v", err)
	}
	if result.Instance == nil {
		t.Error("expected a preview instance for a worker-only app")
	}
}

func TestCreatePreview_NoURLForUnexposedApp(t *testing.T) {
	// A worker/agent app exposes no HTTP route, so the preview has no URL and the
	// UI shows no "Open" link.
	result, err := CreatePreview(PreviewRequest{App: workerOnlyApp(), PreviewName: "pr-1", BuildOpts: defaultBuildOpts()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Instance.URL != "" {
		t.Errorf("URL = %q, want empty for an app with no exposed component", result.Instance.URL)
	}
}

func TestCreatePreview_URLUsesBaseDomain(t *testing.T) {
	// An exposed app's preview URL uses the base env's real domain, not the
	// fabricated localhost default.
	result, err := CreatePreview(PreviewRequest{
		App:         previewApp(),
		PreviewName: "pr-42",
		BaseDomain:  "staging.acme.com",
		BuildOpts:   defaultBuildOpts(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "http://pr-42.hello.preview.staging.acme.com"
	if result.Instance.URL != want {
		t.Errorf("URL = %q, want %q", result.Instance.URL, want)
	}
}

func TestCreatePreview_SecureSchemeOnURL(t *testing.T) {
	// Secure (the org's secure-endpoints setting) picks the URL scheme:
	// https for TLS installs, http for local/dev ingress.
	secure, err := CreatePreview(PreviewRequest{
		App: previewApp(), PreviewName: "pr-42", BuildOpts: defaultBuildOpts(), Secure: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "https://pr-42.hello.preview.localhost"; secure.Instance.URL != want {
		t.Errorf("secure URL = %q, want %q", secure.Instance.URL, want)
	}
	insecure, err := CreatePreview(PreviewRequest{
		App: previewApp(), PreviewName: "pr-42", BuildOpts: defaultBuildOpts(), Secure: false,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "http://pr-42.hello.preview.localhost"; insecure.Instance.URL != want {
		t.Errorf("insecure URL = %q, want %q", insecure.Instance.URL, want)
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

	wantNS := domain.GeneratePreviewNamespaceFromPattern("hello", "pr-42", "demo", "")
	if result.Instance.Namespace != wantNS {
		t.Errorf("Namespace = %q, want %q", result.Instance.Namespace, wantNS)
	}
	// Concrete value from the default pattern "{project}-{app}-preview-{name}".
	if result.Instance.Namespace != "demo-hello-preview-pr-42" {
		t.Errorf("Namespace = %q, want demo-hello-preview-pr-42", result.Instance.Namespace)
	}
}

func TestCreatePreview_NamespaceCustomPattern(t *testing.T) {
	// A project-supplied pattern overrides the default.
	result, err := CreatePreview(PreviewRequest{
		App: previewApp(), PreviewName: "pr-42", NamespacePattern: "{app}-{name}",
		BuildOpts: defaultBuildOpts(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
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

	wantURL := domain.GenerateURL("hello", "pr-42", domain.AppEnvPreview, false)
	if result.Instance.URL != wantURL {
		t.Errorf("URL = %q, want %q", result.Instance.URL, wantURL)
	}
	// Concrete expected value: "http://pr-42.hello.preview.localhost"
	if result.Instance.URL != "http://pr-42.hello.preview.localhost" {
		t.Errorf("URL = %q, want http://pr-42.hello.preview.localhost", result.Instance.URL)
	}
}

// --- CreatePreview Helm values: all enabled components render ---

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
	if argoApp.Spec.Destination.Namespace != "demo-hello-preview-pr-42" {
		t.Errorf("ArgoApp.Destination.Namespace = %q, want demo-hello-preview-pr-42", argoApp.Spec.Destination.Namespace)
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
		{"pr-1", "demo-hello-preview-pr-1", "http://pr-1.hello.preview.localhost"},
		{"feature-login", "demo-hello-preview-feature-login", "http://feature-login.hello.preview.localhost"},
		{"hotfix-123", "demo-hello-preview-hotfix-123", "http://hotfix-123.hello.preview.localhost"},
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
