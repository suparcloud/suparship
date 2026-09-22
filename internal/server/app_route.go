package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	domainapp "github.com/suparcloud/suparship/internal/app"
	"github.com/suparcloud/suparship/internal/domain"
)

// The host swap: route a stable env's hostname to a preview, and restore.
//
// Why this exists next to pin: developers pinned staging to a PR preview's
// image only so external services could exercise the PR at the stable staging
// URL. Pinning freezes staging's image and pauses Kargo (and so auto-promotion
// to prod). Routing swaps the HOSTNAME instead — the preview renders on the
// env's normal host, the env moves to its "-origin" alternate — and leaves the
// image pipeline untouched. The state is one field on the stable env's override
// (EnvironmentOverride.RoutedToPreview); the helmvalues mapper reads it when
// deriving ((platform.routingHost)) for both sides, so every publish path (CI
// re-launch of the preview, republish of the env) keeps the swap until it is
// restored or the preview is deleted.

// routeAppEnvRequest is the body for routing a stable env's hostname to a preview.
type routeAppEnvRequest struct {
	// FromPreview is the preview env (e.g. "pr-712") that takes over the target
	// env's hostname. It must be based on the target env.
	FromPreview string `json:"fromPreview"`
}

// Sentinels specific to routing; the shared not-found/is-preview cases reuse
// the pin sentinels so statusForPinErr's mapping applies.
var (
	errRouteTargetIsProd         = errors.New("cannot route a production environment's hostname to a preview")
	errRouteTargetDecommissioned = errors.New("target environment is decommissioned; re-enable it first")
	errRouteNoIngress            = errors.New("app exposes no HTTP route; there is no hostname to route")
	errRoutePreviewBaseMismatch  = errors.New("preview is not based on the target environment")
)

// statusForRouteErr maps a routeAppEnv/unrouteAppEnv error to an HTTP status.
func statusForRouteErr(err error) int {
	switch {
	case errors.Is(err, errRouteTargetIsProd), errors.Is(err, errRouteTargetDecommissioned),
		errors.Is(err, errRouteNoIngress), errors.Is(err, errRoutePreviewBaseMismatch):
		return http.StatusUnprocessableEntity
	default:
		return statusForPinErr(err)
	}
}

// routeIsSkippable reports whether a route failure means the op does not apply
// to this stack member (a skip row) rather than a real error: no such env, no
// such preview, or nothing exposed to route.
func routeIsSkippable(err error) bool {
	return errors.Is(err, errPinTargetNotFound) ||
		errors.Is(err, errPinPreviewNotFound) ||
		errors.Is(err, errRouteNoIngress)
}

// previewURLFor returns the URL a preview's stored record should carry: the
// donor env's stable URL while the preview serves that env's hostname, else the
// preview's own {preview}.{app}.preview.{domain} URL. Prod never donates, so a
// donor URL always has the staging shape — matching helmvalues.routingHostFor.
func previewURLFor(app *domain.App, previewName, baseDomain string, secure bool) string {
	if donor := app.Spec.EnvRoutedToPreview(previewName); donor != "" {
		return domain.GenerateURLWithDomain(app.Name, donor, domain.AppEnvStaging, baseDomain, secure)
	}
	return domain.GenerateURLWithDomain(app.Name, previewName, domain.AppEnvPreview, baseDomain, secure)
}

// hostOf strips the scheme from a URL for the bare-host fields in responses.
func hostOf(url string) string {
	url = strings.TrimPrefix(url, "https://")
	return strings.TrimPrefix(url, "http://")
}

// stableEnvRecord returns the stored record for a stable env, else the
// org-derived default with that name (an env that was never persisted), else nil.
func (ah *appHandler) stableEnvRecord(ctx context.Context, app *domain.App, envName string) *domain.AppEnvironment {
	if env, err := ah.appStore.GetAppEnvironment(ctx, app.ProjectName, app.Name, envName); err == nil && env.EnvType != domain.AppEnvPreview {
		return env
	}
	for _, e := range ah.stableEnvsFromOrg(ctx, app) {
		if e.EnvName == envName {
			return e
		}
	}
	return nil
}

