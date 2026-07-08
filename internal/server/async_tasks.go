package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"
)

// This file implements the accept-and-poll model for the slow pin/unpin
// endpoints. A caller opts in (Prefer: respond-async, or ?async=1); the request
// validates synchronously, the heavy git clone/commit/push runs on a
// server-tracked background goroutine, and the handler returns 202 with a task
// id. The caller (typically CI) polls GET .../pin-tasks/{taskId} for the
// terminal result — the exact payload the synchronous call would have returned.
// This avoids gateway 504s: the HTTP response no longer waits on git round-trips.

// asyncTaskState is the lifecycle of a deferred operation.
type asyncTaskState string

const (
	asyncPending   asyncTaskState = "pending"
	asyncRunning   asyncTaskState = "running"
	asyncSucceeded asyncTaskState = "succeeded"
	asyncFailed    asyncTaskState = "failed"
)

// asyncTaskTTL is how long a completed task stays queryable before eviction.
// Pins complete in seconds and CI polls promptly, so a generous window covers
// slow pollers without unbounded growth.
const asyncTaskTTL = 30 * time.Minute

// asyncTask is one accepted-and-deferred operation. Status/Result carry exactly
// what the synchronous handler would have written, so a poller gets the same
// status code and body it would have received inline.
type asyncTask struct {
	ID        string         `json:"id"`
	Kind      string         `json:"kind"`
	Project   string         `json:"project,omitempty"`
	State     asyncTaskState `json:"state"`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
	// Status is the terminal HTTP status the sync call would have returned
	// (0 until terminal).
	Status int `json:"status,omitempty"`
	// Result is the terminal success payload (nil until succeeded).
	Result any `json:"result,omitempty"`
	// Error is the terminal failure message (empty unless failed).
	Error string `json:"error,omitempty"`
}

// asyncOp is the deferred work. It returns the HTTP status + body the sync
// handler would have produced; a non-nil error marks the task failed and its
// status is the error's HTTP status.
type asyncOp func(ctx context.Context) (status int, result any, err error)

// asyncTaskStore is a concurrency-safe in-memory registry of tasks with lazy TTL
// eviction. A nil *asyncTaskStore is never used — handlers guard on the runner.
type asyncTaskStore struct {
	mu    sync.Mutex
	ttl   time.Duration
	tasks map[string]*asyncTask
}

func newAsyncTaskStore(ttl time.Duration) *asyncTaskStore {
	return &asyncTaskStore{ttl: ttl, tasks: map[string]*asyncTask{}}
}

func newTaskID() string {
	var b [12]byte
	// crypto/rand.Read never returns a short read / error in practice; ignoring
	// err here only risks a lower-entropy id, never a crash.
	_, _ = rand.Read(b[:])
	return "pintask_" + hex.EncodeToString(b[:])
}

func (s *asyncTaskStore) create(kind, project string) asyncTask {
	now := time.Now()
	t := &asyncTask{
		ID:        newTaskID(),
		Kind:      kind,
		Project:   project,
		State:     asyncPending,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.mu.Lock()
	s.evictExpiredLocked(now)
	s.tasks[t.ID] = t
	s.mu.Unlock()
	return *t
}

func (s *asyncTaskStore) setRunning(id string) {
	s.mu.Lock()
	if t := s.tasks[id]; t != nil {
		t.State = asyncRunning
		t.UpdatedAt = time.Now()
	}
	s.mu.Unlock()
}

func (s *asyncTaskStore) finish(id string, state asyncTaskState, status int, result any, errMsg string) {
	s.mu.Lock()
	if t := s.tasks[id]; t != nil {
		t.State = state
		t.Status = status
		t.Result = result
		t.Error = errMsg
		t.UpdatedAt = time.Now()
	}
	s.mu.Unlock()
}

// get returns a copy of the task so callers never race the background writer.
func (s *asyncTaskStore) get(id string) (asyncTask, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return asyncTask{}, false
	}
	return *t, true
}

