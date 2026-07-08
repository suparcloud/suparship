package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/k8s"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/runtime"
)

// countingOrgProvider counts GetOrg calls so tests can assert the per-request
// memo collapses the fan-out's repeated reads into one.
type countingOrgProvider struct {
	org   *rbac.Org
	calls int64
}

func (p *countingOrgProvider) GetOrg(context.Context) (*rbac.Org, error) {
	atomic.AddInt64(&p.calls, 1)
	return p.org, nil
}

func (p *countingOrgProvider) SaveOrg(_ context.Context, org *rbac.Org) error {
	p.org = org
	return nil
}

// With a memo installed, many concurrent orgOnce callers trigger exactly one
// GetOrg. This is the fix for the ~2×/env uncached ConfigMap reads.
func TestOrgOnceMemoizesUnderConcurrency(t *testing.T) {
	prov := &countingOrgProvider{org: orgWithEnvs(rbac.OrgEnvironment{Name: "staging"})}
	ah := &appHandler{orgProvider: prov}
	ctx := withOrgMemo(context.Background())

	runBounded(64, 8, func(int) {
		if _, err := ah.orgOnce(ctx); err != nil {
			t.Errorf("orgOnce: %v", err)
		}
	})

	if got := atomic.LoadInt64(&prov.calls); got != 1 {
		t.Fatalf("GetOrg called %d times, want 1", got)
	}
}

// Without a memo (paths that never wrapped the ctx), orgOnce falls back to a
// direct read every time — behavior is unchanged for those callers.
func TestOrgOnceFallsBackWithoutMemo(t *testing.T) {
	prov := &countingOrgProvider{org: orgWithEnvs(rbac.OrgEnvironment{Name: "staging"})}
	ah := &appHandler{orgProvider: prov}

	for i := 0; i < 3; i++ {
		if _, err := ah.orgOnce(context.Background()); err != nil {
			t.Fatalf("orgOnce: %v", err)
		}
	}
	if got := atomic.LoadInt64(&prov.calls); got != 3 {
		t.Fatalf("GetOrg called %d times, want 3 (no memo)", got)
	}
}

// A DeployModeAll env still resolves to every target cluster when org reads go
// through the memo — parallelizing/memoizing must not change fan-out results.
func TestWorkloadClustersForEnvUnderMemo(t *testing.T) {
	pool := k8s.NewClusterClientPool(fakeKubeconfigGetter{have: map[string]bool{"c1": true, "c2": true}})
	prov := &countingOrgProvider{org: orgWithEnvs(
		rbac.OrgEnvironment{Name: "staging", ClusterRefs: []string{"c1", "c2"}, DeployMode: rbac.DeployModeAll},
	)}
	ah := &appHandler{clusterPool: pool, orgProvider: prov}
	ctx := withOrgMemo(context.Background())

	clients, _, routed := ah.workloadClustersForEnv(ctx, "", "", "staging")
	if !routed || len(clients) != 2 {
		t.Fatalf("all-mode fan-out: got %d clients (routed=%v), want 2", len(clients), routed)
	}
}

// A seeded app is returned without touching the store — proven by leaving
// appStore nil so any store read would panic.
func TestAppOnceUsesSeedWithoutStore(t *testing.T) {
	ah := &appHandler{} // nil appStore on purpose
	ctx := withAppMemo(context.Background())
	app := &domain.App{Name: "web"}
	appMemoFrom(ctx).seed("proj", app)

	got, err := ah.appOnce(ctx, "proj", "web")
	if err != nil {
		t.Fatalf("appOnce: %v", err)
	}
	if got != app {
		t.Fatalf("appOnce returned %v, want the seeded app", got)
	}
}