// previewPublishTarget rebuilds the publish input for an EXISTING preview from
// its stored record, so it can be republished in place (same namespace, same
// image tag). upsertAppPreview is deliberately not reused: it recomputes the
// namespace (a stack-preview member would leave its shared namespace) and
// reseeds the stored URL.
func (ah *appHandler) previewPublishTarget(ctx context.Context, app *domain.App, env *domain.AppEnvironment) PreviewPublishTarget {
	inst := &domain.EnvironmentInstance{
		AppName:     env.AppName,
		ProjectName: env.ProjectName,
		EnvType:     domain.AppEnvPreview,
		EnvName:     env.EnvName,
		Namespace:   env.Namespace,
		Release:     env.Release,
		Status:      env.Status,
	}
	if len(env.URLs) > 0 {
		inst.URL = env.URLs[0]
	}
	baseEnv := env.BaseEnv
	if baseEnv == "" {
		if stable := ah.stableEnvsFromOrg(ctx, app); len(stable) > 0 {
			baseEnv = stable[0].EnvName
		}
	}
	tag := ""
	if env.Release != nil {
		tag = env.Release.Tag
	}
	return PreviewPublishTarget{App: app, Preview: inst, BaseEnv: baseEnv, ImageTag: tag}
}

// republishStoredPreview republishes one existing preview from its stored record.
func (ah *appHandler) republishStoredPreview(ctx context.Context, app *domain.App, env *domain.AppEnvironment) error {
	if ah.gitOpsPublisher == nil {
		return nil
	}
	t := ah.previewPublishTarget(ctx, app, env)
	return ah.gitOpsPublisher.PublishAppPreview(ctx, app, t.Preview, t.BaseEnv, t.ImageTag)
}

// savePreviewURL stores a preview's routing URL (best-effort: live enrichment
// converges on the real Ingress host anyway). Skipped when the app has no HTTP
// route, so an unexposed preview keeps its empty URL list.
func (ah *appHandler) savePreviewURL(ctx context.Context, app *domain.App, env *domain.AppEnvironment, url string) {
	if !domainapp.AppHasIngressRoute(app) {
		return
	}
	env.URLs = []string{}
	if url != "" {
		env.URLs = []string{url}
	}
	if err := ah.appStore.SaveAppEnvironment(ctx, app.ProjectName, env); err != nil {
		slog.Warn("route: failed to save preview url", "project", app.ProjectName, "app", app.Name, "preview", env.EnvName, "err", err)
	}
}

// setRoutedToPreview writes the override field and saves the app.
func (ah *appHandler) setRoutedToPreview(ctx context.Context, app *domain.App, envName, preview string) error {
	if app.Spec.EnvironmentDefaults == nil {
		app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{}
	}
	ov := app.Spec.EnvironmentDefaults[envName]
	ov.RoutedToPreview = preview
	app.Spec.EnvironmentDefaults[envName] = ov
	if err := ah.appStore.SaveApp(ctx, app.ProjectName, app); err != nil {
		return fmt.Errorf("failed to save app: %w", err)
	}
	return nil
}

