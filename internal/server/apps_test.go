package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/session"
	"github.com/suparcloud/suparship/internal/tpl"
)

// --- In-memory AppStore for tests ---

type memAppStore struct {
	mu   sync.RWMutex
	apps map[string]map[string]*domain.App
	envs map[string]map[string]map[string]*domain.AppEnvironment
}

func newMemAppStore() *memAppStore {
	return &memAppStore{
		apps: make(map[string]map[string]*domain.App),
		envs: make(map[string]map[string]map[string]*domain.AppEnvironment),
	}
}

func (m *memAppStore) addApp(app *domain.App) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.apps[app.ProjectName] == nil {
		m.apps[app.ProjectName] = make(map[string]*domain.App)
	}
	m.apps[app.ProjectName][app.Name] = app
}

func (m *memAppStore) addEnv(env *domain.AppEnvironment) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.envs[env.ProjectName] == nil {
		m.envs[env.ProjectName] = make(map[string]map[string]*domain.AppEnvironment)
	}
	if m.envs[env.ProjectName][env.AppName] == nil {
		m.envs[env.ProjectName][env.AppName] = make(map[string]*domain.AppEnvironment)
	}
	m.envs[env.ProjectName][env.AppName][env.EnvName] = env
}

func (m *memAppStore) ListApps(_ context.Context, projectName string) ([]*domain.App, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	projectApps, ok := m.apps[projectName]
	if !ok {
		return nil, fmt.Errorf("project %q not found", projectName)
	}
	out := make([]*domain.App, 0, len(projectApps))
	for _, a := range projectApps {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *memAppStore) GetApp(_ context.Context, projectName, appName string) (*domain.App, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	projectApps, ok := m.apps[projectName]
	if !ok {
		return nil, fmt.Errorf("project %q not found", projectName)
	}
	a, ok := projectApps[appName]
	if !ok {
		return nil, fmt.Errorf("app %q not found in project %q", appName, projectName)
	}
	return a, nil
}

func (m *memAppStore) ListAppEnvironments(_ context.Context, projectName, appName string) ([]*domain.AppEnvironment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	projectEnvs, ok := m.envs[projectName]
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

func (m *memAppStore) GetAppEnvironment(_ context.Context, projectName, appName, envName string) (*domain.AppEnvironment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	projectEnvs, ok := m.envs[projectName]
	if !ok {
		return nil, fmt.Errorf("environment %q not found for app %q in project %q", envName, appName, projectName)
	}
	envMap, ok := projectEnvs[appName]
	if !ok {
		return nil, fmt.Errorf("environment %q not found for app %q in project %q", envName, appName, projectName)
	}
	e, ok := envMap[envName]
	if !ok {
		return nil, fmt.Errorf("environment %q not found for app %q in project %q", envName, appName, projectName)
	}
	return e, nil
}

func (m *memAppStore) ListAppPreviews(_ context.Context, projectName, appName string) ([]*domain.AppEnvironment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*domain.AppEnvironment
	for proj, appMap := range m.envs {
		if projectName != "" && proj != projectName {
			continue
		}
		for app, envMap := range appMap {
			if appName != "" && app != appName {
				continue
			}
			for _, e := range envMap {
				if e.EnvType == domain.AppEnvPreview {
					out = append(out, e)
				}
			}
		}
	}
	return out, nil
}

func (m *memAppStore) SaveApp(_ context.Context, projectName string, app *domain.App) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.apps[projectName] == nil {
		return fmt.Errorf("project %q not found", projectName)
	}
	app.ProjectName = projectName
	m.apps[projectName][app.Name] = app
	return nil
}

func (m *memAppStore) SaveAppEnvironment(_ context.Context, projectName string, env *domain.AppEnvironment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.envs[projectName] == nil {
		m.envs[projectName] = make(map[string]map[string]*domain.AppEnvironment)
	}
	if m.envs[projectName][env.AppName] == nil {
		m.envs[projectName][env.AppName] = make(map[string]*domain.AppEnvironment)
	}
	env.ProjectName = projectName
	m.envs[projectName][env.AppName][env.EnvName] = env
	return nil
}

func (m *memAppStore) DeleteAppEnvironment(_ context.Context, projectName, appName, envName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	projectEnvs, ok := m.envs[projectName]
	if !ok {
		return fmt.Errorf("environment %q not found for app %q in project %q", envName, appName, projectName)
	}
	envMap, ok := projectEnvs[appName]
	if !ok {
		return fmt.Errorf("environment %q not found for app %q in project %q", envName, appName, projectName)
	}
	if _, ok := envMap[envName]; !ok {
		return fmt.Errorf("environment %q not found for app %q in project %q", envName, appName, projectName)
	}
	delete(envMap, envName)
	return nil
}

func (m *memAppStore) DeleteApp(_ context.Context, projectName, appName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	projectApps, ok := m.apps[projectName]
	if !ok {
		return fmt.Errorf("app %q not found in project %q", appName, projectName)
	}
	if _, ok := projectApps[appName]; !ok {
		return fmt.Errorf("app %q not found in project %q", appName, projectName)
	}
	delete(projectApps, appName)
	if projectEnvs, ok := m.envs[projectName]; ok {
		delete(projectEnvs, appName)
	}
	return nil
}

// --- Test helpers ---

func newTestAppMux() (*http.ServeMux, *authHandler, *memAppStore) {
	mux := http.NewServeMux()

	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	ah.registerRoutes(mux)

	store := newMemAppStore()
	rh := &rbacHandler{
		auth:       ah,
		orgStore:   &staticOrgProvider{org: testRBACOrg()},
		appHandler: newAppHandler(store, nil, nil, nil),
	}
	rh.registerRoutes(mux)

	return mux, ah, store
}

// newTestAppCreateMux returns a mux wired with an appHandler that supports
// app creation (templates + project store).  The returned memProjectStore
// and memAppStore are pre-seeded with the demo project (apps map initialised).
func newTestAppCreateMux() (*http.ServeMux, *authHandler, *memAppStore, *memProjectStore) {
	mux := http.NewServeMux()

	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	ah.registerRoutes(mux)

	appStore := newMemAppStore()
	// Pre-create the project bucket so SaveApp doesn't fail with "project not found".
	appStore.mu.Lock()
	appStore.apps["demo"] = make(map[string]*domain.App)
	appStore.mu.Unlock()

	projStore := newMemProjectStore()
	_ = projStore.Save(context.Background(), appCreateTestProject())

	orgProv := &staticOrgProvider{org: testRBACOrg()}
	appH := newAppHandler(appStore, []*tpl.Template{appCreateTestTemplate()}, nil, projStore)
	appH.orgProvider = orgProv

	rh := &rbacHandler{
		auth:       ah,
		orgStore:   orgProv,
		appHandler: appH,
	}
	rh.registerRoutes(mux)

	return mux, ah, appStore, projStore
}

// newAppCreateMuxWith wires an app-create mux with explicit built-in templates
// and a cluster loader, so tests can exercise live (cluster-overrides-built-in)
// template resolution at app-creation time.
func newAppCreateMuxWith(builtin []*tpl.Template, clusterLoader ClusterTemplateLoader) (*http.ServeMux, *authHandler, *memAppStore) {
	mux := http.NewServeMux()

	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	ah.registerRoutes(mux)

	appStore := newMemAppStore()
	appStore.mu.Lock()
	appStore.apps["demo"] = make(map[string]*domain.App)
	appStore.mu.Unlock()

	projStore := newMemProjectStore()
	_ = projStore.Save(context.Background(), appCreateTestProject())

	orgProv := &staticOrgProvider{org: testRBACOrg()}
	appH := newAppHandler(appStore, builtin, clusterLoader, projStore)
	appH.orgProvider = orgProv

	rh := &rbacHandler{auth: ah, orgStore: orgProv, appHandler: appH}
	rh.registerRoutes(mux)

	return mux, ah, appStore
}

// TestCreateApp_LiveClusterTemplate proves app creation reads templates live:
// a template that exists ONLY in the cluster (no built-in) is usable for
// creation without a server restart — the core of un-freezing the snapshot.
func TestCreateApp_LiveClusterTemplate(t *testing.T) {
	clusterOnly := &tpl.Template{
		APIVersion: tpl.CurrentAPIVersion,
		Kind:       tpl.TemplateKind,
		Metadata:   tpl.Metadata{Name: "synced-chart", Version: "1.0.0"},
		Spec: tpl.TemplateSpec{
			Title:  "Synced Chart",
			Engine: tpl.Engine{Type: tpl.EngineHelm},
			Inputs: []tpl.Input{
				{Name: "image", Title: "Image", Type: tpl.InputTypeString, Required: true},
			},
		},
	}
	loader := func(context.Context) ([]*tpl.Template, error) {
		return []*tpl.Template{clusterOnly}, nil
	}

	mux, ah, appStore := newAppCreateMuxWith(nil /* no built-ins */, loader)

	rec := postCreateAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "demo", createAppRequest{
		Name:     "synced-app",
		Template: "synced-chart",
		Values:   map[string]any{"image": "ghcr.io/org/app:v1"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 creating from a cluster-only template, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := appStore.GetApp(context.Background(), "demo", "synced-app"); err != nil {
		t.Fatalf("expected persisted app from cluster template: %v", err)
	}
}

// --- Fixtures for app creation tests ---

func appCreateTestTemplate() *tpl.Template {
	return &tpl.Template{
		APIVersion: tpl.CurrentAPIVersion,
		Kind:       tpl.TemplateKind,
		Metadata:   tpl.Metadata{Name: "web-service", Version: "1.0.0"},
		Spec: tpl.TemplateSpec{
			Title:    "Web Service",
			Category: "web",
			Engine:   tpl.Engine{Type: tpl.EngineHelm},
			Inputs: []tpl.Input{
				{Name: "image", Title: "Image", Type: tpl.InputTypeString, Required: true},
			},
			SecretInputs: []tpl.SecretInput{
				{Name: "db_url", Title: "Database URL", SecretRef: "db.url"},
			},
		},
	}
}

func appCreateTestProject() *project.Project {
	return &project.Project{
		APIVersion: project.CurrentAPIVersion,
		Kind:       project.ProjectKind,
		Metadata:   project.ProjectMeta{Name: "demo"},
		Spec: project.ProjectSpec{
			Environments: []project.Environment{
				{Name: "staging", Order: 1},
				{Name: "prod", Order: 2},
			},
		},
	}
}

func postCreateAppJSON(mux *http.ServeMux, cookie *http.Cookie, projectName string, body any) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectName+"/apps", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func appTestApp() *domain.App {
	return &domain.App{
		Name:        "hello",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			DisplayName: "Hello App",
			Description: "A simple hello-world web application.",
			Template: domain.AppTemplateRef{
				Name:    "web-service",
				Version: "1.0.0",
			},
			Values: map[string]any{"image_tag": "v1.0.0"},
			Components: []domain.ComponentSpec{
				{Name: "web", Type: domain.ComponentWeb, PreviewEnabled: true},
			},
		},
	}
}

func appTestStagingEnv() *domain.AppEnvironment {
	return &domain.AppEnvironment{
		AppName:     "hello",
		ProjectName: "demo",
		EnvName:     "staging",
		EnvType:     domain.AppEnvStaging,
		Namespace:   "hello-staging",
		URLs:        []string{"http://hello.staging.localhost"},
		Release: &domain.AppReleaseRef{
			Image: "ghcr.io/suparcloud/hello:v1.0.0",
			Tag:   "v1.0.0",
		},
		Status: domain.AppRuntimeStatus{
			Phase:     domain.StatusHealthy,
			Replicas:  2,
			Available: 2,
		},
	}
}

func appTestPreviewEnv() *domain.AppEnvironment {
	return &domain.AppEnvironment{
		AppName:     "hello",
		ProjectName: "demo",
		EnvName:     "pr-1",
		EnvType:     domain.AppEnvPreview,
		Namespace:   "hello-pr-1",
		URLs:        []string{"http://pr-1.hello.preview.localhost"},
		Status: domain.AppRuntimeStatus{
			Phase:     domain.StatusHealthy,
			Replicas:  1,
			Available: 1,
		},
	}
}

func getAppJSON(mux *http.ServeMux, cookie *http.Cookie, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// --- GET /api/v1/projects/{project}/apps ---

func TestListAppsFound(t *testing.T) {
	mux, ah, store := newTestAppMux()
	store.addApp(appTestApp())
	store.addEnv(appTestStagingEnv())

	rec := getAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "/api/v1/projects/demo/apps")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp AppListResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Project != "demo" {
		t.Errorf("expected project %q, got %q", "demo", resp.Project)
	}
	if len(resp.Apps) != 1 {
		t.Fatalf("expected 1 app, got %d", len(resp.Apps))
	}
	app := resp.Apps[0]
	if app.Name != "hello" {
		t.Errorf("expected app name %q, got %q", "hello", app.Name)
	}
	if app.Template.Name != "web-service" {
		t.Errorf("expected template %q, got %q", "web-service", app.Template.Name)
	}
	if app.Status.Phase != domain.StatusHealthy {
		t.Errorf("expected status %q, got %q", domain.StatusHealthy, app.Status.Phase)
	}
	if len(app.URLs) != 1 || app.URLs[0] != "http://hello.staging.localhost" {
		t.Errorf("unexpected URLs: %v", app.URLs)
	}
	if len(app.Components) != 1 || app.Components[0].Name != "web" {
		t.Errorf("unexpected components: %v", app.Components)
	}
}

func TestListAppsEmptyProject(t *testing.T) {
	mux, ah, store := newTestAppMux()
	// Project key present but no apps.
	store.mu.Lock()
	store.apps["demo"] = make(map[string]*domain.App)
	store.mu.Unlock()

	rec := getAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "/api/v1/projects/demo/apps")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp AppListResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.Apps) != 0 {
		t.Errorf("expected 0 apps, got %d", len(resp.Apps))
	}
}

