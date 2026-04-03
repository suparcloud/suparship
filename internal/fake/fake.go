// Package fake provides in-memory implementations of every domain store and
// reader interface for local development and unit testing.
//
// Use NewSeededDevRuntime to get a DevRuntime pre-loaded with a default org,
// demo project, services, templates, previews, status data, and sample logs.
// No Kubernetes cluster or network connection is required.
//
// Activate by setting SUPARSHIP_CLUSTER_MODE=fake (see .env.example).
// The fake data resets to the seeded defaults on every process restart.
package fake

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/suparcloud/suparship/internal/domain"
)

// compile-time interface compliance checks.
var (
	_ domain.ProjectStore        = (*DevRuntime)(nil)
	_ domain.ServiceStore        = (*DevRuntime)(nil)
	_ domain.TemplateStore       = (*DevRuntime)(nil)
	_ domain.PreviewStore        = (*DevRuntime)(nil)
	_ domain.AppStore            = (*DevRuntime)(nil)
	_ domain.RuntimeStatusReader = (*DevRuntime)(nil)
	_ domain.LogReader           = (*DevRuntime)(nil)
)

// DevRuntime is an in-memory bundle of all domain store and reader interfaces.
// It is safe for concurrent reads; writes use a mutex so the fake can be
// exercised in parallel tests without data races.
//
// All write operations (Save*, Create*, Delete*) are transient — data
// returns to its seeded state on the next call to NewSeededDevRuntime.
type DevRuntime struct {
	mu        sync.RWMutex
	org       *domain.Org
	projects  map[string]*domain.Project
	services  map[string]map[string]*domain.Service // projectName → serviceName → Service
	apps      map[string]map[string]*domain.App     // projectName → appName → App
	appEnvs   map[string]map[string]map[string]*domain.AppEnvironment // projectName → appName → envName → AppEnvironment
	templates map[string]*domain.Template
	previews  map[string]*domain.Preview
	statuses  map[string]*domain.ServiceStatus // "project/service/env" → status
	logLines  []domain.LogLine                 // shared fixture lines returned for any service
}

// NewSeededDevRuntime returns a DevRuntime loaded with deterministic demo
// data. The seed values are static — same output on every call.
func NewSeededDevRuntime() *DevRuntime {
	r := &DevRuntime{
		projects:  make(map[string]*domain.Project),
		services:  make(map[string]map[string]*domain.Service),
		apps:      make(map[string]map[string]*domain.App),
		appEnvs:   make(map[string]map[string]map[string]*domain.AppEnvironment),
		templates: make(map[string]*domain.Template),
		previews:  make(map[string]*domain.Preview),
		statuses:  make(map[string]*domain.ServiceStatus),
	}
	seed(r)
	return r
}

// GetOrg returns the default seeded organization. There is no OrgStore
// interface yet; callers that need the org can use this method directly.
func (r *DevRuntime) GetOrg() *domain.Org {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.org
}

// ── ProjectStore ─────────────────────────────────────────────────────────────

func (r *DevRuntime) ListProjects(_ context.Context) ([]*domain.Project, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*domain.Project, 0, len(r.projects))
	for _, p := range r.projects {
		out = append(out, p)
	}
	return out, nil
}

func (r *DevRuntime) GetProject(_ context.Context, name string) (*domain.Project, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.projects[name]
	if !ok {
		return nil, fmt.Errorf("project %q not found", name)
	}
	return p, nil
}

func (r *DevRuntime) SaveProject(_ context.Context, p *domain.Project) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.projects[p.Name] = p
	if r.services[p.Name] == nil {
		r.services[p.Name] = make(map[string]*domain.Service)
	}
	return nil
}

func (r *DevRuntime) DeleteProject(_ context.Context, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.projects[name]; !ok {
		return fmt.Errorf("project %q not found", name)
	}
	delete(r.projects, name)
	delete(r.services, name)
	return nil
}

// ── ServiceStore ─────────────────────────────────────────────────────────────

func (r *DevRuntime) ListServices(_ context.Context, projectName string) ([]*domain.Service, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	svcMap, ok := r.services[projectName]
	if !ok {
		return nil, fmt.Errorf("project %q not found", projectName)
	}
	out := make([]*domain.Service, 0, len(svcMap))
	for _, s := range svcMap {
		out = append(out, s)
	}
	return out, nil
}

func (r *DevRuntime) GetService(_ context.Context, projectName, serviceName string) (*domain.Service, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	svcMap, ok := r.services[projectName]
	if !ok {
		return nil, fmt.Errorf("project %q not found", projectName)
	}
	svc, ok := svcMap[serviceName]
	if !ok {
		return nil, fmt.Errorf("service %q not found in project %q", serviceName, projectName)
	}
	return svc, nil
}

func (r *DevRuntime) SaveService(_ context.Context, projectName string, svc *domain.Service) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.services[projectName] == nil {
		r.services[projectName] = make(map[string]*domain.Service)
	}
	svc.ProjectName = projectName
	r.services[projectName][svc.Name] = svc
	return nil
}

