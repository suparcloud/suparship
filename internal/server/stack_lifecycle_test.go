package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/session"
)

// --- In-memory StackStore for tests ---

type memStackStore struct {
	stacks map[string]map[string]*domain.Stack // project -> name -> stack
}

func newMemStackStore() *memStackStore {
	return &memStackStore{stacks: make(map[string]map[string]*domain.Stack)}
}

func (m *memStackStore) SaveStack(_ context.Context, s *domain.Stack) error {
	if m.stacks[s.ProjectName] == nil {
		m.stacks[s.ProjectName] = make(map[string]*domain.Stack)
	}
	m.stacks[s.ProjectName][s.Name] = s
	return nil
}

func (m *memStackStore) GetStack(_ context.Context, project, name string) (*domain.Stack, error) {
	if s, ok := m.stacks[project][name]; ok {
		return s, nil
	}
	return nil, fmt.Errorf("stack not found: %s", name)
}

func (m *memStackStore) ListStacks(_ context.Context, project string) ([]*domain.Stack, error) {
	var out []*domain.Stack
	for _, s := range m.stacks[project] {
		out = append(out, s)
	}
	return out, nil
}

func (m *memStackStore) DeleteStack(_ context.Context, project, name string) error {
	delete(m.stacks[project], name)
	return nil
}

// newTestStackMux wires an rbacHandler with both an appHandler and a stackStore
// so the stack batch routes are registered.
func newTestStackMux(projectName string) (*http.ServeMux, *authHandler, *memAppStore, *memStackStore) {
	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	ah.registerRoutes(mux)

	store := newMemAppStore()
	store.mu.Lock()
	store.apps[projectName] = make(map[string]*domain.App)
	store.mu.Unlock()

	stackStore := newMemStackStore()
	appH := newAppHandler(store, nil, nil, nil)
	appH.stackStore = stackStore

	rh := &rbacHandler{
		auth:       ah,
		orgStore:   &staticOrgProvider{org: testRBACOrg()},
		appHandler: appH,
		stackStore: stackStore,
	}
	rh.registerRoutes(mux)

	return mux, ah, store, stackStore
}

// seedStackMember adds an app to the store as a member of stackName with a full
// promotion chain (preview → staging → prod), so it can be promoted.
func seedStackMember(store *memAppStore, project, appName, stackName string) {
	ctx := context.Background()
	store.addApp(&domain.App{
		Name:        appName,
		ProjectName: project,
		Spec: domain.AppSpec{
			Stack:      stackName,
			Template:   domain.AppTemplateRef{Name: "web-service"},
			Components: []domain.ComponentSpec{{Name: "web", Type: domain.ComponentWeb}},
		},
	})
	_ = store.SaveAppEnvironment(ctx, project, &domain.AppEnvironment{
		AppName: appName, EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1,
		Namespace: project + "-" + appName + "-staging",
		Release:   &domain.AppReleaseRef{Tag: "v1"},
		Status:    domain.AppRuntimeStatus{Phase: domain.StatusHealthy},
	})
	_ = store.SaveAppEnvironment(ctx, project, &domain.AppEnvironment{
		AppName: appName, EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2,
		Namespace: project + "-" + appName + "-prod",
		Status:    domain.AppRuntimeStatus{Phase: domain.StatusNotDeployed},
	})
}

func postStackJSON(mux *http.ServeMux, cookie *http.Cookie, url string, body any) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestStackPromote_FansOutToMembers verifies a stack promote promotes every
// member app and returns a per-app result row.
func TestStackPromote_FansOutToMembers(t *testing.T) {
	mux, ah, store, stackStore := newTestStackMux(testProject)
	_ = stackStore.SaveStack(context.Background(), &domain.Stack{Name: "voiceai", ProjectName: testProject})
	seedStackMember(store, testProject, "web", "voiceai")
	seedStackMember(store, testProject, "agent", "voiceai")
	// A non-member must NOT be promoted.
	seedStackMember(store, testProject, "loner", "")

	rec := postStackJSON(mux, sessionCookieFor(ah, "alice", "org_admin"),
		"/api/v1/projects/"+testProject+"/stacks/voiceai/promote",
		AppPromoteRequest{TargetEnvironment: "prod"})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp stackBatchResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("expected 2 member results (loner excluded), got %d: %+v", len(resp.Results), resp.Results)
	}
	for _, r := range resp.Results {
		if !r.OK {
			t.Errorf("member %q promote failed: %s", r.App, r.Error)
		}
	}
	// loner's prod must be untouched.
	loner, _ := store.GetAppEnvironment(context.Background(), testProject, "loner", "prod")
	if loner.Release != nil {
		t.Errorf("non-member 'loner' was promoted: %+v", loner.Release)
	}
}

// TestStackSync_RepublishesMembers verifies the sync route returns one ok row
// per member (no publisher wired → republishApp is a no-op success).
func TestStackSync_RepublishesMembers(t *testing.T) {
	mux, ah, store, stackStore := newTestStackMux(testProject)
	_ = stackStore.SaveStack(context.Background(), &domain.Stack{Name: "voiceai", ProjectName: testProject})
	seedStackMember(store, testProject, "web", "voiceai")
	seedStackMember(store, testProject, "agent", "voiceai")

	rec := postStackJSON(mux, sessionCookieFor(ah, "alice", "org_admin"),
		"/api/v1/projects/"+testProject+"/stacks/voiceai/sync", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp stackBatchResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(resp.Results))
	}
}

// TestStackDelete_WithApps removes every member app and the stack record.
func TestStackDelete_WithApps(t *testing.T) {
	mux, ah, store, stackStore := newTestStackMux(testProject)
	_ = stackStore.SaveStack(context.Background(), &domain.Stack{Name: "voiceai", ProjectName: testProject})
	seedStackMember(store, testProject, "web", "voiceai")
	seedStackMember(store, testProject, "agent", "voiceai")

	req := httptest.NewRequest(http.MethodDelete,
		"/api/v1/projects/"+testProject+"/stacks/voiceai?deleteApps=true", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := store.GetApp(context.Background(), testProject, "web"); err == nil {
		t.Error("member app 'web' should have been deleted")
	}
	if _, err := stackStore.GetStack(context.Background(), testProject, "voiceai"); err == nil {
		t.Error("stack record should have been deleted")
	}
}

// TestStackDelete_DefaultDetaches keeps the apps and clears their Stack field.
func TestStackDelete_DefaultDetaches(t *testing.T) {
	mux, ah, store, stackStore := newTestStackMux(testProject)
	_ = stackStore.SaveStack(context.Background(), &domain.Stack{Name: "voiceai", ProjectName: testProject})
	seedStackMember(store, testProject, "web", "voiceai")

	req := httptest.NewRequest(http.MethodDelete,
		"/api/v1/projects/"+testProject+"/stacks/voiceai", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	app, err := store.GetApp(context.Background(), testProject, "web")
	if err != nil {
		t.Fatalf("app should still exist: %v", err)
	}
	if app.Spec.Stack != "" {
		t.Errorf("detached app should have empty Stack, got %q", app.Spec.Stack)
	}
}
