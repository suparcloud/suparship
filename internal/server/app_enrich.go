package server

import (
	"context"
	"sync"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/rbac"
)

// This file holds the read-path performance machinery for app/env live-status
// enrichment: a per-request memo for the org + app lookups (so they run once
// instead of ~2×/env), a bounded-concurrency runner, and a short-TTL cache of
// enriched status. See handleListApps for how they compose.

// appEnrichConcurrency bounds how many (app, env) enrichments run at once. Each
// task issues live K8s + ArgoCD reads across the env's target clusters; a small
// pool keeps one slow cluster from serializing the whole page without stampeding
// the API servers. Mirrors stackPrepConcurrency (stack_lifecycle.go).
const appEnrichConcurrency = 8

// statusCacheTTL bounds how stale a cached list-row status may be. The single-app
// detail path always recomputes live and refreshes the entry, so only list/
// dashboard rows can be up to this old.
const statusCacheTTL = 10 * time.Second

// runBounded runs fn(0..n-1) concurrently with at most `limit` in flight, and
// returns once all have finished. fn must be safe for concurrent use across
// distinct indices. A nil/empty range is a no-op.
func runBounded(n, limit int, fn func(i int)) {
	if n <= 0 {
		return
	}
	if limit < 1 {
		limit = 1
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			fn(i)
		}(i)
	}
	wg.Wait()
}

// --- per-request org memo ---------------------------------------------------

type orgMemoKeyT struct{}

var orgMemoKey orgMemoKeyT

// orgMemo resolves the org config at most once per request. base is the request
// context so the resolve isn't bound to any single per-task timeout (a poisoned
// sync.Once would otherwise fail every env's enrichment on a transient blip).
type orgMemo struct {
	base context.Context
	once sync.Once
	org  *rbac.Org
	err  error
}

// withOrgMemo installs a request-scoped org memo. Call once at handler entry;
// the concurrent env fan-out below then shares a single GetOrg.
func withOrgMemo(ctx context.Context) context.Context {
	return context.WithValue(ctx, orgMemoKey, &orgMemo{base: ctx})
}

// orgOnce returns the org, resolving it once per request when a memo is
// installed. Without a memo (paths that never wrapped the ctx) it falls back to
// a direct read so behavior is unchanged.
func (ah *appHandler) orgOnce(ctx context.Context) (*rbac.Org, error) {
	if ah.orgProvider == nil {
		return nil, nil
	}
	m, ok := ctx.Value(orgMemoKey).(*orgMemo)
	if !ok {
		return ah.orgProvider.GetOrg(ctx)
	}
	m.once.Do(func() {
		rc, cancel := context.WithTimeout(m.base, liveReadTimeout)
		defer cancel()
		m.org, m.err = ah.orgProvider.GetOrg(rc)
	})
	return m.org, m.err
}

// --- per-request app memo ---------------------------------------------------

type appMemoKeyT struct{}

var appMemoKey appMemoKeyT

// appMemo dedupes app lookups within a request. handleListApps pre-seeds it with
// every app it already loaded, so the per-env diagnostics path resolves each
// app's per-env cluster targets without re-reading the store.
type appMemo struct {
	mu   sync.RWMutex
	apps map[string]*domain.App
}

func withAppMemo(ctx context.Context) context.Context {
	return context.WithValue(ctx, appMemoKey, &appMemo{apps: map[string]*domain.App{}})
}

func appMemoFrom(ctx context.Context) *appMemo {
	m, _ := ctx.Value(appMemoKey).(*appMemo)
	return m
}

func (m *appMemo) seed(project string, app *domain.App) {
	if m == nil || app == nil {
		return
	}
	m.mu.Lock()
	m.apps[project+"\x00"+app.Name] = app
	m.mu.Unlock()
}

// appOnce returns the app, preferring a seeded/cached copy so the enrichment
// fan-out doesn't re-read the store per env. Without a memo it reads directly.
func (ah *appHandler) appOnce(ctx context.Context, project, name string) (*domain.App, error) {
	key := project + "\x00" + name
	if m := appMemoFrom(ctx); m != nil {
		m.mu.RLock()
		a, ok := m.apps[key]
		m.mu.RUnlock()
		if ok {
			return a, nil
		}
		a2, err := ah.appStore.GetApp(ctx, project, name)
		if err == nil && a2 != nil {
			m.mu.Lock()
			m.apps[key] = a2
			m.mu.Unlock()
		}
		return a2, err
	}
	return ah.appStore.GetApp(ctx, project, name)
}

// --- per-request ArgoCD diagnostics snapshot -------------------------------

// diagnosticsSnapshotter is the optional batch capability of an
// AppDiagnosticsReader: list every ArgoCD Application once and return a lookup
// closure, so a page's diagnostics cost one LIST instead of N×2×C live Gets. A
// reader that doesn't implement it (e.g. a test fake) falls back to per-app Gets.
type diagnosticsSnapshotter interface {
	SnapshotAppDiagnostics(ctx context.Context) (func(argoAppName, source string) []domain.Diagnostic, error)
}

type diagSnapKeyT struct{}

var diagSnapKey diagSnapKeyT

// diagSnapMemo builds the diagnostics snapshot at most once per request. base is
// the request context so the LIST isn't bound to a per-task timeout.
type diagSnapMemo struct {
	base   context.Context
	once   sync.Once
	lookup func(argoAppName, source string) []domain.Diagnostic
	ok     bool
}

