package server

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/session"
	"github.com/suparcloud/suparship/internal/tpl"
)

// ─── Fake VaultItemWriter ──────────────────────────────────────────────────────

type fakeVaultItemWriter struct {
	mu      sync.Mutex
	upserts []vaultCall
	deletes []vaultCall
}

type vaultCall struct {
	org, project, app, env string
}

func (f *fakeVaultItemWriter) UpsertAppItem(_ context.Context, org, project, app, env string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserts = append(f.upserts, vaultCall{org, project, app, env})
	return nil
}

func (f *fakeVaultItemWriter) DeleteAppItem(_ context.Context, org, project, app, env string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, vaultCall{org, project, app, env})
	return nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// newAppCreateMuxWithVaultWriter builds a mux with app creation enabled and a
// fakeVaultItemWriter wired into the appHandler. The org has staging + prod
// both bound (ClusterRef="in-cluster").
func newAppCreateMuxWithVaultWriter(projectName string) (*httpMuxFixture, *fakeVaultItemWriter) {
	fakeVault := &fakeVaultItemWriter{}
	mux := newHttpServeMux()

	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	ah.registerRoutes(mux)

	appStore := newMemAppStore()
	appStore.mu.Lock()
	appStore.apps[projectName] = make(map[string]*domain.App)
	appStore.mu.Unlock()

	projStore := newMemProjectStore()
	_ = projStore.Save(context.Background(), appCreateTestProject())

	orgProv := &staticOrgProvider{org: testRBACOrg()}
	appH := newAppHandler(appStore, []*tpl.Template{appCreateTestTemplate()}, projStore)
	appH.orgProvider = orgProv
	appH.vaultWriter = fakeVault

	rh := &rbacHandler{
		auth:       ah,
		orgStore:   orgProv,
		appHandler: appH,
	}
	rh.registerRoutes(mux)

	return &httpMuxFixture{mux: mux, ah: ah, store: appStore}, fakeVault
}

// newPreviewMuxWithVaultWriter builds a mux with preview creation enabled and a
// fakeVaultItemWriter wired into the appHandler.
func newPreviewMuxWithVaultWriter(projectName string) (*httpMuxFixture, *fakeVaultItemWriter) {
	fakeVault := &fakeVaultItemWriter{}
	mux := newHttpServeMux()

	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	ah.registerRoutes(mux)

	appStore := newMemAppStore()
	appStore.mu.Lock()
	appStore.apps[projectName] = make(map[string]*domain.App)
	appStore.mu.Unlock()
	appStore.addApp(previewTestAppForProject(projectName))

	projStore := newMemProjectStore()
	orgProv := &staticOrgProvider{org: testRBACOrg()}
	appH := newAppHandler(appStore, nil, projStore)
	appH.orgProvider = orgProv
	appH.vaultWriter = fakeVault

	rh := &rbacHandler{
		auth:       ah,
		orgStore:   orgProv,
		appHandler: appH,
	}
	rh.registerRoutes(mux)

	return &httpMuxFixture{mux: mux, ah: ah, store: appStore}, fakeVault
}

// httpMuxFixture is a minimal helper for vault tests.
type httpMuxFixture struct {
	mux   *http.ServeMux
	ah    *authHandler
	store *memAppStore
}

func newHttpServeMux() *http.ServeMux { return http.NewServeMux() }

// ─── Tests: handleCreateApp vault upsert ──────────────────────────────────────