// routeAppEnvSpec validates and records the swap in the app spec WITHOUT
// publishing. Returns the app, the target env record, the preview record and
// the preview previously routed to this env ("" when none). Shared by the
// per-app op and the stack fan-out (spec prep before one batched publish).
func (ah *appHandler) routeAppEnvSpec(ctx context.Context, projectName, appName, envName, fromPreview string) (app *domain.App, targetEnv, preview *domain.AppEnvironment, prevPreview string, err error) {
	app, err = ah.appStore.GetApp(ctx, projectName, appName)
	if err != nil {
		return nil, nil, nil, "", fmt.Errorf("%w: app %q in project %q", errPinAppNotFound, appName, projectName)
	}
	targetEnv = ah.stableEnvRecord(ctx, app, envName)
	if targetEnv == nil {
		if env, gerr := ah.appStore.GetAppEnvironment(ctx, projectName, appName, envName); gerr == nil && env.EnvType == domain.AppEnvPreview {
			return nil, nil, nil, "", fmt.Errorf("%w: %q", errPinTargetIsPreview, envName)
		}
		return nil, nil, nil, "", fmt.Errorf("%w: %q for app %q", errPinTargetNotFound, envName, appName)
	}
	if targetEnv.EnvType == domain.AppEnvProd {
		return nil, nil, nil, "", fmt.Errorf("%w: %q", errRouteTargetIsProd, envName)
	}
	if ov := app.Spec.EnvironmentDefaults[envName]; ov.Deploy != nil && !*ov.Deploy {
		return nil, nil, nil, "", fmt.Errorf("%w: %q", errRouteTargetDecommissioned, envName)
	}
	if !domainapp.AppHasIngressRoute(app) {
		return nil, nil, nil, "", fmt.Errorf("%w: app %q", errRouteNoIngress, appName)
	}
	preview, err = ah.appStore.GetAppEnvironment(ctx, projectName, appName, fromPreview)
	if err != nil || preview.EnvType != domain.AppEnvPreview {
		return nil, nil, nil, "", fmt.Errorf("%w: %q for app %q", errPinPreviewNotFound, fromPreview, appName)
	}
	// The preview must clone the env whose host it takes: it reuses that env's
	// cluster, vault and base values, so the swapped host lands where callers
	// expect. Records created before BaseEnv was persisted are accepted for the
	// app's base env only (that's what they were cloned from).
	switch {
	case preview.BaseEnv != "" && preview.BaseEnv != envName:
		return nil, nil, nil, "", fmt.Errorf("%w: %q is based on %q, not %q", errRoutePreviewBaseMismatch, fromPreview, preview.BaseEnv, envName)
	case preview.BaseEnv == "":
		if base := ah.baseStableEnvName(ctx, app); base != "" && base != envName {
			return nil, nil, nil, "", fmt.Errorf("%w: %q is based on %q, not %q", errRoutePreviewBaseMismatch, fromPreview, base, envName)
		}
	}
	prevPreview = app.Spec.EnvironmentDefaults[envName].RoutedToPreview
	if err := ah.setRoutedToPreview(ctx, app, envName, fromPreview); err != nil {
		return nil, nil, nil, "", err
	}
	return app, targetEnv, preview, prevPreview, nil
}

// routeAppEnv records the swap and publishes both sides: the preview first (it
// takes the hostname), then the stable env (it moves to the "-origin" host) —
// so an ingress controller that keeps the OLDER claimant on a duplicate host
// hands the host over the moment the env's update syncs, with no 404 window.
// A previously routed preview is moved back to its own host first. On a publish
// failure the spec is reverted so a retry starts clean. Returns the routed URL.
func (ah *appHandler) routeAppEnv(ctx context.Context, projectName, appName, envName, fromPreview string) (string, error) {
	app, targetEnv, preview, prev, err := ah.routeAppEnvSpec(ctx, projectName, appName, envName, fromPreview)
	if err != nil {
		return "", err
	}
	baseDomain, secure := ah.previewRoutingForEnv(ctx, envName)
	revert := func() {
		if rerr := ah.setRoutedToPreview(ctx, app, envName, prev); rerr != nil {
			slog.Warn("route: failed to revert spec after publish failure", "project", projectName, "app", appName, "env", envName, "err", rerr)
		}
	}
	if prev != "" && prev != fromPreview {
		if old, gerr := ah.appStore.GetAppEnvironment(ctx, projectName, appName, prev); gerr == nil && old.EnvType == domain.AppEnvPreview {
			if err := ah.republishStoredPreview(ctx, app, old); err != nil {
				revert()
				return "", fmt.Errorf("failed to publish previously routed preview %s: %w", prev, err)
			}
			ah.savePreviewURL(ctx, app, old, previewURLFor(app, prev, baseDomain, secure))
		}
	}
	if err := ah.republishStoredPreview(ctx, app, preview); err != nil {
		revert()
		return "", fmt.Errorf("failed to publish preview %s: %w", fromPreview, err)
	}
	if err := ah.republishAppsFocus(ctx, []appFocusPublish{{app: app, focusEnv: targetEnv}}); err != nil {
		revert()
		return "", fmt.Errorf("failed to publish environment %s: %w", envName, err)
	}
	url := previewURLFor(app, fromPreview, baseDomain, secure)
	ah.savePreviewURL(ctx, app, preview, url)
	return url, nil
}

