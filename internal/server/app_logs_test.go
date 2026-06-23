package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/runtime"
	"github.com/suparcloud/suparship/internal/session"
)

// newTestAppLogsMux builds a test mux with an appHandler wired up with both
// an in-memory AppStore and a fake LogsProvider so that the
// GET /api/v1/projects/{project}/apps/{app}/logs route is registered.
func newTestAppLogsMux() (*http.ServeMux, *authHandler, *memAppStore, *fakeLogsProvider) {
	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	ah.registerRoutes(mux)

	store := newMemAppStore()
	lp := newFakeLogsProvider()

	appH := newAppHandler(store, nil, nil, nil)
	appH.logsProvider = lp

	rh := &rbacHandler{
		auth:        ah,
		orgStore: &staticOrgProvider{org: testRBACOrg()},
		appHandler:  appH,
	}
	rh.registerRoutes(mux)

	return mux, ah, store, lp
}

// logsTestApp returns a minimal App + staging AppEnvironment for use in logs tests.
func logsTestApp() (*domain.App, *domain.AppEnvironment) {
	app := &domain.App{
		Name:        "hello",
		ProjectName: "myproject",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "web-service"},
		},
	}
	env := &domain.AppEnvironment{
		AppName:     "hello",
		ProjectName: "myproject",
		EnvName:     "staging",
		EnvType:     domain.AppEnvStaging,
		Namespace:   "hello-staging",
	}
	return app, env
}

func getAppLogs(mux *http.ServeMux, cookie *http.Cookie, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// --- Validation tests ---

func TestAppLogsMissingEnvironment(t *testing.T) {
	mux, ah, store, _ := newTestAppLogsMux()
	app, env := logsTestApp()
	store.addApp(app)
	store.addEnv(env)

	rec := getAppLogs(mux, sessionCookieFor(ah, "alice", "org_admin"),
		"/api/v1/projects/myproject/apps/hello/logs")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp errorResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if !contains(resp.Error, "environment") {
		t.Fatalf("expected error about environment, got %q", resp.Error)
	}
}

func TestAppLogsAppNotFound(t *testing.T) {
	mux, ah, _, _ := newTestAppLogsMux()

	rec := getAppLogs(mux, sessionCookieFor(ah, "alice", "org_admin"),
		"/api/v1/projects/myproject/apps/nonexistent/logs?environment=staging")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAppLogsEnvironmentNotFound(t *testing.T) {
	mux, ah, store, _ := newTestAppLogsMux()
	app, _ := logsTestApp()
	store.addApp(app)
	// no environment added

	rec := getAppLogs(mux, sessionCookieFor(ah, "alice", "org_admin"),
		"/api/v1/projects/myproject/apps/hello/logs?environment=staging")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp errorResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if !contains(resp.Error, "environment") {
		t.Fatalf("expected error about environment not found, got %q", resp.Error)
	}
}

func TestAppLogsInvalidTailLines(t *testing.T) {
	mux, ah, store, _ := newTestAppLogsMux()
	app, env := logsTestApp()
	store.addApp(app)
	store.addEnv(env)

	rec := getAppLogs(mux, sessionCookieFor(ah, "alice", "org_admin"),
		"/api/v1/projects/myproject/apps/hello/logs?environment=staging&tailLines=bad")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp errorResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if !contains(resp.Error, "tailLines") {
		t.Fatalf("expected tailLines error, got %q", resp.Error)
	}
}

func TestAppLogsNegativeTailLines(t *testing.T) {
	mux, ah, store, _ := newTestAppLogsMux()
	app, env := logsTestApp()
	store.addApp(app)
	store.addEnv(env)

	rec := getAppLogs(mux, sessionCookieFor(ah, "alice", "org_admin"),
		"/api/v1/projects/myproject/apps/hello/logs?environment=staging&tailLines=-1")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAppLogsNoPods(t *testing.T) {
	mux, ah, store, _ := newTestAppLogsMux()
	app, env := logsTestApp()
	store.addApp(app)
	store.addEnv(env)
	// no pods registered in the fake provider

	rec := getAppLogs(mux, sessionCookieFor(ah, "alice", "org_admin"),
		"/api/v1/projects/myproject/apps/hello/logs?environment=staging")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when no pods, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp errorResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if !contains(resp.Error, "no pods") {
		t.Fatalf("expected 'no pods' error, got %q", resp.Error)
	}
}

// --- Success path tests ---

func TestAppLogsAutoSelectPod(t *testing.T) {
	mux, ah, store, lp := newTestAppLogsMux()
	app, env := logsTestApp()
	store.addApp(app)
	store.addEnv(env)

	lp.pods["hello-staging"] = []runtime.PodInfo{
		{Name: "hello-abc-123", Containers: []string{"web"}},
	}
	lp.logs["hello-staging/hello-abc-123/web"] = "startup log\n"

	rec := getAppLogs(mux, sessionCookieFor(ah, "alice", "org_admin"),
		"/api/v1/projects/myproject/apps/hello/logs?environment=staging")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp AppLogsResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)

	if resp.Project != "myproject" {
		t.Fatalf("project: want %q, got %q", "myproject", resp.Project)
	}
	if resp.App != "hello" {
		t.Fatalf("app: want %q, got %q", "hello", resp.App)
	}
	if resp.Environment != "staging" {
		t.Fatalf("environment: want %q, got %q", "staging", resp.Environment)
	}
	if resp.Namespace != "hello-staging" {
		t.Fatalf("namespace: want %q, got %q", "hello-staging", resp.Namespace)
	}
	if resp.Pod != "hello-abc-123" {
		t.Fatalf("pod: want %q, got %q", "hello-abc-123", resp.Pod)
	}
	if resp.Container != "web" {
		t.Fatalf("container: want %q, got %q", "web", resp.Container)
	}
	if resp.Logs != "startup log\n" {
		t.Fatalf("logs: want %q, got %q", "startup log\n", resp.Logs)
	}
}

func TestAppLogsWithComponent(t *testing.T) {
	mux, ah, store, lp := newTestAppLogsMux()
	app := &domain.App{
		Name:        "hello",
		ProjectName: "myproject",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "web-service"},
			Components: []domain.ComponentSpec{
				{Name: "web", Type: domain.ComponentWeb, Enabled: true},
				{Name: "worker", Type: domain.ComponentWorker, Enabled: true},
			},
		},
	}
	env := &domain.AppEnvironment{
		AppName:     "hello",
		ProjectName: "myproject",
		EnvName:     "staging",
		EnvType:     domain.AppEnvStaging,
		Namespace:   "hello-staging",
	}
	store.addApp(app)
	store.addEnv(env)

	// Pods returned for the "worker" workload selector
	lp.pods["hello-staging"] = []runtime.PodInfo{
		{Name: "hello-worker-xyz-999", Containers: []string{"worker"}},
	}
	lp.logs["hello-staging/hello-worker-xyz-999/worker"] = "worker output\n"

	rec := getAppLogs(mux, sessionCookieFor(ah, "alice", "org_admin"),
		"/api/v1/projects/myproject/apps/hello/logs?environment=staging&component=worker")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp AppLogsResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)

	if resp.Component != "worker" {
		t.Fatalf("component: want %q, got %q", "worker", resp.Component)
	}
	if resp.Logs != "worker output\n" {
		t.Fatalf("logs: want %q, got %q", "worker output\n", resp.Logs)
	}
}

