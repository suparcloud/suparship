package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/project"
)

// Previews are grouped by PR (preview name) across apps: a PR shows as one item
// with its per-app previews, identically for single-app and stack previews.
func TestHandleListPreviewGroups(t *testing.T) {
	store := newMemAppStore()
	store.addApp(&domain.App{Name: "web", ProjectName: "voiceai"})
	store.addApp(&domain.App{Name: "worker", ProjectName: "voiceai"})
	// pr-1 spans web + worker (e.g. a stack/PR); pr-2 is web-only.
	store.addEnv(&domain.AppEnvironment{
		AppName: "web", ProjectName: "voiceai", EnvName: "pr-1", EnvType: domain.AppEnvPreview,
		BaseEnv: "staging", Status: domain.AppRuntimeStatus{Phase: domain.StatusHealthy},
	})
	store.addEnv(&domain.AppEnvironment{
		AppName: "worker", ProjectName: "voiceai", EnvName: "pr-1", EnvType: domain.AppEnvPreview,
		BaseEnv: "staging", Status: domain.AppRuntimeStatus{Phase: domain.StatusHealthy},
	})
	store.addEnv(&domain.AppEnvironment{
		AppName: "web", ProjectName: "voiceai", EnvName: "pr-2", EnvType: domain.AppEnvPreview,
		BaseEnv: "staging", Status: domain.AppRuntimeStatus{Phase: domain.StatusDegraded},
	})

	projStore := newMemProjectStore()
	_ = projStore.Save(context.Background(), &project.Project{Metadata: project.ProjectMeta{Name: "voiceai"}})

	// runtimeProvider/clusterPool nil → enrich is a no-op, so the seeded statuses
	// drive the group health.
	ah := &appHandler{appStore: store}
	rh := &rbacHandler{projectStore: projStore, appHandler: ah}

	rec := httptest.NewRecorder()
	rh.handleListPreviewGroups(rec, httptest.NewRequest(http.MethodGet, "/api/v1/previews", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var resp PreviewGroupsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Previews) != 2 {
		t.Fatalf("expected 2 PR groups, got %d: %+v", len(resp.Previews), resp.Previews)
	}
	byName := map[string]PreviewGroupDTO{}
	for _, g := range resp.Previews {
		byName[g.Name] = g
	}
	pr1 := byName["pr-1"]
	if len(pr1.Apps) != 2 || pr1.Health != domain.StatusHealthy || pr1.BaseEnv != "staging" {
		t.Errorf("pr-1 = %+v, want 2 apps, healthy, base staging", pr1)
	}
	if pr1.Apps[0].AppName != "web" || pr1.Apps[1].AppName != "worker" {
		t.Errorf("pr-1 apps not sorted: %+v", pr1.Apps)
	}
	pr2 := byName["pr-2"]
	if len(pr2.Apps) != 1 || pr2.Health != domain.StatusDegraded {
		t.Errorf("pr-2 = %+v, want 1 app, degraded", pr2)
	}

	// ?project= scopes to one project (used by the stack page).
	rec = httptest.NewRecorder()
	rh.handleListPreviewGroups(rec, httptest.NewRequest(http.MethodGet, "/api/v1/preview-groups?project=voiceai", nil))
	var scoped PreviewGroupsResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &scoped)
	if len(scoped.Previews) != 2 {
		t.Errorf("?project=voiceai → %d groups, want 2", len(scoped.Previews))
	}
	rec = httptest.NewRecorder()
	rh.handleListPreviewGroups(rec, httptest.NewRequest(http.MethodGet, "/api/v1/preview-groups?project=other", nil))
	var none PreviewGroupsResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &none)
	if len(none.Previews) != 0 {
		t.Errorf("?project=other → %d groups, want 0", len(none.Previews))
	}
}