// The cache returns a hit within the TTL and an isolated copy (mutating the
// returned snapshot must not corrupt the cached entry), and a miss after expiry.
func TestStatusCacheHitIsolationAndExpiry(t *testing.T) {
	c := newStatusCache(50 * time.Millisecond)
	key := statusCacheKey("proj", "web", "staging")
	c.put(key, statusSnapshot{
		status: domain.AppRuntimeStatus{Phase: "healthy", Replicas: 2, Available: 2,
			Diagnostics: []domain.Diagnostic{{Title: "orig"}}},
		urls:    []string{"a.example.com"},
		release: &domain.AppReleaseRef{Image: "img:1"},
	})

	got, ok := c.get(key)
	if !ok {
		t.Fatal("expected a cache hit within TTL")
	}
	// Mutate the returned copy; the cache must be unaffected.
	got.status.Diagnostics[0].Title = "mutated"
	got.urls[0] = "evil"
	got.release.Image = "img:evil"

	again, _ := c.get(key)
	if again.status.Diagnostics[0].Title != "orig" || again.urls[0] != "a.example.com" || again.release.Image != "img:1" {
		t.Fatalf("cache entry was corrupted by a caller mutating the returned snapshot: %+v", again)
	}

	time.Sleep(60 * time.Millisecond)
	if _, ok := c.get(key); ok {
		t.Fatal("expected a miss after TTL expiry")
	}
}

// A nil *statusCache is a valid no-op cache (test handlers built as literals).
func TestStatusCacheNilSafe(t *testing.T) {
	var c *statusCache
	c.put(statusCacheKey("p", "a", "e"), statusSnapshot{})
	if _, ok := c.get(statusCacheKey("p", "a", "e")); ok {
		t.Fatal("nil cache must always miss")
	}
}

// recordingRuntime records which apps (serviceName == app name) live enrichment
// queried, so a test can assert ?stack= scopes enrichment to members.
type recordingRuntime struct {
	mu      sync.Mutex
	queried map[string]bool
}

func (r *recordingRuntime) GetServiceRuntime(_ context.Context, _, serviceName string) (*runtime.RuntimeInfo, error) {
	r.mu.Lock()
	r.queried[serviceName] = true
	r.mu.Unlock()
	return &runtime.RuntimeInfo{Status: runtime.StatusNotDeployed}, nil
}

// GET /apps?stack= enriches only the stack's members with live status, while
// still returning every app (names power the "add app" picker). This is the
// StackDetail over-fetch trim.
func TestListAppsStackFilterEnrichesMembersOnly(t *testing.T) {
	store := newMemAppStore()
	store.addApp(&domain.App{Name: "web", ProjectName: "demo", Spec: domain.AppSpec{Stack: "s1"}})
	store.addApp(&domain.App{Name: "worker", ProjectName: "demo"}) // not in the stack
	store.addEnv(&domain.AppEnvironment{ProjectName: "demo", AppName: "web", EnvName: "staging", Namespace: "web-staging"})
	store.addEnv(&domain.AppEnvironment{ProjectName: "demo", AppName: "worker", EnvName: "staging", Namespace: "worker-staging"})

	rec := &recordingRuntime{queried: map[string]bool{}}
	ah := &appHandler{appStore: store, runtimeProvider: rec, statusCache: newStatusCache(statusCacheTTL)}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/demo/apps?stack=s1", nil)
	req.SetPathValue("project", "demo")
	w := httptest.NewRecorder()
	ah.handleListApps(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !rec.queried["web"] {
		t.Error("member app 'web' should have been enriched")
	}
	if rec.queried["worker"] {
		t.Error("non-member app 'worker' must NOT be enriched under ?stack=")
	}

	// Both apps still come back — the picker needs every name.
	var resp AppListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Apps) != 2 {
		t.Fatalf("response listed %d apps, want 2 (both members and non-members)", len(resp.Apps))
	}

	// Without the filter, every app is enriched (dashboard/project behavior).
	rec2 := &recordingRuntime{queried: map[string]bool{}}
	ah.runtimeProvider = rec2
	ah.statusCache = newStatusCache(statusCacheTTL) // avoid the cached web/staging hit
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/projects/demo/apps", nil)
	req2.SetPathValue("project", "demo")
	ah.handleListApps(httptest.NewRecorder(), req2)
	if !rec2.queried["web"] || !rec2.queried["worker"] {
		t.Errorf("unfiltered list should enrich all apps; queried=%v", rec2.queried)
	}
}

