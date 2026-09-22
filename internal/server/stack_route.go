package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
)

// Stack fan-out of the host swap (see app_route.go): route every member's
// stable env hostname to the member's preview of the same name, and restore.
// Two-phase like pin: spec prep per member (concurrent, no git), then ONE
// batched preview publish and ONE batched app publish.

// stackRouteRequest routes a stable env's hostname to a PR preview group across
// the stack. Members without the named preview, not deployed to targetEnv, or
// exposing no HTTP route are skipped, not failed. Apps optionally narrows to a
// subset (default: all).
type stackRouteRequest struct {
	FromPreview string   `json:"fromPreview"`
	TargetEnv   string   `json:"targetEnv"`
	Apps        []string `json:"apps,omitempty"`
}

// prodEnv reports whether envName is a production env of the project (any
// member resolves the same org env set, so the first is representative).
func (rh *rbacHandler) prodEnv(ctx context.Context, members []*domain.App, envName string) bool {
	if len(members) == 0 || rh.appHandler == nil {
		return false
	}
	for _, e := range rh.appHandler.stableEnvsFromOrg(ctx, members[0]) {
		if e.EnvName == envName {
			return e.EnvType == domain.AppEnvProd
		}
	}
	return false
}

// handleRouteStack serves POST .../stacks/{stack}/route.
func (rh *rbacHandler) handleRouteStack(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	name := r.PathValue("stack")
	if rh.appHandler == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "app store not configured"})
		return
	}
	var req stackRouteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	req.FromPreview = strings.TrimSpace(req.FromPreview)
	req.TargetEnv = strings.TrimSpace(req.TargetEnv)
	if req.FromPreview == "" || req.TargetEnv == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "fromPreview and targetEnv are required"})
		return
	}
	if _, err := rh.stackStore.GetStack(r.Context(), project, name); err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "stack not found: " + name})
		return
	}
	members, err := selectStackMembers(rh.stackMemberApps(r.Context(), project, name), req.Apps)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	if !rh.validTargetEnv(r.Context(), members, req.TargetEnv) {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "unknown target environment: " + req.TargetEnv})
		return
	}
	if rh.prodEnv(r.Context(), members, req.TargetEnv) {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: errRouteTargetIsProd.Error()})
		return
	}
	op := func(ctx context.Context) (int, any, error) {
		return http.StatusOK, rh.routeStackExec(ctx, project, name, members, req), nil
	}
	dispatchOp(w, r, rh.appHandler.async, "route-stack", project, op)
}