func TestListAppsProjectNotFound(t *testing.T) {
	mux, ah, _ := newTestAppMux()

	rec := getAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "/api/v1/projects/nonexistent/apps")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestListAppsUnauthenticated(t *testing.T) {
	mux, _, store := newTestAppMux()
	store.addApp(appTestApp())

	rec := getAppJSON(mux, nil, "/api/v1/projects/demo/apps")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestListAppsForbidden(t *testing.T) {
	mux, ah, store := newTestAppMux()
	store.addApp(appTestApp())

	// "carol" is a viewer on "*" so she can view "demo" too — use a project
	// that no team has access to in order to trigger 403.
	rec := getAppJSON(mux, sessionCookieFor(ah, "bob", "developer"), "/api/v1/projects/other/apps")

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- GET /api/v1/projects/{project}/apps/{app} ---

func TestGetAppFound(t *testing.T) {
	mux, ah, store := newTestAppMux()
	store.addApp(appTestApp())
	store.addEnv(appTestStagingEnv())
	store.addEnv(appTestPreviewEnv())

	rec := getAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "/api/v1/projects/demo/apps/hello")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp AppDetailResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	app := resp.App
	if app.Name != "hello" {
		t.Errorf("expected name %q, got %q", "hello", app.Name)
	}
	if app.Project != "demo" {
		t.Errorf("expected project %q, got %q", "demo", app.Project)
	}
	if app.Template.Name != "web-service" {
		t.Errorf("expected template %q, got %q", "web-service", app.Template.Name)
	}
	if len(app.Environments) != 2 {
		t.Errorf("expected 2 environments, got %d", len(app.Environments))
	}
	if len(app.Components) != 1 {
		t.Errorf("expected 1 component, got %d", len(app.Components))
	}
	if app.Values == nil {
		t.Error("expected non-nil values map")
	}
	if app.SecretRefs == nil {
		t.Error("expected non-nil secretRefs slice")
	}
}

func TestGetAppNotFound(t *testing.T) {
	mux, ah, _ := newTestAppMux()

	rec := getAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "/api/v1/projects/demo/apps/missing")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
	var errResp errorResponse
	_ = json.NewDecoder(rec.Body).Decode(&errResp)
	if errResp.Error == "" {
		t.Error("expected non-empty error message")
	}
}

