package server

import (
	"context"
	"encoding/json"
	"fmt"
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
	dispatchOpAsyncDefault(w, r, rh.appHandler.async, "route-stack", project, op)
}

// routeStackExec runs the route for the resolved members and returns the
// per-member batch result, in the order the ingress admission webhook demands
// (see app_route.go): first everything that RELEASES a hostname — envs moving
// to their "-origin" hosts (one batched app publish) and previously routed
// previews moving back to their own (one batched preview publish) — then, once
// ArgoCD has applied those, the previews CLAIMING the hostnames (one batched
// preview publish). On a publish failure every published member's spec is
// reverted so a retry starts clean.
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
	var releasePreviews, claimPreviews []PreviewPublishTarget
	var releaseEnvs []appFocusPublish
	var releaseRefs []envRef
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
			switch {
			case p.prev != "" && p.prev != req.FromPreview:
				// Replacing: the old preview releases the hostname (the env is
				// already on its -origin host).
				if old, gerr := ah.appStore.GetAppEnvironment(ctx, project, a.Name, p.prev); gerr == nil && old.EnvType == domain.AppEnvPreview {
					item.prevPreview = old
					releasePreviews = append(releasePreviews, ah.previewPublishTarget(ctx, p.app, old))
					releaseRefs = append(releaseRefs, envRef{project, a.Name, p.prev})
				}
			case p.prev == "":
				// Fresh route: the env releases the hostname by moving to -origin.
				releaseEnvs = append(releaseEnvs, appFocusPublish{app: p.app, focusEnv: p.targetEnv})
				releaseRefs = append(releaseRefs, envRef{project, a.Name, req.TargetEnv})
			}
			claimPreviews = append(claimPreviews, ah.previewPublishTarget(ctx, p.app, p.preview))
			pending = append(pending, item)
		case routeIsSkippable(p.err):
			results = append(results, skipResult(a.Name, p.err.Error()))
		default:
			results = append(results, errResult(a.Name, p.err))
		}
	}

	pubStart := time.Now()
	if len(pending) > 0 {
		reportProgress(ctx, "release", fmt.Sprintf("publishing %d member env(s) on their -origin hosts", len(releaseEnvs)))
		err := ah.republishAppsFocus(ctx, releaseEnvs)
		if err == nil {
			err = ah.publishPreviewsBatch(ctx, releasePreviews)
		}
		if err == nil {
			ah.waitForEnvsSettled(ctx, releaseRefs, pubStart)
			reportProgress(ctx, "claim", fmt.Sprintf("publishing %d preview(s) on the %s hostnames", len(claimPreviews), req.TargetEnv))
			err = ah.publishPreviewsBatch(ctx, claimPreviews)
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
	dispatchOpAsyncDefault(w, r, rh.appHandler.async, "unroute-stack", project, op)
}

// unrouteStackExec restores every routed member in webhook order: the previews
// still on record release the hostnames first (ONE batched publish — back to
// their own hosts), ArgoCD applies that, then the stable envs take their
// hostnames back (ONE batched publish). A failed publish reverts those members'
// specs for a retry.
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
	revertAll := func(err error) stackBatchResponse {
		for _, p := range pending {
			if rerr := ah.setRoutedToPreview(ctx, p.app, req.TargetEnv, p.preview); rerr != nil {
				slog.Warn("stack unroute: failed to revert spec after publish failure", "project", project, "app", p.app.Name, "err", rerr)
			}
			results = append(results, errResult(p.app.Name, err))
		}
		return stackBatchResponse{Project: project, Stack: name, Action: "unroute", Results: results}
	}
	// Phase 2a: previews still on record release the hostnames.
	baseDomain, secure := ah.previewRoutingForEnv(ctx, req.TargetEnv)
	var previewTargets []PreviewPublishTarget
	var releaseRefs []envRef
	previewOf := map[string]*domain.AppEnvironment{}
	for _, p := range pending {
		pe, gerr := ah.appStore.GetAppEnvironment(ctx, project, p.app.Name, p.preview)
		if gerr != nil || pe.EnvType != domain.AppEnvPreview {
			continue
		}
		previewOf[p.app.Name] = pe
		previewTargets = append(previewTargets, ah.previewPublishTarget(ctx, p.app, pe))
		releaseRefs = append(releaseRefs, envRef{project, p.app.Name, p.preview})
	}
	releaseStart := time.Now()
	reportProgress(ctx, "release", fmt.Sprintf("publishing %d preview(s) back to their own hosts", len(previewTargets)))
	if err := ah.publishPreviewsBatch(ctx, previewTargets); err != nil {
		return revertAll(err)
	}
	for _, p := range pending {
		if pe := previewOf[p.app.Name]; pe != nil {
			ah.savePreviewURL(ctx, p.app, pe, previewURLFor(p.app, p.preview, baseDomain, secure))
		}
	}
	ah.waitForEnvsSettled(ctx, releaseRefs, releaseStart)
	// Phase 2b: envs take their hostnames back.
	reportProgress(ctx, "claim", fmt.Sprintf("publishing %d member env(s) back on their own hostnames", len(focus)))
	if err := ah.republishAppsFocus(ctx, focus); err != nil {
		return revertAll(err)
	}
	for _, p := range pending {
		if previewOf[p.app.Name] != nil {
			results = append(results, okResult(p.app.Name, "restored "+req.TargetEnv+" hostname; "+p.preview+" back on its preview URL"))
		} else {
			results = append(results, okResult(p.app.Name, "restored "+req.TargetEnv+" hostname"))
		}
	}
	return stackBatchResponse{Project: project, Stack: name, Action: "unroute", Results: results}
}

