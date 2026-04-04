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

// promotionOrder defines the canonical MVP promotion chain.
// preview → staging → prod; higher value = later in the chain.
// This mirrors appPromotionOrder in internal/server but is kept here so the
// domain-level Promote function has no dependency on the HTTP layer.
var promotionOrder = map[domain.AppEnvironmentType]int{
	domain.AppEnvPreview: 0,
	domain.AppEnvStaging: 1,
	domain.AppEnvProd:    2,
}

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
//   - FromEnv must be strictly lower in the promotion chain than ToEnv
//     (preview < staging < prod).
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

	srcOrder, srcOK := promotionOrder[srcEnv.EnvType]
	dstOrder, dstOK := promotionOrder[dstEnv.EnvType]
	if !srcOK || !dstOK {
		return PromoteResult{}, fmt.Errorf(
			"unrecognised environment type: source=%q dest=%q",
			srcEnv.EnvType, dstEnv.EnvType,
		)
	}
	if srcOrder >= dstOrder {
		return PromoteResult{}, fmt.Errorf(
			"cannot promote from %q (%s) to %q (%s): source must precede destination in the promotion chain",
			req.FromEnv, srcEnv.EnvType, req.ToEnv, dstEnv.EnvType,
		)
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
