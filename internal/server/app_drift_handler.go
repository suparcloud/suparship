package server

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
)

// driftCacheTTL bounds how often the drift check renders + fetches for one
// app. The app page asks on every load; a check costs a repo fetch and a full
// render, so consecutive loads within the window share one answer. ?refresh=1
// bypasses it (the Re-check button, and after a re-publish).
const driftCacheTTL = 60 * time.Second

// AppDriftDTO is the body of GET /api/v1/projects/{project}/apps/{app}/gitops-drift.
type AppDriftDTO struct {
	// Drifted is true when at least one file differs.
	Drifted bool `json:"drifted"`
	// Files are the repo-relative paths that a publish from suparship's stored
	// state would change (the repo currently holds something else).
	Files []string `json:"files"`
	// CheckedAt is when the comparison ran (may be cached).
	CheckedAt time.Time `json:"checkedAt"`
	// Cached reports that this answer came from the per-app cache.
	Cached bool `json:"cached,omitempty"`
}

type driftCacheEntry struct {
	dto AppDriftDTO
}

// driftCache memoizes drift answers per "project/app".
type driftCache struct {
	mu sync.Mutex
	m  map[string]driftCacheEntry
}

func (c *driftCache) get(key string) (AppDriftDTO, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || time.Since(e.dto.CheckedAt) > driftCacheTTL {
		return AppDriftDTO{}, false
	}
	return e.dto, true
}

func (c *driftCache) put(key string, dto AppDriftDTO) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]driftCacheEntry{}
	}
	c.m[key] = driftCacheEntry{dto: dto}
}

func (c *driftCache) invalidate(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, key)
}

// handleAppGitopsDrift handles GET /api/v1/projects/{project}/apps/{app}/gitops-drift.
//
// It answers "does the gitops repo still match what suparship would publish
// for this app?" by rendering the app from the store into the publisher's
// clone and diffing (nothing is committed). A non-empty Files list means the
// repo was edited or reverted directly; the fix is a re-publish (POST .../sync)
// or a suparship-side change that matches the repo.
func (ah *appHandler) handleAppGitopsDrift(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	appName := r.PathValue("app")
	if ah.gitOpsPublisher == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "gitops publisher not configured"})
		return
	}
	detector, ok := ah.gitOpsPublisher.(AppDriftDetector)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: errDriftUnsupported.Error()})
		return
	}
	key := projectName + "/" + appName
	refresh := r.URL.Query().Get("refresh") == "1"
	if !refresh {
		if dto, hit := ah.drift.get(key); hit {
			dto.Cached = true
			writeJSON(w, http.StatusOK, dto)
			return
		}
	}
	app, err := ah.appStore.GetApp(r.Context(), projectName, appName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Error: "app \"" + appName + "\" not found in project \"" + projectName + "\"",
		})
		return
	}
	allEnvs, err := ah.appStore.ListAppEnvironments(r.Context(), projectName, appName)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list app environments"})
		return
	}
	var stableEnvs []*domain.AppEnvironment
	for _, env := range allEnvs {
		if env.EnvType != domain.AppEnvPreview {
			stableEnvs = append(stableEnvs, env)
		}
	}
	if len(stableEnvs) == 0 {
		stableEnvs = ah.stableEnvsFromOrg(r.Context(), app)
	}
	files, err := detector.DetectAppDrift(r.Context(), app, stableEnvs)
	if err != nil {
		if errors.Is(err, errDriftUnsupported) {
			writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusBadGateway, errorResponse{Error: "drift check failed: " + err.Error()})
		return
	}
	if files == nil {
		files = []string{}
	}
	dto := AppDriftDTO{Drifted: len(files) > 0, Files: files, CheckedAt: time.Now().UTC()}
	ah.drift.put(key, dto)
	writeJSON(w, http.StatusOK, dto)
}
