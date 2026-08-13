package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/session"
)

// An update-app save (the endpoint behind manage-components and values saves)
// opts into accept-and-poll: ?async=1 returns 202 + a task id immediately, the
// save + publish run in the background, and the polled terminal result carries
// the same updateAppResponse the synchronous call returns.
func TestAsyncUpdateAppAcceptAndPoll(t *testing.T) {
	var wg sync.WaitGroup
	runner := newAsyncRunner(context.Background(), &wg)

	mux := http.NewServeMux()
	authH := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	authH.registerRoutes(mux)

	store := newMemAppStore()
	store.mu.Lock()
	store.apps[testProject] = make(map[string]*domain.App)
	store.mu.Unlock()

	appH := newAppHandler(store, nil, nil, nil)
	appH.gitOpsPublisher = &recordingPublisher{}
	appH.async = runner
	rh := &rbacHandler{
		auth:       authH,
		orgStore:   &staticOrgProvider{org: testRBACOrg()},
		appHandler: appH,
	}
	rh.registerRoutes(mux)

	store.addApp(promoteTestApp(testProject))
	cookie := sessionCookieFor(authH, "alice", "org_admin")

	body, _ := json.Marshal(map[string]any{"previewsEnabled": false})
	req := httptest.NewRequest(http.MethodPatch,
		"/api/v1/projects/"+testProject+"/apps/my-app?async=1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	var acc acceptedResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &acc); err != nil || acc.TaskID == "" {
		t.Fatalf("202 must carry a taskId, got %s (err %v)", rec.Body.String(), err)
	}

	wg.Wait() // background save + publish finished

	sreq := httptest.NewRequest(http.MethodGet,
		"/api/v1/projects/"+testProject+"/tasks/"+acc.TaskID, nil)
	sreq.AddCookie(cookie)
	srec := httptest.NewRecorder()
	mux.ServeHTTP(srec, sreq)
	if srec.Code != http.StatusOK {
		t.Fatalf("task poll = %d: %s", srec.Code, srec.Body.String())
	}
	var task asyncTask
	if err := json.Unmarshal(srec.Body.Bytes(), &task); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	if task.State != asyncSucceeded || task.Status != http.StatusOK {
		t.Fatalf("task = %s/%d, want succeeded/200 (err %q)", task.State, task.Status, task.Error)
	}
	res, _ := task.Result.(map[string]any)
	if res["app"] == nil {
		t.Errorf("terminal result must carry the app detail, got %v", task.Result)
	}

	app, _ := store.GetApp(context.Background(), testProject, "my-app")
	if app.Spec.PreviewsEnabled != false {
		t.Error("previewsEnabled=false must be persisted by the background op")
	}
}
