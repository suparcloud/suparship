package server

import (
	"context"
	"net/http"

	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
)

// OnboardingStatusResponse is the JSON body for GET /api/v1/onboarding/status.
type OnboardingStatusResponse struct {
	ClusterConnected bool `json:"clusterConnected"`
	AuthConfigured   bool `json:"authConfigured"`
	OrgExists        bool `json:"orgExists"`
	HasProjects      bool `json:"hasProjects"`
	HasEnvironments  bool `json:"hasEnvironments"`
	HasServices      bool `json:"hasServices"`
	Complete         bool `json:"complete"`
}

// onboardingHandler serves the onboarding status endpoint.
type onboardingHandler struct {
	orgProvider  rbac.OrgProvider // nil when cluster not reachable
	projectStore project.Store   // nil when cluster not reachable
	authEnabled  bool            // true when authenticator was configured
}

// ComputeStatus derives the onboarding state from the available providers.
// Exported for testing.
func ComputeStatus(
	ctx context.Context,
	authEnabled bool,
	orgProvider rbac.OrgProvider,
	projectStore project.Store,
) OnboardingStatusResponse {
	resp := OnboardingStatusResponse{}

	clusterConnected := orgProvider != nil && projectStore != nil
	resp.ClusterConnected = clusterConnected
	resp.AuthConfigured = authEnabled

	if orgProvider != nil {
		org, err := orgProvider.GetOrg(ctx)
		if err == nil && org != nil && org.Name != "" {
			resp.OrgExists = true
			// Environments are now org-level; a non-empty org environments list
			// means the deployment pipeline is defined.
			if len(org.Environments) > 0 {
				resp.HasEnvironments = true
			}
		}
	}

	if projectStore != nil {
		projects, err := projectStore.List(ctx)
		if err == nil && len(projects) > 0 {
			resp.HasProjects = true

			for _, p := range projects {
				// HasEnvironments is satisfied by org-level environments (checked
				// above) OR by project-specific environments as a fallback.
				if !resp.HasEnvironments && len(p.Spec.Environments) > 0 {
					resp.HasEnvironments = true
				}
				if len(p.Spec.Services) > 0 {
					resp.HasServices = true
				}
				if resp.HasEnvironments && resp.HasServices {
					break
				}
			}
		}
	}

	resp.Complete = resp.ClusterConnected &&
		resp.AuthConfigured &&
		resp.OrgExists &&
		resp.HasEnvironments &&
		resp.HasProjects &&
		resp.HasServices

	return resp
}

func (oh *onboardingHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	resp := ComputeStatus(r.Context(), oh.authEnabled, oh.orgProvider, oh.projectStore)
	writeJSON(w, http.StatusOK, resp)
}