// routeStackExec runs the two-phase route for the resolved members and returns
// the per-member batch result. Previews publish first (they take the hostnames),
// then the stable envs (they move to their "-origin" hosts) — same ordering as
// the per-app op, batched. On a publish failure every published member's spec
// is reverted so a retry starts clean.
func (rh *rbacHandler) routeStackExec(ctx context.Context, project, name string, members []*domain.App, req stackRouteRequest) stackBatchResponse {
	ah := rh.appHandler
	type routePrep struct {
		app       *domain.App
		targetEnv *domain.AppEnvironment
		preview   *domain.AppEnvironment
		prev      string
		err       error
	}
	prepStart := time.Now()
	preps := prepMembers(members, func(a *domain.App) routePrep {
		app, targetEnv, preview, prev, err := ah.routeAppEnvSpec(ctx, project, a.Name, req.TargetEnv, req.FromPreview)
		return routePrep{app: app, targetEnv: targetEnv, preview: preview, prev: prev, err: err}
	})
	prepDur := time.Since(prepStart)
	baseDomain, secure := ah.previewRoutingForEnv(ctx, req.TargetEnv)

	results := make([]stackOpResult, 0, len(members))
	var previewTargets []PreviewPublishTarget
	var focus []appFocusPublish
	type routed struct {
		prep        routePrep
		prevPreview *domain.AppEnvironment // previously routed preview moving back, if any
	}
	var pending []routed
	for i, a := range members {
		p := preps[i]
		switch {
		case p.err == nil:
			item := routed{prep: p}
			if p.prev != "" && p.prev != req.FromPreview {
				if old, gerr := ah.appStore.GetAppEnvironment(ctx, project, a.Name, p.prev); gerr == nil && old.EnvType == domain.AppEnvPreview {
					item.prevPreview = old
					previewTargets = append(previewTargets, ah.previewPublishTarget(ctx, p.app, old))
				}
			}
			previewTargets = append(previewTargets, ah.previewPublishTarget(ctx, p.app, p.preview))
			focus = append(focus, appFocusPublish{app: p.app, focusEnv: p.targetEnv})
			pending = append(pending, item)
		case routeIsSkippable(p.err):
			results = append(results, skipResult(a.Name, p.err.Error()))
		default:
			results = append(results, errResult(a.Name, p.err))
		}
	}

	pubStart := time.Now()
	if len(pending) > 0 {
		err := ah.publishPreviewsBatch(ctx, previewTargets)
		if err == nil {
			err = ah.republishAppsFocus(ctx, focus)
		}
		if err != nil {
			for _, it := range pending {
				if rerr := ah.setRoutedToPreview(ctx, it.prep.app, req.TargetEnv, it.prep.prev); rerr != nil {
					slog.Warn("stack route: failed to revert spec after publish failure", "project", project, "app", it.prep.app.Name, "err", rerr)
				}
				results = append(results, errResult(it.prep.app.Name, err))
			}
		} else {
			for _, it := range pending {
				if it.prevPreview != nil {
					ah.savePreviewURL(ctx, it.prep.app, it.prevPreview, previewURLFor(it.prep.app, it.prevPreview.EnvName, baseDomain, secure))
				}
				url := previewURLFor(it.prep.app, req.FromPreview, baseDomain, secure)
				ah.savePreviewURL(ctx, it.prep.app, it.prep.preview, url)
				results = append(results, okResult(it.prep.app.Name, "routed "+hostOf(url)+" → "+req.FromPreview))
			}
		}
	}
	slog.Info("stack route timing",
		"project", project, "stack", name,
		"members", len(members), "published", len(pending),
		"prep", prepDur.Round(time.Millisecond).String(),
		"publish", time.Since(pubStart).Round(time.Millisecond).String(),
	)
	return stackBatchResponse{Project: project, Stack: name, Action: "route", Results: results}
}

// handleUnrouteStack serves DELETE .../stacks/{stack}/route with a JSON body
// {targetEnv, apps?} — symmetric with handleRouteStack. Members whose env is
// not routed are skipped.
func (rh *rbacHandler) handleUnrouteStack(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	name := r.PathValue("stack")
	if rh.appHandler == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "app store not configured"})
		return
	}
	var req stackSuspendRequest // {targetEnv, apps} — same shape as unpin
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	req.TargetEnv = strings.TrimSpace(req.TargetEnv)
	if req.TargetEnv == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "targetEnv is required"})
		return
	}
	if _, err := rh.stackStore.GetStack(r.Context(), project, name); err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "stack not found: " + name})
		return
	}
	members, err := selectStackMembers(rh.stackMemberApps(r.Context(), project, name), req.Apps)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	if !rh.validTargetEnv(r.Context(), members, req.TargetEnv) {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "unknown target environment: " + req.TargetEnv})
		return
	}
	op := func(ctx context.Context) (int, any, error) {
		return http.StatusOK, rh.unrouteStackExec(ctx, project, name, members, req), nil
	}
	dispatchOp(w, r, rh.appHandler.async, "unroute-stack", project, op)
}

