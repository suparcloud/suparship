package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/runtime"
	"github.com/suparcloud/suparship/internal/session"
)

// --- Fake logs provider ---

type fakeLogsProvider struct {
	pods map[string][]runtime.PodInfo // namespace → pods
	logs map[string]string            // "namespace/pod/container" → log output
}

func newFakeLogsProvider() *fakeLogsProvider {
	return &fakeLogsProvider{
		pods: make(map[string][]runtime.PodInfo),
		logs: make(map[string]string),
	}
}

func (f *fakeLogsProvider) ListPods(_ context.Context, namespace, _ string) ([]runtime.PodInfo, error) {
	pods, ok := f.pods[namespace]
	if !ok {
		return []runtime.PodInfo{}, nil
	}
	return pods, nil
}

func (f *fakeLogsProvider) GetLogs(_ context.Context, req runtime.LogsRequest) (*runtime.LogsResult, error) {
	container := req.Container
	if container == "" {
		container = "(default)"
	}
	key := req.Namespace + "/" + req.Pod + "/" + container
	logs, ok := f.logs[key]
	if !ok {
		return nil, fmt.Errorf("pod %s/%s not found", req.Namespace, req.Pod)
	}
	return &runtime.LogsResult{
		Pod:       req.Pod,
		Container: container,
		Logs:      logs,
	}, nil
}

// --- Test helpers ---

func logsTestProject() *project.Project {
	return &project.Project{
		APIVersion: project.CurrentAPIVersion,
		Kind:       project.ProjectKind,
		Metadata:   project.ProjectMeta{Name: "api"},
		Spec: project.ProjectSpec{
			Environments: []project.Environment{
				{Name: "dev", Order: 1},
				{Name: "staging", Order: 2},
				{Name: "prod", Order: 3},
			},
			Services: []project.Service{
				{Name: "backend", Template: project.TemplateRef{Name: "web-service"}},
			},
		},
	}
}

func newTestLogsMux() (*http.ServeMux, *authHandler, *memProjectStore, *fakeLogsProvider) {
	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	ah.registerRoutes(mux)

	store := newMemProjectStore()
	lp := newFakeLogsProvider()

	rh := &rbacHandler{
		auth:        ah,
		orgProvider: &staticOrgProvider{org: testRBACOrg()},
		logsHandler: newLogsHandler(store, lp),
	}
	rh.registerRoutes(mux)

	return mux, ah, store, lp
}

