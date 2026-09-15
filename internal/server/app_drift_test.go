package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/tpl"
)

// driftPublisher is a publisher that also detects drift with a canned answer.
type driftPublisher struct {
	updatePublisher
	files []string
	calls int
}

func (d *driftPublisher) DetectAppDrift(_ context.Context, _ *domain.App, _ []*domain.AppEnvironment) ([]string, error) {
	d.calls++
	return d.files, nil
}

func getDrift(mux *http.ServeMux, cookie *http.Cookie, project, app string, refresh bool) (int, AppDriftDTO) {
	url := "/api/v1/projects/" + project + "/apps/" + app + "/gitops-drift"
	if refresh {
		url += "?refresh=1"
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var dto AppDriftDTO
	_ = json.NewDecoder(rec.Body).Decode(&dto)
	return rec.Code, dto
}

func TestAppGitopsDrift_ReportsAndCaches(t *testing.T) {
	pub := &driftPublisher{files: []string{"envs/staging/api/my-app/values.yaml"}}
	mux, ah, store := newTestAppUpdateMuxWithTemplates(testProject, pub, []*tpl.Template{templateAt("web-service", "1.0.0")})
	store.addApp(pinnedComponentApp(testProject))
	cookie := sessionCookieFor(ah, "alice", "org_admin")

	code, dto := getDrift(mux, cookie, testProject, "my-app", false)
	if code != http.StatusOK || !dto.Drifted || len(dto.Files) != 1 || dto.Cached {
		t.Fatalf("first check: code=%d dto=%+v", code, dto)
	}
	// Second call within the TTL is served from the cache.
	code, dto = getDrift(mux, cookie, testProject, "my-app", false)
	if code != http.StatusOK || !dto.Cached || pub.calls != 1 {
		t.Errorf("second check should be cached: code=%d cached=%v calls=%d", code, dto.Cached, pub.calls)
	}
	// refresh=1 bypasses it.
	pub.files = nil
	code, dto = getDrift(mux, cookie, testProject, "my-app", true)
	if code != http.StatusOK || dto.Drifted || dto.Cached || pub.calls != 2 {
		t.Errorf("refresh: code=%d dto=%+v calls=%d", code, dto, pub.calls)
	}
}

// A publisher without drift support answers 503, never 500.
func TestAppGitopsDrift_Unsupported(t *testing.T) {
	mux, ah, store := newTestAppUpdateMuxWithTemplates(testProject, &updatePublisher{}, []*tpl.Template{templateAt("web-service", "1.0.0")})
	store.addApp(pinnedComponentApp(testProject))
	code, _ := getDrift(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app", false)
	if code != http.StatusServiceUnavailable {
		t.Errorf("code = %d, want 503", code)
	}
}

// force=true re-publishes the stored state when every target equals the
// stored pin — the no-op branch normally returns without publishing.
func TestUpgradeTemplate_ForceRepublishesUnchangedPins(t *testing.T) {
	pub := &updatePublisher{}
	mux, ah, store := newTestAppUpdateMuxWithTemplates(testProject, pub, []*tpl.Template{templateAt("web-service", "1.0.0")})
	store.addApp(pinnedComponentApp(testProject))
	seedStableEnvs(store, testProject)
	cookie := sessionCookieFor(ah, "alice", "org_admin")

	// Same version, no force → no publish.
	rec := postUpgradeTemplateJSON(mux, cookie, testProject, "my-app", map[string]any{"components": map[string]string{"web": "1.0.0"}})
	if rec.Code != http.StatusOK || pub.publishApps != 0 {
		t.Fatalf("no-op: code=%d publishes=%d", rec.Code, pub.publishApps)
	}
	// Same version, force → one publish, pins untouched.
	rec = postUpgradeTemplateJSON(mux, cookie, testProject, "my-app", map[string]any{"components": map[string]string{"web": "1.0.0"}, "force": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("force: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Republished bool `json:"republished"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if !resp.Republished || pub.publishApps != 1 {
		t.Errorf("force should republish once: republished=%v publishes=%d", resp.Republished, pub.publishApps)
	}
	got, _ := store.GetApp(context.Background(), testProject, "my-app")
	if got.Spec.Components[0].Template.Version != "1.0.0" {
		t.Errorf("force must not move pins: %+v", got.Spec.Components[0].Template)
	}
	// Env-scoped no-op with force also republishes.
	rec = postUpgradeTemplateJSON(mux, cookie, testProject, "my-app", map[string]any{"components": map[string]string{"web": "1.0.0"}, "environment": "staging", "force": true})
	if rec.Code != http.StatusOK || pub.publishApps != 2 {
		t.Errorf("env-scoped force: code=%d publishes=%d (%s)", rec.Code, pub.publishApps, rec.Body.String())
	}
}
