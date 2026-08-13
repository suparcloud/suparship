package server

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
)

// The CD auto-promotion reconciler: each tick it promotes apps that opted in
// (cd.autoPromote, or their stack's flag) one step down their pipeline whenever
// an upstream env runs newer freight than its successor.
//
// Why server-side, when Kargo has autoPromotionEnabled: Kargo only
// auto-promotes freight that is AVAILABLE to a stage — verified in an upstream
// stage or explicitly approved for it. suparship models no Stage verification,
// and in practice freight doesn't reliably reach verifiedIn — the same reason
// manual promotion approves freight explicitly (see KargoStore.CreatePromotion).
// So the policy flag alone leaves prod waiting on availability that never
// comes: staging keeps auto-promoting (its freight comes DIRECT from the
// Warehouse) while prod never follows. Driving the promotion through
// promoteAppEnv reuses the manual path end to end: freight approval, the
// ArgoCD-Application gate, and first-promotion env materialization — so
// auto-promote also handles an env that was never manually promoted.
//
// The gate for "staging is good" stays the staging promotion's argocd-update
// health-wait: a stage's CurrentFreight only advances when its promotion
// (including that wait) succeeded, and we additionally require the source
// stage to be Healthy and not mid-promotion at reconcile time.

// autoPromoteRetryCooldown bounds how often a FAILING promotion is retried for
// the same freight — without it, a persistently failing target would get a new
// Promotion CR every tick. A new freight resets the cooldown immediately.
const autoPromoteRetryCooldown = 10 * time.Minute

type autoPromoteAttempt struct {
	freight string
	at      time.Time
}

// runAutoPromoteOnce scans every app once and promotes where due. Errors are
// logged, never returned — this is a background concern.
func (ah *appHandler) runAutoPromoteOnce(ctx context.Context) {
	if ah.kargoPipelineReader == nil || ah.kargoPromoter == nil || ah.projectStore == nil {
		return
	}
	projects, err := ah.projectStore.List(ctx)
	if err != nil {
		slog.Debug("auto-promote: listing projects failed", "err", err)
		return
	}
	for _, proj := range projects {
		apps, err := ah.appStore.ListApps(ctx, proj.Metadata.Name)
		if err != nil {
			slog.Debug("auto-promote: listing apps failed", "project", proj.Metadata.Name, "err", err)
			continue
		}
		for _, app := range apps {
			if ctx.Err() != nil {
				return
			}
			ah.autoPromoteApp(ctx, app)
		}
	}
}

// effectiveAutoPromote mirrors the publish adapter: the app's own flag, ORed
// with its stack's cascade.
func (ah *appHandler) effectiveAutoPromote(ctx context.Context, app *domain.App) bool {
	if app.Spec.CD.AutoPromote {
		return true
	}
	if app.Spec.Stack != "" && ah.stackStore != nil {
		if st, err := ah.stackStore.GetStack(ctx, app.ProjectName, app.Spec.Stack); err == nil && st != nil &&
			st.Spec.AutoPromote != nil && *st.Spec.AutoPromote {
			return true
		}
	}
	return false
}

// autoPromoteApp promotes one app a single step down its pipeline where due.
// One step per tick is deliberate: a chain (staging→prod→…) advances one env
// per interval, each step re-gated on the newly-promoted env's health.
func (ah *appHandler) autoPromoteApp(ctx context.Context, app *domain.App) {
	if app.Spec.IsDirect() || !ah.effectiveAutoPromote(ctx, app) {
		return
	}
	envs, err := ah.appStore.ListAppEnvironments(ctx, app.ProjectName, app.Name)
	if err != nil {
		return
	}
	var stable []*domain.AppEnvironment
	for _, e := range envs {
		if e.EnvType != domain.AppEnvPreview {
			stable = append(stable, e)
		}
	}
	if len(stable) < 2 {
		return
	}
	sort.Slice(stable, func(i, j int) bool { return stable[i].Order < stable[j].Order })

	stages, err := ah.kargoPipelineReader.ListAppStageStatuses(ctx, app.ProjectName, app.Name)
	if err != nil {
		slog.Debug("auto-promote: stage status read failed",
			"project", app.ProjectName, "app", app.Name, "err", err)
		return
	}
	byEnv := make(map[string]KargoStageStatusResult, len(stages))
	for _, s := range stages {
		byEnv[s.EnvName] = s
	}

	for i := 1; i < len(stable); i++ {
		source, target := stable[i-1], stable[i]
		// A held (pinned/rolled-back) SOURCE must not fan its frozen image
		// downstream; a held or decommissioned TARGET is frozen by definition
		// (promoteAppEnv would refuse both — skip quietly to keep logs sane).
		if app.Spec.EnvironmentDefaults[source.EnvName].PinnedFrom != "" {
			continue
		}
		tov := app.Spec.EnvironmentDefaults[target.EnvName]
		if tov.PinnedFrom != "" || (tov.Deploy != nil && !*tov.Deploy) {
			continue
		}
		src, ok := byEnv[source.EnvName]
		if !ok || src.CurrentFreight == "" || src.Health != "Healthy" || src.Phase == "Promoting" {
			continue
		}
		// tgt may be missing entirely (stage not yet materialized — first
		// promotion); the zero value's empty CurrentFreight handles that.
		tgt := byEnv[target.EnvName]
		if tgt.Phase == "Promoting" || tgt.CurrentFreight == src.CurrentFreight {
			continue
		}

		key := app.ProjectName + "/" + app.Name + "/" + target.EnvName
		ah.autoPromoteMu.Lock()
		if a, ok := ah.autoPromoteAttempts[key]; ok &&
			a.freight == src.CurrentFreight && time.Since(a.at) < autoPromoteRetryCooldown {
			ah.autoPromoteMu.Unlock()
			continue
		}
		ah.autoPromoteAttempts[key] = autoPromoteAttempt{freight: src.CurrentFreight, at: time.Now()}
		ah.autoPromoteMu.Unlock()

		resp, err := ah.promoteAppEnv(ctx, app.ProjectName, app.Name, target.EnvName)
		switch {
		case errors.Is(err, errPromoteTargetNotReady):
			// Manifests published, Application not generated yet — normal on a
			// first promotion; the next tick (after cooldown reset below) retries.
			slog.Info("auto-promote: target not ready yet — will retry",
				"project", app.ProjectName, "app", app.Name, "env", target.EnvName)
			// Retry sooner than the failure cooldown: readiness is expected.
			ah.autoPromoteMu.Lock()
			ah.autoPromoteAttempts[key] = autoPromoteAttempt{freight: src.CurrentFreight, at: time.Now().Add(-autoPromoteRetryCooldown + 2*time.Minute)}
			ah.autoPromoteMu.Unlock()
		case err != nil:
			slog.Warn("auto-promote: promotion failed",
				"project", app.ProjectName, "app", app.Name, "env", target.EnvName, "err", err)
		default:
			promotion := ""
			if resp.KargoPromotion != nil {
				promotion = resp.KargoPromotion.Name
			}
			slog.Info("auto-promote: promoted",
				"project", app.ProjectName, "app", app.Name,
				"from", source.EnvName, "to", target.EnvName,
				"freight", src.CurrentFreight, "promotion", promotion)
		}
	}
}
