// Package kube – adapters.go provides thin adapter types that bridge the
// concrete K8s-backed stores (project.K8sStore, preview.K8sStore,
// runtime.K8sProvider) to the domain-level interfaces consumed by the compat
// layer (internal/compat.ServiceBackedAppStore).
//
// Adapter naming convention:
//
//	k8s<Domain>Adapter – wraps a K8s store and exposes domain interface methods.
//
// All adapters are unexported; they are constructed only inside NewServerDeps
// and are not part of the public kube API.
package kube

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/preview"
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/runtime"
)

// compile-time interface compliance checks.
var (
	_ domain.ServiceStore        = (*k8sServiceAdapter)(nil)
	_ domain.ProjectStore        = (*k8sProjectDomainAdapter)(nil)
	_ domain.RuntimeStatusReader = (*k8sRuntimeStatusAdapter)(nil)
	_ domain.PreviewStore        = (*k8sPreviewDomainAdapter)(nil)
	_ domain.AppStore            = (*nullAppStore)(nil)
)

// ── k8sServiceAdapter ────────────────────────────────────────────────────────

// k8sServiceAdapter implements domain.ServiceStore by reading service
// definitions from the project ConfigMap stored by project.K8sStore.
// Services in the ConfigMap are stored under spec.services[].
type k8sServiceAdapter struct {
	projects *project.K8sStore
}

func (a *k8sServiceAdapter) ListServices(ctx context.Context, projectName string) ([]*domain.Service, error) {
	proj, err := a.projects.Get(ctx, projectName)
	if err != nil {
		return nil, fmt.Errorf("listing services: project %q not found: %w", projectName, err)
	}
	svcs := make([]*domain.Service, 0, len(proj.Spec.Services))
	for _, s := range proj.Spec.Services {
		svcs = append(svcs, projectServiceToDomain(projectName, s))
	}
	sort.Slice(svcs, func(i, j int) bool { return svcs[i].Name < svcs[j].Name })
	return svcs, nil
}

func (a *k8sServiceAdapter) GetService(ctx context.Context, projectName, serviceName string) (*domain.Service, error) {
	proj, err := a.projects.Get(ctx, projectName)
	if err != nil {
		return nil, fmt.Errorf("service %q not found: project %q missing: %w", serviceName, projectName, err)
	}
	for _, s := range proj.Spec.Services {
		if s.Name == serviceName {
			return projectServiceToDomain(projectName, s), nil
		}
	}
	return nil, fmt.Errorf("service %q not found in project %q", serviceName, projectName)
}

// SaveService is not implemented for K8s-backed services in MVP; mutations
// should go through the project store directly.
func (a *k8sServiceAdapter) SaveService(_ context.Context, _ string, _ *domain.Service) error {
	return fmt.Errorf("k8sServiceAdapter: SaveService not implemented")
}

// DeleteService is not implemented for K8s-backed services in MVP.
func (a *k8sServiceAdapter) DeleteService(_ context.Context, _, _ string) error {
	return fmt.Errorf("k8sServiceAdapter: DeleteService not implemented")
}

func projectServiceToDomain(projectName string, s project.Service) *domain.Service {
	return &domain.Service{
		Name:         s.Name,
		ProjectName:  projectName,
		TemplateName: s.Template.Name,
	}
}

// ── k8sProjectDomainAdapter ──────────────────────────────────────────────────

// k8sProjectDomainAdapter implements domain.ProjectStore by reading projects
// from project.K8sStore and mapping them to the domain model.
type k8sProjectDomainAdapter struct {
	projects *project.K8sStore
}

func (a *k8sProjectDomainAdapter) ListProjects(ctx context.Context) ([]*domain.Project, error) {
	projs, err := a.projects.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing projects: %w", err)
	}
	out := make([]*domain.Project, 0, len(projs))
	for _, p := range projs {
		out = append(out, projectToDomain(p))
	}
	return out, nil
}

func (a *k8sProjectDomainAdapter) GetProject(ctx context.Context, name string) (*domain.Project, error) {
	p, err := a.projects.Get(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("project %q not found: %w", name, err)
	}
	return projectToDomain(p), nil
}

// SaveProject and DeleteProject are not used by the compat read path; they
// are stubs to satisfy the domain.ProjectStore interface.
func (a *k8sProjectDomainAdapter) SaveProject(_ context.Context, _ *domain.Project) error {
	return fmt.Errorf("k8sProjectDomainAdapter: SaveProject not implemented")
}

func (a *k8sProjectDomainAdapter) DeleteProject(_ context.Context, _ string) error {
	return fmt.Errorf("k8sProjectDomainAdapter: DeleteProject not implemented")
}

func projectToDomain(p *project.Project) *domain.Project {
	envs := make([]domain.Environment, 0, len(p.Spec.Environments))
	for _, e := range p.Spec.Environments {
		envs = append(envs, domain.Environment{
			Name:        e.Name,
			DisplayName: e.DisplayName,
			Order:       e.Order,
		})
	}
	return &domain.Project{
		Name:         p.Metadata.Name,
		DisplayName:  p.Spec.DisplayName,
		Description:  p.Spec.Description,
		Environments: envs,
	}
}

// ── k8sRuntimeStatusAdapter ──────────────────────────────────────────────────