func withDiagSnap(ctx context.Context) context.Context {
	return context.WithValue(ctx, diagSnapKey, &diagSnapMemo{base: ctx})
}

// diagLookup returns a per-request snapshot lookup and true when a memo is
// installed, the reader supports snapshots, and the single LIST succeeded.
// Otherwise it returns (nil, false) and the caller uses per-app live Gets — so
// un-memoized callers and LIST failures behave exactly as before.
func (ah *appHandler) diagLookup(ctx context.Context) (func(string, string) []domain.Diagnostic, bool) {
	m, ok := ctx.Value(diagSnapKey).(*diagSnapMemo)
	if !ok {
		return nil, false
	}
	sn, ok := ah.diagnosticsReader.(diagnosticsSnapshotter)
	if !ok {
		return nil, false
	}
	m.once.Do(func() {
		rc, cancel := context.WithTimeout(m.base, liveReadTimeout)
		defer cancel()
		if lookup, err := sn.SnapshotAppDiagnostics(rc); err == nil {
			m.lookup, m.ok = lookup, true
		}
	})
	return m.lookup, m.ok
}

// withEnrichMemos installs the org, app, and diagnostics-snapshot memos shared by
// the concurrent env fan-out. Call once at handler entry.
func withEnrichMemos(ctx context.Context) context.Context {
	return withDiagSnap(withAppMemo(withOrgMemo(ctx)))
}

// --- short-TTL live-status cache -------------------------------------------

// statusSnapshot holds exactly the env fields that live enrichment overwrites,
// so a cache hit can reproduce a recent enrichment without hitting K8s/ArgoCD.
type statusSnapshot struct {
	status  domain.AppRuntimeStatus
	urls    []string
	release *domain.AppReleaseRef
}

// snapshotEnv captures the post-enrichment state of env. Slices/pointers are
// deep-copied so a cached snapshot never aliases live request state.
func snapshotEnv(env *domain.AppEnvironment) statusSnapshot {
	return statusSnapshot{
		status:  env.Status,
		urls:    env.URLs,
		release: env.Release,
	}.clone()
}

func (s statusSnapshot) clone() statusSnapshot {
	out := statusSnapshot{status: s.status}
	if s.status.Diagnostics != nil {
		out.status.Diagnostics = append([]domain.Diagnostic(nil), s.status.Diagnostics...)
	}
	if s.urls != nil {
		out.urls = append([]string(nil), s.urls...)
	}
	if s.release != nil {
		r := *s.release
		out.release = &r
	}
	return out
}

// applyTo writes the snapshot's status/urls/release onto env, reproducing the
// effect of a fresh enrichment.
func (s statusSnapshot) applyTo(env *domain.AppEnvironment) {
	c := s.clone()
	env.Status = c.status
	env.URLs = c.urls
	env.Release = c.release
}

type statusCacheEntry struct {
	snap      statusSnapshot
	expiresAt time.Time
}

// statusCache is a small, concurrency-safe TTL cache of enriched env status,
// keyed by (project, app, env). A nil *statusCache is a valid no-op cache, so
// handlers constructed without one (tests) simply skip caching.
type statusCache struct {
	ttl     time.Duration
	mu      sync.RWMutex
	entries map[string]statusCacheEntry
}

func newStatusCache(ttl time.Duration) *statusCache {
	return &statusCache{ttl: ttl, entries: map[string]statusCacheEntry{}}
}

func statusCacheKey(project, app, env string) string {
	return project + "\x00" + app + "\x00" + env
}

func (c *statusCache) get(key string) (statusSnapshot, bool) {
	if c == nil {
		return statusSnapshot{}, false
	}
	c.mu.RLock()
	e, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok || time.Now().After(e.expiresAt) {
		return statusSnapshot{}, false
	}
	return e.snap.clone(), true
}

func (c *statusCache) put(key string, snap statusSnapshot) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.entries[key] = statusCacheEntry{snap: snap.clone(), expiresAt: time.Now().Add(c.ttl)}
	c.mu.Unlock()
}

// --- enrichment entry points ------------------------------------------------

// enrichEnvBounded runs live enrichment with a per-call timeout so one slow or
// unreachable cluster can't stall the whole request into a gateway timeout.
func (ah *appHandler) enrichEnvBounded(ctx context.Context, appName string, env *domain.AppEnvironment) {
	tctx, cancel := context.WithTimeout(ctx, liveReadTimeout)
	defer cancel()
	ah.enrichEnvWithLiveStatus(tctx, appName, env)
}

// enrichAndCache enriches live and refreshes the cache entry. Used by the
// single-app/single-env detail paths, which must always reflect current state
// while still warming the cache for list views.
func (ah *appHandler) enrichAndCache(ctx context.Context, project, appName string, env *domain.AppEnvironment) {
	ah.enrichEnvBounded(ctx, appName, env)
	ah.statusCache.put(statusCacheKey(project, appName, env.EnvName), snapshotEnv(env))
}

// enrichCachedOrLive serves a recent cached snapshot when present, else enriches
// live and caches the result. Used by the list path (dashboard/project/stack),
// where ≤statusCacheTTL staleness on rows is acceptable for the speedup.
func (ah *appHandler) enrichCachedOrLive(ctx context.Context, project, appName string, env *domain.AppEnvironment) {
	key := statusCacheKey(project, appName, env.EnvName)
	if snap, ok := ah.statusCache.get(key); ok {
		snap.applyTo(env)
		return
	}
	ah.enrichEnvBounded(ctx, appName, env)
	ah.statusCache.put(key, snapshotEnv(env))
}
