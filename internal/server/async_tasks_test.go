package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestWantAsync(t *testing.T) {
	cases := []struct {
		name   string
		query  string
		prefer string
		want   bool
	}{
		{"default sync", "", "", false},
		{"query 1", "async=1", "", true},
		{"query true", "async=true", "", true},
		{"query false", "async=false", "", false},
		{"prefer header", "", "respond-async", true},
		{"prefer header mixed case", "", "Respond-Async, wait=10", true},
		{"unrelated prefer", "", "wait=10", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/x?"+c.query, nil)
			if c.prefer != "" {
				req.Header.Set("Prefer", c.prefer)
			}
			if got := wantAsync(req); got != c.want {
				t.Errorf("wantAsync = %v, want %v", got, c.want)
			}
		})
	}
}

// The default (no opt-in) path runs the op synchronously and writes its result
// inline — so the web UI and existing sync callers are unaffected.
func TestDispatchOpSyncDefault(t *testing.T) {
	op := func(context.Context) (int, any, error) {
		return http.StatusOK, map[string]string{"message": "pinned", "imageTag": "v1"}, nil
	}
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	w := httptest.NewRecorder()
	dispatchOp(w, req, nil, "pin-app", "demo", op) // async nil → always sync

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["imageTag"] != "v1" {
		t.Errorf("body = %v, want the op's result inline", body)
	}
}

// A sync op error is written with the op's status code and error body.
func TestDispatchOpSyncError(t *testing.T) {
	op := func(context.Context) (int, any, error) {
		return http.StatusConflict, nil, errors.New("boom")
	}
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	w := httptest.NewRecorder()
	dispatchOp(w, req, nil, "pin-app", "demo", op)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
}

// A nil result with a 2xx status writes just the status code, no JSON body —
// preserving the 204 No Content contract of the delete-preview endpoints.
func TestDispatchOpNoBody204(t *testing.T) {
	op := func(context.Context) (int, any, error) {
		return http.StatusNoContent, nil, nil
	}
	req := httptest.NewRequest(http.MethodDelete, "/x", nil)
	w := httptest.NewRecorder()
	dispatchOp(w, req, nil, "preview-app-delete", "demo", op)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Errorf("204 must have an empty body, got %q", w.Body.String())
	}
}

// An async no-body op records a terminal 204 the poller can read.
func TestAsyncNoBodyRecords204(t *testing.T) {
	var wg sync.WaitGroup
	runner := newAsyncRunner(context.Background(), &wg)
	op := func(context.Context) (int, any, error) {
		return http.StatusNoContent, nil, nil
	}
	req := httptest.NewRequest(http.MethodDelete, "/x?async=1", nil)
	w := httptest.NewRecorder()
	dispatchOp(w, req, runner, "preview-app-delete", "demo", op)
	wg.Wait()

	var acc acceptedResponse
	json.Unmarshal(w.Body.Bytes(), &acc)
	task, ok := runner.store.get(acc.TaskID)
	if !ok || task.State != asyncSucceeded || task.Status != http.StatusNoContent || task.Result != nil {
		t.Errorf("no-body task = %+v (ok=%v)", task, ok)
	}
}

// The async path returns 202 + a task id, runs the op in the background, and the
// status endpoint then reports the terminal result — the same payload the sync
// call would have produced.
func TestAsyncPinAcceptAndPoll(t *testing.T) {
	var wg sync.WaitGroup
	runner := newAsyncRunner(context.Background(), &wg)

	op := func(context.Context) (int, any, error) {
		return http.StatusOK, map[string]string{"message": "pinned", "imageTag": "v1"}, nil
	}
	req := httptest.NewRequest(http.MethodPost, "/x?async=1", nil)
	w := httptest.NewRecorder()
	dispatchOp(w, req, runner, "pin-app", "demo", op)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}
	var acc acceptedResponse
	if err := json.Unmarshal(w.Body.Bytes(), &acc); err != nil {
		t.Fatalf("decode 202: %v", err)
	}
	if acc.TaskID == "" || acc.StatusURL == "" {
		t.Fatalf("202 must carry a taskId + statusUrl, got %+v", acc)
	}

	wg.Wait() // background op finished

	rh := &rbacHandler{appHandler: &appHandler{async: runner}}
	sreq := httptest.NewRequest(http.MethodGet, acc.StatusURL, nil)
	sreq.SetPathValue("project", "demo")
	sreq.SetPathValue("taskId", acc.TaskID)
	sw := httptest.NewRecorder()
	rh.handleGetTask(sw, sreq)

	if sw.Code != http.StatusOK {
		t.Fatalf("status endpoint = %d, want 200", sw.Code)
	}
	var task asyncTask
	if err := json.Unmarshal(sw.Body.Bytes(), &task); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	if task.State != asyncSucceeded {
		t.Errorf("state = %q, want succeeded", task.State)
	}
	if task.Status != http.StatusOK {
		t.Errorf("terminal status = %d, want 200", task.Status)
	}
	res, _ := task.Result.(map[string]any)
	if res["imageTag"] != "v1" {
		t.Errorf("result = %v, want the op's payload preserved", task.Result)
	}
}

// A failed async op records state=failed, the error's status, and the message.
func TestAsyncPinFailureRecorded(t *testing.T) {
	var wg sync.WaitGroup
	runner := newAsyncRunner(context.Background(), &wg)
	op := func(context.Context) (int, any, error) {
		return http.StatusUnprocessableEntity, nil, errors.New("no such preview")
	}
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set("Prefer", "respond-async")
	w := httptest.NewRecorder()
	dispatchOp(w, req, runner, "pin-stack", "demo", op)
	wg.Wait()

	var acc acceptedResponse
	json.Unmarshal(w.Body.Bytes(), &acc)
	task, ok := runner.store.get(acc.TaskID)
	if !ok {
		t.Fatal("task not found")
	}
	if task.State != asyncFailed || task.Status != http.StatusUnprocessableEntity || task.Error != "no such preview" {
		t.Errorf("failed task = %+v", task)
	}
}

// A task id for one project must not be readable via another project's path.
func TestGetPinTaskCrossProjectIsolation(t *testing.T) {
	var wg sync.WaitGroup
	runner := newAsyncRunner(context.Background(), &wg)
	task := runner.store.create("pin-app", "projA")
	rh := &rbacHandler{appHandler: &appHandler{async: runner}}

	sreq := httptest.NewRequest(http.MethodGet, "/x", nil)
	sreq.SetPathValue("project", "projB") // wrong project
	sreq.SetPathValue("taskId", task.ID)
	sw := httptest.NewRecorder()
	rh.handleGetTask(sw, sreq)
	if sw.Code != http.StatusNotFound {
		t.Errorf("cross-project read = %d, want 404", sw.Code)
	}
}

// Completed tasks are evicted after the TTL (checked lazily on the next create).
func TestAsyncStoreTTLEviction(t *testing.T) {
	s := newAsyncTaskStore(10 * time.Millisecond)
	old := s.create("pin-app", "demo")
	s.finish(old.ID, asyncSucceeded, http.StatusOK, nil, "")
	time.Sleep(20 * time.Millisecond)
	s.create("pin-app", "demo") // triggers eviction sweep
	if _, ok := s.get(old.ID); ok {
		t.Error("expected the completed task to be evicted after TTL")
	}
}
