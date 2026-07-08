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
	"github.com/suparcloud/suparship/internal/project"
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
	mux, ah, store, ss, _ := newTestStackMuxPub(projectName, nil)
	return mux, ah, store, ss
}

// newTestStackMuxPub is newTestStackMux with an injectable GitOpsPublisher and the
// appHandler exposed, so a test can assert publish behavior (e.g. batching).
func newTestStackMuxPub(projectName string, pub GitOpsPublisher) (*http.ServeMux, *authHandler, *memAppStore, *memStackStore, *appHandler) {
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
	if pub != nil {
		appH.gitOpsPublisher = pub
	}

	rh := &rbacHandler{
		auth:       ah,
		orgStore:   orgProv,
		appHandler: appH,
		stackStore: stackStore,
	}
	rh.registerRoutes(mux)

	return mux, ah, store, stackStore, appH
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

// setPreviewsEnabled flips an existing stored app's PreviewsEnabled flag.
func setPreviewsEnabled(store *memAppStore, project, appName string, enabled bool) {
	ctx := context.Background()
	a, _ := store.GetApp(ctx, project, appName)
	a.Spec.PreviewsEnabled = enabled
	_ = store.SaveApp(ctx, project, a)
}

// TestStackPreview_SkipsDisabledHonorsSubsetAndTag covers the stack preview
// fan-out: members with previews disabled are skipped (not failed), an optional
// apps subset targets only the named members, a single imageTag is recorded on
// every previewed member, and each preview lands in the shared stack namespace.
func TestStackPreview_SkipsDisabledHonorsSubsetAndTag(t *testing.T) {
	mux, ah, store, stackStore := newTestStackMux(testProject)
	_ = stackStore.SaveStack(context.Background(), &domain.Stack{Name: "voiceai", ProjectName: testProject})
	seedStackMember(store, testProject, "web", "voiceai")
	seedStackMember(store, testProject, "agent", "voiceai")
	seedStackMember(store, testProject, "nopreview", "voiceai")
	setPreviewsEnabled(store, testProject, "web", true)
	setPreviewsEnabled(store, testProject, "agent", true)
	setPreviewsEnabled(store, testProject, "nopreview", false)
	cookie := sessionCookieFor(ah, "alice", "org_admin")
	ctx := context.Background()

	// Full stack preview with an image tag.
	rec := postStackJSON(mux, cookie,
		"/api/v1/projects/"+testProject+"/stacks/voiceai/previews",
		stackPreviewRequest{Name: "pr-42", ImageTag: "sha-abc1234"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp stackBatchResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	byApp := map[string]stackOpResult{}
	for _, r := range resp.Results {
		byApp[r.App] = r
	}
	if len(byApp) != 3 {
		t.Fatalf("expected 3 rows, got %d: %+v", len(resp.Results), resp.Results)
	}
	if !byApp["nopreview"].Skipped {
		t.Errorf("nopreview should be skipped, got %+v", byApp["nopreview"])
	}
	if byApp["web"].Skipped || !byApp["web"].OK {
		t.Errorf("web should be a non-skipped success, got %+v", byApp["web"])
	}
	env, err := store.GetAppEnvironment(ctx, testProject, "web", "pr-42")
	if err != nil {
		t.Fatalf("web preview missing: %v", err)
	}
	if env.EnvType != domain.AppEnvPreview {
		t.Errorf("env type = %q, want preview", env.EnvType)
	}
	if env.Release == nil || env.Release.Tag != "sha-abc1234" {
		t.Errorf("web preview tag = %+v, want sha-abc1234", env.Release)
	}
	if want := testProject + "-voiceai-preview-pr-42"; env.Namespace != want {
		t.Errorf("web preview namespace = %q, want %q", env.Namespace, want)
	}
	if env.BaseEnv != "staging" {
		t.Errorf("web preview baseEnv = %q, want staging", env.BaseEnv)
	}
	if _, err := store.GetAppEnvironment(ctx, testProject, "nopreview", "pr-42"); err == nil {
		t.Error("nopreview should not have a preview env (skipped)")
	}

	// Subset: only agent gets pr-99.
	rec = postStackJSON(mux, cookie,
		"/api/v1/projects/"+testProject+"/stacks/voiceai/previews",
		stackPreviewRequest{Name: "pr-99", Apps: []string{"agent"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("subset preview: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := store.GetAppEnvironment(ctx, testProject, "agent", "pr-99"); err != nil {
		t.Errorf("agent should have pr-99: %v", err)
	}
	if _, err := store.GetAppEnvironment(ctx, testProject, "web", "pr-99"); err == nil {
		t.Error("web should not have pr-99 (excluded by subset)")
	}

	// Unknown app in subset → 400.
	rec = postStackJSON(mux, cookie,
		"/api/v1/projects/"+testProject+"/stacks/voiceai/previews",
		stackPreviewRequest{Name: "pr-1", Apps: []string{"ghost"}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown subset app: expected 400, got %d: %s", rec.Code, rec.Body.String())
	}

	// Delete with a differently-cased name resolves the sanitized env: the
	// preview "pr-42" is torn down when the caller deletes "PR-42".
	delReq := httptest.NewRequest(http.MethodDelete,
		"/api/v1/projects/"+testProject+"/stacks/voiceai/previews/PR-42", nil)
	delReq.AddCookie(cookie)
	delRec := httptest.NewRecorder()
	mux.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusOK {
		t.Fatalf("delete PR-42: expected 200, got %d: %s", delRec.Code, delRec.Body.String())
	}
	if _, err := store.GetAppEnvironment(ctx, testProject, "web", "pr-42"); err == nil {
		t.Error("web preview pr-42 should be deleted via the raw name 'PR-42'")
	}
}

// TestStackPreview_DeveloperRole verifies a developer (not just project admin)
// can create a stack preview — the route CI uses once per PR.
func TestStackPreview_DeveloperRole(t *testing.T) {
	mux, ah, store, stackStore := newTestStackMux(testProject)
	_ = stackStore.SaveStack(context.Background(), &domain.Stack{Name: "voiceai", ProjectName: testProject})
	seedStackMember(store, testProject, "web", "voiceai")
	setPreviewsEnabled(store, testProject, "web", true)

	rec := postStackJSON(mux, sessionCookieFor(ah, "bob", "developer"),
		"/api/v1/projects/"+testProject+"/stacks/voiceai/previews",
		stackPreviewRequest{Name: "pr-7"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("developer stack preview: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestStackPin_FansOutSkippingDirectAndMissing covers the stack pin/unpin
// fan-out: a pipeline member with the named preview is pinned to its own tag, a
// member lacking that preview is skipped, a direct-delivery member is skipped,
// and unpin clears the pin (skipping members that weren't pinned).
func TestStackPin_FansOutSkippingDirectAndMissing(t *testing.T) {
	mux, ah, store, stackStore := newTestStackMux(testProject)
	_ = stackStore.SaveStack(context.Background(), &domain.Stack{Name: "voiceai", ProjectName: testProject})
	seedStackMember(store, testProject, "web", "voiceai")
	seedStackMember(store, testProject, "agent", "voiceai")
	// Direct-delivery member: pinning does not apply → skipped.
	store.addApp(&domain.App{
		Name: "cache", ProjectName: testProject,
		Spec: domain.AppSpec{
			Stack:        "voiceai",
			DeliveryMode: domain.DeliveryDirect,
			Template:     domain.AppTemplateRef{Name: "redis"},
		},
	})
	_ = store.SaveAppEnvironment(context.Background(), testProject, &domain.AppEnvironment{
		AppName: "cache", EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1,
		Namespace: testProject + "-cache-staging",
	})
	// Only web has the PR preview with a built image.
	_ = store.SaveAppEnvironment(context.Background(), testProject, &domain.AppEnvironment{
		AppName: "web", EnvName: "pr-5", EnvType: domain.AppEnvPreview, BaseEnv: "staging",
		Namespace: testProject + "-voiceai-preview-pr-5",
		Release:   &domain.AppReleaseRef{Tag: "sha-web5"},
	})
	cookie := sessionCookieFor(ah, "alice", "org_admin")
	ctx := context.Background()

	rec := postStackJSON(mux, cookie,
		"/api/v1/projects/"+testProject+"/stacks/voiceai/pin",
		stackPinRequest{FromPreview: "pr-5", TargetEnv: "staging"})
	if rec.Code != http.StatusOK {
		t.Fatalf("pin: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp stackBatchResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	byApp := map[string]stackOpResult{}
	for _, r := range resp.Results {
		byApp[r.App] = r
	}
	if len(byApp) != 3 {
		t.Fatalf("expected 3 rows, got %+v", resp.Results)
	}
	if byApp["web"].Skipped || !byApp["web"].OK {
		t.Errorf("web should be pinned, got %+v", byApp["web"])
	}
	if !byApp["agent"].Skipped {
		t.Errorf("agent should be skipped (no pr-5 preview), got %+v", byApp["agent"])
	}
	if !byApp["cache"].Skipped {
		t.Errorf("cache should be skipped (direct delivery), got %+v", byApp["cache"])
	}
	web, _ := store.GetApp(ctx, testProject, "web")
	if ov := web.Spec.EnvironmentDefaults["staging"]; ov.PinnedFrom != "pr-5" || ov.PinnedImageTag != "sha-web5" {
		t.Errorf("web staging pin = %+v, want from=pr-5 tag=sha-web5", ov)
	}

	// Unpin the stack (JSON body, symmetric with pin).
	body, _ := json.Marshal(stackSuspendRequest{TargetEnv: "staging"})
	req := httptest.NewRequest(http.MethodDelete,
		"/api/v1/projects/"+testProject+"/stacks/voiceai/pin", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("unpin: expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var uresp stackBatchResponse
	_ = json.NewDecoder(rec2.Body).Decode(&uresp)
	ubyApp := map[string]stackOpResult{}
	for _, r := range uresp.Results {
		ubyApp[r.App] = r
	}
	if ubyApp["web"].Skipped || !ubyApp["web"].OK {
		t.Errorf("web should be unpinned, got %+v", ubyApp["web"])
	}
	if !ubyApp["agent"].Skipped {
		t.Errorf("agent should be skipped (not pinned), got %+v", ubyApp["agent"])
	}
	web2, _ := store.GetApp(ctx, testProject, "web")
	if pf := web2.Spec.EnvironmentDefaults["staging"].PinnedFrom; pf != "" {
		t.Errorf("web staging should be unpinned, got PinnedFrom=%q", pf)
	}

	// A mistyped targetEnv is rejected (422), not silently skipped as success.
	recBad := postStackJSON(mux, cookie,
		"/api/v1/projects/"+testProject+"/stacks/voiceai/pin",
		stackPinRequest{FromPreview: "pr-5", TargetEnv: "stagingX"})
	if recBad.Code != http.StatusUnprocessableEntity {
		t.Errorf("bogus targetEnv: expected 422, got %d: %s", recBad.Code, recBad.Body.String())
	}
}

// TestStackSuspend_FansOutAndResumes covers stack suspend/resume: it fans out
// over members setting the per-env Suspend flag (a member without the target env
// is skipped), works for a developer, and resume clears the flag.
func TestStackSuspend_FansOutAndResumes(t *testing.T) {
	mux, ah, store, stackStore := newTestStackMux(testProject)
	_ = stackStore.SaveStack(context.Background(), &domain.Stack{Name: "voiceai", ProjectName: testProject})
	seedStackMember(store, testProject, "web", "voiceai")
	seedStackMember(store, testProject, "agent", "voiceai")
	// A member with no staging env → skipped.
	store.addApp(&domain.App{
		Name: "noenv", ProjectName: testProject,
		Spec: domain.AppSpec{Stack: "voiceai", Template: domain.AppTemplateRef{Name: "web-service"}},
	})
	cookie := sessionCookieFor(ah, "bob", "developer") // developer role is allowed
	ctx := context.Background()

	rec := postStackJSON(mux, cookie,
		"/api/v1/projects/"+testProject+"/stacks/voiceai/suspend",
		stackSuspendRequest{TargetEnv: "staging"})
	if rec.Code != http.StatusOK {
		t.Fatalf("suspend: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp stackBatchResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	byApp := map[string]stackOpResult{}
	for _, r := range resp.Results {
		byApp[r.App] = r
	}
	if byApp["web"].Skipped || !byApp["web"].OK {
		t.Errorf("web should be suspended, got %+v", byApp["web"])
	}
	if !byApp["noenv"].Skipped {
		t.Errorf("noenv should be skipped (no staging env), got %+v", byApp["noenv"])
	}
	web, _ := store.GetApp(ctx, testProject, "web")
	if s := web.Spec.EnvironmentDefaults["staging"].Suspend; s == nil || !*s {
		t.Errorf("web staging Suspend = %v, want true", s)
	}

	// Resume clears the flag.
	rec2 := postStackJSON(mux, cookie,
		"/api/v1/projects/"+testProject+"/stacks/voiceai/resume",
		stackSuspendRequest{TargetEnv: "staging"})
	if rec2.Code != http.StatusOK {
		t.Fatalf("resume: expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	web2, _ := store.GetApp(ctx, testProject, "web")
	if s := web2.Spec.EnvironmentDefaults["staging"].Suspend; s != nil {
		t.Errorf("web staging Suspend after resume = %v, want nil", s)
	}
}

// TestStackSuspend_BatchesGitPublish verifies the fan-out publishes every member
// in ONE batched call (PublishAppsEnv) rather than N per-member publishes — the
// fix for the 504 on large stacks.
func TestStackSuspend_BatchesGitPublish(t *testing.T) {
	pub := &recordingPublisher{}
	mux, ah, store, stackStore, _ := newTestStackMuxPub(testProject, pub)
	_ = stackStore.SaveStack(context.Background(), &domain.Stack{Name: "voiceai", ProjectName: testProject})
	seedStackMember(store, testProject, "web", "voiceai")
	seedStackMember(store, testProject, "agent", "voiceai")
	seedStackMember(store, testProject, "worker", "voiceai")

	rec := postStackJSON(mux, sessionCookieFor(ah, "bob", "developer"),
		"/api/v1/projects/"+testProject+"/stacks/voiceai/suspend",
		stackSuspendRequest{TargetEnv: "staging"})
	if rec.Code != http.StatusOK {
		t.Fatalf("suspend: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if pub.batchCalls != 1 {
		t.Errorf("expected 1 batched PublishAppsEnv call, got %d (targets=%v)", pub.batchCalls, pub.batchTargets)
	}
	if len(pub.batchTargets) != 1 || pub.batchTargets[0] != 3 {
		t.Errorf("expected one batch of 3 targets, got %v", pub.batchTargets)
	}
}

// TestStackPreview_BatchesGitPublish verifies the stack preview fan-out prepares
// each member's spec concurrently then publishes them all in ONE batched
// PublishPreviews call — not N per-member PublishAppPreview — the 504 fix.
func TestStackPreview_BatchesGitPublish(t *testing.T) {
	pub := &recordingPublisher{}
	mux, ah, store, stackStore, _ := newTestStackMuxPub(testProject, pub)
	_ = stackStore.SaveStack(context.Background(), &domain.Stack{Name: "voiceai", ProjectName: testProject})
	for _, app := range []string{"web", "agent", "worker"} {
		seedStackMember(store, testProject, app, "voiceai")
		setPreviewsEnabled(store, testProject, app, true)
	}

	rec := postStackJSON(mux, sessionCookieFor(ah, "bob", "developer"),
		"/api/v1/projects/"+testProject+"/stacks/voiceai/previews",
		stackPreviewRequest{Name: "pr-42", ImageTag: "sha-abc"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("preview: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if pub.batchPreviewCalls != 1 {
		t.Errorf("expected 1 batched PublishPreviews call, got %d (targets=%v)", pub.batchPreviewCalls, pub.batchPreviewTargets)
	}
	if len(pub.batchPreviewTargets) != 1 || pub.batchPreviewTargets[0] != 3 {
		t.Errorf("expected one batch of 3 targets, got %v", pub.batchPreviewTargets)
	}
	if pub.previewCalls != 0 {
		t.Errorf("per-member PublishAppPreview should not be called on the batch path, got %d", pub.previewCalls)
	}
}

// TestStackPin_BatchesGitPublish verifies the pin fan-out publishes every
// pinned member in ONE batched PublishApps call (tree + Kargo pause + target
// env) rather than N per-member republish+publish — the 504 fix for pin.
func TestStackPin_BatchesGitPublish(t *testing.T) {
	pub := &recordingPublisher{}
	mux, ah, store, stackStore, _ := newTestStackMuxPub(testProject, pub)
	_ = stackStore.SaveStack(context.Background(), &domain.Stack{Name: "voiceai", ProjectName: testProject})
	seedStackMember(store, testProject, "web", "voiceai")
	seedStackMember(store, testProject, "agent", "voiceai")
	// Both members carry the PR preview with a built image.
	for _, app := range []string{"web", "agent"} {
		_ = store.SaveAppEnvironment(context.Background(), testProject, &domain.AppEnvironment{
			AppName: app, EnvName: "pr-5", EnvType: domain.AppEnvPreview, BaseEnv: "staging",
			Namespace: testProject + "-voiceai-preview-pr-5",
			Release:   &domain.AppReleaseRef{Tag: "sha-" + app},
		})
	}

	rec := postStackJSON(mux, sessionCookieFor(ah, "alice", "org_admin"),
		"/api/v1/projects/"+testProject+"/stacks/voiceai/pin",
		stackPinRequest{FromPreview: "pr-5", TargetEnv: "staging"})
	if rec.Code != http.StatusOK {
		t.Fatalf("pin: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if pub.batchAppCalls != 1 {
		t.Errorf("expected 1 batched PublishApps call, got %d (targets=%v)", pub.batchAppCalls, pub.batchAppTargets)
	}
	if len(pub.batchAppTargets) != 1 || pub.batchAppTargets[0] != 2 {
		t.Errorf("expected one batch of 2 targets, got %v", pub.batchAppTargets)
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

// TestStackTargetClusters_FansOutAndClears verifies the stack target-clusters
// batch sets EnvironmentDefaults[env].TargetClusters on every member deployed to
// the env (skipping members without it) and clears it when sent an empty list.
func TestStackTargetClusters_FansOutAndClears(t *testing.T) {
	mux, ah, store, stackStore := newTestStackMux(testProject)
	_ = stackStore.SaveStack(context.Background(), &domain.Stack{Name: "voiceai", ProjectName: testProject})
	seedStackMember(store, testProject, "web", "voiceai")
	seedStackMember(store, testProject, "agent", "voiceai")
	// A member with no staging env → skipped.
	store.addApp(&domain.App{
		Name: "noenv", ProjectName: testProject,
		Spec: domain.AppSpec{Stack: "voiceai", Template: domain.AppTemplateRef{Name: "web-service"}},
	})
	cookie := sessionCookieFor(ah, "alice", "org_admin") // manageProject route
	ctx := context.Background()

	// Set "all clusters" (sentinel is always valid, so no ClusterRefs setup needed).
	rec := postStackJSON(mux, cookie,
		"/api/v1/projects/"+testProject+"/stacks/voiceai/target-clusters",
		stackTargetClustersRequest{TargetEnv: "staging", Clusters: []string{domain.AllClustersSentinel}})
	if rec.Code != http.StatusOK {
		t.Fatalf("target-clusters: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp stackBatchResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	byApp := map[string]stackOpResult{}
	for _, r := range resp.Results {
		byApp[r.App] = r
	}
	if byApp["web"].Skipped || !byApp["web"].OK {
		t.Errorf("web should be set, got %+v", byApp["web"])
	}
	if !byApp["noenv"].Skipped {
		t.Errorf("noenv should be skipped (no staging env), got %+v", byApp["noenv"])
	}
	web, _ := store.GetApp(ctx, testProject, "web")
	if got := web.Spec.EnvironmentDefaults["staging"].TargetClusters; len(got) != 1 || got[0] != domain.AllClustersSentinel {
		t.Errorf("web staging TargetClusters = %v, want [*]", got)
	}

	// Empty list clears the override (back to env default).
	rec2 := postStackJSON(mux, cookie,
		"/api/v1/projects/"+testProject+"/stacks/voiceai/target-clusters",
		stackTargetClustersRequest{TargetEnv: "staging", Clusters: []string{}})
	if rec2.Code != http.StatusOK {
		t.Fatalf("clear: expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	web2, _ := store.GetApp(ctx, testProject, "web")
	if got := web2.Spec.EnvironmentDefaults["staging"].TargetClusters; got != nil {
		t.Errorf("web staging TargetClusters after clear = %v, want nil", got)
	}
}

// TestStackPreviewNamespace_HonorsProjectPattern pins the namespace resolution
// for stack previews. The empty pattern must reproduce the historical hardcoded
// "{project}-{stack}-preview-{name}" namespace exactly (no migration for existing
// previews), and a project-configured pattern must win — including a shared one
// that omits {name}.
func TestStackPreviewNamespace_HonorsProjectPattern(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		want    string
	}{
		{"empty falls back to legacy hardcoded namespace", "", "voiceai-lk-sh-preview-pr-724"},
		{"explicit default matches legacy", "{project}-{app}-preview-{name}", "voiceai-lk-sh-preview-pr-724"},
		{"project pattern wins", "{project}-preview-{name}", "voiceai-preview-pr-724"},
		{"shared namespace omits name", "{project}-preview", "voiceai-preview"},
		{"stack fills the app slot", "{app}-{name}", "lk-sh-pr-724"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := stackPreviewNamespace("voiceai", "lk-sh", "pr-724", tc.pattern)
			if got != tc.want {
				t.Errorf("stackPreviewNamespace(pattern=%q) = %q, want %q", tc.pattern, got, tc.want)
			}
		})
	}
}

// TestStackPreview_UsesProjectPreviewNamespacePattern is the end-to-end guard for
// the bug this fixes: a project that sets previewNamespacePattern was ignored by
// the stack preview path, which hardcoded its own namespace. Every member must
// land in the project-configured namespace, and still be co-located there.
func TestStackPreview_UsesProjectPreviewNamespacePattern(t *testing.T) {
	mux, ah, store, stackStore, appH := newTestStackMuxPub(testProject, nil)
	projStore := newMemProjectStore()
	_ = projStore.Save(context.Background(), &project.Project{
		Metadata: project.ProjectMeta{Name: testProject},
		Spec:     project.ProjectSpec{PreviewNamespacePattern: "{project}-preview-{name}"},
	})
	appH.projectStore = projStore

	ctx := context.Background()
	_ = stackStore.SaveStack(ctx, &domain.Stack{Name: "lk-sh", ProjectName: testProject})
	seedStackMember(store, testProject, "web", "lk-sh")
	seedStackMember(store, testProject, "agent", "lk-sh")
	setPreviewsEnabled(store, testProject, "web", true)
	setPreviewsEnabled(store, testProject, "agent", true)

	rec := postStackJSON(mux, sessionCookieFor(ah, "alice", "org_admin"),
		"/api/v1/projects/"+testProject+"/stacks/lk-sh/previews",
		stackPreviewRequest{Name: "pr-724"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	want := testProject + "-preview-pr-724"
	for _, app := range []string{"web", "agent"} {
		env, err := store.GetAppEnvironment(ctx, testProject, app, "pr-724")
		if err != nil {
			t.Fatalf("no preview env for %q: %v", app, err)
		}
		if env.Namespace != want {
			t.Errorf("%s preview namespace = %q, want %q (project pattern ignored)", app, env.Namespace, want)
		}
	}
}
