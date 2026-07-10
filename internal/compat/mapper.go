// Package compat provides a backwards-compatibility adapter that surfaces
// legacy service-oriented data through the app-centric domain.AppStore
// interface.
//
// # Background
//
// suparship is mid-transition from a service-centric model
// (domain.Service, project.Service) to an app-centric model
// (domain.App, domain.AppEnvironment).  During this transition, existing
// runtime and demo data is expressed in service-oriented types while the HTTP
// layer increasingly serves app-oriented DTOs through domain.AppStore.
//
// # Purpose
//
// This package is the single, explicit location for all service→app
// translation logic.  Keeping it here means:
//   - Mapping rules are easy to audit and test independently.
//   - No conversion logic leaks into HTTP handlers, fakes, or domain types.
//   - The adapter can be deleted once native app persistence is complete.
//
// # Deletion plan
//
// When domain.AppStore is backed by real Kubernetes storage and all services
// have been migrated to the app model, replace every use of
// ServiceBackedAppStore with the native store and remove this package.
package compat

import "github.com/suparcloud/suparship/internal/domain"

// MapServiceToApp converts a legacy domain.Service to a domain.App using the
// 1-service → 1-app-with-one-web-component default.
//
// Mapping rules:
//   - App.Name and App.ProjectName are copied directly from the service.
//   - DisplayName and Description are passed through unchanged.
//   - Template.Name is taken from Service.TemplateName; Template.Version is
//     left empty because domain.Service does not carry a version string.
//   - A single ComponentSpec named "web" with Type=ComponentWeb is synthesised.
//     This reflects the assumption that every legacy service exposes an HTTP
//     endpoint as its primary workload.
//   - Values and SecretRefs are left empty: domain.Service does not carry
//     input values (those live in project.Service and are not needed for the
//     read-only app views this adapter targets).
func MapServiceToApp(svc *domain.Service) *domain.App {
	return &domain.App{
		Name:        svc.Name,
		ProjectName: svc.ProjectName,
		Spec: domain.AppSpec{
			DisplayName: svc.DisplayName,
			Description: svc.Description,
			Template: domain.AppTemplateRef{
				Name: svc.TemplateName,
				// Version intentionally omitted: not available in domain.Service.
			},
			// Default web component: every legacy service is treated as an
			// HTTP-facing workload with a single deployable component named
			// "web".
			Components: []domain.ComponentSpec{
				{
					Name:       "web",
					Type:       domain.ComponentWeb,
					Enabled:    true,
					ExposeMode: domain.ExposeExternal,
				},
			},
			PreviewsEnabled: true,
		},
	}
}

// MapServiceStatusToAppEnvironment maps a domain.ServiceStatus for one stable
// environment into a domain.AppEnvironment.
//
// Parameters:
//   - svc: the owning service (provides AppName and ProjectName).
//   - envName: the logical environment name (e.g. "staging", "prod").
//   - status: the live runtime observation for that environment.
//
// Mapping rules:
//   - EnvType is inferred via ParseAppEnvironmentType.  When envName does not
//     match a known type (e.g. a custom name like "qa" or "dev"),
//     AppEnvStaging is used as a safe default so the environment still renders
//     in the UI without dropping data.
//   - Release is populated only when ServiceStatus.Image is non-empty.
//   - URLs are taken from ServiceStatus.IngressURLs; a nil slice is normalised
//     to an empty slice so callers never receive nil.
//   - Replica counts and LastDeployed are mapped directly from the status.
func MapServiceStatusToAppEnvironment(svc *domain.Service, envName string, status *domain.ServiceStatus) *domain.AppEnvironment {
	envType, err := domain.ParseAppEnvironmentType(envName)
	if err != nil {
		// Custom environment names (e.g. "dev", "qa") default to staging type
		// so the environment remains visible in the UI without silent data loss.
		envType = domain.AppEnvStaging
	}

	var release *domain.AppReleaseRef
	if status.Image != "" {
		release = &domain.AppReleaseRef{Image: status.Image}
	}

	urls := status.IngressURLs
	if urls == nil {
		urls = []string{}
	}

	return &domain.AppEnvironment{
		AppName:     svc.Name,
		ProjectName: svc.ProjectName,
		EnvName:     envName,
		EnvType:     envType,
		Namespace:   status.Namespace,
		Release:     release,
		URLs:        urls,
		Status: domain.AppRuntimeStatus{
			Phase:        status.Status,
			Replicas:     status.Replicas,
			Available:    status.Available,
			LastDeployed: status.LastDeployed,
		},
	}
}

// MapPreviewToAppEnvironment converts a legacy domain.Preview to a preview
// domain.AppEnvironment.
//
// Mapping rules:
//   - AppName comes from Preview.ServiceName (service name == app name during
//     the transition period; they share the same identifier space).
//   - EnvType is always AppEnvPreview regardless of any other field.
//   - EnvName is the preview name (e.g. "pr-42").
//   - URLs contains Preview.URL when non-empty; otherwise an empty slice.
//   - Status.Phase is taken from Preview.Status.  Replica counts are zero
//     because the legacy Preview type does not track them.
//   - Release is not populated: the legacy Preview type does not carry image
//     information.
func MapPreviewToAppEnvironment(p *domain.Preview) *domain.AppEnvironment {
	urls := []string{}
	if p.URL != "" {
		urls = []string{p.URL}
	}

	return &domain.AppEnvironment{
		AppName:     p.ServiceName,
		ProjectName: p.ProjectName,
		EnvName:     p.Name,
		EnvType:     domain.AppEnvPreview,
		Namespace:   p.Namespace,
		URLs:        urls,
		// Release is nil: legacy Preview does not carry image information.
		Status: domain.AppRuntimeStatus{
			Phase: p.Status,
		},
	}
}
