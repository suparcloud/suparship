// Package app — preview creation pipeline.
//
// CreatePreview is the end-to-end, pure-function pipeline for provisioning a
// preview EnvironmentInstance. It mirrors the Create function for stable
// environments and is designed to be composed in the same way: the caller
// performs validation, calls CreatePreview, then persists and returns the
// result.
package app

import (
	"fmt"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/gitops"
	"github.com/suparcloud/suparship/internal/helmvalues"
)

// PreviewRequest carries the inputs required to create a new preview
// EnvironmentInstance through the full creation pipeline.
type PreviewRequest struct {
	// App is the parent app. Must not be nil.
	App *domain.App

	// PreviewName is the sanitised, validated preview identifier (e.g. "pr-42").
	// Callers must sanitise with domain.SanitizePreviewName and validate with
	// domain.ValidatePreviewName before passing the value here.
	PreviewName string

	// BuildOpts carries GitOps and ArgoCD configuration for the generated
	// ArgoCD Application manifest. RepoURL is required when the ArgoCD
	// Application should be committed to a GitOps repository; all other
	// fields have sensible defaults.
	BuildOpts gitops.BuildOptions
}

// PreviewResult holds the pure-function outputs of CreatePreview.
type PreviewResult struct {
	// Instance is the EnvironmentInstance representing the new preview.
	// Its namespace and URL are deterministically derived from the app name
	// and preview name.
	Instance *domain.EnvironmentInstance

	// HelmValues is the generated Helm values for the preview environment.
	// Non-preview-enabled components are disabled in the output regardless
	// of their ComponentSpec.Enabled state.
	HelmValues helmvalues.HelmValues

	// ArgoApp is the generated ArgoCD Application manifest for this preview.
	// Callers that commit to a GitOps repository should serialise this to
	// YAML and write it to the gitops-output tree.
	ArgoApp *gitops.Application
}

// CreatePreview is the end-to-end preview creation pipeline. It is a pure
// function with no I/O; the caller is responsible for persistence.
//
// Steps:
//  1. Validate that app is non-nil and previewName is non-empty.
//  2. Verify that the app has previews enabled and at least one enabled
//     component.
//  3. Build the EnvironmentInstance with a deterministic namespace and URL
//     (via domain.GenerateNamespace / domain.GenerateURL).
//  4. Generate Helm values using helmvalues.MapToHelmValues. A preview deploys
//     all of the app's enabled components — the same set its base env runs.
//  5. Generate the ArgoCD Application manifest using
//     gitops.BuildArgoApplicationFromInstance.
//
// Returns an error when the app has previews disabled, has no enabled
// components, or the request is structurally invalid (nil app, empty preview
// name). All other errors are programming mistakes.
func CreatePreview(req PreviewRequest) (*PreviewResult, error) {
	if req.App == nil {
		return nil, fmt.Errorf("app must not be nil")
	}
	if req.PreviewName == "" {
		return nil, fmt.Errorf("preview name must not be empty")
	}

	if !req.App.Spec.PreviewsEnabled {
		return nil, fmt.Errorf("app %q has previews disabled", req.App.Name)
	}
	if len(EnabledComponents(req.App.Spec.Components)) == 0 {
		return nil, fmt.Errorf("app %q has no enabled components", req.App.Name)
	}

	ns := domain.GenerateNamespace(req.App.Name, req.PreviewName, domain.AppEnvPreview)
	url := domain.GenerateURL(req.App.Name, req.PreviewName, domain.AppEnvPreview)

	inst := &domain.EnvironmentInstance{
		AppName:     req.App.Name,
		ProjectName: req.App.ProjectName,
		EnvType:     domain.AppEnvPreview,
		EnvName:     req.PreviewName,
		Namespace:   ns,
		URL:         url,
		Status:      domain.AppRuntimeStatus{Phase: domain.StatusNotDeployed},
	}

	hv := helmvalues.MapToHelmValues(req.App, req.PreviewName, domain.AppEnvPreview)

	argoApp := gitops.BuildArgoApplicationFromInstance(req.App, inst, req.BuildOpts)

	return &PreviewResult{
		Instance:   inst,
		HelmValues: hv,
		ArgoApp:    argoApp,
	}, nil
}
