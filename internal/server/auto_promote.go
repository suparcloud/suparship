package server

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
)

// Suparship owns downstream automatic promotion, including first-deployment
// preparation. Kargo owns Warehouse-to-first-stage delivery and executes all
// promotions and verification. Release selection uses upstream evidence, not
// the source's current occupant; a pin only freezes its own target.
type verifiedAutoPromoter interface {
	NextVerifiedRelease(context.Context, string, string, string, string) (string, error)
}

type downstreamSchedulerGate interface {
	DownstreamSchedulerReady(context.Context, string, string, string) (bool, error)
}

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

	sources, err := domain.ReleasePipeline(app, stable)
	if err != nil {
		return
	}
	byName := map[string]*domain.AppEnvironment{}
	for _, env := range stable {
		byName[env.EnvName] = env
	}

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

	for _, target := range stable {
		source := byName[sources[target.EnvName]]
		if source == nil {
			continue
		}

		tov := app.Spec.EnvironmentDefaults[target.EnvName]
		if tov.PinnedFrom != "" || (tov.Deploy != nil && !*tov.Deploy) {
			continue
		}
		tgt := byEnv[target.EnvName]
		if tgt.Phase == "Promoting" || tgt.Phase == "Verifying" {
			continue
		}
		freight := ""
		if selector, ok := ah.kargoPromoter.(verifiedAutoPromoter); ok {
			gate, ok := ah.kargoPromoter.(downstreamSchedulerGate)
			if !ok {
				continue
			}
			ready, err := gate.DownstreamSchedulerReady(ctx, app.ProjectName, app.Name, target.EnvName)
			if err != nil || !ready {
				slog.Debug("auto-promote: waiting for Kargo scheduler handoff", "app", app.Name, "env", target.EnvName, "err", err)
				continue
			}
			var errSelection error
			freight, errSelection = selector.NextVerifiedRelease(ctx, app.ProjectName, app.Name, source.EnvName, target.EnvName)
			if errSelection != nil {
				slog.Warn("auto-promote: release eligibility unavailable", "app", app.Name, "env", target.EnvName, "err", errSelection)
				continue
			}
			if freight == "" {
				continue
			}
		} else {
			// Compatibility for local/fake providers without verified freight.
			if app.Spec.EnvironmentDefaults[source.EnvName].PinnedFrom != "" {
				continue
			}
			src, ok := byEnv[source.EnvName]
			if !ok || src.CurrentFreight == "" || src.Health != "Healthy" || src.Phase == "Promoting" {
				continue
			}
			freight = src.CurrentFreight
		}
		if tgt.CurrentFreight == freight {
			// The verified selector may deliberately return the current Freight
			// to repair a failed promotion that changed Git before failing.
			if _, verified := ah.kargoPromoter.(verifiedAutoPromoter); !verified {
				continue
			}
		}

		key := app.ProjectName + "/" + app.Name + "/" + target.EnvName
		ah.autoPromoteMu.Lock()
		if a, ok := ah.autoPromoteAttempts[key]; ok &&
			a.freight == freight && time.Since(a.at) < autoPromoteRetryCooldown {
			ah.autoPromoteMu.Unlock()
			continue
		}
		ah.autoPromoteAttempts[key] = autoPromoteAttempt{freight: freight, at: time.Now()}
		ah.autoPromoteMu.Unlock()

		var selection []string
		if _, ok := ah.kargoPromoter.(verifiedAutoPromoter); ok {
			selection = []string{freight}
		}
		resp, err := ah.promoteAppEnv(ctx, app.ProjectName, app.Name, target.EnvName, selection...)
		switch {
		case errors.Is(err, errPromoteTargetNotReady):
			// Target provisioning is incomplete. Retry readiness without approving
			// freight or publishing a potentially unverified seed image.
			slog.Info("auto-promote: target not ready yet — will retry",
				"project", app.ProjectName, "app", app.Name, "env", target.EnvName)
			// Retry sooner than the failure cooldown: readiness is expected.
			ah.autoPromoteMu.Lock()
			ah.autoPromoteAttempts[key] = autoPromoteAttempt{freight: freight, at: time.Now().Add(-autoPromoteRetryCooldown + 2*time.Minute)}
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
				"freight", freight, "promotion", promotion)
		}
	}
}