// unrouteStackExec restores every routed member: stable envs first (ONE batched
// publish — they take their hostnames back), then the previews still on record
// (ONE batched publish — back to their own hosts). A failed env publish reverts
// those members' specs for a retry.
func (rh *rbacHandler) unrouteStackExec(ctx context.Context, project, name string, members []*domain.App, req stackSuspendRequest) stackBatchResponse {
	ah := rh.appHandler
	type unroutePrep struct {
		app       *domain.App
		targetEnv *domain.AppEnvironment
		preview   string
		wasRouted bool
		err       error
	}
	preps := prepMembers(members, func(a *domain.App) unroutePrep {
		app, targetEnv, preview, wasRouted, err := ah.unrouteAppEnvSpec(ctx, project, a.Name, req.TargetEnv)
		return unroutePrep{app: app, targetEnv: targetEnv, preview: preview, wasRouted: wasRouted, err: err}
	})
	results := make([]stackOpResult, 0, len(members))
	var focus []appFocusPublish
	var pending []unroutePrep
	for i, a := range members {
		p := preps[i]
		switch {
		case p.err != nil:
			results = append(results, errResult(a.Name, p.err))
		case !p.wasRouted:
			results = append(results, skipResult(a.Name, "not routed"))
		default:
			focus = append(focus, appFocusPublish{app: p.app, focusEnv: p.targetEnv})
			pending = append(pending, p)
		}
	}
	if len(pending) == 0 {
		return stackBatchResponse{Project: project, Stack: name, Action: "unroute", Results: results}
	}
	if err := ah.republishAppsFocus(ctx, focus); err != nil {
		for _, p := range pending {
			if rerr := ah.setRoutedToPreview(ctx, p.app, req.TargetEnv, p.preview); rerr != nil {
				slog.Warn("stack unroute: failed to revert spec after publish failure", "project", project, "app", p.app.Name, "err", rerr)
			}
			results = append(results, errResult(p.app.Name, err))
		}
		return stackBatchResponse{Project: project, Stack: name, Action: "unroute", Results: results}
	}
	// Previews back to their own hosts — those still on record.
	baseDomain, secure := ah.previewRoutingForEnv(ctx, req.TargetEnv)
	var previewTargets []PreviewPublishTarget
	type restored struct {
		p       unroutePrep
		preview *domain.AppEnvironment
	}
	var withPreview []restored
	for _, p := range pending {
		pe, gerr := ah.appStore.GetAppEnvironment(ctx, project, p.app.Name, p.preview)
		if gerr != nil || pe.EnvType != domain.AppEnvPreview {
			results = append(results, okResult(p.app.Name, "restored "+req.TargetEnv+" hostname"))
			continue
		}
		previewTargets = append(previewTargets, ah.previewPublishTarget(ctx, p.app, pe))
		withPreview = append(withPreview, restored{p: p, preview: pe})
	}
	if len(withPreview) == 0 {
		return stackBatchResponse{Project: project, Stack: name, Action: "unroute", Results: results}
	}
	if err := ah.publishPreviewsBatch(ctx, previewTargets); err != nil {
		for _, r := range withPreview {
			results = append(results, errResult(r.p.app.Name, err))
		}
		return stackBatchResponse{Project: project, Stack: name, Action: "unroute", Results: results}
	}
	for _, r := range withPreview {
		ah.savePreviewURL(ctx, r.p.app, r.preview, previewURLFor(r.p.app, r.p.preview, baseDomain, secure))
		results = append(results, okResult(r.p.app.Name, "restored "+req.TargetEnv+" hostname; "+r.p.preview+" back on its preview URL"))
	}
	return stackBatchResponse{Project: project, Stack: name, Action: "unroute", Results: results}
}

// restoreRoutingForDeletedStackPreview hands stable env hostnames back before a
// stack preview is torn down: every member whose env routes to the preview has
// its swap cleared, then all are republished in ONE batch. Members whose
// restore failed get an error row, have their swap reinstated, and are
// reported in the returned set so the caller skips their prune (retryable).
func (rh *rbacHandler) restoreRoutingForDeletedStackPreview(ctx context.Context, members []*domain.App, preview string, results *[]stackOpResult) map[string]bool {
	ah := rh.appHandler
	failed := map[string]bool{}
	var items []appFocusPublish
	for _, a := range members {
		app, gerr := ah.appStore.GetApp(ctx, a.ProjectName, a.Name)
		if gerr != nil {
			continue
		}
		item, err := ah.clearRoutingForPreview(ctx, app, preview)
		if err != nil {
			*results = append(*results, errResult(a.Name, err))
			failed[a.Name] = true
			continue
		}
		if item != nil {
			items = append(items, *item)
		}
	}
	if len(items) == 0 {
		return failed
	}
	if err := ah.republishAppsFocus(ctx, items); err != nil {
		for _, it := range items {
			if rerr := ah.setRoutedToPreview(ctx, it.app, it.focusEnv.EnvName, preview); rerr != nil {
				slog.Warn("stack preview delete: failed to revert routing after publish failure", "app", it.app.Name, "err", rerr)
			}
			*results = append(*results, errResult(it.app.Name, err))
			failed[it.app.Name] = true
		}
	}
	return failed
}