func TestGetAppUnauthenticated(t *testing.T) {
	mux, _, store := newTestAppMux()
	store.addApp(appTestApp())

	rec := getAppJSON(mux, nil, "/api/v1/projects/demo/apps/hello")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

// --- GET /api/v1/projects/{project}/apps/{app}/environments ---

func TestListAppEnvironmentsFound(t *testing.T) {
	mux, ah, store := newTestAppMux()
	store.addApp(appTestApp())
	store.addEnv(appTestStagingEnv())
	store.addEnv(appTestPreviewEnv())

	rec := getAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "/api/v1/projects/demo/apps/hello/environments")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp AppEnvironmentsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Project != "demo" {
		t.Errorf("expected project %q, got %q", "demo", resp.Project)
	}
	if resp.AppName != "hello" {
		t.Errorf("expected appName %q, got %q", "hello", resp.AppName)
	}
	if len(resp.Environments) != 2 {
		t.Fatalf("expected 2 environments, got %d", len(resp.Environments))
	}
	// Results are sorted by envName; pr-1 < staging.
	if resp.Environments[0].EnvName != "pr-1" {
		t.Errorf("expected first env %q, got %q", "pr-1", resp.Environments[0].EnvName)
	}
	if resp.Environments[1].EnvName != "staging" {
		t.Errorf("expected second env %q, got %q", "staging", resp.Environments[1].EnvName)
	}
}

