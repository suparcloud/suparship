package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

const projectionInterval = 5 * time.Second
const projectionBudget = 15 * time.Second
const projectionTimeout = 8 * time.Second
const projectionCapacity = 1024
const projectionMaxBytes = 2 << 20
const projectionMemoryBudget = 64 << 20

// readProjection contains only disposable, identity-independent read views.
// Wrap MUST be inside authorization middleware. Never wrap secret-value reads
// or handlers whose output depends on the authenticated user.
type readProjection struct {
	mu      sync.Mutex
	entries map[string]*readView
	active  bool
	bytes   int
	wake    chan struct{}
}
type readView struct {
	request    *http.Request
	loader     http.HandlerFunc
	body       []byte
	status     int
	observed   time.Time
	attempted  time.Time
	touched    time.Time
	failed     bool
	running    bool
	generation uint64
	revision   string
	changed    chan struct{}
}

func newReadProjection() *readProjection {
	return &readProjection{entries: make(map[string]*readView), wake: make(chan struct{}, 1)}
}

func (p *readProjection) start(ctx context.Context) {
	p.mu.Lock()
	if p.active {
		p.mu.Unlock()
		return
	}
	p.active = true
	p.mu.Unlock()
	for i := 0; i < 8; i++ {
		go p.worker(ctx)
	}
}

func (p *readProjection) worker(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-p.wake:
		}
		for ctx.Err() == nil {
			p.mu.Lock()
			var chosen *readView
			now := time.Now()
			for key, v := range p.entries {
				if !v.running && now.Sub(v.touched) > 5*time.Minute {
					p.bytes -= len(v.body)
					delete(p.entries, key)
					continue
				}
				if !v.running && now.Sub(v.attempted) >= projectionInterval && (chosen == nil || v.attempted.Before(chosen.attempted)) {
					chosen = v
				}
			}
			if chosen == nil {
				p.mu.Unlock()
				break
			}
			chosen.running = true
			chosen.attempted = now
			generation := chosen.generation
			p.mu.Unlock()
			p.refresh(ctx, chosen, generation)
		}
	}
}

// projectionResponse enforces the per-view memory limit even while loading.
type projectionResponse struct {
	header   http.Header
	body     bytes.Buffer
	status   int
	overflow bool
}

func (w *projectionResponse) Header() http.Header { return w.header }
func (w *projectionResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *projectionResponse) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.body.Len()+len(b) > projectionMaxBytes {
		w.overflow = true
		return 0, fmt.Errorf("projection too large")
	}
	return w.body.Write(b)
}

func (p *readProjection) refresh(parent context.Context, v *readView, generation uint64) {
	ctx, cancel := context.WithTimeout(parent, projectionTimeout)
	defer cancel()
	failure := &atomic.Bool{}
	ctx = context.WithValue(ctx, projectionFailureKey{}, failure)
	w := &projectionResponse{header: make(http.Header)}
	v.loader(w, v.request.Clone(ctx))
	p.mu.Lock()
	defer p.mu.Unlock()
	v.running = false
	if generation != v.generation {
		v.attempted = time.Time{}
		return
	}
	if p.bytes-len(v.body)+w.body.Len() > projectionMemoryBudget || (failure.Load() && v.status != 0) || ctx.Err() != nil || w.overflow || (w.status != http.StatusOK && w.status != http.StatusNotFound) {
		v.failed = true
	} else {
		p.bytes += w.body.Len() - len(v.body)
		v.body = append([]byte(nil), w.body.Bytes()...)
		v.status = w.status
		v.observed = time.Now()
		v.failed = failure.Load()
		v.revision = fmt.Sprintf("%x", sha256.Sum256(append([]byte(fmt.Sprint(w.status)), v.body...)))
	}
	close(v.changed)
	v.changed = make(chan struct{})
}

func (p *readProjection) invalidate() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, v := range p.entries {
		v.generation++
		v.failed = true
		v.attempted = time.Time{}
		close(v.changed)
		v.changed = make(chan struct{})
	}
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *readProjection) mutations(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" || r.Method == "HEAD" || r.Method == "OPTIONS" || strings.HasPrefix(r.URL.Path, "/api/v1/auth/") {
			next.ServeHTTP(w, r)
			return
		}
		recorder := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		if recorder.status >= 200 && recorder.status < 400 {
			p.invalidate()
		}
	})
}

