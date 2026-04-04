package gitops_test

import (
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/gitops"
)

// helloApp is a minimal reusable app fixture.
var helloApp = &domain.App{
	Name:        "hello",
	ProjectName: "demo",
	Spec: domain.AppSpec{
		DisplayName: "Hello",
		Template:    domain.AppTemplateRef{Name: "web-service"},
		Values: map[string]any{
			"image_repository": "ghcr.io/org/hello",
			"image_tag":        "v1.0.0",
		},
		Components: []domain.ComponentSpec{
			{Name: "web", Type: domain.ComponentWeb, Enabled: true, Expose: true, PreviewEnabled: true},
		},
	},
}

func TestApplicationName(t *testing.T) {
	tests := []struct {
		app  string
		env  string
		want string
	}{
		{"hello", "staging", "hello-staging"},
		{"hello", "prod", "hello-prod"},
		{"hello", "pr-42", "hello-pr-42"},
		{"api-gateway", "staging", "api-gateway-staging"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			got := gitops.ApplicationName(tc.app, tc.env)
			if got != tc.want {
				t.Errorf("ApplicationName(%q, %q) = %q, want %q", tc.app, tc.env, got, tc.want)
			}
		})
	}
}

func TestBuildArgoApplication_Defaults(t *testing.T) {
	env := domain.AppEnvironment{
		AppName:     "hello",
		ProjectName: "demo",
		EnvName:     "staging",
		EnvType:     domain.AppEnvStaging,
		Namespace:   "hello-staging",
	}

	got := gitops.BuildArgoApplication(helloApp, env, gitops.BuildOptions{
		RepoURL: "https://github.com/org/gitops",
	})

	// Basic shape
	if got.APIVersion != "argoproj.io/v1alpha1" {
		t.Errorf("APIVersion = %q, want argoproj.io/v1alpha1", got.APIVersion)
	}
	if got.Kind != "Application" {
		t.Errorf("Kind = %q, want Application", got.Kind)
	}

	// Name convention: <app>-<env>
	wantName := "hello-staging"
	if got.Metadata.Name != wantName {
		t.Errorf("Metadata.Name = %q, want %q", got.Metadata.Name, wantName)
	}

	// Default ArgoCD namespace
	if got.Metadata.Namespace != "argocd" {
		t.Errorf("Metadata.Namespace = %q, want argocd", got.Metadata.Namespace)
	}

	// Labels
	if got.Metadata.Labels["suparship.io/app"] != "hello" {
		t.Errorf("label suparship.io/app = %q, want hello", got.Metadata.Labels["suparship.io/app"])
	}
	if got.Metadata.Labels["suparship.io/project"] != "demo" {
		t.Errorf("label suparship.io/project = %q, want demo", got.Metadata.Labels["suparship.io/project"])
	}
	if got.Metadata.Labels["suparship.io/env"] != "staging" {
		t.Errorf("label suparship.io/env = %q, want staging", got.Metadata.Labels["suparship.io/env"])
	}
	if got.Metadata.Labels["suparship.io/env-type"] != "staging" {
		t.Errorf("label suparship.io/env-type = %q, want staging", got.Metadata.Labels["suparship.io/env-type"])
	}

	// Source defaults
	if got.Spec.Source.RepoURL != "https://github.com/org/gitops" {
		t.Errorf("Source.RepoURL = %q", got.Spec.Source.RepoURL)
	}
	wantPath := "gitops-output/demo/hello/staging"
	if got.Spec.Source.Path != wantPath {
		t.Errorf("Source.Path = %q, want %q", got.Spec.Source.Path, wantPath)
	}
	if got.Spec.Source.TargetRevision != "HEAD" {
		t.Errorf("Source.TargetRevision = %q, want HEAD", got.Spec.Source.TargetRevision)
	}

	// No Helm section when no values supplied
	if got.Spec.Source.Helm != nil {
		t.Errorf("Source.Helm should be nil when no values provided, got %+v", got.Spec.Source.Helm)
	}

	// Destination
	if got.Spec.Destination.Namespace != "hello-staging" {
		t.Errorf("Destination.Namespace = %q, want hello-staging", got.Spec.Destination.Namespace)
	}
	if got.Spec.Destination.Server != "https://kubernetes.default.svc" {
		t.Errorf("Destination.Server = %q", got.Spec.Destination.Server)
	}

	// ArgoCD project defaults to suparship project name
	if got.Spec.Project != "demo" {
		t.Errorf("Spec.Project = %q, want demo", got.Spec.Project)
	}

	// No auto-sync by default
	if got.Spec.SyncPolicy != nil {
		t.Errorf("SyncPolicy should be nil by default, got %+v", got.Spec.SyncPolicy)
	}
}

