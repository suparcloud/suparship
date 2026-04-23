// Package app provides business logic for creating and managing apps.
//
// This file implements the Promote function, which copies a release bundle
// from a source environment to a destination environment atomically.
package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/suparcloud/suparship/internal/domain"
)

// ErrNoRelease is returned by Promote when the source environment has no
// release ref to copy. The caller should surface this as a 4xx rather than 5xx.
var ErrNoRelease = errors.New("source environment has no release to promote")

// PromoteRequest carries the parameters for an app release promotion.
//
// Partial component promotions are not supported: all components in an app
// share a single AppReleaseRef, so copying that ref is inherently atomic —
// every component moves together or not at all.
type PromoteRequest struct {
	// ProjectName is the owning project.
	ProjectName string
	// AppName is the app whose release should be promoted.
	AppName string
	// FromEnv is the name of the source environment (e.g. "pr-42", "staging").
	FromEnv string
	// ToEnv is the name of the destination environment (e.g. "staging", "prod").
	ToEnv string
}

// PromoteResult describes the outcome of a successful promotion.
type PromoteResult struct {
	// ProjectName is the owning project.
	ProjectName string
	// AppName is the promoted app.
	AppName string
	// FromEnv is the source environment name.
	FromEnv string
	// ToEnv is the destination environment name.
	ToEnv string
	// Release is the release ref that was copied to the destination.
	// It is a value copy; mutations do not affect the stored record.
	Release domain.AppReleaseRef
}

// Promote copies the release bundle from req.FromEnv to req.ToEnv for the
// given app, then persists the updated destination environment via store.
//
// All-or-nothing guarantee: the release ref is deep-copied and written to the
// destination in a single SaveAppEnvironment call. If the save fails the
// destination is left unchanged.
//
// Validation rules enforced before the write:
//   - ProjectName, AppName, FromEnv, and ToEnv must all be non-empty.
//   - FromEnv and ToEnv must be different names.
//   - Both environments must exist for the app in the store.
//   - ToEnv must not be a preview environment.
//   - For non-preview source envs: FromEnv.Order must be strictly less than ToEnv.Order.
//   - Preview source envs may promote to any non-preview destination.
//   - FromEnv must have a non-nil release (returns ErrNoRelease otherwise).
func Promote(ctx context.Context, store domain.AppStore, req PromoteRequest) (PromoteResult, error) {
	if req.ProjectName == "" || req.AppName == "" || req.FromEnv == "" || req.ToEnv == "" {
		return PromoteResult{}, fmt.Errorf("ProjectName, AppName, FromEnv, and ToEnv are all required")
	}
	if req.FromEnv == req.ToEnv {
		return PromoteResult{}, fmt.Errorf("source and destination environments must differ")
	}

	srcEnv, err := store.GetAppEnvironment(ctx, req.ProjectName, req.AppName, req.FromEnv)
	if err != nil {
		return PromoteResult{}, fmt.Errorf("source environment %q not found: %w", req.FromEnv, err)
	}
	dstEnv, err := store.GetAppEnvironment(ctx, req.ProjectName, req.AppName, req.ToEnv)
	if err != nil {
		return PromoteResult{}, fmt.Errorf("destination environment %q not found: %w", req.ToEnv, err)
	}

	if dstEnv.EnvType == domain.AppEnvPreview {
		return PromoteResult{}, fmt.Errorf("cannot promote to preview environment %q", req.ToEnv)
	}

	// Preview sources may promote to any non-preview destination.
	// For stable-to-stable promotions, the source must precede the destination
	// in the pipeline order (lower Order value = earlier in the chain).
	if srcEnv.EnvType != domain.AppEnvPreview {
		if srcEnv.Order >= dstEnv.Order {
			return PromoteResult{}, fmt.Errorf(
				"cannot promote from %q (order %d) to %q (order %d): source must precede destination in the promotion chain",
				req.FromEnv, srcEnv.Order, req.ToEnv, dstEnv.Order,
			)
		}
	}

	if srcEnv.Release == nil {
		return PromoteResult{}, fmt.Errorf("%w: environment %q has no release", ErrNoRelease, req.FromEnv)
	}

	// Deep-copy the release ref so that any future mutation of the destination
	// record cannot inadvertently alter the source record in the store.
	promoted := domain.AppReleaseRef{
		Image:  srcEnv.Release.Image,
		Tag:    srcEnv.Release.Tag,
		Commit: srcEnv.Release.Commit,
	}
	dstEnv.Release = &promoted

	if err := store.SaveAppEnvironment(ctx, req.ProjectName, dstEnv); err != nil {
		return PromoteResult{}, fmt.Errorf("failed to persist destination environment: %w", err)
	}

	return PromoteResult{
		ProjectName: req.ProjectName,
		AppName:     req.AppName,
		FromEnv:     req.FromEnv,
		ToEnv:       req.ToEnv,
		Release:     promoted,
	}, nil
}