// clearRoutingForDeletedStackPreview is phase 1 of handing hostnames back when
// a stack preview is torn down: every member whose env routes to the preview
// has its swap cleared (spec only) and its env focus item collected for the
// restore publish that follows the prune. Members whose spec save failed get an
// error row and are reported so the caller skips their prune (retryable).
func (rh *rbacHandler) clearRoutingForDeletedStackPreview(ctx context.Context, members []*domain.App, preview string, results *[]stackOpResult) (items []appFocusPublish, failed map[string]bool) {
	ah := rh.appHandler
	failed = map[string]bool{}
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
	return items, failed
}

// restoreRoutingAfterStackPrune is phase 2: once the preview has been pruned
// for every routed member (their Ingresses release the hostnames), wait for
// the preview Applications to be gone, then republish the envs in ONE batch.
// Members whose prune failed keep their swap (reinstated) and are left out.
// A publish failure reinstates every included member's swap so a later
// unroute republishes the envs.
func (rh *rbacHandler) restoreRoutingAfterStackPrune(ctx context.Context, items []appFocusPublish, preview string, pruned map[string]bool, results *[]stackOpResult) {
	ah := rh.appHandler
	var publish []appFocusPublish
	for _, it := range items {
		if !pruned[it.app.Name] {
			_ = ah.setRoutedToPreview(ctx, it.app, it.focusEnv.EnvName, preview)
			continue
		}
		publish = append(publish, it)
	}
	if len(publish) == 0 {
		return
	}
	for _, it := range publish {
		ah.waitForPreviewAppGone(ctx, it.app.ProjectName, it.app.Name, preview)
	}
	if err := ah.republishAppsFocus(ctx, publish); err != nil {
		for _, it := range publish {
			if rerr := ah.setRoutedToPreview(ctx, it.app, it.focusEnv.EnvName, preview); rerr != nil {
				slog.Warn("stack preview delete: failed to reinstate routing after publish failure", "app", it.app.Name, "err", rerr)
			}
			*results = append(*results, errResult(it.app.Name, fmt.Errorf("preview removed, but failed to restore %s's hostname: %w — run unroute to retry", it.focusEnv.EnvName, err)))
		}
	}
}