func getLogs(mux *http.ServeMux, cookie *http.Cookie, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// --- Tests ---

func TestLogsMissingEnvironment(t *testing.T) {
	mux, ah, store, _ := newTestLogsMux()
	_ = store.Save(context.Background(), logsTestProject())

	rec := getLogs(mux, sessionCookieFor(ah, "alice", "org_admin"),
		"/api/v1/projects/api/services/backend/logs")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp errorResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if !contains(resp.Error, "environment") {
		t.Fatalf("expected error about environment, got %q", resp.Error)
	}
}

func TestLogsProjectNotFound(t *testing.T) {
	mux, ah, _, _ := newTestLogsMux()

	rec := getLogs(mux, sessionCookieFor(ah, "alice", "org_admin"),
		"/api/v1/projects/nonexistent/services/backend/logs?environment=dev")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestLogsServiceNotFound(t *testing.T) {
	mux, ah, store, _ := newTestLogsMux()
	_ = store.Save(context.Background(), logsTestProject())

	rec := getLogs(mux, sessionCookieFor(ah, "alice", "org_admin"),
		"/api/v1/projects/api/services/nonexistent/logs?environment=dev")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestLogsInvalidEnvironment(t *testing.T) {
	mux, ah, store, _ := newTestLogsMux()
	_ = store.Save(context.Background(), logsTestProject())

	rec := getLogs(mux, sessionCookieFor(ah, "alice", "org_admin"),
		"/api/v1/projects/api/services/backend/logs?environment=nope")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestLogsInvalidTailLines(t *testing.T) {
	mux, ah, store, _ := newTestLogsMux()
	_ = store.Save(context.Background(), logsTestProject())

	rec := getLogs(mux, sessionCookieFor(ah, "alice", "org_admin"),
		"/api/v1/projects/api/services/backend/logs?environment=dev&tailLines=abc")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp errorResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if !contains(resp.Error, "tailLines") {
		t.Fatalf("expected tailLines error, got %q", resp.Error)
	}
}

func TestLogsNegativeTailLines(t *testing.T) {
	mux, ah, store, _ := newTestLogsMux()
	_ = store.Save(context.Background(), logsTestProject())

	rec := getLogs(mux, sessionCookieFor(ah, "alice", "org_admin"),
		"/api/v1/projects/api/services/backend/logs?environment=dev&tailLines=-5")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestLogsNoPods(t *testing.T) {
	mux, ah, store, _ := newTestLogsMux()
	_ = store.Save(context.Background(), logsTestProject())

	rec := getLogs(mux, sessionCookieFor(ah, "alice", "org_admin"),
		"/api/v1/projects/api/services/backend/logs?environment=dev")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when no pods, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp errorResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if !contains(resp.Error, "no pods") {
		t.Fatalf("expected 'no pods' error, got %q", resp.Error)
	}
}

func TestLogsAutoSelectPod(t *testing.T) {
	mux, ah, store, lp := newTestLogsMux()
	_ = store.Save(context.Background(), logsTestProject())

	lp.pods["api-dev"] = []runtime.PodInfo{
		{Name: "backend-abc-123", Containers: []string{"backend"}},
	}
	lp.logs["api-dev/backend-abc-123/backend"] = "hello world\n"

	rec := getLogs(mux, sessionCookieFor(ah, "alice", "org_admin"),
		"/api/v1/projects/api/services/backend/logs?environment=dev")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp LogsResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Pod != "backend-abc-123" {
		t.Fatalf("pod: want %q, got %q", "backend-abc-123", resp.Pod)
	}
	if resp.Container != "backend" {
		t.Fatalf("container: want %q, got %q", "backend", resp.Container)
	}
	if resp.Logs != "hello world\n" {
		t.Fatalf("logs: want %q, got %q", "hello world\n", resp.Logs)
	}
	if resp.Project != "api" || resp.Service != "backend" {
		t.Fatalf("project/service mismatch: %+v", resp)
	}
}

func TestLogsExplicitPodAndContainer(t *testing.T) {
	mux, ah, store, lp := newTestLogsMux()
	_ = store.Save(context.Background(), logsTestProject())

	lp.pods["api-staging"] = []runtime.PodInfo{
		{Name: "backend-xyz-789", Containers: []string{"backend", "sidecar"}},
	}
	lp.logs["api-staging/backend-xyz-789/sidecar"] = "sidecar output\n"

	rec := getLogs(mux, sessionCookieFor(ah, "alice", "org_admin"),
		"/api/v1/projects/api/services/backend/logs?environment=staging&pod=backend-xyz-789&container=sidecar")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp LogsResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Container != "sidecar" {
		t.Fatalf("container: want %q, got %q", "sidecar", resp.Container)
	}
	if resp.Logs != "sidecar output\n" {
		t.Fatalf("logs: want %q, got %q", "sidecar output\n", resp.Logs)
	}
}

func TestLogsWithTailLines(t *testing.T) {
	mux, ah, store, lp := newTestLogsMux()
	_ = store.Save(context.Background(), logsTestProject())

	lp.pods["api-dev"] = []runtime.PodInfo{
		{Name: "backend-abc-123", Containers: []string{"backend"}},
	}
	lp.logs["api-dev/backend-abc-123/backend"] = "line 1\nline 2\n"

	rec := getLogs(mux, sessionCookieFor(ah, "alice", "org_admin"),
		"/api/v1/projects/api/services/backend/logs?environment=dev&tailLines=100")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestLogsUnauthenticated(t *testing.T) {
	mux, _, store, _ := newTestLogsMux()
	_ = store.Save(context.Background(), logsTestProject())

	rec := getLogs(mux, nil,
		"/api/v1/projects/api/services/backend/logs?environment=dev")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestLogsViewerCanAccess(t *testing.T) {
	mux, ah, store, lp := newTestLogsMux()
	_ = store.Save(context.Background(), logsTestProject())

	lp.pods["api-dev"] = []runtime.PodInfo{
		{Name: "backend-abc-123", Containers: []string{"backend"}},
	}
	lp.logs["api-dev/backend-abc-123/backend"] = "viewer logs\n"

	// carol is a viewer with wildcard access
	rec := getLogs(mux, sessionCookieFor(ah, "carol", "viewer"),
		"/api/v1/projects/api/services/backend/logs?environment=dev")

	if rec.Code != http.StatusOK {
		t.Fatalf("viewer should access logs, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestLogsDeveloperNoAccessToOtherProject(t *testing.T) {
	mux, ah, store, _ := newTestLogsMux()
	_ = store.Save(context.Background(), logsTestProject())

	// bob has developer access on "api" only, not "web"
	rec := getLogs(mux, sessionCookieFor(ah, "bob", "developer"),
		"/api/v1/projects/web/services/backend/logs?environment=dev")

	if rec.Code != http.StatusForbidden {
		t.Fatalf("developer should not access other project logs, got %d", rec.Code)
	}
}