func TestAppLogsExplicitPodAndContainer(t *testing.T) {
	mux, ah, store, lp := newTestAppLogsMux()
	app, env := logsTestApp()
	store.addApp(app)
	store.addEnv(env)

	lp.pods["hello-staging"] = []runtime.PodInfo{
		{Name: "hello-abc-123", Containers: []string{"web", "sidecar"}},
	}
	lp.logs["hello-staging/hello-abc-123/sidecar"] = "sidecar output\n"

	rec := getAppLogs(mux, sessionCookieFor(ah, "alice", "org_admin"),
		"/api/v1/projects/myproject/apps/hello/logs?environment=staging&pod=hello-abc-123&container=sidecar")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp AppLogsResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Container != "sidecar" {
		t.Fatalf("container: want %q, got %q", "sidecar", resp.Container)
	}
	if resp.Logs != "sidecar output\n" {
		t.Fatalf("logs: want %q, got %q", "sidecar output\n", resp.Logs)
	}
}

func TestAppLogsWithTailLines(t *testing.T) {
	mux, ah, store, lp := newTestAppLogsMux()
	app, env := logsTestApp()
	store.addApp(app)
	store.addEnv(env)

	lp.pods["hello-staging"] = []runtime.PodInfo{
		{Name: "hello-abc-123", Containers: []string{"web"}},
	}
	lp.logs["hello-staging/hello-abc-123/web"] = "line1\nline2\n"

	rec := getAppLogs(mux, sessionCookieFor(ah, "alice", "org_admin"),
		"/api/v1/projects/myproject/apps/hello/logs?environment=staging&tailLines=50")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- Auth / authz tests ---

func TestAppLogsUnauthenticated(t *testing.T) {
	mux, _, store, _ := newTestAppLogsMux()
	app, env := logsTestApp()
	store.addApp(app)
	store.addEnv(env)

	rec := getAppLogs(mux, nil, "/api/v1/projects/myproject/apps/hello/logs?environment=staging")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestAppLogsViewerCanAccess(t *testing.T) {
	mux, ah, store, lp := newTestAppLogsMux()
	app := &domain.App{
		Name:        "hello",
		ProjectName: "myproject",
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "web-service"}},
	}
	// carol (viewer) has wildcard project access per testRBACOrg
	env := &domain.AppEnvironment{
		AppName:     "hello",
		ProjectName: "myproject",
		EnvName:     "staging",
		EnvType:     domain.AppEnvStaging,
		Namespace:   "hello-staging",
	}
	store.addApp(app)
	store.addEnv(env)

	lp.pods["hello-staging"] = []runtime.PodInfo{
		{Name: "hello-pod-1", Containers: []string{"web"}},
	}
	lp.logs["hello-staging/hello-pod-1/web"] = "viewer log\n"

	rec := getAppLogs(mux, sessionCookieFor(ah, "carol", "viewer"),
		"/api/v1/projects/myproject/apps/hello/logs?environment=staging")

	if rec.Code != http.StatusOK {
		t.Fatalf("viewer should access app logs, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAppLogsDeveloperNoAccessToOtherProject(t *testing.T) {
	mux, ah, store, _ := newTestAppLogsMux()
	// bob (developer) only has access to "api" project per testRBACOrg
	app := &domain.App{
		Name:        "hello",
		ProjectName: "other",
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "web-service"}},
	}
	store.addApp(app)

	rec := getAppLogs(mux, sessionCookieFor(ah, "bob", "developer"),
		"/api/v1/projects/other/apps/hello/logs?environment=staging")

	if rec.Code != http.StatusForbidden {
		t.Fatalf("developer should not access other project logs, got %d", rec.Code)
	}
}