func (r *DevRuntime) DeleteService(_ context.Context, projectName, serviceName string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	svcMap, ok := r.services[projectName]
	if !ok {
		return fmt.Errorf("project %q not found", projectName)
	}
	if _, ok := svcMap[serviceName]; !ok {
		return fmt.Errorf("service %q not found in project %q", serviceName, projectName)
	}
	delete(svcMap, serviceName)
	return nil
}

// ── TemplateStore ────────────────────────────────────────────────────────────

func (r *DevRuntime) ListTemplates(_ context.Context) ([]*domain.Template, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*domain.Template, 0, len(r.templates))
	for _, t := range r.templates {
		out = append(out, t)
	}
	return out, nil
}

func (r *DevRuntime) GetTemplate(_ context.Context, name string) (*domain.Template, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.templates[name]
	if !ok {
		return nil, fmt.Errorf("template %q not found", name)
	}
	return t, nil
}

// ── PreviewStore ─────────────────────────────────────────────────────────────

func (r *DevRuntime) ListPreviews(_ context.Context) ([]*domain.Preview, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*domain.Preview, 0, len(r.previews))
	for _, p := range r.previews {
		out = append(out, p)
	}
	return out, nil
}

func (r *DevRuntime) GetPreview(_ context.Context, name string) (*domain.Preview, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.previews[name]
	if !ok {
		return nil, fmt.Errorf("preview %q not found", name)
	}
	return p, nil
}

func (r *DevRuntime) CreatePreview(_ context.Context, p *domain.Preview) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.previews[p.Name]; exists {
		return fmt.Errorf("preview %q already exists", p.Name)
	}
	r.previews[p.Name] = p
	return nil
}

func (r *DevRuntime) DeletePreview(_ context.Context, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.previews[name]; !ok {
		return fmt.Errorf("preview %q not found", name)
	}
	delete(r.previews, name)
	return nil
}

// ── AppStore ─────────────────────────────────────────────────────────────────

func (r *DevRuntime) ListApps(_ context.Context, projectName string) ([]*domain.App, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	appMap, ok := r.apps[projectName]
	if !ok {
		return nil, fmt.Errorf("project %q not found", projectName)
	}
	out := make([]*domain.App, 0, len(appMap))
	for _, a := range appMap {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (r *DevRuntime) GetApp(_ context.Context, projectName, appName string) (*domain.App, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	appMap, ok := r.apps[projectName]
	if !ok {
		return nil, fmt.Errorf("project %q not found", projectName)
	}
	a, ok := appMap[appName]
	if !ok {
		return nil, fmt.Errorf("app %q not found in project %q", appName, projectName)
	}
	return a, nil
}

func (r *DevRuntime) ListAppEnvironments(_ context.Context, projectName, appName string) ([]*domain.AppEnvironment, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	projectEnvs, ok := r.appEnvs[projectName]
	if !ok {
		return []*domain.AppEnvironment{}, nil
	}
	envMap, ok := projectEnvs[appName]
	if !ok {
		return []*domain.AppEnvironment{}, nil
	}
	out := make([]*domain.AppEnvironment, 0, len(envMap))
	for _, e := range envMap {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EnvName < out[j].EnvName })
	return out, nil
}

func (r *DevRuntime) GetAppEnvironment(_ context.Context, projectName, appName, envName string) (*domain.AppEnvironment, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	projectEnvs, ok := r.appEnvs[projectName]
	if !ok {
		return nil, fmt.Errorf("app environment %q not found for app %q in project %q", envName, appName, projectName)
	}
	envMap, ok := projectEnvs[appName]
	if !ok {
		return nil, fmt.Errorf("app environment %q not found for app %q in project %q", envName, appName, projectName)
	}
	env, ok := envMap[envName]
	if !ok {
		return nil, fmt.Errorf("app environment %q not found for app %q in project %q", envName, appName, projectName)
	}
	return env, nil
}

// ── RuntimeStatusReader ──────────────────────────────────────────────────────

// GetServiceStatus returns a pre-seeded ServiceStatus for known
// project/service/environment combinations, and a "not_deployed" status for
// anything else. This allows UI development without a real cluster.
func (r *DevRuntime) GetServiceStatus(_ context.Context, projectName, serviceName, environment string) (*domain.ServiceStatus, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	key := statusKey(projectName, serviceName, environment)
	if s, ok := r.statuses[key]; ok {
		return s, nil
	}
	return &domain.ServiceStatus{
		Status:      domain.StatusNotDeployed,
		Environment: environment,
		Namespace:   projectName + "-" + environment,
		IngressURLs: []string{},
	}, nil
}

// ── LogReader ────────────────────────────────────────────────────────────────

// GetLogs returns a fixed set of sample log lines. When tailLines is > 0,
// only the last tailLines entries are returned.
func (r *DevRuntime) GetLogs(_ context.Context, _, _, _ string, tailLines int) ([]domain.LogLine, error) {
	r.mu.RLock()
	lines := r.logLines
	r.mu.RUnlock()

	if tailLines > 0 && tailLines < len(lines) {
		lines = lines[len(lines)-tailLines:]
	}
	return lines, nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

func statusKey(project, service, env string) string {
	return project + "/" + service + "/" + env
}