// countingDiagReader implements both the base reader and the optional snapshot
// capability, counting each so a test can prove the batch path is taken.
type countingDiagReader struct {
	mu                  sync.Mutex
	getCalls, snapCalls int
}

func (c *countingDiagReader) GetAppDiagnostics(context.Context, string, string) ([]domain.Diagnostic, error) {
	c.mu.Lock()
	c.getCalls++
	c.mu.Unlock()
	return nil, nil
}

func (c *countingDiagReader) SnapshotAppDiagnostics(context.Context) (func(string, string) []domain.Diagnostic, error) {
	c.mu.Lock()
	c.snapCalls++
	c.mu.Unlock()
	return func(string, string) []domain.Diagnostic { return nil }, nil
}

// A list load builds the ArgoCD diagnostics snapshot exactly once for the whole
// request (via the memo + sync.Once) and never falls back to per-app Gets — even
// though every env's diagnostics run concurrently.
func TestListAppsUsesDiagnosticsSnapshotOncePerRequest(t *testing.T) {
	store := newMemAppStore()
	store.addApp(&domain.App{Name: "web", ProjectName: "demo"})
	store.addApp(&domain.App{Name: "worker", ProjectName: "demo"})
	store.addEnv(&domain.AppEnvironment{ProjectName: "demo", AppName: "web", EnvName: "staging", Namespace: "web-staging"})
	store.addEnv(&domain.AppEnvironment{ProjectName: "demo", AppName: "worker", EnvName: "staging", Namespace: "worker-staging"})

	diag := &countingDiagReader{}
	ah := &appHandler{appStore: store, diagnosticsReader: diag, statusCache: newStatusCache(statusCacheTTL)}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/demo/apps", nil)
	req.SetPathValue("project", "demo")
	ah.handleListApps(httptest.NewRecorder(), req)

	if diag.snapCalls != 1 {
		t.Errorf("SnapshotAppDiagnostics called %d times, want exactly 1 per request", diag.snapCalls)
	}
	if diag.getCalls != 0 {
		t.Errorf("GetAppDiagnostics called %d times, want 0 (snapshot path should be used)", diag.getCalls)
	}
}

// snapshotEnv → applyTo reproduces the enriched fields onto a fresh env, with
// no aliasing back to the source env.
func TestSnapshotRoundTrip(t *testing.T) {
	src := &domain.AppEnvironment{
		EnvName: "staging",
		URLs:    []string{"web.example.com"},
		Release: &domain.AppReleaseRef{Image: "img:1"},
		Status: domain.AppRuntimeStatus{Phase: "healthy", Replicas: 3, Available: 3,
			Diagnostics: []domain.Diagnostic{{Title: "d1"}}},
	}
	snap := snapshotEnv(src)

	dst := &domain.AppEnvironment{EnvName: "staging"}
	snap.applyTo(dst)
	if dst.Status.Phase != "healthy" || dst.Status.Replicas != 3 || len(dst.URLs) != 1 || dst.Release == nil || dst.Release.Image != "img:1" {
		t.Fatalf("applyTo did not reproduce status: %+v", dst)
	}

	// Mutating the source after snapshotting must not affect the applied copy.
	src.Status.Diagnostics[0].Title = "changed"
	src.URLs[0] = "changed"
	src.Release.Image = "changed"
	if dst.Status.Diagnostics[0].Title != "d1" || dst.URLs[0] != "web.example.com" || dst.Release.Image != "img:1" {
		t.Fatalf("snapshot aliased the source env: %+v", dst)
	}
}
