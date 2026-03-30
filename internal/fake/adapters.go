// Package fake – adapters.go bridges the domain-level DevRuntime with the
// concrete interface types expected by the suparship HTTP server
// (project.Store, preview.Store, runtime.Provider, runtime.LogsProvider,
// auth.Authenticator, rbac.OrgProvider).
//
// These adapters are intended ONLY for local development.  Activate them by
// setting SUPARSHIP_CLUSTER_MODE=fake in your environment (see .env.example).
// No Kubernetes API calls are made; all state lives in process memory and
// resets to seed defaults on every process restart.
//
// Use NewDevServerDeps to obtain a fully-wired bundle ready to be passed
// directly into server.Config.
package fake

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/suparcloud/suparship/internal/auth"
	"github.com/suparcloud/suparship/internal/preview"
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/runtime"
)

// compile-time interface compliance checks for adapters.
var (
	_ auth.Authenticator   = (*FakeAuthenticator)(nil)
	_ rbac.OrgProvider     = (*FakeOrgProvider)(nil)
	_ project.Store        = (*FakeProjectStore)(nil)
	_ preview.Store        = (*FakePreviewStore)(nil)
	_ runtime.Provider     = (*FakeRuntimeProvider)(nil)
	_ runtime.LogsProvider = (*FakeLogsProvider)(nil)
)

// DevServerDeps bundles all fake adapter implementations needed to run the
// suparship HTTP server without a Kubernetes cluster.  Pass each exported
// field to the matching server.Config field.
//
// Default dev credentials: username "admin", password "admin".
// All data is seeded deterministically; writes are transient.
type DevServerDeps struct {
	Authenticator   *FakeAuthenticator
	OrgProvider     *FakeOrgProvider
	ProjectStore    *FakeProjectStore
	PreviewStore    *FakePreviewStore
	RuntimeProvider *FakeRuntimeProvider
	LogsProvider    *FakeLogsProvider
}

// NewDevServerDeps returns a DevServerDeps bundle pre-loaded with the same
// demo seed data as NewSeededDevRuntime.  Each call returns an independent
// in-memory instance; there is no shared state between instances.
func NewDevServerDeps() *DevServerDeps {
	return &DevServerDeps{
		Authenticator:   &FakeAuthenticator{},
		OrgProvider:     &FakeOrgProvider{},
		ProjectStore:    newFakeProjectStore(),
		PreviewStore:    newFakePreviewStore(),
		RuntimeProvider: newFakeRuntimeProvider(),
		LogsProvider:    &FakeLogsProvider{},
	}
}

// ── FakeAuthenticator ────────────────────────────────────────────────────────

// FakeAdminUsername and FakeAdminPassword are the hardcoded credentials
// accepted by FakeAuthenticator.  They are intentionally obvious — never use
// them outside local development.
const (
	FakeAdminUsername = "admin"
	FakeAdminPassword = "admin"
)

// FakeAuthenticator implements auth.Authenticator with a single hardcoded
// dev credential.  Passwords are compared in plain text; no bcrypt is used.
type FakeAuthenticator struct{}

func (a *FakeAuthenticator) Authenticate(_ context.Context, username, password string) (*auth.Credentials, error) {
	if username == FakeAdminUsername && password == FakeAdminPassword {
		return &auth.Credentials{Username: username}, nil
	}
	return nil, auth.ErrInvalidCredentials
}

// ── FakeOrgProvider ──────────────────────────────────────────────────────────

// FakeOrgProvider implements rbac.OrgProvider with a static default org that
// gives the fake admin user org_admin on all projects.
type FakeOrgProvider struct{}

func (p *FakeOrgProvider) GetOrg(_ context.Context) (*rbac.Org, error) {
	return &rbac.Org{
		Name:        "default",
		DisplayName: "My Organization",
		CreatedAt:   seedCreatedAt.Format(time.RFC3339),
		Teams: []rbac.Team{
			{
				Name:        "admins",
				DisplayName: "Administrators",
				Members:     []string{FakeAdminUsername},
			},
		},
		RoleBindings: []rbac.RoleBinding{
			{Project: "*", Team: "admins", Role: rbac.RoleOrgAdmin},
		},
	}, nil
}

// ── FakeProjectStore ─────────────────────────────────────────────────────────

