package gitops

import (
	"testing"

	"github.com/suparcloud/suparship/internal/secrets"
)

func TestRenderArgoAppName_Default(t *testing.T) {
	pattern := secrets.ResourceNaming{}.EffectiveArgoAppName() // "{project}-{app}-{cluster}"
	got := RenderArgoAppName(pattern, "demo", "color-app", "staging", "staging-eastus")
	if want := "demo-color-app-staging-eastus"; got != want {
		t.Errorf("RenderArgoAppName = %q, want %q", got, want)
	}
}

func TestRenderArgoAppName_CustomWithEnv(t *testing.T) {
	got := RenderArgoAppName("{project}-{app}-{env}-{cluster}", "demo", "api", "prod", "prod-westus")
	if want := "demo-api-prod-prod-westus"; got != want {
		t.Errorf("RenderArgoAppName = %q, want %q", got, want)
	}
}

func TestRenderArgoAppNameTemplate(t *testing.T) {
	// {env} becomes a literal (AppSet is per-env); the rest become ArgoCD params.
	got := RenderArgoAppNameTemplate(secrets.DefaultArgoAppName, "staging")
	if want := "{{project}}-{{name}}-{{clusterName}}"; got != want {
		t.Errorf("template = %q, want %q", got, want)
	}
	gotEnv := RenderArgoAppNameTemplate("{project}-{app}-{env}-{cluster}", "staging")
	if want := "{{project}}-{{name}}-staging-{{clusterName}}"; gotEnv != want {
		t.Errorf("template w/ env = %q, want %q", gotEnv, want)
	}
}

func TestResourceNaming_ArgoAppName_Validate(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		wantErr bool
	}{
		{"empty ok (uses default)", "", false},
		{"valid with cluster", "{project}-{app}-{cluster}", false},
		{"valid with env+cluster", "{project}-{app}-{env}-{cluster}", false},
		{"missing cluster", "{project}-{app}-{env}", true},
		{"missing app", "{project}-{cluster}", true},
		{"renders to invalid dns", "{project}_{app}_{cluster}", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := secrets.ResourceNaming{ArgoAppName: tc.pattern}.Validate()
			if tc.wantErr && err == nil {
				t.Errorf("expected error for %q, got nil", tc.pattern)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error for %q: %v", tc.pattern, err)
			}
		})
	}
}
