package server

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
)

type validationRunController interface {
	WithValidationLease(context.Context, string, string, string, func(context.Context, *domain.ValidationRun) error) error
	NextValidationFreight(context.Context, string, string, time.Time) (string, time.Time, error)
	ObserveValidation(context.Context, string, string, string, string) (domain.ValidationObservation, error)
	PromoteValidationFreight(context.Context, string, string, string, string) error
	ValidationApplicationsReady(context.Context, []string, bool) (bool, error)
	DownstreamSchedulerReady(context.Context, string, string, string) (bool, error)
}
type validationWorkloadPublisher interface {
	SetValidationWorkloads(context.Context, *domain.App, *domain.AppEnvironment, bool) error
}

const failedValidationRetention = time.Hour

func (ah *appHandler) reconcileEphemeralValidation(ctx context.Context, app *domain.App) {
	if !app.Spec.CD.EphemeralValidation() {
		return
	}
	controller, ok := ah.kargoPromoter.(validationRunController)
	if !ok {
		return
	}
	publisher, ok := ah.gitOpsPublisher.(validationWorkloadPublisher)
	if !ok {
		return
	}
	envName := app.Spec.CD.ReleaseValidationEnvironment
	env, err := ah.appStore.GetAppEnvironment(ctx, app.ProjectName, app.Name, envName)
	if err != nil {
		return
	}
	err = controller.WithValidationLease(ctx, app.ProjectName, app.Name, envName, func(ctx context.Context, run *domain.ValidationRun) error {
		// A queued tick must not act on configuration that was replaced meanwhile.
		latest, err := ah.appStore.GetApp(ctx, app.ProjectName, app.Name)
		if err != nil {
			return err
		}
		if !latest.Spec.CD.EphemeralValidation() || latest.Spec.CD.ReleaseValidationEnvironment != envName {
			return nil
		}
		app = latest
		ready, err := controller.DownstreamSchedulerReady(ctx, app.ProjectName, app.Name, envName)
		if err != nil {
			return err
		}
		if !ready {
			return nil
		}
		observation, err := controller.ObserveValidation(ctx, app.ProjectName, app.Name, envName, run.Freight)
		if err != nil {
			return err
		}
		names := []string{}
		for _, a := range ah.argoAppNamesForEnv(ctx, app.ProjectName, app.Name, envName) {
			names = append(names, a.name)
		}
		return advanceValidationRun(ctx, run, observation, time.Now(), validationRunActions{
			paused: app.Spec.EnvironmentDefaults[envName].PinnedFrom != "" || (app.Spec.EnvironmentDefaults[envName].Suspend != nil && *app.Spec.EnvironmentDefaults[envName].Suspend),
			next: func() (string, time.Time, error) {
				return controller.NextValidationFreight(ctx, app.ProjectName, app.Name, run.Cursor)
			},
			workloads: func(active bool) error { return publisher.SetValidationWorkloads(ctx, app, env, active) },
			ready:     func(present bool) (bool, error) { return controller.ValidationApplicationsReady(ctx, names, present) },
			promote: func() error {
				return controller.PromoteValidationFreight(ctx, app.ProjectName, app.Name, envName, run.Freight)
			},
		})
	})
	if err != nil {
		slog.Debug("ephemeral validation: retrying lifecycle", "project", app.ProjectName, "app", app.Name, "err", err)
	}
}

type validationRunActions struct {
	paused    bool
	next      func() (string, time.Time, error)
	workloads func(bool) error
	ready     func(bool) (bool, error)
	promote   func() error
}

// Advance one durable transition per tick. Never remove resources while Kargo
// is executing, and never reuse capacity until cascading deletion has completed.
func advanceValidationRun(ctx context.Context, run *domain.ValidationRun, obs domain.ValidationObservation, now time.Time, a validationRunActions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if obs.Active {
		return nil
	}
	switch run.Phase {
	case "":
		if obs.CurrentFreight != "" {
			run.Freight = obs.CurrentFreight
			run.Cursor = obs.CurrentCreatedAt
			run.StartedAt = now
			run.Phase = "running"
		} else {
			run.Phase = "cleaning"
		}
	case "idle":
		if a.paused {
			return nil
		}
		freight, at, err := a.next()
		if err != nil {
			return err
		}
		if freight == "" {
			return nil
		}
		run.Freight = freight
		run.Cursor = at
		run.StartedAt = now
		run.FailedAt = time.Time{}
		run.Phase = "provisioning"
	case "provisioning":
		if err := a.workloads(true); err != nil {
			return err
		}
		run.Phase = "waiting"
	case "waiting":
		ready, err := a.ready(true)
		if err != nil {
			return err
		}
		if !ready {
			return nil
		}
		if err := a.promote(); err != nil {
			return err
		}
		run.Phase = "running"
	case "running":
		if obs.Verified {
			run.Phase = "cleaning"
			return nil
		}
		if obs.Failed {
			if run.FailedAt.IsZero() {
				run.FailedAt = now
			}
			if !now.Before(run.FailedAt.Add(failedValidationRetention)) {
				run.Phase = "cleaning"
			}
		}
	case "cleaning":
		if err := a.workloads(false); err != nil {
			return err
		}
		run.Phase = "deleting"
	case "deleting":
		gone, err := a.ready(false)
		if err != nil {
			return err
		}
		if !gone {
			return nil
		}
		run.Phase = "idle"
		run.Freight = ""
		run.StartedAt = time.Time{}
		run.FailedAt = time.Time{}
	default:
		return fmt.Errorf("unknown validation lifecycle phase %q", run.Phase)
	}
	return nil
}

func (ah *appHandler) runEphemeralValidationOnce(ctx context.Context) {
	if ah.projectStore == nil || ah.kargoPromoter == nil || ah.gitOpsPublisher == nil {
		return
	}
	projects, err := ah.projectStore.List(ctx)
	if err != nil {
		return
	}
	for _, project := range projects {
		apps, err := ah.appStore.ListApps(ctx, project.Metadata.Name)
		if err != nil {
			continue
		}
		for _, app := range apps {
			if ctx.Err() != nil {
				return
			}
			ah.reconcileEphemeralValidation(ctx, app)
		}
	}
}