// FakeProjectStore implements project.Store with an in-memory map.  It is
// seeded with the demo project (two environments, one hello service).
// Mutations (Save, Delete) are transient — data resets when
// NewDevServerDeps is called again.
type FakeProjectStore struct {
	mu       sync.RWMutex
	projects map[string]*project.Project
}

func newFakeProjectStore() *FakeProjectStore {
	s := &FakeProjectStore{projects: make(map[string]*project.Project)}

	demo := &project.Project{
		APIVersion: project.CurrentAPIVersion,
		Kind:       project.ProjectKind,
		Metadata:   project.ProjectMeta{Name: "demo"},
		Spec: project.ProjectSpec{
			DisplayName: "Demo Project",
			Description: "Explore suparShip with a pre-seeded project.",
			Environments: []project.Environment{
				{Name: "staging", DisplayName: "Staging", Order: 1},
				{Name: "prod", DisplayName: "Production", Order: 2},
			},
			Services: []project.Service{
				{
					Name:     "hello",
					Template: project.TemplateRef{Name: "web-service", Version: "1.0.0"},
					Values: map[string]any{
						"image_repository": "ghcr.io/suparcloud/hello",
						"image_tag":        "v1.0.0",
					},
				},
			},
		},
	}
	s.projects[demo.Metadata.Name] = demo
	return s
}

func (s *FakeProjectStore) List(_ context.Context) ([]*project.Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*project.Project, 0, len(s.projects))
	for _, p := range s.projects {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Metadata.Name < out[j].Metadata.Name
	})
	return out, nil
}

func (s *FakeProjectStore) Get(_ context.Context, name string) (*project.Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	p, ok := s.projects[name]
	if !ok {
		return nil, fmt.Errorf("project %q not found", name)
	}
	return p, nil
}

// Save upserts the project, replacing any existing entry for the same name.
// The server's service-creation handler uses Save to persist new services by
// appending to proj.Spec.Services and calling Save on the modified project.
func (s *FakeProjectStore) Save(_ context.Context, p *project.Project) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.projects[p.Metadata.Name] = p
	return nil
}

func (s *FakeProjectStore) Delete(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.projects[name]; !ok {
		return fmt.Errorf("project %q not found", name)
	}
	delete(s.projects, name)
	return nil
}

// ── FakePreviewStore ─────────────────────────────────────────────────────────

// FakePreviewStore implements preview.Store with an in-memory map.  It is
// seeded with a single demo preview (pr-42 for the hello service).
// Save performs an upsert; Delete removes the entry.
type FakePreviewStore struct {
	mu       sync.RWMutex
	previews map[string]*preview.Preview
}

func newFakePreviewStore() *FakePreviewStore {
	s := &FakePreviewStore{previews: make(map[string]*preview.Preview)}

	pr42 := &preview.Preview{
		APIVersion: preview.CurrentAPIVersion,
		Kind:       preview.PreviewKind,
		Metadata: preview.PreviewMeta{
			Name:      "pr-42",
			CreatedAt: seedCreatedAt.Format(time.RFC3339),
		},
		Spec: preview.PreviewSpec{
			Project: "demo",
			Service: "hello",
		},
	}
	s.previews[pr42.Metadata.Name] = pr42
	return s
}

func (s *FakePreviewStore) List(_ context.Context) ([]*preview.Preview, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*preview.Preview, 0, len(s.previews))
	for _, p := range s.previews {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Metadata.Name < out[j].Metadata.Name
	})
	return out, nil
}

func (s *FakePreviewStore) Get(_ context.Context, name string) (*preview.Preview, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	p, ok := s.previews[name]
	if !ok {
		return nil, fmt.Errorf("preview %q not found", name)
	}
	return p, nil
}

func (s *FakePreviewStore) Save(_ context.Context, p *preview.Preview) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.previews[p.Metadata.Name] = p
	return nil
}

func (s *FakePreviewStore) Delete(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.previews[name]; !ok {
		return fmt.Errorf("preview %q not found", name)
	}
	delete(s.previews, name)
	return nil
}

// ── FakeRuntimeProvider ──────────────────────────────────────────────────────