func TestListAppEnvironmentsAppNotFound(t *testing.T) {
	mux, ah, _ := newTestAppMux()

	rec := getAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "/api/v1/projects/demo/apps/missing/environments")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestListAppEnvironmentsUnauthenticated(t *testing.T) {
	mux, _, store := newTestAppMux()
	store.addApp(appTestApp())

	rec := getAppJSON(mux, nil, "/api/v1/projects/demo/apps/hello/environments")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

// --- GET /api/v1/projects/{project}/apps/{app}/environments/{env} ---

func TestGetAppEnvironmentFound(t *testing.T) {
	mux, ah, store := newTestAppMux()
	store.addApp(appTestApp())
	store.addEnv(appTestStagingEnv())

	rec := getAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "/api/v1/projects/demo/apps/hello/environments/staging")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp AppEnvironmentResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	env := resp.Environment
	if env.EnvName != "staging" {
		t.Errorf("expected envName %q, got %q", "staging", env.EnvName)
	}
	if env.EnvType != "staging" {
		t.Errorf("expected envType %q, got %q", "staging", env.EnvType)
	}
	if env.Namespace != "hello-staging" {
		t.Errorf("expected namespace %q, got %q", "hello-staging", env.Namespace)
	}
	if env.Status.Phase != domain.StatusHealthy {
		t.Errorf("expected phase %q, got %q", domain.StatusHealthy, env.Status.Phase)
	}
	if env.Release == nil {
		t.Fatal("expected non-nil release")
	}
	if env.Release.Tag != "v1.0.0" {
		t.Errorf("expected release tag %q, got %q", "v1.0.0", env.Release.Tag)
	}
	if env.Preview != nil {
		t.Error("expected nil preview for staging environment")
	}
	if len(env.URLs) != 1 {
		t.Errorf("expected 1 URL, got %d", len(env.URLs))
	}
}