// TestHandleCreateApp_UpsertsVaultItemPerBoundStableEnv asserts that creating
// an app triggers one UpsertAppItem call per bound stable env (staging + prod
// in testRBACOrg). The project in newTestAppCreateMux is "demo".
func TestHandleCreateApp_UpsertsVaultItemPerBoundStableEnv(t *testing.T) {
	// appCreateTestProject uses "demo", so use that project name.
	fx, fakeVault := newAppCreateMuxWithVaultWriter("demo")
	cookie := sessionCookieFor(fx.ah, "alice", "org_admin")

	rec := postCreateAppJSON(fx.mux, cookie, "demo", createAppRequest{
		Name:     "myapp",
		Template: "web-service",
		Values:   map[string]any{"image": "nginx:latest"},
	})
	if rec.Code != 201 {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	fakeVault.mu.Lock()
	upserts := fakeVault.upserts
	fakeVault.mu.Unlock()

	if len(upserts) != 2 {
		t.Fatalf("expected 2 UpsertAppItem calls (staging + prod), got %d: %v", len(upserts), upserts)
	}
	envsSeen := map[string]bool{}
	for _, u := range upserts {
		if u.org != "test" {
			t.Errorf("expected org 'test', got %q", u.org)
		}
		if u.project != "demo" {
			t.Errorf("expected project 'demo', got %q", u.project)
		}
		if u.app != "myapp" {
			t.Errorf("expected app 'myapp', got %q", u.app)
		}
		envsSeen[u.env] = true
	}
	if !envsSeen["staging"] || !envsSeen["prod"] {
		t.Errorf("expected upserts for staging and prod, got envs: %v", envsSeen)
	}
}

// TestHandleCreateApp_NoVaultUpsertWhenWriterNil asserts that app creation
// succeeds without panicking when no vaultWriter is configured.
func TestHandleCreateApp_NoVaultUpsertWhenWriterNil(t *testing.T) {
	mux, ah, _, _ := newTestAppCreateMux()
	cookie := sessionCookieFor(ah, "alice", "org_admin")

	rec := postCreateAppJSON(mux, cookie, "demo", createAppRequest{
		Name:     "another-app",
		Template: "web-service",
		Values:   map[string]any{"image": "nginx:latest"},
	})
	if rec.Code != 201 {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ─── Tests: handleCreateAppPreview vault upsert ───────────────────────────────

func TestHandleCreateAppPreview_UpsertsVaultItem(t *testing.T) {
	fx, fakeVault := newPreviewMuxWithVaultWriter(testProject)
	cookie := sessionCookieFor(fx.ah, "bob", "developer")

	rec := postAppPreviewJSON(fx.mux, cookie, testProject, "my-app", CreateAppPreviewRequest{Name: "PR-99"})
	if rec.Code != 201 {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	fakeVault.mu.Lock()
	upserts := fakeVault.upserts
	fakeVault.mu.Unlock()

	if len(upserts) != 1 {
		t.Fatalf("expected 1 UpsertAppItem call for preview, got %d: %v", len(upserts), upserts)
	}
	u := upserts[0]
	if u.org != "test" || u.project != testProject || u.app != "my-app" || u.env != "pr-99" {
		t.Errorf("unexpected upsert call: %+v", u)
	}
}

// ─── Tests: handleDeleteAppPreview vault delete ───────────────────────────────

func TestHandleDeleteAppPreview_DeletesVaultItem(t *testing.T) {
	fx, fakeVault := newPreviewMuxWithVaultWriter(testProject)
	cookie := sessionCookieFor(fx.ah, "bob", "developer")

	// Create preview.
	createRec := postAppPreviewJSON(fx.mux, cookie, testProject, "my-app", CreateAppPreviewRequest{Name: "PR-77"})
	if createRec.Code != 201 {
		t.Fatalf("create preview: expected 201, got %d: %s", createRec.Code, createRec.Body.String())
	}

	// Reset recorded upserts before delete so we can assert cleanly.
	fakeVault.mu.Lock()
	fakeVault.upserts = nil
	fakeVault.mu.Unlock()

	// Delete preview.
	delRec := deleteAppPreview(fx.mux, cookie, testProject, "my-app", "pr-77")
	if delRec.Code != 204 {
		t.Fatalf("delete preview: expected 204, got %d: %s", delRec.Code, delRec.Body.String())
	}

	fakeVault.mu.Lock()
	deletes := fakeVault.deletes
	fakeVault.mu.Unlock()

	if len(deletes) != 1 {
		t.Fatalf("expected 1 DeleteAppItem call, got %d: %v", len(deletes), deletes)
	}
	d := deletes[0]
	if d.org != "test" || d.project != testProject || d.app != "my-app" || d.env != "pr-77" {
		t.Errorf("unexpected delete call: %+v", d)
	}
}