// FakeRuntimeProvider implements runtime.Provider with pre-seeded RuntimeInfo
// entries keyed by "namespace/serviceName".  The convention for a namespace is
// {project}-{environment}, e.g. "demo-staging".  Unknown combinations return a
// StatusNotDeployed response so the UI renders gracefully without a cluster.
type FakeRuntimeProvider struct {
	runtimes map[string]*runtime.RuntimeInfo
}

func newFakeRuntimeProvider() *FakeRuntimeProvider {
	p := &FakeRuntimeProvider{runtimes: make(map[string]*runtime.RuntimeInfo)}

	ts := seedCreatedAt.Format(time.RFC3339)

	seeded := map[string]string{
		"demo-staging":          "http://hello.staging.demo.localhost",
		"demo-prod":             "http://hello.prod.demo.localhost",
		"demo-preview-pr-42":    "http://hello.demo-preview-pr-42.localhost",
	}
	for ns, url := range seeded {
		replicas := int32(2)
		if strings.Contains(ns, "preview") {
			replicas = 1
		}
		p.runtimes[ns+"/hello"] = &runtime.RuntimeInfo{
			Status:       runtime.StatusHealthy,
			Image:        "ghcr.io/suparcloud/hello:v1.0.0",
			Replicas:     replicas,
			Available:    replicas,
			IngressURLs:  []string{url},
			Namespace:    ns,
			LastDeployed: ts,
		}
	}
	return p
}

// GetServiceRuntime returns pre-seeded runtime data for known namespaces or
// a not_deployed response for anything unknown.
func (p *FakeRuntimeProvider) GetServiceRuntime(_ context.Context, namespace, serviceName string) (*runtime.RuntimeInfo, error) {
	key := namespace + "/" + serviceName
	if info, ok := p.runtimes[key]; ok {
		return info, nil
	}
	return &runtime.RuntimeInfo{
		Status:      runtime.StatusNotDeployed,
		IngressURLs: []string{},
		Namespace:   namespace,
	}, nil
}

// ── FakeLogsProvider ─────────────────────────────────────────────────────────

// FakeLogsProvider implements runtime.LogsProvider with a fixed set of sample
// log lines.  It returns the same log output for any namespace/service
// combination, which is sufficient for local UI development and testing.
type FakeLogsProvider struct{}

const fakeDefaultPod      = "hello-abc12"
const fakeDefaultContainer = "hello"

func (p *FakeLogsProvider) ListPods(_ context.Context, _, _ string) ([]runtime.PodInfo, error) {
	return []runtime.PodInfo{
		{Name: fakeDefaultPod, Containers: []string{fakeDefaultContainer}},
	}, nil
}

func (p *FakeLogsProvider) GetLogs(_ context.Context, req runtime.LogsRequest) (*runtime.LogsResult, error) {
	lines := fakeLogLines()

	if req.TailLines != nil && *req.TailLines > 0 && int(*req.TailLines) < len(lines) {
		tail := int(*req.TailLines)
		lines = lines[len(lines)-tail:]
	}

	pod := req.Pod
	if pod == "" {
		pod = fakeDefaultPod
	}
	container := req.Container
	if container == "" {
		container = fakeDefaultContainer
	}

	return &runtime.LogsResult{
		Pod:       pod,
		Container: container,
		Logs:      strings.Join(lines, "\n"),
	}, nil
}

// fakeLogLines returns the fixed sample log output used for local development.
// Values are deterministic and match the timestamps in seedLogs.
func fakeLogLines() []string {
	return []string{
		"2026-01-01T00:00:00Z INFO  starting hello service...",
		"2026-01-01T00:00:01Z INFO  listening on :8080",
		"2026-01-01T00:00:02Z INFO  GET /healthz → 200 OK (0ms)",
		"2026-01-01T00:00:03Z INFO  GET / → 200 OK (2ms)",
		"2026-01-01T00:00:04Z INFO  GET /api/items → 200 OK (5ms)",
		"2026-01-01T00:00:05Z INFO  GET /healthz → 200 OK (0ms)",
		"2026-01-01T00:00:06Z INFO  GET /api/items → 200 OK (4ms)",
		"2026-01-01T00:00:07Z WARN  slow query detected (120ms)",
		"2026-01-01T00:00:08Z INFO  GET /healthz → 200 OK (0ms)",
		"2026-01-01T00:00:09Z INFO  GET /api/items → 200 OK (6ms)",
	}
}