// k8sRuntimeStatusAdapter implements domain.RuntimeStatusReader by querying
// Kubernetes Deployments and Ingresses via runtime.K8sProvider.
// The namespace is derived as {projectName}-{environment}.
type k8sRuntimeStatusAdapter struct {
	provider *runtime.K8sProvider
}

func (a *k8sRuntimeStatusAdapter) GetServiceStatus(ctx context.Context, projectName, serviceName, environment string) (*domain.ServiceStatus, error) {
	ns := runtime.Namespace(projectName, environment)
	info, err := a.provider.GetServiceRuntime(ctx, ns, serviceName)
	if err != nil {
		return nil, fmt.Errorf("getting runtime status for %s/%s in %s: %w", projectName, serviceName, ns, err)
	}
	urls := info.IngressURLs
	if urls == nil {
		urls = []string{}
	}
	return &domain.ServiceStatus{
		Status:       info.Status,
		Environment:  environment,
		Replicas:     info.Replicas,
		Available:    info.Available,
		Image:        info.Image,
		IngressURLs:  urls,
		Namespace:    info.Namespace,
		LastDeployed: info.LastDeployed,
	}, nil
}

// ── k8sPreviewDomainAdapter ──────────────────────────────────────────────────

// k8sPreviewDomainAdapter implements domain.PreviewStore by delegating to
// preview.K8sStore and mapping between the preview and domain model types.
type k8sPreviewDomainAdapter struct {
	store *preview.K8sStore
}

func (a *k8sPreviewDomainAdapter) ListPreviews(ctx context.Context) ([]*domain.Preview, error) {
	previews, err := a.store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing previews: %w", err)
	}
	out := make([]*domain.Preview, 0, len(previews))
	for _, p := range previews {
		out = append(out, previewToDomain(p))
	}
	return out, nil
}

func (a *k8sPreviewDomainAdapter) GetPreview(ctx context.Context, name string) (*domain.Preview, error) {
	p, err := a.store.Get(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("preview %q not found: %w", name, err)
	}
	return previewToDomain(p), nil
}

func (a *k8sPreviewDomainAdapter) CreatePreview(ctx context.Context, p *domain.Preview) error {
	kp := domainToPreview(p)
	return a.store.Save(ctx, kp)
}

func (a *k8sPreviewDomainAdapter) DeletePreview(ctx context.Context, name string) error {
	return a.store.Delete(ctx, name)
}

func previewToDomain(p *preview.Preview) *domain.Preview {
	ns := p.Spec.Project + "-preview-" + p.Metadata.Name
	var createdAt time.Time
	if p.Metadata.CreatedAt != "" {
		if t, err := time.Parse(time.RFC3339, p.Metadata.CreatedAt); err == nil {
			createdAt = t
		}
	}
	return &domain.Preview{
		Name:        p.Metadata.Name,
		ProjectName: p.Spec.Project,
		ServiceName: p.Spec.Service,
		Namespace:   ns,
		Status:      domain.StatusNotDeployed,
		CreatedAt:   createdAt,
	}
}

func domainToPreview(p *domain.Preview) *preview.Preview {
	return &preview.Preview{
		APIVersion: preview.CurrentAPIVersion,
		Kind:       preview.PreviewKind,
		Metadata: preview.PreviewMeta{
			Name:      p.Name,
			CreatedAt: p.CreatedAt.Format(time.RFC3339),
		},
		Spec: preview.PreviewSpec{
			Project: p.ProjectName,
			Service: p.ServiceName,
		},
	}
}

// ── nullAppStore ─────────────────────────────────────────────────────────────

// nullAppStore implements domain.AppStore with empty/error returns.
// It is used as the "primary" store inside compat.ServiceBackedAppStore so
// that every call falls through to the legacy service path, which reads from
// the project ConfigMap. Once a native K8s AppStore is implemented, replace
// nullAppStore with it.
type nullAppStore struct{}

func (nullAppStore) ListApps(_ context.Context, _ string) ([]*domain.App, error) {
	return nil, nil // empty → compat falls back to services
}

func (nullAppStore) GetApp(_ context.Context, _, _ string) (*domain.App, error) {
	return nil, fmt.Errorf("no native app store: using compat fallback")
}

func (nullAppStore) ListAppEnvironments(_ context.Context, _, _ string) ([]*domain.AppEnvironment, error) {
	return nil, nil // empty → compat falls back to services
}

func (nullAppStore) GetAppEnvironment(_ context.Context, _, _, _ string) (*domain.AppEnvironment, error) {
	return nil, fmt.Errorf("no native app store: using compat fallback")
}

func (nullAppStore) ListAppPreviews(_ context.Context, _, _ string) ([]*domain.AppEnvironment, error) {
	return nil, nil // empty → compat falls back to previews
}

func (nullAppStore) SaveApp(_ context.Context, _ string, _ *domain.App) error {
	return fmt.Errorf("nullAppStore: SaveApp not implemented")
}

func (nullAppStore) SaveAppEnvironment(_ context.Context, _ string, _ *domain.AppEnvironment) error {
	return fmt.Errorf("nullAppStore: SaveAppEnvironment not implemented")
}

func (nullAppStore) DeleteAppEnvironment(_ context.Context, _, _, _ string) error {
	return fmt.Errorf("nullAppStore: DeleteAppEnvironment not implemented")
}
