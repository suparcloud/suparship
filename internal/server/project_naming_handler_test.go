package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/session"
)

// newNamingMux wires a mux with the project naming routes, seeded with project
// "api" (alice=org_admin, bob=developer per testRBACOrg).
func newNamingMux(t *testing.T) (*http.ServeMux, *authHandler, *memProjectStore) {
	t.Helper()
	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	ah.registerRoutes(mux)

	projStore := newMemProjectStore()
	_ = projStore.Save(context.Background(), &project.Project{
		Metadata: project.ProjectMeta{Name: "api"},
		Spec:     project.ProjectSpec{DisplayName: "API"},
	})

	rh := &rbacHandler{
		auth:         ah,
		orgStore:     &staticOrgProvider{org: testRBACOrg()},
		projectStore: projStore,
	}
	rh.registerRoutes(mux)
	return mux, ah, projStore
}

func putNaming(mux *http.ServeMux, cookie *http.Cookie, body any) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	req := httptest.NewRequest("PUT", "/api/v1/projects/api/naming", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestPutProjectNaming_PreviewPattern(t *testing.T) {
	mux, ah, store := newNamingMux(t)
	admin := sessionCookieFor(ah, "alice", "org_admin")

	rec := putNaming(mux, admin, ProjectNamingDTO{PreviewNamespacePattern: "{app}-{name}"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var got ProjectNamingDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.PreviewNamespacePattern != "{app}-{name}" {
		t.Errorf("response pattern = %q, want {app}-{name}", got.PreviewNamespacePattern)
	}
	// Persisted to the project spec.
	proj, _ := store.Get(context.Background(), "api")
	if proj.Spec.PreviewNamespacePattern != "{app}-{name}" {
		t.Errorf("persisted pattern = %q, want {app}-{name}", proj.Spec.PreviewNamespacePattern)
	}
}

func TestPutProjectNaming_PreviewPatternSharedNamespace(t *testing.T) {
	mux, ah, store := newNamingMux(t)
	admin := sessionCookieFor(ah, "alice", "org_admin")

	// A pattern without {name} is a SHARED preview namespace — allowed; the
	// publisher suffixes platform resources per preview.
	rec := putNaming(mux, admin, ProjectNamingDTO{PreviewNamespacePattern: "{project}-previews"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for shared-namespace pattern (%s)", rec.Code, rec.Body.String())
	}
	proj, _ := store.Get(context.Background(), "api")
	if proj.Spec.PreviewNamespacePattern != "{project}-previews" {
		t.Errorf("persisted pattern = %q, want {project}-previews", proj.Spec.PreviewNamespacePattern)
	}
}

func TestPutProjectNaming_PreviewPatternRejectsUnknownToken(t *testing.T) {
	mux, ah, _ := newNamingMux(t)
	admin := sessionCookieFor(ah, "alice", "org_admin")

	rec := putNaming(mux, admin, ProjectNamingDTO{PreviewNamespacePattern: "{app}-{env}-{name}"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for unsupported {env} token", rec.Code)
	}
}

func TestGetProjectNaming_ReturnsPreviewPattern(t *testing.T) {
	mux, ah, store := newNamingMux(t)
	proj, _ := store.Get(context.Background(), "api")
	proj.Spec.PreviewNamespacePattern = "{project}-{app}-pr-{name}"
	_ = store.Save(context.Background(), proj)

	req := httptest.NewRequest("GET", "/api/v1/projects/api/naming", nil)
	req.AddCookie(sessionCookieFor(ah, "carol", "viewer"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var got ProjectNamingDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.PreviewNamespacePattern != "{project}-{app}-pr-{name}" {
		t.Errorf("pattern = %q, want {project}-{app}-pr-{name}", got.PreviewNamespacePattern)
	}
}
