package compat

import (
	"context"
	"fmt"
	"sort"

	"github.com/suparcloud/suparship/internal/domain"
)

// ServiceBackedAppStore implements domain.AppStore with a transparent fallback
// to legacy service-oriented stores when native app data is absent.
//
// On every read operation the primary AppStore is consulted first.  When the
// primary returns an error or an empty result, the fallback path synthesises an
// equivalent response from legacy service, project, status, and preview stores
// using the pure mapping functions in mapper.go.
//
// Design decisions:
//   - All conversion is in one place (this package).  No if/else leaks into
//     HTTP handlers or fakes.
//   - The adapter is stateless: it holds no data of its own.  All concurrency
//     guarantees belong to the injected stores.
//   - Deletion path: when native AppStore persistence is complete, replace
//     every NewServiceBackedAppStore call with the native store directly and
//     delete this package.
type ServiceBackedAppStore struct {
	// primary is the authoritative native AppStore. Its data is preferred
	// whenever it returns a non-empty result without error.
	primary domain.AppStore

	// services is the legacy service data source used on the fallback path.
	services domain.ServiceStore

	// projects provides project configuration (environment list) needed to
	// enumerate stable environment instances during fallback.
	projects domain.ProjectStore

	// statuses provides live runtime status per service+environment pair.
	statuses domain.RuntimeStatusReader

	// previews provides legacy preview data used to build preview
	// AppEnvironments during fallback.
	previews domain.PreviewStore
}

// compile-time interface compliance check.
var _ domain.AppStore = (*ServiceBackedAppStore)(nil)

// NewServiceBackedAppStore returns a ServiceBackedAppStore ready for use.
// All parameters are required; nil values will cause panics at runtime.
func NewServiceBackedAppStore(
	primary domain.AppStore,
	services domain.ServiceStore,
	projects domain.ProjectStore,
	statuses domain.RuntimeStatusReader,
	previews domain.PreviewStore,
) *ServiceBackedAppStore {
	return &ServiceBackedAppStore{
		primary:  primary,
		services: services,
		projects: projects,
		statuses: statuses,
		previews: previews,
	}
}

// ListApps returns apps for a project.  The primary AppStore is consulted
// first; when it returns an error or an empty list, legacy services are mapped
// to apps via the fallback path.
func (s *ServiceBackedAppStore) ListApps(ctx context.Context, projectName string) ([]*domain.App, error) {
	apps, err := s.primary.ListApps(ctx, projectName)
	if err == nil && len(apps) > 0 {
		return apps, nil
	}
	return s.listAppsFromServices(ctx, projectName)
}

// GetApp returns a single app by name.  The primary AppStore is consulted
// first; when it returns an error the matching legacy service is mapped to an
// app via the fallback path.
func (s *ServiceBackedAppStore) GetApp(ctx context.Context, projectName, appName string) (*domain.App, error) {
	app, err := s.primary.GetApp(ctx, projectName, appName)
	if err == nil {
		return app, nil
	}
	return s.getAppFromService(ctx, projectName, appName)
}

// ListAppEnvironments returns all environment instances (stable + preview) for
// an app.  The primary AppStore is consulted first; when it returns an empty
// list, the fallback builds environments from runtime status and previews.
func (s *ServiceBackedAppStore) ListAppEnvironments(ctx context.Context, projectName, appName string) ([]*domain.AppEnvironment, error) {
	envs, err := s.primary.ListAppEnvironments(ctx, projectName, appName)
	if err == nil && len(envs) > 0 {
		return envs, nil
	}
	return s.listEnvsFromService(ctx, projectName, appName)
}

// GetAppEnvironment returns a single environment by name.  The primary
// AppStore is consulted first; when it returns an error the fallback checks
// the preview store (for preview names) and then runtime status (for stable
// environment names).
func (s *ServiceBackedAppStore) GetAppEnvironment(ctx context.Context, projectName, appName, envName string) (*domain.AppEnvironment, error) {
	env, err := s.primary.GetAppEnvironment(ctx, projectName, appName, envName)
	if err == nil {
		return env, nil
	}
	return s.getEnvFromService(ctx, projectName, appName, envName)
}

// ListAppPreviews returns preview environment instances for an app.  The
// primary AppStore is consulted first; when it returns an empty list the
// fallback builds previews from the legacy PreviewStore.
func (s *ServiceBackedAppStore) ListAppPreviews(ctx context.Context, projectName, appName string) ([]*domain.AppEnvironment, error) {
	previews, err := s.primary.ListAppPreviews(ctx, projectName, appName)
	if err == nil && len(previews) > 0 {
		return previews, nil
	}
	return s.listPreviewsFromLegacy(ctx, projectName, appName)
}

