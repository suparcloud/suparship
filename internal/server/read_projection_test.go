package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/runtime"
	"github.com/suparcloud/suparship/internal/session"
)

func manualProjection() *readProjection { p := newReadProjection(); p.active = true; return p }
func projectionGet(h http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w
}
func refreshView(p *readProjection, key string) {
	p.mu.Lock()
	v := p.entries[key]
	generation := v.generation
	p.mu.Unlock()
	p.refresh(context.Background(), v, generation)
}

func TestReadProjectionColdWarmFailureDeletionAndIsolation(t *testing.T) {
	p := manualProjection()
	calls := 0
	status := 200
	h := p.wrap(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `{"path":%q,"env":%q}`, r.URL.Path, r.URL.Query().Get("env"))
	})
	if got := projectionGet(h, "/a?env=prod"); got.Code != 503 || got.Header().Get("X-Read-State") != "synchronizing" {
		t.Fatal(got)
	}
	if calls != 0 {
		t.Fatal("cold read called upstream")
	}
	refreshView(p, "/a?env=prod")
	for i := 0; i < 50; i++ {
		if got := projectionGet(h, "/a?env=prod"); got.Code != 200 || !strings.Contains(got.Body.String(), `"env":"prod"`) {
			t.Fatal(got)
		}
	}
	if calls != 1 {
		t.Fatalf("warm reads called upstream %d times", calls)
	}
	if got := projectionGet(h, "/a?env=staging"); got.Code != 503 {
		t.Fatal("query scope leak")
	}
	if got := projectionGet(h, "/b"); got.Code != 503 {
		t.Fatal("resource scope leak")
	}
	status = 500
	refreshView(p, "/a?env=prod")
	got := projectionGet(h, "/a?env=prod")
	if got.Code != 200 || got.Header().Get("X-Read-State") != "stale" {
		t.Fatal("lost last good view", got)
	}
	status = 404
	refreshView(p, "/a?env=prod")
	if got := projectionGet(h, "/a?env=prod"); got.Code != 404 {
		t.Fatal("deleted resource retained")
	}
}

func TestReadProjectionInvalidationFencesOldRefresh(t *testing.T) {
	p := manualProjection()
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	h := p.wrap(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		_, _ = w.Write([]byte(`{"old":true}`))
	})
	projectionGet(h, "/a")
	go func() { refreshView(p, "/a"); close(done) }()
	<-entered
	p.invalidate()
	close(release)
	<-done
	if got := projectionGet(h, "/a"); got.Code != 503 {
		t.Fatal("pre-write refresh committed")
	}
}

func TestReadProjectionWorkersCancelAndWarmReadsIgnoreBlockedSource(t *testing.T) {
	p := newReadProjection()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.start(ctx)
	var calls atomic.Int32
	entered := make(chan struct{})
	finished := make(chan struct{})
	h := p.wrap(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) > 1 {
			close(entered)
			<-r.Context().Done()
			close(finished)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	projectionGet(h, "/a")
	deadline := time.Now().Add(3 * time.Second)
	for projectionGet(h, "/a").Code != 200 {
		if time.Now().After(deadline) {
			t.Fatal("worker never synchronized")
		}
		time.Sleep(time.Millisecond)
	}
	p.invalidate()
	<-entered
	started := time.Now()
	for i := 0; i < 100; i++ {
		if projectionGet(h, "/a").Code != 200 {
			t.Fatal("warm view unavailable")
		}
	}
	if time.Since(started) > 300*time.Millisecond {
		t.Fatal("warm reads blocked on source")
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("worker did not cancel")
	}
}

func TestReadProjectionAuthorizationRunsBeforeReadAndWatch(t *testing.T) {
	p := manualProjection()
	mux := http.NewServeMux()
	auth := &authHandler{authenticator: &fakeAuthenticator{username: "admin", password: "pass"}, sessions: session.NewStore(time.Hour)}
	auth.registerRoutes(mux)
	store := newMemAppStore()
	store.addApp(&domain.App{Name: "hello", ProjectName: "demo"})
	rh := &rbacHandler{auth: auth, orgStore: &staticOrgProvider{org: testRBACOrg()}, appHandler: newAppHandler(store, nil, nil, nil), reads: p}
	rh.registerRoutes(mux)
	path := "/api/v1/projects/demo/apps"
	// Populate directly to prove a cached result never bypasses route auth.
	v := &readView{request: httptest.NewRequest("GET", path, nil), loader: rh.appHandler.handleListApps, changed: make(chan struct{})}
	v.request.SetPathValue("project", "demo")
	p.entries[path] = v
	refreshView(p, path)
	for _, suffix := range []string{"", "?watch=1"} {
		if got := projectionGet(mux, path+suffix); got.Code != 401 {
			t.Fatalf("unauthenticated request returned %d", got.Code)
		}
	}
}

func TestReadProjectionRevokedAccessAndFailedMutation(t *testing.T) {
	p := manualProjection()
	mux := http.NewServeMux()
	auth := &authHandler{sessions: session.NewStore(time.Hour)}
	org := testRBACOrg()
	rh := &rbacHandler{auth: auth, orgStore: &staticOrgProvider{org: org}}
	mux.HandleFunc("GET /api/v1/projects/{project}/apps", rh.requireRole("viewer", ProjectFromPathValue("project"))(p.wrap(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" || sessionFromContext(r.Context()) != nil {
			t.Error("identity retained in shared view")
		}
		_, _ = w.Write([]byte(`{"apps":[]}`))
	})))
	cookie := sessionCookieFor(auth, "carol", "viewer")
	path := "/api/v1/projects/demo/apps"
	request := func(suffix string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path+suffix, nil)
		req.AddCookie(cookie)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	request("")
	refreshView(p, path)
	if request("").Code != 200 {
		t.Fatal("authorized warm read failed")
	}
	org.RoleBindings = nil
	for _, suffix := range []string{"", "?watch=1"} {
		if got := request(suffix); got.Code != 403 {
			t.Fatalf("revoked access returned %d", got.Code)
		}
	}
	mutation := p.mutations(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	mutation.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/v1/projects/demo/apps", nil))
	if p.entries[path].failed {
		t.Fatal("unauthorized write invalidated shared views")
	}
}