// unrouteAppEnvSpec clears the swap in the spec WITHOUT publishing. Returns the
// preview that was routed and wasRouted (false = no-op success).
func (ah *appHandler) unrouteAppEnvSpec(ctx context.Context, projectName, appName, envName string) (app *domain.App, targetEnv *domain.AppEnvironment, previewName string, wasRouted bool, err error) {
	app, err = ah.appStore.GetApp(ctx, projectName, appName)
	if err != nil {
		return nil, nil, "", false, fmt.Errorf("%w: app %q in project %q", errPinAppNotFound, appName, projectName)
	}
	previewName = app.Spec.EnvironmentDefaults[envName].RoutedToPreview
	if previewName == "" {
		return app, nil, "", false, nil
	}
	if err := ah.setRoutedToPreview(ctx, app, envName, ""); err != nil {
		return nil, nil, "", true, err
	}
	return app, ah.stableEnvRecord(ctx, app, envName), previewName, true, nil
}

// unrouteAppEnv clears the swap and publishes: the stable env first (it takes
// its hostname back), then the preview (back to its own host) if it still
// exists. A failed env publish reverts the spec so a retry starts clean.
func (ah *appHandler) unrouteAppEnv(ctx context.Context, projectName, appName, envName string) (string, bool, error) {
	app, targetEnv, previewName, wasRouted, err := ah.unrouteAppEnvSpec(ctx, projectName, appName, envName)
	if err != nil || !wasRouted {
		return previewName, wasRouted, err
	}
	if err := ah.republishAppsFocus(ctx, []appFocusPublish{{app: app, focusEnv: targetEnv}}); err != nil {
		if rerr := ah.setRoutedToPreview(ctx, app, envName, previewName); rerr != nil {
			slog.Warn("unroute: failed to revert spec after publish failure", "project", projectName, "app", appName, "env", envName, "err", rerr)
		}
		return previewName, true, fmt.Errorf("failed to publish environment %s: %w", envName, err)
	}
	if err := ah.republishPreviewOwnHost(ctx, app, envName, previewName); err != nil {
		return previewName, true, err
	}
	return previewName, true, nil
}

// republishPreviewOwnHost moves a (no longer routed) preview back to its own
// host and URL, if its record still exists. Missing preview = nothing to do.
func (ah *appHandler) republishPreviewOwnHost(ctx context.Context, app *domain.App, envName, previewName string) error {
	preview, err := ah.appStore.GetAppEnvironment(ctx, app.ProjectName, app.Name, previewName)
	if err != nil || preview.EnvType != domain.AppEnvPreview {
		return nil
	}
	if err := ah.republishStoredPreview(ctx, app, preview); err != nil {
		return fmt.Errorf("environment %s restored, but failed to publish preview %s: %w", envName, previewName, err)
	}
	baseDomain, secure := ah.previewRoutingForEnv(ctx, envName)
	ah.savePreviewURL(ctx, app, preview, previewURLFor(app, previewName, baseDomain, secure))
	return nil
}

