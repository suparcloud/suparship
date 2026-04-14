// Package fake – adapters.go bridges the domain-level DevRuntime with the
// concrete interface types expected by the suparship HTTP server
// (project.Store, preview.Store, runtime.Provider, runtime.LogsProvider,
// auth.Authenticator, rbac.OrgProvider).
//
// WARNING: this package is intended ONLY for local development.
// Activate it by setting SUPARSHIP_DEV_MODE=local (or SUPARSHIP_CLUSTER_MODE=fake)
// in your environment — see .env.example.
//
// No Kubernetes API calls are made; all state lives in process memory and
// resets to seed defaults on every process restart.  Credentials are
// plain-text and intentionally weak.  Never use this package in production.
//
// Use NewDevServerDeps to obtain a fully-wired bundle ready to be passed
// directly into server.Config.
package fake

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/suparcloud/suparship/internal/auth"
	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/preview"
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/runtime"
)

// compile-time check: FakeOrgProvider must satisfy rbac.OrgStore.
var _ rbac.OrgStore = (*FakeOrgProvider)(nil)

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
// The effective login credentials are sourced (in priority order) from:
//  1. SUPARSHIP_ADMIN_EMAIL / SUPARSHIP_ADMIN_PASSWORD env vars
//  2. FakeAdminUsername / FakeAdminPassword package constants
//
// All data is seeded deterministically; writes are transient.
//
// WARNING: for local development only — never deploy this in production.
type DevServerDeps struct {
	// AdminUsername is the effective login username for the dev admin account.
	// Populated by NewDevServerDeps so callers can include it in startup logs.
	AdminUsername string
	Authenticator *FakeAuthenticator
	// OrgProvider implements rbac.OrgStore (GetOrg + SaveOrg) with in-memory state.
	OrgProvider  *FakeOrgProvider
	ProjectStore *FakeProjectStore
	PreviewStore    *FakePreviewStore
	RuntimeProvider *FakeRuntimeProvider
	LogsProvider    *FakeLogsProvider
	// AppStore provides in-memory app and environment data seeded with the demo
	// hello app.  It implements domain.AppStore via DevRuntime.
	AppStore domain.AppStore
	// ClusterStore provides in-memory cluster registry seeded with staging-cluster
	// and prod-cluster. It implements domain.ClusterStore via DevRuntime.
	ClusterStore domain.ClusterStore
}

// NewDevServerDeps returns a DevServerDeps bundle pre-loaded with demo seed
// data.  Credentials are read from SUPARSHIP_ADMIN_EMAIL and
// SUPARSHIP_ADMIN_PASSWORD environment variables; when unset the package
// defaults (FakeAdminUsername / FakeAdminPassword) are used.
//
// Each call returns an independent in-memory instance with no shared state.
func NewDevServerDeps() *DevServerDeps {
	username := os.Getenv("SUPARSHIP_ADMIN_EMAIL")
	if username == "" {
		username = FakeAdminUsername
	}
	password := os.Getenv("SUPARSHIP_ADMIN_PASSWORD")
	if password == "" {
		password = FakeAdminPassword
	}
	seeded := NewSeededDevRuntime()
	return &DevServerDeps{
		AdminUsername:   username,
		Authenticator:   &FakeAuthenticator{Username: username, Password: password},
		OrgProvider:     newFakeOrgProvider(),
		ProjectStore:    newFakeProjectStore(),
		PreviewStore:    newFakePreviewStore(),
		RuntimeProvider: newFakeRuntimeProvider(),
		LogsProvider:    &FakeLogsProvider{},
		AppStore:        seeded,
		ClusterStore:    seeded,
	}
}

// ── FakeAuthenticator ────────────────────────────────────────────────────────

// FakeAdminUsername and FakeAdminPassword are the default dev credentials
// used by FakeAuthenticator when no env-configured values are provided.
// They match the defaults in .env.example.
//
// WARNING: these credentials are intentionally obvious and must never be
// used outside local development.
const (
	FakeAdminUsername = "admin@local"
	FakeAdminPassword = "admin123"
)

// FakeAuthenticator implements auth.Authenticator with a single configurable
// dev credential.  Passwords are compared in plain text; no bcrypt is used.
//
// Zero-value fields fall back to FakeAdminUsername / FakeAdminPassword.
// Construct via NewDevServerDeps to get env-configured credentials.
//
// WARNING: for local development only.
type FakeAuthenticator struct {
	Username string // if empty, uses FakeAdminUsername
	Password string // if empty, uses FakeAdminPassword
}