func TestGetAppEnvironmentPreviewMeta(t *testing.T) {
	mux, ah, store := newTestAppMux()
	store.addApp(appTestApp())
	store.addEnv(appTestPreviewEnv())

	rec := getAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "/api/v1/projects/demo/apps/hello/environments/pr-1")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp AppEnvironmentResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	env := resp.Environment
	if env.EnvType != "preview" {
		t.Errorf("expected envType %q, got %q", "preview", env.EnvType)
	}
	if env.Preview == nil {
		t.Fatal("expected non-nil preview metadata for preview environment")
	}
	if env.Preview.PreviewName != "pr-1" {
		t.Errorf("expected previewName %q, got %q", "pr-1", env.Preview.PreviewName)
	}
}

func TestGetAppEnvironmentNotFound(t *testing.T) {
	mux, ah, store := newTestAppMux()
	store.addApp(appTestApp())

	rec := getAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "/api/v1/projects/demo/apps/hello/environments/prod")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
	var errResp errorResponse
	_ = json.NewDecoder(rec.Body).Decode(&errResp)
	if errResp.Error == "" {
		t.Error("expected non-empty error message")
	}
}

func TestGetAppEnvironmentUnauthenticated(t *testing.T) {
	mux, _, store := newTestAppMux()
	store.addApp(appTestApp())
	store.addEnv(appTestStagingEnv())

	rec := getAppJSON(mux, nil, "/api/v1/projects/demo/apps/hello/environments/staging")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

// --- POST /api/v1/projects/{project}/apps ---

func TestCreateAppValid(t *testing.T) {
	mux, ah, appStore, _ := newTestAppCreateMux()

	rec := postCreateAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "demo", createAppRequest{
		Name:        "my-app",
		DisplayName: "My App",
		Template:    "web-service",
		Values:      map[string]any{"image": "ghcr.io/org/app:v1"},
	})

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp createAppResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.App.Name != "my-app" {
		t.Errorf("expected app name %q, got %q", "my-app", resp.App.Name)
	}
	if resp.App.Project != "demo" {
		t.Errorf("expected project %q, got %q", "demo", resp.App.Project)
	}
	if resp.App.Template.Name != "web-service" {
		t.Errorf("expected template %q, got %q", "web-service", resp.App.Template.Name)
	}
	if resp.App.DisplayName != "My App" {
		t.Errorf("expected displayName %q, got %q", "My App", resp.App.DisplayName)
	}
	if len(resp.App.Environments) != 2 {
		t.Errorf("expected 2 default environments, got %d", len(resp.App.Environments))
	}
	if len(resp.App.Components) != 1 || resp.App.Components[0].Name != "web" {
		t.Errorf("expected default web component, got %v", resp.App.Components)
	}

	// Verify persisted in store.
	app, err := appStore.GetApp(context.Background(), "demo", "my-app")
	if err != nil {
		t.Fatalf("expected persisted app: %v", err)
	}
	if app.Name != "my-app" {
		t.Errorf("persisted app name mismatch: %q", app.Name)
	}
}

