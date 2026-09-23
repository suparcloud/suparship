package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	domainapp "github.com/suparcloud/suparship/internal/app"
	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/helmvalues"
	"github.com/suparcloud/suparship/internal/platform"
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
	errRouteHostNotTokenized     = errors.New("hostname is not platform-managed")
)

// statusForRouteErr maps a routeAppEnv/unrouteAppEnv error to an HTTP status.
func statusForRouteErr(err error) int {
	switch {
	case errors.Is(err, errRouteTargetIsProd), errors.Is(err, errRouteTargetDecommissioned),
		errors.Is(err, errRouteNoIngress), errors.Is(err, errRoutePreviewBaseMismatch),
		errors.Is(err, errRouteHostNotTokenized):
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

// routingTokens are the ((platform.*)) tokens the host swap can move. A host
// value that carries none of them is a literal the platform cannot swap.
var routingTokens = []string{
	"platform.routingHost", "platform.externalRoutingHost", "platform.internalRoutingHost",
	"platform.appRoutingName", "platform.appComponentRoutingName",
}

// componentsWithoutRoutingToken returns the exposed components whose effective
// values (template ⊕ org override ⊕ app ⊕ env ⊕ component overlays, before
// interpolation) carry NO routing token in any string leaf — i.e. whose host
// is a literal the swap could not move. Best-effort: a component whose template
// can't be resolved (fake mode, test harness) is not reported, so the check
// never blocks where it can't see.
func (ah *appHandler) componentsWithoutRoutingToken(ctx context.Context, app *domain.App, envName string) []string {
	var missing []string
	envOv := app.Spec.EnvironmentDefaults[envName]
	for _, c := range app.Spec.Components {
		if c.ExposeMode != domain.ExposeExternal && c.ExposeMode != domain.ExposeInternal {
			continue
		}
		// A component with its own template (composed app) is interpolated
		// against its own values; one without (single-template app) against
		// the app-level values — mirroring the publisher's two overlay paths.
		tref, appRaw, envRaw := c.Template, c.Values, envOv.ComponentValues[c.Name]
		if tref == nil {
			tref, appRaw, envRaw = &app.Spec.Template, app.Spec.RawValues, envOv.RawValues
		}
		if tref.Name == "" {
			continue
		}
		t, ok := ah.lookupTemplate(ctx, tref.Name)
		if !ok || t == nil {
			continue
		}
		ov := loadOverride(ctx, ah.kubeClient, tref.Name)
		values := computeEffectiveValues(nil, t, ov, envName, ah.envCluster(ctx, envName), appRaw, envRaw)
		if !valuesContainAnyToken(values, routingTokens) {
			missing = append(missing, c.Name)
		}
	}
	return missing
}

// valuesContainAnyToken reports whether any string leaf of a values tree
// mentions one of the token names (either delimiter).
func valuesContainAnyToken(v any, names []string) bool {
	switch x := v.(type) {
	case string:
		for _, n := range names {
			if strings.Contains(x, n) {
				return true
			}
		}
	case map[string]any:
		for _, e := range x {
			if valuesContainAnyToken(e, names) {
				return true
			}
		}
	case []any:
		for _, e := range x {
			if valuesContainAnyToken(e, names) {
				return true
			}
		}
	}
	return false
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

// setRoutedToPreview writes the override field and saves the app. Clearing
// the preview also clears the recorded host and mode.
func (ah *appHandler) setRoutedToPreview(ctx context.Context, app *domain.App, envName, preview string) error {
	if app.Spec.EnvironmentDefaults == nil {
		app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{}
	}
	ov := app.Spec.EnvironmentDefaults[envName]
	ov.RoutedToPreview = preview
	if preview == "" {
		ov.RoutedHost, ov.RoutedMode = "", ""
	}
	app.Spec.EnvironmentDefaults[envName] = ov
	if err := ah.appStore.SaveApp(ctx, app.ProjectName, app); err != nil {
		return fmt.Errorf("failed to save app: %w", err)
	}
	return nil
}

// setRoutedHost records, after a successful route, which URL now reaches the
// preview and by which mechanism (best-effort: the route already happened).
func (ah *appHandler) setRoutedHost(ctx context.Context, app *domain.App, envName, host, mode string) {
	ov := app.Spec.EnvironmentDefaults[envName]
	ov.RoutedHost, ov.RoutedMode = host, mode
	app.Spec.EnvironmentDefaults[envName] = ov
	if err := ah.appStore.SaveApp(ctx, app.ProjectName, app); err != nil {
		slog.Warn("route: failed to record routed host", "project", app.ProjectName, "app", app.Name, "env", envName, "err", err)
	}
}

// argoEnvSettledReader is the optional capability (kube.ArgoCDStatusReader) to
// tell whether an env's generated Applications have applied a change committed
// at a given time. Asserted from the ArgoAppGate; absent (fake mode, tests) the
// sequencing below degrades to "publish, then publish".
type argoEnvSettledReader interface {
	EnvAppsSettled(ctx context.Context, projectName, appName, envName string, since time.Time) (bool, []string, error)
}

// Ordering rule for the host swap: ingress-nginx's admission webhook refuses an
// Ingress claiming a host+path another Ingress still holds. So whichever side
// GIVES UP a hostname is published first and its Applications are given time to
// sync before the side CLAIMING the hostname is published. Route: env → -origin
// host, then preview → env host. Restore: preview → own host, then env → its
// host. Delete: preview pruned, then env → its host. The cost is a few seconds
// where the hostname answers 404; the alternative is a guaranteed SyncFailed.
//
// routeSettleTimeout bounds the wait; on timeout the claim is published anyway
// and the Applications' sync retry policy finishes the handover.
const routeSettleTimeout = 2 * time.Minute

// routeSettlePoll is a var so tests can shorten the wait loop.
var routeSettlePoll = 2 * time.Second

// envRef names one app env to wait on.
type envRef struct{ project, app, env string }

// waitForEnvsSettled waits until every listed env's Applications have applied
// what was committed at `since` (see argoEnvSettledReader), nudging ArgoCD to
// refresh instead of waiting out its poll cycle. Best-effort: returns on
// timeout or a read error so the caller can proceed.
func (ah *appHandler) waitForEnvsSettled(ctx context.Context, refs []envRef, since time.Time) {
	r, ok := ah.argoAppGate.(argoEnvSettledReader)
	if !ok || len(refs) == 0 {
		return
	}
	// ArgoCD records reconciledAt at second precision; give the comparison a
	// second of slack so a commit and a refresh in the same second still count.
	since = since.Add(-time.Second)
	deadline := time.Now().Add(routeSettleTimeout)
	pending := append([]envRef(nil), refs...)
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		names = append(names, ref.env)
	}
	reportProgress(ctx, "wait", "waiting for ArgoCD to apply the hostname release on "+strings.Join(names, ", "))
	for i := 0; len(pending) > 0; i++ {
		var still []envRef
		for _, ref := range pending {
			settled, names, err := r.EnvAppsSettled(ctx, ref.project, ref.app, ref.env, since)
			if err != nil {
				slog.Warn("route: argocd sync read failed — proceeding without the wait",
					"project", ref.project, "app", ref.app, "env", ref.env, "err", err)
				continue
			}
			if settled {
				continue
			}
			if ah.argoChainNudger != nil && i%3 == 0 && len(names) > 0 {
				if nerr := ah.argoChainNudger.RefreshAppsByName(ctx, names); nerr != nil {
					slog.Debug("route: argocd refresh nudge failed", "env", ref.env, "err", nerr)
				}
			}
			still = append(still, ref)
		}
		pending = still
		if len(pending) == 0 || time.Now().After(deadline) {
			if len(pending) > 0 {
				slog.Warn("route: timed out waiting for argocd to apply the hostname release — proceeding; the sync retry policy completes the handover",
					"pending", len(pending))
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(routeSettlePoll):
		}
	}
}

// waitForPreviewAppGone waits until a pruned preview's Application has been
// removed (its Ingress with it), nudging the previews ApplicationSet. Same
// best-effort posture as waitForEnvsSettled.
func (ah *appHandler) waitForPreviewAppGone(ctx context.Context, projectName, appName, previewName string) {
	if ah.argoAppGate == nil {
		return
	}
	deadline := time.Now().Add(routeSettleTimeout)
	reportProgress(ctx, "wait", "waiting for ArgoCD to remove preview "+previewName)
	for i := 0; ; i++ {
		if ah.argoChainNudger != nil && i%3 == 0 {
			if err := ah.argoChainNudger.RefreshAppSets(ctx, []string{"previews"}); err != nil {
				slog.Debug("route: previews appset nudge failed", "err", err)
			}
		}
		exists, err := ah.argoAppGate.HasAppForEnv(ctx, projectName, appName, previewName)
		if err != nil || !exists {
			return
		}
		if time.Now().After(deadline) {
			slog.Warn("route: timed out waiting for the preview Application to be pruned — proceeding; the sync retry policy completes the handover",
				"project", projectName, "app", appName, "preview", previewName)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(routeSettlePoll):
		}
	}
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
	return ah.routeAppEnvSpecFor(ctx, app, envName, fromPreview, ah.platformRouted(ctx, app))
}

// routeAppEnvSpecFor is routeAppEnvSpec with the app loaded and its
// routability decided by the caller — the stack fan-out decides it for every
// member up front (sequentially) so the concurrent per-member prep never reads
// other apps while they are being saved.
func (ah *appHandler) routeAppEnvSpecFor(ctx context.Context, app *domain.App, envName, fromPreview string, platformRouted bool) (_ *domain.App, targetEnv, preview *domain.AppEnvironment, prevPreview string, err error) {
	projectName, appName := app.ProjectName, app.Name
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
	// Platform-routed apps (their own or another app's/stack's routes forward
	// to them) are switched at the HTTPRoute backend — no chart host to check.
	if !platformRouted {
		if !domainapp.AppHasIngressRoute(app) {
			return nil, nil, nil, "", fmt.Errorf("%w: app %q", errRouteNoIngress, appName)
		}
		if missing := ah.componentsWithoutRoutingToken(ctx, app, envName); len(missing) > 0 {
			return nil, nil, nil, "", fmt.Errorf("%w: component(s) %s set a literal host; use ((platform.appRoutingName)), ((platform.appComponentRoutingName)) or ((platform.routingHost)) in the host value so the platform can move it",
				errRouteHostNotTokenized, strings.Join(missing, ", "))
		}
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

// routeAppEnv records the swap and publishes both sides in the order the
// ingress admission webhook demands (see the ordering rule above): the side
// releasing the hostname first — the env moving to its "-origin" host, or the
// previously routed preview moving back to its own — then, once ArgoCD has
// applied that, the preview claiming it. On a publish failure the spec is
// reverted so a retry starts clean. Returns the routed URL.
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
	if ah.platformRouted(ctx, app) {
		// Backend switch: the env's HTTPRoute keeps its hostname; its backendRef
		// now names the preview namespace's Service. One publish of the env
		// (switched backends) and one of the preview (the ReferenceGrant that
		// lets the env's HTTPRoute cross into it). No hostname changes hands, so
		// no admission-webhook ordering and no ArgoCD wait. A previously routed
		// preview simply loses the switch in the same env publish.
		reportProgress(ctx, "switch", "pointing "+envName+"'s routes at preview "+fromPreview)
		if err := ah.republishAppsFocus(ctx, []appFocusPublish{{app: app, focusEnv: targetEnv}}); err != nil {
			revert()
			return "", fmt.Errorf("failed to publish environment %s: %w", envName, err)
		}
		if err := ah.republishStoredPreview(ctx, app, preview); err != nil {
			revert()
			return "", fmt.Errorf("failed to publish preview %s: %w", fromPreview, err)
		}
		if prev != "" && prev != fromPreview {
			if old, gerr := ah.appStore.GetAppEnvironment(ctx, projectName, appName, prev); gerr == nil && old.EnvType == domain.AppEnvPreview {
				if err := ah.republishStoredPreview(ctx, app, old); err != nil {
					slog.Warn("route: previously routed preview keeps a stale ReferenceGrant", "project", projectName, "app", appName, "preview", prev, "err", err)
				}
			}
		}
		// The preview keeps its own URL: staging's route forwards to it in
		// addition. Record the forwarded host for the UI.
		url := ah.routeHostURL(ctx, app, envName, targetEnv.EnvType, envName, baseDomain, secure)
		if url == "" {
			url = domain.GenerateURLWithDomain(app.Name, envName, targetEnv.EnvType, baseDomain, secure)
		}
		ah.setRoutedHost(ctx, app, envName, url, domain.RouteModeSwitch)
		return url, nil
	}
	releaseStart := time.Now()
	var release []envRef
	switch {
	case prev != "" && prev != fromPreview:
		// Replacing: the old preview holds the hostname; move it off first. The
		// env is already on its -origin host.
		if old, gerr := ah.appStore.GetAppEnvironment(ctx, projectName, appName, prev); gerr == nil && old.EnvType == domain.AppEnvPreview {
			reportProgress(ctx, "release", "publishing preview "+prev+" back to its own host")
			if err := ah.republishStoredPreview(ctx, app, old); err != nil {
				revert()
				return "", fmt.Errorf("failed to publish previously routed preview %s: %w", prev, err)
			}
			ah.savePreviewURL(ctx, app, old, previewURLFor(app, prev, baseDomain, secure))
			release = append(release, envRef{projectName, appName, prev})
		}
	case prev == "":
		// Fresh route: the env releases the hostname by moving to -origin.
		reportProgress(ctx, "release", "publishing "+envName+" on its -origin host")
		if err := ah.republishAppsFocus(ctx, []appFocusPublish{{app: app, focusEnv: targetEnv}}); err != nil {
			revert()
			return "", fmt.Errorf("failed to publish environment %s: %w", envName, err)
		}
		release = append(release, envRef{projectName, appName, envName})
	}
	ah.waitForEnvsSettled(ctx, release, releaseStart)
	reportProgress(ctx, "claim", "publishing preview "+fromPreview+" on "+envName+"'s hostname")
	if err := ah.republishStoredPreview(ctx, app, preview); err != nil {
		revert()
		return "", fmt.Errorf("failed to publish preview %s: %w", fromPreview, err)
	}
	url := previewURLFor(app, fromPreview, baseDomain, secure)
	ah.savePreviewURL(ctx, app, preview, url)
	ah.setRoutedHost(ctx, app, envName, url, domain.RouteModeSwap)
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

// unrouteAppEnv clears the swap and publishes in webhook order: the preview
// releases the hostname (back to its own host) if it still exists, ArgoCD
// applies that, then the stable env takes its hostname back. A failed publish
// reverts the spec so a retry starts clean.
func (ah *appHandler) unrouteAppEnv(ctx context.Context, projectName, appName, envName string) (string, bool, error) {
	app, targetEnv, previewName, wasRouted, err := ah.unrouteAppEnvSpec(ctx, projectName, appName, envName)
	if err != nil || !wasRouted {
		return previewName, wasRouted, err
	}
	revert := func() {
		if rerr := ah.setRoutedToPreview(ctx, app, envName, previewName); rerr != nil {
			slog.Warn("unroute: failed to revert spec after publish failure", "project", projectName, "app", appName, "env", envName, "err", rerr)
		}
	}
	if ah.platformRouted(ctx, app) {
		reportProgress(ctx, "switch", "pointing "+envName+"'s routes back at its own Services")
		if err := ah.republishAppsFocus(ctx, []appFocusPublish{{app: app, focusEnv: targetEnv}}); err != nil {
			revert()
			return previewName, true, fmt.Errorf("failed to publish environment %s: %w", envName, err)
		}
		// Prunes the preview's ReferenceGrant; a missing preview is nothing to do.
		if _, err := ah.republishPreviewOwnHost(ctx, app, envName, previewName); err != nil {
			return previewName, true, err
		}
		return previewName, true, nil
	}
	releaseStart := time.Now()
	reportProgress(ctx, "release", "publishing preview "+previewName+" back to its own host")
	released, err := ah.republishPreviewOwnHost(ctx, app, envName, previewName)
	if err != nil {
		revert()
		return previewName, true, err
	}
	if released {
		ah.waitForEnvsSettled(ctx, []envRef{{projectName, appName, previewName}}, releaseStart)
	}
	reportProgress(ctx, "claim", "publishing "+envName+" back on its own hostname")
	if err := ah.republishAppsFocus(ctx, []appFocusPublish{{app: app, focusEnv: targetEnv}}); err != nil {
		revert()
		return previewName, true, fmt.Errorf("failed to publish environment %s: %w", envName, err)
	}
	return previewName, true, nil
}

// republishPreviewOwnHost moves a (no longer routed) preview back to its own
// host and URL, if its record still exists. Returns whether a preview was
// republished (a missing preview = nothing to release).
func (ah *appHandler) republishPreviewOwnHost(ctx context.Context, app *domain.App, envName, previewName string) (bool, error) {
	preview, err := ah.appStore.GetAppEnvironment(ctx, app.ProjectName, app.Name, previewName)
	if err != nil || preview.EnvType != domain.AppEnvPreview {
		return false, nil
	}
	if err := ah.republishStoredPreview(ctx, app, preview); err != nil {
		return false, fmt.Errorf("failed to publish preview %s: %w", previewName, err)
	}
	baseDomain, secure := ah.previewRoutingForEnv(ctx, envName)
	ah.savePreviewURL(ctx, app, preview, ah.previewOwnURL(ctx, app, previewName, envName, baseDomain, secure))
	return true, nil
}

// previewOwnURL is the URL a preview serves on its own: the platform route's
// hostname in preview form when the app is platform-routed, else the legacy
// preview shape.
func (ah *appHandler) previewOwnURL(ctx context.Context, app *domain.App, previewName, baseEnv, baseDomain string, secure bool) string {
	if u := ah.routeHostURL(ctx, app, baseEnv, domain.AppEnvPreview, previewName, baseDomain, secure); u != "" {
		return u
	}
	return previewURLFor(app, previewName, baseDomain, secure)
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
	env := ah.stableEnvRecord(ctx, app, donor)
	if env == nil {
		// No record anywhere (org env removed?): still name the env so the
		// focus publish and any spec revert have something to address.
		env = &domain.AppEnvironment{AppName: app.Name, ProjectName: app.ProjectName, EnvName: donor, EnvType: domain.AppEnvStaging}
	}
	return &appFocusPublish{app: app, focusEnv: env}, nil
}

// restoreRoutingAfterPrune gives a stable env its hostname back once the
// preview serving it has been pruned from gitops: waits for the preview's
// Application (and its Ingress) to be gone, then republishes the env. The spec
// was cleared by clearRoutingForPreview before the prune; a publish failure
// reinstates it so a later unroute republishes the env (a missing preview is
// then simply nothing to release).
func (ah *appHandler) restoreRoutingAfterPrune(ctx context.Context, item *appFocusPublish, previewName string) error {
	if item == nil {
		return nil
	}
	if !ah.platformRouted(ctx, item.app) {
		ah.waitForPreviewAppGone(ctx, item.app.ProjectName, item.app.Name, previewName)
	}
	reportProgress(ctx, "claim", "publishing "+item.focusEnv.EnvName+" back on its own hostname")
	if err := ah.republishAppsFocus(ctx, []appFocusPublish{*item}); err != nil {
		if rerr := ah.setRoutedToPreview(ctx, item.app, item.focusEnv.EnvName, previewName); rerr != nil {
			slog.Warn("preview delete: failed to reinstate routing after publish failure", "project", item.app.ProjectName, "app", item.app.Name, "preview", previewName, "err", rerr)
		}
		return fmt.Errorf("preview %s removed, but failed to restore %s's hostname: %w — run unroute to retry", previewName, item.focusEnv.EnvName, err)
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
	dispatchOpAsyncDefault(w, r, ah.async, "route-app", projectName, op)
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
	dispatchOpAsyncDefault(w, r, ah.async, "unroute-app", projectName, op)
}

// ── platform-owned routing (backend switch) ──────────────────────────────────

// effectiveRoutes returns an app's own routes plus its stack's routes expanded
// to it — the same view the publish adapter renders.
func (ah *appHandler) effectiveRoutes(ctx context.Context, app *domain.App) []domain.RouteSpec {
	routes := append([]domain.RouteSpec(nil), app.Spec.Routes...)
	if app.Spec.Stack != "" && ah.stackStore != nil {
		if st, err := ah.stackStore.GetStack(ctx, app.ProjectName, app.Spec.Stack); err == nil && st != nil {
			routes = append(routes, domain.ExpandStackRoutes(st.Spec.Routes, app.Name)...)
		}
	}
	return routes
}

// platformRouted reports whether an app is fronted by platform-owned routes:
// its own, its stack's, or another app's routes in the project that forward
// to it. Such an app is routed to a preview by a backend switch, never by the
// chart host swap.
func (ah *appHandler) platformRouted(ctx context.Context, app *domain.App) bool {
	if len(ah.effectiveRoutes(ctx, app)) > 0 {
		return true
	}
	apps, err := ah.appStore.ListApps(ctx, app.ProjectName)
	if err != nil {
		return false
	}
	for _, o := range apps {
		if o.Name == app.Name {
			continue
		}
		if domain.RoutesCoverApp(ah.effectiveRoutes(ctx, o), o.Name, app.Name) {
			return true
		}
	}
	return false
}

// routeHostnames returns the hostname expressions of an app's effective routes.
func routeHostnames(routes []domain.RouteSpec) map[string]bool {
	out := map[string]bool{}
	for _, r := range routes {
		for _, h := range r.EffectiveHostnames(domain.DefaultAppRouteHostname) {
			out[h] = true
		}
	}
	return out
}

// routeSiblings returns the project's other apps whose effective routes share
// a hostname expression with this app's — the apps a composite preview forwards
// to, and whose base env therefore carries a ReferenceGrant for it.
func (ah *appHandler) routeSiblings(ctx context.Context, app *domain.App) []*domain.App {
	mine := routeHostnames(ah.effectiveRoutes(ctx, app))
	if len(mine) == 0 {
		return nil
	}
	apps, err := ah.appStore.ListApps(ctx, app.ProjectName)
	if err != nil {
		return nil
	}
	var out []*domain.App
	for _, o := range apps {
		if o.Name == app.Name {
			continue
		}
		theirs := routeHostnames(ah.effectiveRoutes(ctx, o))
		for h := range mine {
			if theirs[h] {
				out = append(out, o)
				break
			}
		}
	}
	return out
}

// republishRouteSiblings republishes the base env of every app sharing a
// hostname with this app, so their ReferenceGrants follow this app's preview
// (created → grant appears; deleted → grant pruned). One batched publish; a
// failure is logged, not fatal — the grant self-heals on the siblings' next
// publish.
func (ah *appHandler) republishRouteSiblings(ctx context.Context, app *domain.App, baseEnv string) {
	sibs := ah.routeSiblings(ctx, app)
	if len(sibs) == 0 {
		return
	}
	items := make([]appFocusPublish, 0, len(sibs))
	for _, o := range sibs {
		if env := ah.stableEnvRecord(ctx, o, baseEnv); env != nil {
			items = append(items, appFocusPublish{app: o, focusEnv: env})
		}
	}
	if err := ah.republishAppsFocus(ctx, items); err != nil {
		slog.Warn("route: failed to refresh sibling ReferenceGrants", "project", app.ProjectName, "app", app.Name, "env", baseEnv, "err", err)
	}
}

// routeHostURL resolves a platform-routed app's first route hostname for an
// env (envType stable, instance = env) or a preview (envType preview,
// instance = preview name, profiles of baseEnv) — the URL the platform's
// Ingress/HTTPRoute will actually carry. "" when the app has no platform
// routes or a token can't resolve (caller falls back to the legacy shape).
func (ah *appHandler) routeHostURL(ctx context.Context, app *domain.App, baseEnv string, envType domain.AppEnvironmentType, instance, baseDomain string, secure bool) string {
	routes := ah.effectiveRoutes(ctx, app)
	if len(routes) == 0 {
		return ""
	}
	host := routes[0].EffectiveHostnames(domain.DefaultAppRouteHostname)[0]
	// Resolve against a copy without the host swap (a backend switch never
	// changes a hostname) and the org/env routing profiles for the tier domains.
	cp := *app
	cp.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{}
	for k, v := range app.Spec.EnvironmentDefaults {
		v.RoutedToPreview = ""
		cp.Spec.EnvironmentDefaults[k] = v
	}
	var orgProfiles, envProfiles domain.RoutingProfiles
	if ah.orgProvider != nil {
		if org, err := ah.orgProvider.GetOrg(ctx); err == nil && org != nil {
			orgProfiles = org.RoutingProfiles
			for _, e := range org.Environments {
				if e.Name == baseEnv {
					envProfiles = e.RoutingProfiles
				}
			}
		}
	}
	pv := helmvalues.MapPlatformValuesForEnv(&cp, instance, envType, baseDomain, "", "", "", orgProfiles, envProfiles, nil)
	resolved := (platform.Context{Platform: pv}).Interpolate(host)
	if resolved == "" || strings.Contains(resolved, "((") {
		return ""
	}
	scheme := "http://"
	if secure {
		scheme = "https://"
	}
	return scheme + resolved
}

// ── live route view (Traffic tab) ────────────────────────────────────────────

// RouteRuleStatusDTO is one rendered rule: where a path currently goes.
type RouteRuleStatusDTO struct {
	PathPrefix string `json:"pathPrefix"`
	App        string `json:"app"`
	Component  string `json:"component,omitempty"`
	Service    string `json:"service"`
	Port       int    `json:"port"`
	// Namespace the Service is resolved in; empty = the env's own.
	Namespace string `json:"namespace,omitempty"`
	// ForwardedTo names the preview this rule is switched to (backend switch).
	ForwardedTo string `json:"forwardedTo,omitempty"`
	// Deployed is false when the backend app has no deployment in this env
	// (the rule is dropped from the rendered object).
	Deployed bool `json:"deployed"`
}

// RouteStatusDTO is one route as rendered for one env or preview.
type RouteStatusDTO struct {
	Name      string               `json:"name"`
	Tier      string               `json:"tier"`
	Hostnames []string             `json:"hostnames"`
	Rules     []RouteRuleStatusDTO `json:"rules"`
	// Composite marks a preview route that forwards other apps' paths to the
	// base env (single-app preview of a shared hostname).
	Composite bool `json:"composite,omitempty"`
}

// EnvRoutesStatusDTO groups an env's (or preview's) routes.
type EnvRoutesStatusDTO struct {
	EnvName string           `json:"envName"`
	EnvType string           `json:"envType"`
	BaseEnv string           `json:"baseEnv,omitempty"`
	Routes  []RouteStatusDTO `json:"routes"`
}

// AppRoutesStatusDTO is the Traffic tab's view of platform-owned routing.
type AppRoutesStatusDTO struct {
	PlatformRouted bool `json:"platformRouted"`
	// Edge is "gateway" or "ingress" per the external tier's profile ("" unknown).
	Edge string               `json:"edge,omitempty"`
	Envs []EnvRoutesStatusDTO `json:"envs"`
}

// handleGetAppRoutes serves GET .../apps/{app}/routes: the app's effective
// platform routes as rendered per env and preview — hostnames resolved,
// each rule's current backend Service and namespace, and any backend switch.
func (ah *appHandler) handleGetAppRoutes(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	appName := r.PathValue("app")
	ctx := r.Context()
	app, err := ah.appStore.GetApp(ctx, projectName, appName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "app \"" + appName + "\" not found in project \"" + projectName + "\""})
		return
	}
	out := AppRoutesStatusDTO{PlatformRouted: ah.platformRouted(ctx, app), Envs: []EnvRoutesStatusDTO{}}
	routes := ah.effectiveRoutes(ctx, app)
	if ah.orgProvider != nil {
		if org, oerr := ah.orgProvider.GetOrg(ctx); oerr == nil && org != nil {
			if prof, perr := domain.ResolveRoutingProfile(org.RoutingProfiles, nil, nil, domain.ExposeExternal); perr == nil {
				out.Edge = "ingress"
				if prof.Gateway != nil && prof.Gateway.Name != "" {
					out.Edge = "gateway"
				}
			}
		}
	}
	if len(routes) == 0 {
		writeJSON(w, http.StatusOK, out)
		return
	}
	envs, _ := ah.appStore.ListAppEnvironments(ctx, projectName, appName)
	siblings := ah.routeSiblings(ctx, app)
	byName := map[string]*domain.App{}
	if apps, aerr := ah.appStore.ListApps(ctx, projectName); aerr == nil {
		for _, o := range apps {
			byName[o.Name] = o
		}
	}
	backendNS := func(backendApp, envName, selfNS string) (string, bool) {
		if backendApp == app.Name {
			return selfNS, true
		}
		o := byName[backendApp]
		if o == nil {
			return "", false
		}
		ns := ah.stableEnvRecord(ctx, o, envName)
		if ns == nil || ns.Namespace == "" {
			return "", false
		}
		return ns.Namespace, true
	}
	rulesFor := func(owner string, rs []domain.RouteSpec, envName, localNS string, resolve func(string) (string, bool), forwardedTo string) []RouteRuleStatusDTO {
		var rules []RouteRuleStatusDTO
		for _, rt := range rs {
			for _, rule := range rt.Rules {
				b := rule.Backend
				bapp := b.AppName(owner)
				ns, ok := resolve(bapp)
				d := RouteRuleStatusDTO{PathPrefix: rule.PathPrefix, App: bapp, Component: b.Component, Service: b.ServiceName(owner), Port: b.Port, Deployed: ok}
				if ok && ns != localNS {
					d.Namespace = ns
				}
				if bapp == app.Name && forwardedTo != "" {
					d.ForwardedTo = forwardedTo
				}
				rules = append(rules, d)
			}
		}
		return rules
	}
	hostsFor := func(rt domain.RouteSpec, baseEnv string, envType domain.AppEnvironmentType, instance string) []string {
		baseDomain, secure := ah.previewRoutingForEnv(ctx, baseEnv)
		var hosts []string
		for _, h := range rt.EffectiveHostnames(domain.DefaultAppRouteHostname) {
			one := []domain.RouteSpec{{Hostnames: []string{h}, Rules: rt.Rules}}
			if u := ah.routeHostURLFor(ctx, app, one, baseEnv, envType, instance, baseDomain, secure); u != "" {
				hosts = append(hosts, hostOf(u))
			} else {
				hosts = append(hosts, h)
			}
		}
		return hosts
	}

	sort.Slice(envs, func(i, j int) bool {
		if (envs[i].EnvType == domain.AppEnvPreview) != (envs[j].EnvType == domain.AppEnvPreview) {
			return envs[i].EnvType != domain.AppEnvPreview
		}
		if envs[i].Order != envs[j].Order {
			return envs[i].Order < envs[j].Order
		}
		return envs[i].EnvName < envs[j].EnvName
	})
	for _, env := range envs {
		e := EnvRoutesStatusDTO{EnvName: env.EnvName, EnvType: string(env.EnvType), BaseEnv: env.BaseEnv, Routes: []RouteStatusDTO{}}
		if env.EnvType != domain.AppEnvPreview {
			ov := app.Spec.EnvironmentDefaults[env.EnvName]
			if ov.Deploy != nil && !*ov.Deploy {
				continue
			}
			selfNS := env.Namespace
			forwarded := ""
			if ov.RoutedToPreview != "" && ov.RoutedMode == domain.RouteModeSwitch {
				forwarded = ov.RoutedToPreview
				if pe, perr := ah.appStore.GetAppEnvironment(ctx, projectName, appName, ov.RoutedToPreview); perr == nil && pe.Namespace != "" {
					selfNS = pe.Namespace
				}
			}
			for i, rt := range routes {
				e.Routes = append(e.Routes, RouteStatusDTO{
					Name:      rt.EffectiveName(i),
					Tier:      string(rt.EffectiveTier()),
					Hostnames: hostsFor(rt, env.EnvName, env.EnvType, env.EnvName),
					Rules:     rulesFor(app.Name, []domain.RouteSpec{rt}, env.EnvName, env.Namespace, func(a string) (string, bool) { return backendNS(a, env.EnvName, selfNS) }, forwarded),
				})
			}
			out.Envs = append(out.Envs, e)
			continue
		}
		// Preview: own rules local, sibling rules forwarded to their base env
		// unless the sibling has a same-named preview.
		baseEnv := env.BaseEnv
		if baseEnv == "" {
			baseEnv = ah.baseStableEnvName(ctx, app)
		}
		for i, rt := range routes {
			st := RouteStatusDTO{
				Name:      rt.EffectiveName(i),
				Tier:      string(rt.EffectiveTier()),
				Hostnames: hostsFor(rt, baseEnv, domain.AppEnvPreview, env.EnvName),
				Rules:     rulesFor(app.Name, []domain.RouteSpec{rt}, baseEnv, env.Namespace, func(a string) (string, bool) { return backendNS(a, baseEnv, env.Namespace) }, ""),
			}
			myHosts := routeHostnames([]domain.RouteSpec{rt})
			for _, sib := range siblings {
				if _, perr := ah.appStore.GetAppEnvironment(ctx, projectName, sib.Name, env.EnvName); perr == nil {
					continue // co-located same-named preview renders its own rules
				}
				sibNS := ah.stableEnvRecord(ctx, sib, baseEnv)
				if sibNS == nil {
					continue
				}
				for _, sr := range ah.effectiveRoutes(ctx, sib) {
					if !sharesAnyHost(myHosts, routeHostnames([]domain.RouteSpec{sr})) {
						continue
					}
					st.Composite = true
					st.Rules = append(st.Rules, rulesFor(sib.Name, []domain.RouteSpec{sr}, baseEnv, env.Namespace, func(a string) (string, bool) {
						if a == sib.Name {
							return sibNS.Namespace, true
						}
						return backendNS(a, baseEnv, sibNS.Namespace)
					}, "")...)
				}
			}
			e.Routes = append(e.Routes, st)
		}
		out.Envs = append(out.Envs, e)
	}
	writeJSON(w, http.StatusOK, out)
}

func sharesAnyHost(a, b map[string]bool) bool {
	for h := range a {
		if b[h] {
			return true
		}
	}
	return false
}

// routeHostURLFor is routeHostURL over an explicit route list (the first
// route's first hostname), so callers can resolve each hostname of a route.
func (ah *appHandler) routeHostURLFor(ctx context.Context, app *domain.App, routes []domain.RouteSpec, baseEnv string, envType domain.AppEnvironmentType, instance, baseDomain string, secure bool) string {
	cp := *app
	cp.Spec.Routes = routes
	cp.Spec.Stack = "" // the explicit list already carries any stack expansion
	return ah.routeHostURL(ctx, &cp, baseEnv, envType, instance, baseDomain, secure)
}
