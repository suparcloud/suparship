package server

import (
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
	"github.com/suparcloud/suparship/internal/session"
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
		auth:        ah,
		orgProvider: &staticOrgProvider{org: testRBACOrg()},
		appHandler:  newAppHandler(store),
	}
	rh.registerRoutes(mux)

	return mux, ah, store
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
			Components: []domain.Component{
				{Name: "web", Type: domain.ComponentWeb, EnabledInPreview: true},
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
		Namespace:   "hello-preview-pr-1",
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