func TestBuildArgoApplication_AllEnvTypes(t *testing.T) {
	tests := []struct {
		name          string
		envName       string
		envType       domain.AppEnvironmentType
		namespace     string
		wantArgoName  string
		wantEnvLabel  string
		wantTypeLabel string
	}{
		{
			name:          "staging",
			envName:       "staging",
			envType:       domain.AppEnvStaging,
			namespace:     "hello-staging",
			wantArgoName:  "hello-staging",
			wantEnvLabel:  "staging",
			wantTypeLabel: "staging",
		},
		{
			name:          "prod",
			envName:       "prod",
			envType:       domain.AppEnvProd,
			namespace:     "hello-prod",
			wantArgoName:  "hello-prod",
			wantEnvLabel:  "prod",
			wantTypeLabel: "prod",
		},
		{
			name:          "preview",
			envName:       "pr-42",
			envType:       domain.AppEnvPreview,
			namespace:     "hello-pr-42",
			wantArgoName:  "hello-pr-42",
			wantEnvLabel:  "pr-42",
			wantTypeLabel: "preview",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := domain.AppEnvironment{
				AppName:     helloApp.Name,
				ProjectName: helloApp.ProjectName,
				EnvName:     tc.envName,
				EnvType:     tc.envType,
				Namespace:   tc.namespace,
			}
			got := gitops.BuildArgoApplication(helloApp, env, gitops.BuildOptions{
				RepoURL: "https://github.com/org/gitops",
			})

			if got.Metadata.Name != tc.wantArgoName {
				t.Errorf("Metadata.Name = %q, want %q", got.Metadata.Name, tc.wantArgoName)
			}
			if got.Spec.Destination.Namespace != tc.namespace {
				t.Errorf("Destination.Namespace = %q, want %q", got.Spec.Destination.Namespace, tc.namespace)
			}
			if got.Metadata.Labels["suparship.io/env"] != tc.wantEnvLabel {
				t.Errorf("label suparship.io/env = %q, want %q", got.Metadata.Labels["suparship.io/env"], tc.wantEnvLabel)
			}
			if got.Metadata.Labels["suparship.io/env-type"] != tc.wantTypeLabel {
				t.Errorf("label suparship.io/env-type = %q, want %q", got.Metadata.Labels["suparship.io/env-type"], tc.wantTypeLabel)
			}
		})
	}
}

func TestBuildArgoApplication_WithValuesFiles(t *testing.T) {
	env := domain.AppEnvironment{
		AppName:   "hello",
		EnvName:   "staging",
		EnvType:   domain.AppEnvStaging,
		Namespace: "hello-staging",
	}

	got := gitops.BuildArgoApplication(helloApp, env, gitops.BuildOptions{
		RepoURL:     "https://github.com/org/gitops",
		ValuesFiles: []string{"values.yaml", "values-staging.yaml"},
	})

	if got.Spec.Source.Helm == nil {
		t.Fatal("Source.Helm is nil, expected Helm section with ValuesFiles")
	}
	if len(got.Spec.Source.Helm.ValueFiles) != 2 {
		t.Errorf("ValueFiles length = %d, want 2", len(got.Spec.Source.Helm.ValueFiles))
	}
	if got.Spec.Source.Helm.ValueFiles[0] != "values.yaml" {
		t.Errorf("ValueFiles[0] = %q, want values.yaml", got.Spec.Source.Helm.ValueFiles[0])
	}
	// ReleaseName matches the Application name
	if got.Spec.Source.Helm.ReleaseName != "hello-staging" {
		t.Errorf("Helm.ReleaseName = %q, want hello-staging", got.Spec.Source.Helm.ReleaseName)
	}
}

func TestBuildArgoApplication_WithInlineValues(t *testing.T) {
	inlineVals := "app:\n  name: hello\n  env: staging\n"
	env := domain.AppEnvironment{
		AppName:   "hello",
		EnvName:   "staging",
		EnvType:   domain.AppEnvStaging,
		Namespace: "hello-staging",
	}

	got := gitops.BuildArgoApplication(helloApp, env, gitops.BuildOptions{
		RepoURL:      "https://github.com/org/gitops",
		InlineValues: inlineVals,
	})

	if got.Spec.Source.Helm == nil {
		t.Fatal("Source.Helm is nil, expected Helm section with InlineValues")
	}
	if got.Spec.Source.Helm.Values != inlineVals {
		t.Errorf("Helm.Values = %q, want %q", got.Spec.Source.Helm.Values, inlineVals)
	}
}

