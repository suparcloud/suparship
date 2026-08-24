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

	// BaseDomain is the ingress DNS zone of the base env the preview clones
	// (e.g. "staging.acme.com"). It drives the preview's routing host so the
	// Instance URL matches what the chart actually renders. Empty falls back to
	// "localhost" (local/dev). Only consulted when the app exposes an HTTP route.
	BaseDomain string

	// NamespacePattern is the project's preview namespace pattern. Tokens:
	// {project}, {app}, {name}. Empty falls back to
	// domain.DefaultPreviewNamespacePattern. Validate with
	// domain.ValidatePreviewNamespacePattern before passing it here.
	NamespacePattern string

	// Secure selects https (true) vs http (false) for the generated preview
	// URL. Handlers supply it from the org's EffectiveSecureEndpoints so this
	// pipeline stays free of org I/O.
	Secure bool
}

// PreviewResult holds the pure-function outputs of CreatePreview.
type PreviewResult struct {
	// Instance is the EnvironmentInstance representing the new preview.
	// Its namespace and URL are deterministically derived from the app name
	// and preview name.
	Instance *domain.EnvironmentInstance

	// ArgoApp is the generated ArgoCD Application manifest for this preview.
	// Callers that commit to a GitOps repository should serialise this to
	// YAML and write it to the gitops-output tree.
	ArgoApp *gitops.Application
}

// CreatePreview is the end-to-end preview creation pipeline. It is a pure
// function with no I/O; the caller is responsible for persistence.
//
// Previews are an app-level concept: a preview clones the app's base env and is
// gated solely by the app's PreviewsEnabled opt-in — there is no per-component
// preview gate. A preview renders exactly what the base env renders (the same
// components, with the same enabled flags), so apps that deploy via a chart /
// raw values without enumerated components preview correctly too.
//
// Steps:
//  1. Validate that app is non-nil and previewName is non-empty.
//  2. Verify that the app has previews enabled.
//  3. Build the EnvironmentInstance with a deterministic namespace and URL
//     (via domain.GenerateNamespace / domain.GenerateURL).
//  4. Generate the ArgoCD Application manifest using
//     gitops.BuildArgoApplicationFromInstance.
//
// Returns an error when the app has previews disabled or the request is
// structurally invalid (nil app, empty preview name). All other errors are
// programming mistakes.
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

	ns := domain.GeneratePreviewNamespaceFromPattern(req.App.Name, req.PreviewName, req.App.ProjectName, req.NamespacePattern)

	// A preview gets a routing URL only when the app actually exposes an HTTP
	// route — a worker/agent with no ingress (e.g. a LiveKit agent) gets none,
	// so the UI shows no "Open" link. The host uses the base env's real domain
	// (matching what the chart renders), not a fabricated localhost default.
	url := ""
	if AppHasIngressRoute(req.App) {
		base := req.BaseDomain
		if base == "" {
			base = "localhost"
		}
		url = domain.GenerateURLWithDomain(req.App.Name, req.PreviewName, domain.AppEnvPreview, base, req.Secure)
	}

	inst := &domain.EnvironmentInstance{
		AppName:     req.App.Name,
		ProjectName: req.App.ProjectName,
		EnvType:     domain.AppEnvPreview,
		EnvName:     req.PreviewName,
		Namespace:   ns,
		URL:         url,
		Status:      domain.AppRuntimeStatus{Phase: domain.StatusNotDeployed},
	}

	argoApp := gitops.BuildArgoApplicationFromInstance(req.App, inst, req.BuildOpts)

	return &PreviewResult{
		Instance: inst,
		ArgoApp:  argoApp,
	}, nil
}