// clearRoutingForPreview clears the swap on whichever env routes to the named
// preview (spec only). Returns the donor env's focus-publish item, or nil when
// no env routes to it. Shared by the per-app and stack preview deletes.
func (ah *appHandler) clearRoutingForPreview(ctx context.Context, app *domain.App, previewName string) (*appFocusPublish, error) {
	donor := app.Spec.EnvRoutedToPreview(previewName)
	if donor == "" {
		return nil, nil
	}
	if err := ah.setRoutedToPreview(ctx, app, donor, ""); err != nil {
		return nil, err
	}
	return &appFocusPublish{app: app, focusEnv: ah.stableEnvRecord(ctx, app, donor)}, nil
}

// restoreRoutingForDeletedPreview gives a stable env its hostname back before
// the preview serving it is deleted (spec + one publish). Reverts the spec on a
// publish failure so the delete can be retried with the swap still recorded.
func (ah *appHandler) restoreRoutingForDeletedPreview(ctx context.Context, app *domain.App, previewName string) error {
	item, err := ah.clearRoutingForPreview(ctx, app, previewName)
	if err != nil || item == nil {
		return err
	}
	if err := ah.republishAppsFocus(ctx, []appFocusPublish{*item}); err != nil {
		if rerr := ah.setRoutedToPreview(ctx, app, item.focusEnv.EnvName, previewName); rerr != nil {
			slog.Warn("preview delete: failed to revert routing after publish failure", "project", app.ProjectName, "app", app.Name, "preview", previewName, "err", rerr)
		}
		return fmt.Errorf("failed to restore %s routing before deleting preview %s: %w", item.focusEnv.EnvName, previewName, err)
	}
	return nil
}

// handleRouteAppEnv serves POST .../apps/{app}/environments/{env}/route.
func (ah *appHandler) handleRouteAppEnv(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	appName := r.PathValue("app")
	envName := r.PathValue("env")

	var req routeAppEnvRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	req.FromPreview = strings.TrimSpace(req.FromPreview)
	if req.FromPreview == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "fromPreview is required"})
		return
	}
	// Two publishes (preview, then env) are the slow part; defer them when the
	// caller opts in (Prefer: respond-async / ?async=1) so the gateway doesn't 504.
	op := func(ctx context.Context) (int, any, error) {
		url, err := ah.routeAppEnv(ctx, projectName, appName, envName, req.FromPreview)
		if err != nil {
			return statusForRouteErr(err), nil, err
		}
		return http.StatusOK, map[string]string{
			"message": "traffic for " + hostOf(url) + " is now served by preview " + req.FromPreview,
			"project": projectName,
			"app":     appName,
			"env":     envName,
			"from":    req.FromPreview,
			"host":    hostOf(url),
		}, nil
	}
	dispatchOp(w, r, ah.async, "route-app", projectName, op)
}

// handleUnrouteAppEnv serves DELETE .../apps/{app}/environments/{env}/route.
func (ah *appHandler) handleUnrouteAppEnv(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	appName := r.PathValue("app")
	envName := r.PathValue("env")

	op := func(ctx context.Context) (int, any, error) {
		previewName, wasRouted, err := ah.unrouteAppEnv(ctx, projectName, appName, envName)
		if err != nil {
			return statusForRouteErr(err), nil, err
		}
		if !wasRouted {
			return http.StatusOK, map[string]string{"message": "environment " + envName + " is not routed to a preview", "project": projectName, "app": appName, "env": envName}, nil
		}
		return http.StatusOK, map[string]string{
			"message": "environment " + envName + " serves its own hostname again; preview " + previewName + " is back on its preview URL",
			"project": projectName,
			"app":     appName,
			"env":     envName,
			"from":    previewName,
		}, nil
	}
	dispatchOp(w, r, ah.async, "unroute-app", projectName, op)
}