// evictExpiredLocked drops terminal tasks older than the TTL. Caller holds mu.
func (s *asyncTaskStore) evictExpiredLocked(now time.Time) {
	for id, t := range s.tasks {
		if (t.State == asyncSucceeded || t.State == asyncFailed) && now.Sub(t.UpdatedAt) > s.ttl {
			delete(s.tasks, id)
		}
	}
}

// asyncRunner ties the task store to the server's background lifecycle: tasks run
// under baseCtx (NOT the request context, which is cancelled when the 202 is
// sent) and are tracked by wg so graceful shutdown waits for them.
type asyncRunner struct {
	store   *asyncTaskStore
	wg      *sync.WaitGroup
	baseCtx context.Context
}

func newAsyncRunner(baseCtx context.Context, wg *sync.WaitGroup) *asyncRunner {
	return &asyncRunner{store: newAsyncTaskStore(asyncTaskTTL), wg: wg, baseCtx: baseCtx}
}

// accept registers a task, launches op on a tracked goroutine, and writes 202
// with the ack id + poll URL.
func (a *asyncRunner) accept(w http.ResponseWriter, kind, project string, op asyncOp) {
	t := a.store.create(kind, project)
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.store.setRunning(t.ID)
		status, result, err := op(a.baseCtx)
		if err != nil {
			a.store.finish(t.ID, asyncFailed, status, errorResponse{Error: err.Error()}, err.Error())
			return
		}
		a.store.finish(t.ID, asyncSucceeded, status, result, "")
	}()
	writeJSON(w, http.StatusAccepted, acceptedResponse{
		TaskID:    t.ID,
		State:     string(t.State),
		StatusURL: taskStatusURL(project, t.ID),
		Message:   "accepted; poll statusUrl for the result",
	})
}

// acceptedResponse is the 202 body returned to an async caller.
type acceptedResponse struct {
	TaskID    string `json:"taskId"`
	State     string `json:"state"`
	StatusURL string `json:"statusUrl"`
	Message   string `json:"message,omitempty"`
}

func taskStatusURL(project, id string) string {
	return "/api/v1/projects/" + project + "/tasks/" + id
}

// wantAsync reports whether the caller opted into async handling, via the
// standard RFC 7240 "Prefer: respond-async" header or a ?async=1 query param.
func wantAsync(r *http.Request) bool {
	switch strings.ToLower(r.URL.Query().Get("async")) {
	case "1", "true", "yes":
		return true
	}
	for _, v := range r.Header.Values("Prefer") {
		if strings.Contains(strings.ToLower(v), "respond-async") {
			return true
		}
	}
	return false
}

// dispatchOp runs op synchronously (the default — sync callers and the web UI
// keep their inline 200/result), or accepts it for background execution and
// returns 202 when the caller opts in and an async runner is wired. Request
// validation must happen BEFORE this call so bad input still fails fast (400)
// even in async mode.
func dispatchOp(w http.ResponseWriter, r *http.Request, async *asyncRunner, kind, project string, op asyncOp) {
	if async != nil && wantAsync(r) {
		async.accept(w, kind, project, op)
		return
	}
	status, result, err := op(r.Context())
	if err != nil {
		writeJSON(w, status, errorResponse{Error: err.Error()})
		return
	}
	if result == nil {
		// No-body success (e.g. a 204 delete). Never write a JSON null body.
		w.WriteHeader(status)
		return
	}
	writeJSON(w, status, result)
}

// handleGetTask serves GET /api/v1/projects/{project}/tasks/{taskId} (and the
// legacy /pin-tasks/{taskId} alias). Project-scoped (viewProject RBAC) and
// cross-checked against the task's own project so a token for one project can't
// read another's task.
func (rh *rbacHandler) handleGetTask(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	id := r.PathValue("taskId")
	if rh.appHandler == nil || rh.appHandler.async == nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "async tasks not enabled"})
		return
	}
	t, ok := rh.appHandler.async.store.get(id)
	if !ok || t.Project != project {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "task not found: " + id})
		return
	}
	writeJSON(w, http.StatusOK, t)
}