// ── fallback helpers ──────────────────────────────────────────────────────────

func (s *ServiceBackedAppStore) listAppsFromServices(ctx context.Context, projectName string) ([]*domain.App, error) {
	svcs, err := s.services.ListServices(ctx, projectName)
	if err != nil {
		return nil, fmt.Errorf("compat: listing services for project %q: %w", projectName, err)
	}
	apps := make([]*domain.App, 0, len(svcs))
	for _, svc := range svcs {
		apps = append(apps, MapServiceToApp(svc))
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })
	return apps, nil
}

func (s *ServiceBackedAppStore) getAppFromService(ctx context.Context, projectName, appName string) (*domain.App, error) {
	svc, err := s.services.GetService(ctx, projectName, appName)
	if err != nil {
		return nil, fmt.Errorf("app %q not found in project %q", appName, projectName)
	}
	return MapServiceToApp(svc), nil
}

func (s *ServiceBackedAppStore) listEnvsFromService(ctx context.Context, projectName, appName string) ([]*domain.AppEnvironment, error) {
	svc, err := s.services.GetService(ctx, projectName, appName)
	if err != nil {
		return nil, fmt.Errorf("app %q not found in project %q", appName, projectName)
	}

	proj, err := s.projects.GetProject(ctx, projectName)
	if err != nil {
		return nil, fmt.Errorf("compat: getting project %q: %w", projectName, err)
	}

	var envs []*domain.AppEnvironment

	// Build one AppEnvironment per stable project environment by querying the
	// runtime status for that service+environment combination.
	for _, projEnv := range proj.Environments {
		status, statusErr := s.statuses.GetServiceStatus(ctx, projectName, svc.Name, projEnv.Name)
		if statusErr != nil {
			// Best-effort: skip this environment if status is unavailable.
			// This avoids a partial failure blocking the entire list.
			continue
		}
		envs = append(envs, MapServiceStatusToAppEnvironment(svc, projEnv.Name, status))
	}

	// Append preview environments sourced from the legacy PreviewStore.
	// Errors here are swallowed: a missing or empty preview store must not
	// prevent stable environments from being returned.
	previewEnvs, _ := s.listPreviewsFromLegacy(ctx, projectName, appName)
	envs = append(envs, previewEnvs...)

	sort.Slice(envs, func(i, j int) bool { return envs[i].EnvName < envs[j].EnvName })
	return envs, nil
}

func (s *ServiceBackedAppStore) getEnvFromService(ctx context.Context, projectName, appName, envName string) (*domain.AppEnvironment, error) {
	svc, err := s.services.GetService(ctx, projectName, appName)
	if err != nil {
		return nil, fmt.Errorf("app %q not found in project %q", appName, projectName)
	}

	// Try as a preview first: preview names are exact (e.g. "pr-42") and
	// distinct from stable environment names ("staging", "prod").
	preview, previewErr := s.previews.GetPreview(ctx, envName)
	if previewErr == nil && preview.ProjectName == projectName && preview.ServiceName == appName {
		return MapPreviewToAppEnvironment(preview), nil
	}

	// Fall back to a stable environment via runtime status.  The status reader
	// returns a not_deployed response rather than an error for unknown envs, so
	// we always get a valid AppEnvironment when the service exists.
	status, statusErr := s.statuses.GetServiceStatus(ctx, projectName, svc.Name, envName)
	if statusErr != nil {
		return nil, fmt.Errorf("environment %q not found for app %q in project %q", envName, appName, projectName)
	}
	return MapServiceStatusToAppEnvironment(svc, envName, status), nil
}

func (s *ServiceBackedAppStore) listPreviewsFromLegacy(ctx context.Context, projectName, appName string) ([]*domain.AppEnvironment, error) {
	allPreviews, err := s.previews.ListPreviews(ctx)
	if err != nil {
		return nil, fmt.Errorf("compat: listing previews: %w", err)
	}

	var envs []*domain.AppEnvironment
	for _, p := range allPreviews {
		if p.ProjectName != projectName {
			continue
		}
		if appName != "" && p.ServiceName != appName {
			continue
		}
		envs = append(envs, MapPreviewToAppEnvironment(p))
	}
	sort.Slice(envs, func(i, j int) bool { return envs[i].EnvName < envs[j].EnvName })
	return envs, nil
}
