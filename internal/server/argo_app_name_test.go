package server

import (
	"context"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/secrets"
)

func clusterAppNames(apps []argoClusterApp) []string {
	out := make([]string, len(apps))
	for i, a := range apps {
		out[i] = a.name
	}
	return out
}

// argoAppNamesForEnv resolves the org ArgoAppName pattern once per deploy-target
// cluster: one entry for an active env, one per cluster for a fan-out ("all")
// env, and an in-cluster fallback when no cluster resolves.
func TestAppHandler_ArgoAppNamesForEnv(t *testing.T) {
	org := &rbac.Org{
		Environments: []rbac.OrgEnvironment{
			{Name: "staging", ClusterRefs: []string{"staging-eastus"}},
			{Name: "prod", DeployMode: rbac.DeployModeAll, ClusterRefs: []string{"prod-eastus", "prod-westus"}},
			{Name: "edge"}, // no cluster → in-cluster fallback
		},
	}
	ah := &appHandler{orgProvider: &staticOrgProvider{org: org}}
	ctx := context.Background()

	if got := clusterAppNames(ah.argoAppNamesForEnv(ctx, "demo", "web", "staging")); len(got) != 1 || got[0] != "demo-web-staging-eastus" {
		t.Errorf("staging = %v, want [demo-web-staging-eastus]", got)
	}
	// Fan-out env: one Application per cluster.
	got := clusterAppNames(ah.argoAppNamesForEnv(ctx, "demo", "web", "prod"))
	if len(got) != 2 || got[0] != "demo-web-prod-eastus" || got[1] != "demo-web-prod-westus" {
		t.Errorf("prod = %v, want [demo-web-prod-eastus demo-web-prod-westus]", got)
	}
	if got := clusterAppNames(ah.argoAppNamesForEnv(ctx, "demo", "web", "edge")); len(got) != 1 || got[0] != "demo-web-in-cluster" {
		t.Errorf("edge = %v, want [demo-web-in-cluster]", got)
	}

	// Custom pattern keeping {env}.
	org2 := *org
	org2.ResourceNaming = secrets.ResourceNaming{ArgoAppName: "{project}-{app}-{env}-{cluster}"}
	ah2 := &appHandler{orgProvider: &staticOrgProvider{org: &org2}}
	if got := clusterAppNames(ah2.argoAppNamesForEnv(ctx, "demo", "web", "staging")); got[0] != "demo-web-staging-staging-eastus" {
		t.Errorf("custom = %v, want demo-web-staging-staging-eastus", got)
	}
}

// fakeDiagReader returns canned diagnostics keyed by ArgoCD Application name.
type fakeDiagReader struct{ byApp map[string][]domain.Diagnostic }

func (f *fakeDiagReader) GetAppDiagnostics(_ context.Context, app, _ string) ([]domain.Diagnostic, error) {
	return f.byApp[app], nil
}

// enrichEnvWithDiagnostics must read EVERY cluster's Application in a fan-out
// env, so a failure on a secondary (non-active) cluster is surfaced and tagged
// with its cluster.
func TestEnrichEnvWithDiagnostics_AggregatesClusters(t *testing.T) {
	org := &rbac.Org{Environments: []rbac.OrgEnvironment{
		{Name: "prod", DeployMode: rbac.DeployModeAll, ClusterRefs: []string{"prod-eastus", "prod-westus"}},
	}}
	// Only the westus cluster's chart Application is degraded.
	reader := &fakeDiagReader{byApp: map[string][]domain.Diagnostic{
		"demo-web-prod-westus": {{Source: "argocd", Level: domain.DiagnosticError, Title: "Sync failed"}},
	}}
	ah := &appHandler{orgProvider: &staticOrgProvider{org: org}, diagnosticsReader: reader}

	env := &domain.AppEnvironment{ProjectName: "demo", EnvName: "prod"}
	ah.enrichEnvWithDiagnostics(context.Background(), "web", env)

	var found bool
	for _, d := range env.Status.Diagnostics {
		if d.Title == "Sync failed" {
			found = true
			if d.Cluster != "prod-westus" {
				t.Errorf("diagnostic cluster = %q, want prod-westus", d.Cluster)
			}
		}
	}
	if !found {
		t.Fatalf("secondary-cluster sync failure not surfaced; diagnostics = %+v", env.Status.Diagnostics)
	}
}

// A single-cluster env leaves diagnostics untagged (Cluster empty).
func TestEnrichEnvWithDiagnostics_SingleClusterUntagged(t *testing.T) {
	org := &rbac.Org{Environments: []rbac.OrgEnvironment{
		{Name: "staging", ClusterRefs: []string{"staging-eastus"}},
	}}
	reader := &fakeDiagReader{byApp: map[string][]domain.Diagnostic{
		"demo-web-staging-eastus": {{Source: "argocd", Level: domain.DiagnosticError, Title: "Sync failed"}},
	}}
	ah := &appHandler{orgProvider: &staticOrgProvider{org: org}, diagnosticsReader: reader}

	env := &domain.AppEnvironment{ProjectName: "demo", EnvName: "staging"}
	ah.enrichEnvWithDiagnostics(context.Background(), "web", env)

	if len(env.Status.Diagnostics) != 1 || env.Status.Diagnostics[0].Cluster != "" {
		t.Errorf("single-cluster diagnostics should be untagged, got %+v", env.Status.Diagnostics)
	}
}