func TestReadProjectionSSEAndReconnect(t *testing.T) {
	p := manualProjection()
	h := p.wrap(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"ok":true}`)) })
	projectionGet(h, "/a")
	refreshView(p, "/a")
	server := httptest.NewServer(h)
	defer server.Close()
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/a?watch=1", nil)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		scanner := bufio.NewScanner(resp.Body)
		found := false
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "data:") {
				found = strings.Contains(scanner.Text(), `"state":"fresh"`)
				break
			}
		}
		cancel()
		resp.Body.Close()
		if !found {
			t.Fatal("no resync event on connection")
		}
	}
}

func TestReadProjectionCapacityAndOversizedViews(t *testing.T) {
	p := manualProjection()
	h := p.wrap(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", projectionMaxBytes+1)))
	})
	projectionGet(h, "/a")
	refreshView(p, "/a")
	if got := projectionGet(h, "/a"); got.Code != 503 {
		t.Fatal("oversized payload accepted")
	}
	for i := 1; i < projectionCapacity; i++ {
		projectionGet(h, fmt.Sprintf("/%d", i))
	}
	if got := projectionGet(h, "/overflow"); got.Code != 503 || got.Header().Get("Retry-After") != "5" {
		t.Fatal("capacity not bounded")
	}
}

// Declared fixture: 100 apps, three environments each; simulated upstream
// latency 10ms per runtime read; concurrency 16; 1,600 loopback HTTP requests
// per endpoint. Warm requests include real session and role middleware backed
// by in-memory authority. No production-cluster latency is implied.
type latencyRuntime struct{}

func (latencyRuntime) GetServiceRuntime(ctx context.Context, _, _ string) (*runtime.RuntimeInfo, error) {
	timer := time.NewTimer(10 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}
	return &runtime.RuntimeInfo{Status: runtime.StatusHealthy, Replicas: 1, Available: 1}, nil
}
func TestReadProjectionLatencyFixture(t *testing.T) {
	if testing.Short() {
		t.Skip("latency fixture")
	}
	store := newMemAppStore()
	for i := 0; i < 100; i++ {
		name := fmt.Sprintf("app-%03d", i)
		store.addApp(&domain.App{Name: name, ProjectName: "demo"})
		for _, env := range []string{"dev", "staging", "prod"} {
			store.addEnv(&domain.AppEnvironment{AppName: name, ProjectName: "demo", EnvName: env, Namespace: name + "-" + env})
		}
	}
	ah := newAppHandler(store, nil, nil, nil)
	ah.runtimeProvider = latencyRuntime{}
	p := manualProjection()
	mux := http.NewServeMux()
	auth := &authHandler{sessions: session.NewStore(time.Hour)}
	rh := &rbacHandler{auth: auth, orgStore: &staticOrgProvider{org: testRBACOrg()}, appHandler: ah, reads: p}
	rh.registerRoutes(mux)
	cookie := sessionCookieFor(auth, "carol", "viewer")
	server := httptest.NewServer(mux)
	defer server.Close()
	for _, kind := range []string{"list", "detail"} {
		t.Run(kind, func(t *testing.T) {
			path := "/api/v1/projects/demo/apps"
			loader := ah.handleListApps
			if kind == "detail" {
				path += "/app-000"
				loader = ah.handleGetApp
			}
			req := httptest.NewRequest("GET", path, nil)
			req.SetPathValue("project", "demo")
			req.SetPathValue("app", "app-000")
			baselineStart := time.Now()
			loader(httptest.NewRecorder(), req)
			baseline := time.Since(baselineStart)
			req.AddCookie(cookie)
			mux.ServeHTTP(httptest.NewRecorder(), req)
			begin := time.Now()
			refreshView(p, path)
			ingestion := time.Since(begin)
			if p.entries[path].status != 200 {
				t.Fatal("fixture failed to synchronize")
			}
			durations := make([]time.Duration, 1600)
			var wg sync.WaitGroup
			for worker := 0; worker < 16; worker++ {
				wg.Add(1)
				go func(worker int) {
					defer wg.Done()
					for i := worker * 100; i < (worker+1)*100; i++ {
						req, _ := http.NewRequest("GET", server.URL+path, nil)
						req.AddCookie(cookie)
						start := time.Now()
						resp, err := server.Client().Do(req)
						if err != nil {
							t.Error(err)
							return
						}
						_, _ = io.Copy(io.Discard, resp.Body)
						resp.Body.Close()
						durations[i] = time.Since(start)
						if resp.StatusCode != 200 {
							t.Errorf("status %d", resp.StatusCode)
						}
					}
				}(worker)
			}
			wg.Wait()
			sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
			p95 := durations[len(durations)*95/100]
			t.Logf("apps=100 envs=300 concurrency=16 requests=1600 baseline=%s p50=%s p95=%s p99=%s ingestion=%s", baseline, durations[800], p95, durations[1584], ingestion)
			if p95 >= 300*time.Millisecond {
				t.Fatalf("p95 %s exceeds 300ms", p95)
			}
		})
	}
}

func TestReadProjectionPeriodicObservationAndRestart(t *testing.T) {
	var value atomic.Int32
	loader := func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprintf(w, `{"value":%d}`, value.Load()) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := newReadProjection()
	p.start(ctx)
	h := p.wrap(loader)
	wait := func(expected string) {
		t.Helper()
		deadline := time.Now().Add(projectionBudget)
		for {
			got := projectionGet(h, "/a")
			if got.Code == 200 && got.Body.String() == expected {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("observation budget exceeded: %s", got.Body.String())
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	wait(`{"value":0}`)
	changed := time.Now()
	value.Store(1)
	wait(`{"value":1}`)
	t.Logf("periodic source-to-view observation lag=%s (budget %s)", time.Since(changed), projectionBudget)
	cancel()
	rebuilt := manualProjection()
	next := rebuilt.wrap(loader)
	if projectionGet(next, "/a").Code != 503 {
		t.Fatal("restart should begin cold")
	}
	refreshView(rebuilt, "/a")
	if got := projectionGet(next, "/a"); got.Body.String() != `{"value":1}` {
		t.Fatal("restart rebuild lost source state")
	}
}

func TestReadProjectionPinConfigurationSurvivesRuntimeFailure(t *testing.T) {
	p := manualProjection()
	pinned, unavailable := false, false
	h := p.wrap(func(w http.ResponseWriter, r *http.Request) {
		detail := AppDetailResponse{App: AppDetailDTO{Name: "app", Environments: []AppEnvironmentSummaryDTO{{EnvName: "staging"}}}}
		if pinned {
			detail.App.Environments[0].PinnedFrom = "pr-42"
			detail.App.Environments[0].PinnedTag = "preview-build"
		}
		configuration, _ := json.Marshal(detail)
		w.(*projectionResponse).configuration = configuration
		if unavailable {
			markProjectionFailure(r.Context(), context.DeadlineExceeded)
		} else {
			detail.App.Environments[0].Status = AppStatusSummaryDTO{Phase: "running", Available: 2}
			detail.App.Environments[0].Release = &AppReleaseRefDTO{Tag: "main-build"}
		}
		writeJSON(w, http.StatusOK, detail)
	})
	projectionGet(h, "/app")
	refreshView(p, "/app")
	observed := p.entries["/app"].observed
	pinned, unavailable = true, true
	p.invalidate()
	refreshView(p, "/app")
	got := projectionGet(h, "/app")
	var detail AppDetailResponse
	if err := json.Unmarshal(got.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	env := detail.App.Environments[0]
	if env.PinnedFrom != "pr-42" || env.PinnedTag != "preview-build" {
		t.Fatalf("saved pin hidden: %+v", env)
	}
	if env.Status.Available != 2 || env.Release.Tag != "main-build" {
		t.Fatalf("previous runtime observation lost: %+v", env)
	}
	if got.Header().Get("X-Read-State") != "stale" || !p.entries["/app"].observed.Equal(observed) {
		t.Fatal("stale runtime presented as fresh")
	}
	// Unpin must also become visible during the same outage.
	pinned = false
	p.invalidate()
	refreshView(p, "/app")
	got = projectionGet(h, "/app")
	if strings.Contains(got.Body.String(), "pinnedFrom") {
		t.Fatal("unpin hidden by failed runtime read")
	}
}
