package server

import (
	"context"
	"net/http"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/secrets"
)

// SetupGate is one step of the platform-setup checklist for the SRE team
// standing up suparship. Unlike the boolean flags below — which only report
// "is it configured" — a gate reports whether the step is actually usable,
// with a remediation message and a UI route to fix it.
type SetupGate struct {
	// Key is a stable identifier, e.g. "auth", "gitops", "clusters",
	// "prerequisites", "secret-backend", "environments".
	Key string `json:"key"`
	// Title is the human-readable step name.
	Title string `json:"title"`
	// Status is "ok", "incomplete", or "error".
	Status string `json:"status"`
	// Message explains an incomplete/error gate (empty when ok).
	Message string `json:"message,omitempty"`
	// Action is a UI route hint for fixing the gate, e.g. "/settings/gitops".
	Action string `json:"action,omitempty"`
}

const (
	gateOK         = "ok"
	gateIncomplete = "incomplete"
	gateError      = "error"
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
	// Gates is the ordered platform-setup checklist with real readiness per
	// step. PlatformReady is true when every non-optional gate is ok.
	Gates         []SetupGate `json:"gates,omitempty"`
	PlatformReady bool        `json:"platformReady"`
}

// clusterLister is the subset of the cluster store the onboarding gates need.
type clusterLister interface {
	ListClusters(ctx context.Context) ([]domain.Cluster, error)
}

// onboardingHandler serves the onboarding status endpoint.
type onboardingHandler struct {
	orgProvider  rbac.OrgProvider               // nil when cluster not reachable
	projectStore project.Store                  // nil when cluster not reachable
	authEnabled  bool                           // true when authenticator was configured
	clusterStore clusterLister                  // optional: enables the clusters/secrets gates
	gitopsCheck  func(ctx context.Context) bool // optional: reports whether a gitops repo is configured
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

// SetupGateInputs is the already-gathered state computeSetupGates turns into an
// ordered checklist. Kept as plain values so the computation is pure and unit
// testable without live providers.
type SetupGateInputs struct {
	AuthConfigured   bool
	GitOpsConfigured bool
	OrgName          string
	Environments     []rbac.OrgEnvironment
	Backend          secrets.BackendConfig
	ClusterNames     []string
}

// computeSetupGates derives the ordered platform-setup checklist. Pure.
func computeSetupGates(in SetupGateInputs) []SetupGate {
	gates := make([]SetupGate, 0, 6)

	// 1. Admin auth.
	auth := SetupGate{Key: "auth", Title: "Admin authentication", Action: ""}
	if in.AuthConfigured {
		auth.Status = gateOK
	} else {
		auth.Status = gateError
		auth.Message = "No admin auth configured. Run `suparship admin bootstrap` in the server pod."
	}
	gates = append(gates, auth)

	// 2. GitOps repo.
	git := SetupGate{Key: "gitops", Title: "GitOps repository", Action: "/settings/gitops"}
	if in.GitOpsConfigured {
		git.Status = gateOK
		git.Message = "Configured. Use \"Test connection\" to verify reachability."
	} else {
		git.Status = gateError
		git.Message = "No GitOps repo configured — apps can't be published until one is set."
	}
	gates = append(gates, git)

	// 3. Workload clusters.
	cl := SetupGate{Key: "clusters", Title: "Workload clusters", Action: "/settings/clusters"}
	switch {
	case len(in.ClusterNames) == 0:
		cl.Status = gateError
		cl.Message = "Register at least one workload cluster to deploy to."
	default:
		cl.Status = gateOK
	}
	gates = append(gates, cl)

	// 4. Environments bound to clusters.
	boundClusters := boundClusterNames(in.Environments)
	env := SetupGate{Key: "environments", Title: "Environments", Action: "/settings/org"}
	switch {
	case len(in.Environments) == 0:
		env.Status = gateError
		env.Message = "Define at least one environment (e.g. staging) and bind it to a cluster."
	case len(boundClusters) == 0:
		env.Status = gateIncomplete
		env.Message = "Environments exist but none is bound to a cluster. Assign a cluster under Settings → Environments."
	default:
		env.Status = gateOK
	}
	gates = append(gates, env)

	// 5. Secret backend completeness (1Password multi-step setup).
	sb := SetupGate{Key: "secret-backend", Title: "Secret backend", Action: "/settings/org"}
	envNames := make([]string, 0, len(in.Environments))
	for _, e := range in.Environments {
		envNames = append(envNames, e.Name)
	}
	if ready, msg := in.Backend.SetupComplete(envNames, boundClusters); ready {
		sb.Status = gateOK
	} else {
		sb.Status = gateIncomplete
		sb.Message = msg
	}
	gates = append(gates, sb)

	return gates
}

// boundClusterNames returns the distinct clusters at least one env deploys to.
func boundClusterNames(envs []rbac.OrgEnvironment) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range envs {
		ref := e.EffectiveClusterRef()
		if ref != "" && !seen[ref] {
			seen[ref] = true
			out = append(out, ref)
		}
	}
	return out
}

func (oh *onboardingHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	resp := ComputeStatus(ctx, oh.authEnabled, oh.orgProvider, oh.projectStore)

	in := SetupGateInputs{AuthConfigured: oh.authEnabled}
	if oh.gitopsCheck != nil {
		in.GitOpsConfigured = oh.gitopsCheck(ctx)
	}
	if oh.orgProvider != nil {
		if org, err := oh.orgProvider.GetOrg(ctx); err == nil && org != nil {
			in.OrgName = org.Name
			in.Environments = org.Environments
			in.Backend = org.SecretBackend
		}
	}
	if oh.clusterStore != nil {
		if clusters, err := oh.clusterStore.ListClusters(ctx); err == nil {
			for _, c := range clusters {
				in.ClusterNames = append(in.ClusterNames, c.Name)
			}
		}
	}

	resp.Gates = computeSetupGates(in)
	resp.PlatformReady = true
	for _, g := range resp.Gates {
		if g.Status != gateOK {
			resp.PlatformReady = false
			break
		}
	}
	writeJSON(w, http.StatusOK, resp)
}