func (a *FakeAuthenticator) Authenticate(_ context.Context, username, password string) (*auth.Credentials, error) {
	wantUser := a.Username
	if wantUser == "" {
		wantUser = FakeAdminUsername
	}
	wantPass := a.Password
	if wantPass == "" {
		wantPass = FakeAdminPassword
	}
	if username == wantUser && password == wantPass {
		return &auth.Credentials{Username: username}, nil
	}
	return nil, auth.ErrInvalidCredentials
}

// ── FakeOrgProvider ──────────────────────────────────────────────────────────

// FakeOrgProvider implements rbac.OrgStore (GetOrg + SaveOrg) with an
// in-memory org seeded with the default org, canonical environments, and an
// admins team. Mutations via SaveOrg are transient and reset on restart.
type FakeOrgProvider struct {
	mu  sync.RWMutex
	org *rbac.Org
}

// newFakeOrgProvider returns a FakeOrgProvider seeded with demo defaults.
func newFakeOrgProvider() *FakeOrgProvider {
	return &FakeOrgProvider{org: defaultFakeOrg()}
}

func defaultFakeOrg() *rbac.Org {
	return &rbac.Org{
		Name:        "default",
		DisplayName: "My Organization",
		CreatedAt:   seedCreatedAt.Format(time.RFC3339),
		Environments: []rbac.OrgEnvironment{
			{
				Name:             "staging",
				DisplayName:      "Staging",
				Order:            1,
				ClusterRef:       "staging-cluster",
				BaseDomain:       "localhost",
				NamespacePattern: "{app}-{env}",
			},
			{
				Name:             "prod",
				DisplayName:      "Production",
				Order:            2,
				ClusterRef:       "prod-cluster",
				BaseDomain:       "localhost",
				NamespacePattern: "{app}-{env}",
			},
		},
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
	}
}

func (p *FakeOrgProvider) GetOrg(_ context.Context) (*rbac.Org, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.org, nil
}

func (p *FakeOrgProvider) SaveOrg(_ context.Context, org *rbac.Org) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.org = org
	return nil
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
			// Environments are inherited from the org defaults; no project-level
		// overrides or project-specific environments for the demo project.
		Environments: []project.Environment{},
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

// ── FakeDeploymentHistoryReader ───────────────────────────────────────────────

// FakeDeploymentHistoryEntry mirrors server.DeploymentHistoryEntry but lives
// in the fake package to avoid an import cycle (fake → server → fake).
// The cmd/suparship/server.go adapter converts these to server.DeploymentHistoryEntry.
type FakeDeploymentHistoryEntry struct {
	ID              int64
	Revision        string
	DeployedAt      string
	DeployStartedAt string
	RepoURL         string
	Path            string
	TargetRevision  string
}

// FakeDeploymentHistoryReader implements a minimal deployment history reader
// for local development. It returns deterministic seed entries for any app/env
// so the Deployments tab is immediately populated in fake mode.
type FakeDeploymentHistoryReader struct{}

// GetFakeHistory returns seed deployment history entries for an app/env.
// It returns 3 synthetic entries anchored to the seed timestamps, most recent first.
func (r *FakeDeploymentHistoryReader) GetFakeHistory(appName, envName string) []FakeDeploymentHistoryEntry {
	base := seedCreatedAt
	return []FakeDeploymentHistoryEntry{
		{
			ID:              2,
			Revision:        "d4f9e2a1b3c8f507",
			DeployedAt:      base.Add(2 * 24 * time.Hour).UTC().Format(time.RFC3339),
			DeployStartedAt: base.Add(2*24*time.Hour - 90*time.Second).UTC().Format(time.RFC3339),
			RepoURL:         "https://github.com/suparcloud/gitops",
			Path:            "gitops-output/" + envName + "/demo/" + appName,
			TargetRevision:  "HEAD",
		},
		{
			ID:              1,
			Revision:        "a1b2c3d4e5f67890",
			DeployedAt:      base.Add(24 * time.Hour).UTC().Format(time.RFC3339),
			DeployStartedAt: base.Add(24*time.Hour - 75*time.Second).UTC().Format(time.RFC3339),
			RepoURL:         "https://github.com/suparcloud/gitops",
			Path:            "gitops-output/" + envName + "/demo/" + appName,
			TargetRevision:  "HEAD",
		},
		{
			ID:              0,
			Revision:        "f0e1d2c3b4a59687",
			DeployedAt:      base.UTC().Format(time.RFC3339),
			DeployStartedAt: base.Add(-60 * time.Second).UTC().Format(time.RFC3339),
			RepoURL:         "https://github.com/suparcloud/gitops",
			Path:            "gitops-output/" + envName + "/demo/" + appName,
			TargetRevision:  "HEAD",
		},
	}
}