func TestBuildArgoApplication_SyncAutomated(t *testing.T) {
	env := domain.AppEnvironment{
		AppName:   "hello",
		EnvName:   "staging",
		EnvType:   domain.AppEnvStaging,
		Namespace: "hello-staging",
	}

	got := gitops.BuildArgoApplication(helloApp, env, gitops.BuildOptions{
		RepoURL:       "https://github.com/org/gitops",
		SyncAutomated: true,
	})

	if got.Spec.SyncPolicy == nil {
		t.Fatal("SyncPolicy is nil, want automated sync enabled")
	}
	if got.Spec.SyncPolicy.Automated == nil {
		t.Fatal("SyncPolicy.Automated is nil")
	}
	if !got.Spec.SyncPolicy.Automated.Prune {
		t.Error("SyncPolicy.Automated.Prune should be true")
	}
	if !got.Spec.SyncPolicy.Automated.SelfHeal {
		t.Error("SyncPolicy.Automated.SelfHeal should be true")
	}
}

func TestBuildArgoApplication_ExplicitOptions(t *testing.T) {
	env := domain.AppEnvironment{
		AppName:   "hello",
		EnvName:   "prod",
		EnvType:   domain.AppEnvProd,
		Namespace: "hello-prod",
	}

	got := gitops.BuildArgoApplication(helloApp, env, gitops.BuildOptions{
		RepoURL:           "https://github.com/org/gitops",
		RepoPath:          "deploy/hello/prod",
		TargetRevision:    "v1.2.3",
		ArgoCDNamespace:   "argo",
		DestinationServer: "https://my-cluster.example.com",
		ArgoCDProject:     "platform",
		Annotations: map[string]string{
			"notifications.argoproj.io/subscribe.on-sync-succeeded.slack": "deployments",
		},
	})

	if got.Metadata.Namespace != "argo" {
		t.Errorf("Metadata.Namespace = %q, want argo", got.Metadata.Namespace)
	}
	if got.Spec.Source.Path != "deploy/hello/prod" {
		t.Errorf("Source.Path = %q, want deploy/hello/prod", got.Spec.Source.Path)
	}
	if got.Spec.Source.TargetRevision != "v1.2.3" {
		t.Errorf("Source.TargetRevision = %q, want v1.2.3", got.Spec.Source.TargetRevision)
	}
	if got.Spec.Destination.Server != "https://my-cluster.example.com" {
		t.Errorf("Destination.Server = %q", got.Spec.Destination.Server)
	}
	if got.Spec.Project != "platform" {
		t.Errorf("Spec.Project = %q, want platform", got.Spec.Project)
	}
	if got.Metadata.Annotations["notifications.argoproj.io/subscribe.on-sync-succeeded.slack"] != "deployments" {
		t.Errorf("annotation not propagated")
	}
}

func TestBuildArgoApplication_Determinism(t *testing.T) {
	env := domain.AppEnvironment{
		AppName:     "hello",
		ProjectName: "demo",
		EnvName:     "staging",
		EnvType:     domain.AppEnvStaging,
		Namespace:   "hello-staging",
	}
	opts := gitops.BuildOptions{
		RepoURL:      "https://github.com/org/gitops",
		ValuesFiles:  []string{"values.yaml"},
		SyncAutomated: true,
	}

	a := gitops.BuildArgoApplication(helloApp, env, opts)
	b := gitops.BuildArgoApplication(helloApp, env, opts)

	if a.Metadata.Name != b.Metadata.Name {
		t.Error("BuildArgoApplication is non-deterministic: names differ")
	}
	if a.Spec.Source.Path != b.Spec.Source.Path {
		t.Error("BuildArgoApplication is non-deterministic: paths differ")
	}
	if a.Spec.Destination.Namespace != b.Spec.Destination.Namespace {
		t.Error("BuildArgoApplication is non-deterministic: namespaces differ")
	}
}

func TestDefaultRepoPath(t *testing.T) {
	// When RepoPath is empty the builder derives it from project/app/env.
	env := domain.AppEnvironment{
		AppName:     "api-gateway",
		ProjectName: "platform",
		EnvName:     "pr-99",
		EnvType:     domain.AppEnvPreview,
		Namespace:   "api-gateway-pr-99",
	}
	app := &domain.App{
		Name:        "api-gateway",
		ProjectName: "platform",
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "web-service"}},
	}

	got := gitops.BuildArgoApplication(app, env, gitops.BuildOptions{
		RepoURL: "https://github.com/org/gitops",
	})

	wantPath := "gitops-output/platform/api-gateway/pr-99"
	if got.Spec.Source.Path != wantPath {
		t.Errorf("Source.Path = %q, want %q", got.Spec.Source.Path, wantPath)
	}
}