func (p *readProjection) wrap(next http.HandlerFunc) http.HandlerFunc {
	if p == nil {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		if !p.active {
			p.mu.Unlock()
			next(w, r)
			return
		}
		// Preserve the cluster configuration environment selector. App list requests
		// (brief/stack) share the complete project projection to deduplicate reads.
		query := make(url.Values)
		if env := r.URL.Query().Get("env"); env != "" {
			query.Set("env", env)
		}
		key := r.URL.EscapedPath()
		if len(query) > 0 {
			key += "?" + query.Encode()
		}
		v := p.entries[key]
		if v == nil {
			if len(p.entries) >= projectionCapacity {
				p.mu.Unlock()
				w.Header().Set("Retry-After", "5")
				writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "Read view capacity reached; retry shortly"})
				return
			}
			// Preserve routing, stripping credentials, identity and nonsemantic input.
			req := r.Clone(context.Background())
			req.Header = make(http.Header)
			req.Body = nil
			req.GetBody = nil
			req.Form = nil
			req.PostForm = nil
			req.MultipartForm = nil
			req.Trailer = nil
			req.TLS = nil
			req.RemoteAddr = ""
			req.URL = &url.URL{Path: r.URL.Path, RawPath: r.URL.RawPath, RawQuery: query.Encode()}
			req.RequestURI = req.URL.RequestURI()
			v = &readView{request: req, loader: next, changed: make(chan struct{})}
			p.entries[key] = v
		}
		v.touched = time.Now()
		p.mu.Unlock()
		select {
		case p.wake <- struct{}{}:
		default:
		}
		if r.URL.Query().Get("watch") == "1" {
			p.watch(w, r, v)
			return
		}
		p.mu.Lock()
		status, body := v.status, v.body
		freshness, observed, revision := viewFreshness(v)
		p.mu.Unlock()
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Read-State", freshness)
		w.Header().Set("X-Read-Observed-At", observed)
		w.Header().Set("X-Read-Revision", revision)
		if status == 0 {
			w.Header().Set("Retry-After", "1")
			message := "Synchronizing view; retry shortly"
			if freshness == "unavailable" {
				message = "Unable to synchronize view: source unavailable or read-view capacity exceeded. Retry shortly; check server capacity and upstream connectivity."
			}
			writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: message})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}
}

func viewFreshness(v *readView) (string, string, string) {
	state := "fresh"
	if v.status == 0 {
		state = "synchronizing"
		if v.failed {
			state = "unavailable"
		}
	} else if v.failed || time.Since(v.observed) > projectionBudget {
		state = "stale"
	}
	observed := ""
	if !v.observed.IsZero() {
		observed = v.observed.UTC().Format(time.RFC3339Nano)
	}
	return state, observed, v.revision
}

// Short streams enforce authorization again on reconnect. Events are hints:
// clients always GET the current view, including after a missed event/restart.
func (p *readProjection) watch(w http.ResponseWriter, r *http.Request, v *readView) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	last := ""
	for {
		p.mu.Lock()
		state, observed, revision := viewFreshness(v)
		changed := v.changed
		p.mu.Unlock()
		value := fmt.Sprintf(`{"state":%q,"observedAt":%q,"revision":%q}`, state, observed, revision)
		if value != last {
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := fmt.Fprintf(w, "retry: 1000\nevent: view\ndata: %s\n\n", value); err != nil {
				return
			}
			flusher.Flush()
			last = value
		}
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			return
		case <-changed:
		case <-ticker.C:
		}
	}
}

type projectionFailureKey struct{}

func markProjectionFailure(ctx context.Context, causes ...error) {
	if failed, ok := ctx.Value(projectionFailureKey{}).(*atomic.Bool); ok {
		if failed.CompareAndSwap(false, true) && len(causes) > 0 {
			slog.Debug("read projection source failed", "error", causes[0])
		}
	}
}

// Keep legacy direct-handler status behavior; projected reads require a typed
// absence before removing a previously known resource.
func projectionNotFoundStatus(ctx context.Context, err error) int {
	var missing *domain.NotFoundError
	if ctx.Value(projectionFailureKey{}) != nil && !errors.As(err, &missing) && !apierrors.IsNotFound(err) {
		return http.StatusServiceUnavailable
	}
	return http.StatusNotFound
}
