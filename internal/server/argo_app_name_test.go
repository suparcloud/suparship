package server

import (
	"context"
	"testing"

	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/secrets"
)

// argoAppName resolves the org's ArgoAppName pattern + the env's effective
// cluster so the status/diagnostics readers look up the same Application name
// the publisher's ApplicationSet renders.
func TestAppHandler_ArgoAppName(t *testing.T) {
	org := &rbac.Org{
		Environments: []rbac.OrgEnvironment{
			{Name: "staging", ClusterRefs: []string{"staging-eastus"}},
			{Name: "prod", ClusterRefs: []string{"prod-eastus", "prod-westus"}, ActiveClusterRef: "prod-westus"},
			{Name: "edge"}, // no cluster → in-cluster fallback
		},
	}

	t.Run("default pattern + active cluster", func(t *testing.T) {
		ah := &appHandler{orgProvider: &staticOrgProvider{org: org}}
		if got := ah.argoAppName(context.Background(), "demo", "web", "staging"); got != "demo-web-staging-eastus" {
			t.Errorf("got %q, want demo-web-staging-eastus", got)
		}
		// ActiveClusterRef wins over ClusterRefs[0].
		if got := ah.argoAppName(context.Background(), "demo", "web", "prod"); got != "demo-web-prod-westus" {
			t.Errorf("got %q, want demo-web-prod-westus", got)
		}
	})

	t.Run("unbound env falls back to in-cluster", func(t *testing.T) {
		ah := &appHandler{orgProvider: &staticOrgProvider{org: org}}
		if got := ah.argoAppName(context.Background(), "demo", "web", "edge"); got != "demo-web-in-cluster" {
			t.Errorf("got %q, want demo-web-in-cluster", got)
		}
	})

	t.Run("custom pattern with env", func(t *testing.T) {
		org2 := *org
		org2.ResourceNaming = secrets.ResourceNaming{ArgoAppName: "{project}-{app}-{env}-{cluster}"}
		ah := &appHandler{orgProvider: &staticOrgProvider{org: &org2}}
		if got := ah.argoAppName(context.Background(), "demo", "web", "staging"); got != "demo-web-staging-staging-eastus" {
			t.Errorf("got %q, want demo-web-staging-staging-eastus", got)
		}
	})
}