// TestAppLogsNamespaceFromEnvironment confirms that the namespace used for pod
// listing comes from the resolved AppEnvironment record, not from a derived
// convention. This validates the resolution order described in the handler doc.
func TestAppLogsNamespaceFromEnvironment(t *testing.T) {
	mux, ah, store, lp := newTestAppLogsMux()
	app, _ := logsTestApp()
	store.addApp(app)

	// Register environment with a custom namespace (not the default convention).
	env := &domain.AppEnvironment{
		AppName:     "hello",
		ProjectName: "myproject",
		EnvName:     "staging",
		EnvType:     domain.AppEnvStaging,
		Namespace:   "custom-ns-override",
	}
	store.addEnv(env)

	lp.pods["custom-ns-override"] = []runtime.PodInfo{
		{Name: "hello-pod-custom", Containers: []string{"web"}},
	}
	lp.logs["custom-ns-override/hello-pod-custom/web"] = "custom ns log\n"

	rec := getAppLogs(mux, sessionCookieFor(ah, "alice", "org_admin"),
		"/api/v1/projects/myproject/apps/hello/logs?environment=staging")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp AppLogsResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Namespace != "custom-ns-override" {
		t.Fatalf("namespace: want %q, got %q", "custom-ns-override", resp.Namespace)
	}
	if resp.Logs != "custom ns log\n" {
		t.Fatalf("logs: want %q, got %q", "custom ns log\n", resp.Logs)
	}
}

// TestAppLogsPreviewEnvironment verifies that preview environments work exactly
// like stable environments — the namespace is read from the stored AppEnvironment.
func TestAppLogsPreviewEnvironment(t *testing.T) {
	mux, ah, store, lp := newTestAppLogsMux()
	app, _ := logsTestApp()
	store.addApp(app)

	previewEnv := &domain.AppEnvironment{
		AppName:     "hello",
		ProjectName: "myproject",
		EnvName:     "pr-42",
		EnvType:     domain.AppEnvPreview,
		Namespace:   "hello-pr-42",
	}
	store.addEnv(previewEnv)

	lp.pods["hello-pr-42"] = []runtime.PodInfo{
		{Name: "hello-pr42-pod", Containers: []string{"web"}},
	}
	lp.logs["hello-pr-42/hello-pr42-pod/web"] = "preview log\n"

	rec := getAppLogs(mux, sessionCookieFor(ah, "alice", "org_admin"),
		"/api/v1/projects/myproject/apps/hello/logs?environment=pr-42")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for preview env, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp AppLogsResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Environment != "pr-42" {
		t.Fatalf("environment: want %q, got %q", "pr-42", resp.Environment)
	}
	if resp.Logs != "preview log\n" {
		t.Fatalf("logs: want %q, got %q", "preview log\n", resp.Logs)
	}
}

// TestAppLogsRouteNotRegisteredWithoutProvider ensures the app logs route is
// absent when logsProvider is nil, so the handler is not accidentally exposed.
func TestAppLogsRouteNotRegisteredWithoutProvider(t *testing.T) {
	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	ah.registerRoutes(mux)

	store := newMemAppStore()
	app, env := logsTestApp()
	store.addApp(app)
	store.addEnv(env)

	appH := newAppHandler(store, nil, nil, nil)
	// intentionally leave appH.logsProvider = nil

	rh := &rbacHandler{
		auth:        ah,
		orgStore: &staticOrgProvider{org: testRBACOrg()},
		appHandler:  appH,
	}
	rh.registerRoutes(mux)

	rec := getAppLogs(mux, sessionCookieFor(ah, "alice", "org_admin"),
		"/api/v1/projects/myproject/apps/hello/logs?environment=staging")

	// Without a logsProvider, Go's default mux returns 405 (method not allowed)
	// or 404 — either is acceptable; the important thing is it is not 200.
	if rec.Code == http.StatusOK {
		t.Fatal("expected non-200 when logsProvider is not set, got 200")
	}
}
