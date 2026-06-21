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
	orgProv := &staticOrgProvider{org: testRBACOrg()}
	appH := newAppHandler(store, nil, nil, nil)
	appH.stackStore = stackStore
	appH.orgProvider = orgProv // needed so relocate can resolve env namespaces

	rh := &rbacHandler{
		auth:       ah,
		orgStore:   orgProv,
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

func putStackJSON(mux *http.ServeMux, cookie *http.Cookie, url string, body any) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, url, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestSetAppStack_JoinAndDetach covers stack membership: joining a stack sets
// AppSpec.Stack, an unknown stack is rejected, and detaching clears it.
func TestSetAppStack_JoinAndDetach(t *testing.T) {
	mux, ah, store, stackStore := newTestStackMux(testProject)
	_ = stackStore.SaveStack(context.Background(), &domain.Stack{Name: "voiceai", ProjectName: testProject})
	seedStackMember(store, testProject, "web", "") // starts unattached
	cookie := sessionCookieFor(ah, "alice", "org_admin")
	url := "/api/v1/projects/" + testProject + "/apps/web/stack"

	if rec := putStackJSON(mux, cookie, url, setAppStackRequest{Stack: "voiceai"}); rec.Code != http.StatusOK {
		t.Fatalf("join: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got, _ := store.GetApp(context.Background(), testProject, "web"); got.Spec.Stack != "voiceai" {
		t.Errorf("stack = %q, want voiceai", got.Spec.Stack)
	}

	if rec := putStackJSON(mux, cookie, url, setAppStackRequest{Stack: "nope"}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown stack: expected 404, got %d", rec.Code)
	}

	if rec := putStackJSON(mux, cookie, url, setAppStackRequest{Stack: ""}); rec.Code != http.StatusOK {
		t.Fatalf("detach: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got, _ := store.GetApp(context.Background(), testProject, "web"); got.Spec.Stack != "" {
		t.Errorf("stack = %q, want empty after detach", got.Spec.Stack)
	}
}

// TestStack_SharedNamespaceCoLocation verifies that joining a shared-namespace
// stack relocates the app into the co-located {project}-{stack}-{env} namespace.
func TestStack_SharedNamespaceCoLocation(t *testing.T) {
	mux, ah, store, stackStore := newTestStackMux(testProject)
	_ = stackStore.SaveStack(context.Background(), &domain.Stack{
		Name: "voiceai", ProjectName: testProject,
		Spec: domain.StackSpec{SharedNamespace: true},
	})
	seedStackMember(store, testProject, "web", "")

	rec := putStackJSON(mux, sessionCookieFor(ah, "alice", "org_admin"),
		"/api/v1/projects/"+testProject+"/apps/web/stack", setAppStackRequest{Stack: "voiceai"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	staging, _ := store.GetAppEnvironment(context.Background(), testProject, "web", "staging")
	if want := testProject + "-voiceai-staging"; staging.Namespace != want {
		t.Errorf("staging namespace = %q, want %q (shared-stack co-location)", staging.Namespace, want)
	}
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

// TestStackClone_CopiesMembersUnderNewNames clones a stack: the source stays
// intact, the new stack exists, and each member is copied under {newStack}-{old}
// (or an explicit override) with Spec.Stack pointing at the new stack.
func TestStackClone_CopiesMembersUnderNewNames(t *testing.T) {
	mux, ah, store, stackStore := newTestStackMux(testProject)
	_ = stackStore.SaveStack(context.Background(), &domain.Stack{
		Name: "voiceai", ProjectName: testProject,
		Spec: domain.StackSpec{DisplayName: "VoiceAI", RawValues: map[string]any{"replicas": 2}},
	})
	seedStackMember(store, testProject, "web", "voiceai")
	seedStackMember(store, testProject, "agent", "voiceai")

	rec := postStackJSON(mux, sessionCookieFor(ah, "alice", "org_admin"),
		"/api/v1/projects/"+testProject+"/stacks/voiceai/clone",
		cloneStackRequest{
			NewName:  "voiceai-selfhosted",
			AppNames: map[string]string{"web": "selfhosted-web"}, // override one
		})

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp cloneStackResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("expected 2 copy results, got %d: %+v", len(resp.Results), resp.Results)
	}
	for _, r := range resp.Results {
		if !r.OK {
			t.Errorf("clone of %q failed: %s", r.App, r.Error)
		}
	}
	ctx := context.Background()
	// Overridden name.
	if a, err := store.GetApp(ctx, testProject, "selfhosted-web"); err != nil {
		t.Errorf("expected cloned app 'selfhosted-web': %v", err)
	} else if a.Spec.Stack != "voiceai-selfhosted" {
		t.Errorf("cloned app Stack = %q, want %q", a.Spec.Stack, "voiceai-selfhosted")
	}
	// Derived name.
	if _, err := store.GetApp(ctx, testProject, "voiceai-selfhosted-agent"); err != nil {
		t.Errorf("expected derived clone 'voiceai-selfhosted-agent': %v", err)
	}
	// Source intact.
	if _, err := store.GetApp(ctx, testProject, "web"); err != nil {
		t.Errorf("source app 'web' should still exist: %v", err)
	}
	if _, err := stackStore.GetStack(ctx, testProject, "voiceai"); err != nil {
		t.Errorf("source stack should still exist: %v", err)
	}
	// New stack carries the copied override but a reset (empty) display name.
	ns, err := stackStore.GetStack(ctx, testProject, "voiceai-selfhosted")
	if err != nil {
		t.Fatalf("new stack missing: %v", err)
	}
	if ns.Spec.DisplayName != "" {
		t.Errorf("clone DisplayName = %q, want empty (falls back to name)", ns.Spec.DisplayName)
	}
	if ns.Spec.RawValues["replicas"] != 2 {
		t.Errorf("clone should carry source RawValues, got %+v", ns.Spec.RawValues)
	}
}

// TestStackClone_ConflictOnExistingName rejects cloning onto an existing stack.
func TestStackClone_ConflictOnExistingName(t *testing.T) {
	mux, ah, _, stackStore := newTestStackMux(testProject)
	_ = stackStore.SaveStack(context.Background(), &domain.Stack{Name: "voiceai", ProjectName: testProject})
	_ = stackStore.SaveStack(context.Background(), &domain.Stack{Name: "taken", ProjectName: testProject})

	rec := postStackJSON(mux, sessionCookieFor(ah, "alice", "org_admin"),
		"/api/v1/projects/"+testProject+"/stacks/voiceai/clone",
		cloneStackRequest{NewName: "taken"})

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
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
