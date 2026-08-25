package server

import (
	"net/http"
	"sort"

	"github.com/suparcloud/suparship/internal/domain"
)

// PreviewGroupDTO is a PR-level preview: all app-previews that share a preview
// name within a project, presented as one item with its per-app previews nested.
type PreviewGroupDTO struct {
	Name    string `json:"name"`    // PR/preview name, e.g. "pr-712"
	Project string `json:"project"` // owning project
	// BaseEnv is the stable env the preview clones (e.g. "staging"). Common across
	// the group's app-previews.
	BaseEnv string `json:"baseEnv,omitempty"`
	// Health is the aggregate phase across the group's app-previews.
	Health string                 `json:"health"`
	Apps   []AppPreviewSummaryDTO `json:"apps"`
}

// PreviewGroupsResponse is the JSON body for GET /api/v1/previews.
type PreviewGroupsResponse struct {
	Previews []PreviewGroupDTO `json:"previews"`
}

// handleListPreviewGroups serves GET /api/v1/previews: previews grouped by PR
// (preview name) across all projects, each carrying its per-app previews. Built
// from the app-scoped preview AppEnvironments (the real source), so single-app
// and stack previews group identically. Each app-preview's live status is
// enriched (base-env-routed) so the group health is accurate.
func (rh *rbacHandler) handleListPreviewGroups(w http.ResponseWriter, r *http.Request) {
	if rh.appHandler == nil || rh.appHandler.appStore == nil || rh.projectStore == nil {
		writeJSON(w, http.StatusOK, PreviewGroupsResponse{Previews: []PreviewGroupDTO{}})
		return
	}
	// Same enrichment machinery as the app-list path: per-request memos (org,
	// apps, one diagnostics snapshot), the short-TTL status cache, and a
	// bounded concurrent fan-out. This endpoint used to enrich every preview
	// serially with none of that — the slowest call on the stack page.
	ctx := withEnrichMemos(r.Context())
	projects, err := rh.projectStore.List(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list projects"})
		return
	}
	// Optional ?project= scopes the result to one project (e.g. the stack page).
	onlyProject := r.URL.Query().Get("project")

	groups := map[string]*PreviewGroupDTO{}
	order := []string{} // stable insertion order before final sort

	// Collect every (project, app, preview-env) first, then enrich with the
	// same bounded concurrency + cache as handleListApps.
	type previewTask struct {
		project string
		appName string
		env     *domain.AppEnvironment
	}
	var tasks []previewTask
	memo := appMemoFrom(ctx)
	for _, p := range projects {
		project := p.Metadata.Name
		if onlyProject != "" && project != onlyProject {
			continue
		}
		apps, err := rh.appHandler.appStore.ListApps(ctx, project)
		if err != nil {
			continue
		}
		for _, app := range apps {
			memo.seed(project, app)
			previews, err := rh.appHandler.appStore.ListAppPreviews(ctx, project, app.Name)
			if err != nil {
				continue
			}
			for _, env := range previews {
				tasks = append(tasks, previewTask{project: project, appName: app.Name, env: env})
			}
		}
	}
	runBounded(len(tasks), appEnrichConcurrency, func(i int) {
		t := tasks[i]
		rh.appHandler.enrichCachedOrLive(ctx, t.project, t.appName, t.env)
	})
	for _, t := range tasks {
		env := t.env
		key := t.project + "\x00" + env.EnvName
		g, ok := groups[key]
		if !ok {
			g = &PreviewGroupDTO{Name: env.EnvName, Project: t.project, BaseEnv: env.BaseEnv}
			groups[key] = g
			order = append(order, key)
		}
		if g.BaseEnv == "" {
			g.BaseEnv = env.BaseEnv
		}
		g.Apps = append(g.Apps, appPreviewToDTO(env))
	}

	out := make([]PreviewGroupDTO, 0, len(order))
	for _, key := range order {
		g := groups[key]
		sort.Slice(g.Apps, func(i, j int) bool { return g.Apps[i].AppName < g.Apps[j].AppName })
		phases := make([]string, len(g.Apps))
		for i, a := range g.Apps {
			phases[i] = a.Status.Phase
		}
		g.Health = previewGroupHealth(phases)
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Project != out[j].Project {
			return out[i].Project < out[j].Project
		}
		return out[i].Name < out[j].Name
	})

	writeJSON(w, http.StatusOK, PreviewGroupsResponse{Previews: out})
}

// previewGroupHealth aggregates per-app preview phases into one PR-level phase:
// Healthy when all are running/idle, else Degraded/Progressing if any is, else
// Healthy when at least one is up (partial), else not_deployed.
func previewGroupHealth(phases []string) string {
	if len(phases) == 0 {
		return domain.StatusNotDeployed
	}
	up := func(p string) bool { return p == domain.StatusHealthy || p == "idle" }
	allUp := true
	for _, p := range phases {
		if !up(p) {
			allUp = false
			break
		}
	}
	if allUp {
		return domain.StatusHealthy
	}
	for _, p := range phases {
		if p == domain.StatusDegraded {
			return domain.StatusDegraded
		}
	}
	for _, p := range phases {
		if p == domain.StatusProgressing {
			return domain.StatusProgressing
		}
	}
	for _, p := range phases {
		if up(p) {
			return domain.StatusHealthy
		}
	}
	return domain.StatusNotDeployed
}