func TestCreateAppDefaultEnvironments(t *testing.T) {
	mux, ah, appStore, _ := newTestAppCreateMux()

	rec := postCreateAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "demo", createAppRequest{
		Name:     "env-app",
		Template: "web-service",
		Values:   map[string]any{"image": "img:v1"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	envs, err := appStore.ListAppEnvironments(context.Background(), "demo", "env-app")
	if err != nil {
		t.Fatalf("list environments: %v", err)
	}
	if len(envs) != 2 {
		t.Fatalf("expected 2 environments, got %d", len(envs))
	}
	names := map[string]bool{}
	for _, e := range envs {
		names[e.EnvName] = true
		if e.Status.Phase != domain.StatusNotDeployed {
			t.Errorf("env %q: expected not_deployed, got %q", e.EnvName, e.Status.Phase)
		}
	}
	if !names["staging"] || !names["prod"] {
		t.Errorf("expected staging and prod, got %v", names)
	}
}

func TestCreateAppWithExplicitComponents(t *testing.T) {
	mux, ah, appStore, _ := newTestAppCreateMux()

	rec := postCreateAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "demo", createAppRequest{
		Name:     "multi-app",
		Template: "web-service",
		Values:   map[string]any{"image": "img:v1"},
		Components: []ComponentCreateDTO{
			{Name: "web", Type: "web", PreviewEnabled: true},
			{Name: "worker", Type: "worker", PreviewEnabled: false},
		},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	app, _ := appStore.GetApp(context.Background(), "demo", "multi-app")
	if len(app.Spec.Components) != 2 {
		t.Fatalf("expected 2 components persisted, got %d", len(app.Spec.Components))
	}
}

func TestCreateAppWithSecretRef(t *testing.T) {
	mux, ah, _, _ := newTestAppCreateMux()

	rec := postCreateAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "demo", createAppRequest{
		Name:     "secret-app",
		Template: "web-service",
		Values:   map[string]any{"image": "img:v1"},
		SecretRefs: []AppSecretRefDTO{
			{Name: "db_url", SecretRef: "my-secret.db_url"},
		},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp createAppResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.App.SecretRefs) != 1 || resp.App.SecretRefs[0].SecretRef != "my-secret.db_url" {
		t.Errorf("unexpected secretRefs in response: %v", resp.App.SecretRefs)
	}
}

func TestCreateAppMissingName(t *testing.T) {
	mux, ah, _, _ := newTestAppCreateMux()

	rec := postCreateAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "demo", createAppRequest{
		Template: "web-service",
		Values:   map[string]any{"image": "img:v1"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateAppInvalidName(t *testing.T) {
	mux, ah, _, _ := newTestAppCreateMux()

	rec := postCreateAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "demo", createAppRequest{
		Name:     "INVALID NAME",
		Template: "web-service",
		Values:   map[string]any{"image": "img:v1"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateAppMissingTemplate(t *testing.T) {
	mux, ah, _, _ := newTestAppCreateMux()

	rec := postCreateAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "demo", createAppRequest{
		Name:   "my-app",
		Values: map[string]any{"image": "img:v1"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateAppUnknownTemplate(t *testing.T) {
	mux, ah, _, _ := newTestAppCreateMux()

	rec := postCreateAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "demo", createAppRequest{
		Name:     "my-app",
		Template: "nonexistent",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateApp_NoValuesSkipsInputValidation(t *testing.T) {
	// Values-editor-first: the create form sends no `values`. Template inputs are
	// no longer enforced, so omitting a required input ("image" in
	// appCreateTestTemplate) must SUCCEED — developers configure via the values
	// editor (rawValues), not template inputs.
	mux, ah, _, _ := newTestAppCreateMux()

	rec := postCreateAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "demo", createAppRequest{
		Name:     "my-app",
		Template: "web-service",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 (inputs not enforced when values omitted), got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateApp_StillValidatesProvidedValues(t *testing.T) {
	// When a legacy client DOES send values, validation still runs (e.g. unknown
	// input is rejected) — the guard only skips enforcement for empty values.
	mux, ah, _, _ := newTestAppCreateMux()

	rec := postCreateAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "demo", createAppRequest{
		Name:     "my-app",
		Template: "web-service",
		Values:   map[string]any{"not_a_real_input": "x"},
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for unknown provided input, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateAppSecretAsPlaintext(t *testing.T) {
	mux, ah, _, _ := newTestAppCreateMux()

	rec := postCreateAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "demo", createAppRequest{
		Name:     "my-app",
		Template: "web-service",
		Values:   map[string]any{"image": "img:v1", "db_url": "postgres://..."},
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateAppProjectNotFound(t *testing.T) {
	mux, ah, _, _ := newTestAppCreateMux()

	rec := postCreateAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "nonexistent", createAppRequest{
		Name:     "my-app",
		Template: "web-service",
		Values:   map[string]any{"image": "img:v1"},
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateAppDuplicate(t *testing.T) {
	mux, ah, appStore, _ := newTestAppCreateMux()
	appStore.addApp(appTestApp()) // "hello" in project "demo"

	rec := postCreateAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "demo", createAppRequest{
		Name:     "hello",
		Template: "web-service",
		Values:   map[string]any{"image": "img:v1"},
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	var errResp errorResponse
	_ = json.NewDecoder(rec.Body).Decode(&errResp)
	if !contains(errResp.Error, "already exists") {
		t.Errorf("expected 'already exists' error, got %q", errResp.Error)
	}
}

func TestCreateAppInvalidComponentType(t *testing.T) {
	mux, ah, _, _ := newTestAppCreateMux()

	rec := postCreateAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "demo", createAppRequest{
		Name:     "my-app",
		Template: "web-service",
		Values:   map[string]any{"image": "img:v1"},
		Components: []ComponentCreateDTO{
			{Name: "web", Type: "badtype", PreviewEnabled: true},
		},
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateAppUnauthenticated(t *testing.T) {
	mux, _, _, _ := newTestAppCreateMux()

	rec := postCreateAppJSON(mux, nil, "demo", createAppRequest{
		Name:     "my-app",
		Template: "web-service",
		Values:   map[string]any{"image": "img:v1"},
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestCreateAppInsufficientPermissions(t *testing.T) {
	mux, ah, _, _ := newTestAppCreateMux()

	// "carol" is a viewer on "*"; creating requires developer or higher.
	rec := postCreateAppJSON(mux, sessionCookieFor(ah, "carol", "viewer"), "demo", createAppRequest{
		Name:     "my-app",
		Template: "web-service",
		Values:   map[string]any{"image": "img:v1"},
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAppSummaryStatusFallback verifies that an app with no stable environments
// gets a "not_deployed" phase in its summary (not an empty string).
func TestAppSummaryStatusFallback(t *testing.T) {
	mux, ah, store := newTestAppMux()
	store.addApp(appTestApp())
	// Only a preview env — no staging or prod.
	store.addEnv(appTestPreviewEnv())

	rec := getAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "/api/v1/projects/demo/apps")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp AppListResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.Apps) != 1 {
		t.Fatalf("expected 1 app, got %d", len(resp.Apps))
	}
	if resp.Apps[0].Status.Phase != domain.StatusNotDeployed {
		t.Errorf("expected status %q, got %q", domain.StatusNotDeployed, resp.Apps[0].Status.Phase)
	}
}

// TestCreateAppNoOrgEnvironmentsRejects verifies that app creation returns 400
// when no environments are registered in the org, with an actionable message.
func TestCreateAppNoOrgEnvironmentsRejects(t *testing.T) {
	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	ah.registerRoutes(mux)

	appStore := newMemAppStore()
	appStore.mu.Lock()
	appStore.apps["demo"] = make(map[string]*domain.App)
	appStore.mu.Unlock()

	projStore := newMemProjectStore()
	_ = projStore.Save(context.Background(), appCreateTestProject())

	// Org with NO environments registered.
	emptyEnvOrg := &rbac.Org{
		Name:         "test",
		DisplayName:  "Test Org",
		Environments: []rbac.OrgEnvironment{}, // explicitly empty
		Teams: []rbac.Team{
			{Name: "admins", DisplayName: "Admins", Members: []string{"alice"}},
		},
		RoleBindings: []rbac.RoleBinding{
			{Project: "*", Team: "admins", Role: rbac.RoleOrgAdmin},
		},
	}
	orgProv := &staticOrgProvider{org: emptyEnvOrg}
	appH := newAppHandler(appStore, []*tpl.Template{appCreateTestTemplate()}, nil, projStore)
	appH.orgProvider = orgProv

	rh := &rbacHandler{
		auth:       ah,
		orgStore:   orgProv,
		appHandler: appH,
	}
	rh.registerRoutes(mux)

	rec := postCreateAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "demo", createAppRequest{
		Name:     "my-app",
		Template: "web-service",
		Values:   map[string]any{"image": "img:v1"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when no org envs, got %d: %s", rec.Code, rec.Body.String())
	}
	var errResp errorResponse
	_ = json.NewDecoder(rec.Body).Decode(&errResp)
	if !contains(errResp.Error, "no environments registered") {
		t.Errorf("expected actionable error message, got %q", errResp.Error)
	}
}

// TestCreateAppSingleEnvOnlyCreatesOneEnv verifies that when only one environment
// is registered, the created app has exactly one AppEnvironment (not hardcoded staging+prod).
func TestCreateAppSingleEnvOnlyCreatesOneEnv(t *testing.T) {
	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	ah.registerRoutes(mux)

	appStore := newMemAppStore()
	appStore.mu.Lock()
	appStore.apps["demo"] = make(map[string]*domain.App)
	appStore.mu.Unlock()

	projStore := newMemProjectStore()
	_ = projStore.Save(context.Background(), appCreateTestProject())

	// Org with only one environment registered.
	singleEnvOrg := &rbac.Org{
		Name:        "test",
		DisplayName: "Test Org",
		Environments: []rbac.OrgEnvironment{
			{Name: "dev", DisplayName: "Development", Order: 1, ClusterRefs: []string{"in-cluster"}, ActiveClusterRef: "in-cluster"},
		},
		Teams: []rbac.Team{
			{Name: "admins", DisplayName: "Admins", Members: []string{"alice"}},
		},
		RoleBindings: []rbac.RoleBinding{
			{Project: "*", Team: "admins", Role: rbac.RoleOrgAdmin},
		},
	}
	orgProv2 := &staticOrgProvider{org: singleEnvOrg}
	appH2 := newAppHandler(appStore, []*tpl.Template{appCreateTestTemplate()}, nil, projStore)
	appH2.orgProvider = orgProv2

	rh := &rbacHandler{
		auth:       ah,
		orgStore:   orgProv2,
		appHandler: appH2,
	}
	rh.registerRoutes(mux)

	rec := postCreateAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "demo", createAppRequest{
		Name:     "single-env-app",
		Template: "web-service",
		Values:   map[string]any{"image": "img:v1"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp createAppResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.App.Environments) != 1 {
		t.Fatalf("expected exactly 1 environment, got %d: %v", len(resp.App.Environments), resp.App.Environments)
	}
	if resp.App.Environments[0].EnvName != "dev" {
		t.Errorf("expected env name %q, got %q", "dev", resp.App.Environments[0].EnvName)
	}
}
