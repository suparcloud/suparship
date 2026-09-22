package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
)

// seedRoutedStackMember is seedStackMember with an EXPOSED web component and a
// pr-5 preview cloned from staging (when withPreview), so the host swap applies.
func seedRoutedStackMember(store *memAppStore, project, appName, stackName string, withPreview bool) {
	seedStackMember(store, project, appName, stackName)
	app, _ := store.GetApp(context.Background(), project, appName)
	app.Spec.Components[0].ExposeMode = domain.ExposeExternal
	app.Spec.PreviewsEnabled = true
	_ = store.SaveApp(context.Background(), project, app)
	if withPreview {
		_ = store.SaveAppEnvironment(context.Background(), project, &domain.AppEnvironment{
			AppName: appName, ProjectName: project, EnvName: "pr-5", EnvType: domain.AppEnvPreview, BaseEnv: "staging",
			Namespace: project + "-" + stackName + "-preview-pr-5",
			Release:   &domain.AppReleaseRef{Tag: "sha-" + appName},
			URLs:      []string{"https://pr-5." + appName + ".preview.localhost"},
		})
	}
}

func deleteStackJSON(mux *http.ServeMux, cookie *http.Cookie, url string, body any) *httptest.ResponseRecorder {
	var rd *bytes.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rd = bytes.NewReader(data)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(http.MethodDelete, url, rd)
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func stackResultsByApp(t *testing.T, rec *httptest.ResponseRecorder) map[string]stackOpResult {
	t.Helper()
	var resp stackBatchResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	by := map[string]stackOpResult{}
	for _, r := range resp.Results {
		by[r.App] = r
	}
	return by
}

func TestStackRoute_FansOutAndRestores(t *testing.T) {
	pub := &recordingPublisher{}
	mux, ah, store, stackStore, _ := newTestStackMuxPub(testProject, pub)
	_ = stackStore.SaveStack(context.Background(), &domain.Stack{Name: "voiceai", ProjectName: testProject})
	seedRoutedStackMember(store, testProject, "web", "voiceai", true)
	seedRoutedStackMember(store, testProject, "api", "voiceai", true)
	seedRoutedStackMember(store, testProject, "agent", "voiceai", false) // no pr-5 → skipped
	seedStackMember(store, testProject, "worker", "voiceai")             // nothing exposed → skipped
	_ = store.SaveAppEnvironment(context.Background(), testProject, &domain.AppEnvironment{
		AppName: "worker", ProjectName: testProject, EnvName: "pr-5", EnvType: domain.AppEnvPreview, BaseEnv: "staging",
		Namespace: testProject + "-voiceai-preview-pr-5",
	})
	dev := sessionCookieFor(ah, "bob", "developer")
	ctx := context.Background()

	rec := postStackJSON(mux, dev, "/api/v1/projects/"+testProject+"/stacks/voiceai/route",
		stackRouteRequest{FromPreview: "pr-5", TargetEnv: "staging"})
	if rec.Code != http.StatusOK {
		t.Fatalf("route: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	by := stackResultsByApp(t, rec)
	if len(by) != 4 {
		t.Fatalf("expected 4 rows, got %+v", by)
	}
	for _, n := range []string{"web", "api"} {
		if by[n].Skipped || !by[n].OK {
			t.Errorf("%s should be routed, got %+v", n, by[n])
		}
		a, _ := store.GetApp(ctx, testProject, n)
		if got := a.Spec.EnvironmentDefaults["staging"].RoutedToPreview; got != "pr-5" {
			t.Errorf("%s staging RoutedToPreview = %q, want pr-5", n, got)
		}
		env, _ := store.GetAppEnvironment(ctx, testProject, n, "pr-5")
		if len(env.URLs) != 1 || env.URLs[0] != "https://"+n+".staging.localhost" {
			t.Errorf("%s pr-5 URLs = %v, want the staging URL", n, env.URLs)
		}
	}
	if !by["agent"].Skipped || !by["worker"].Skipped {
		t.Errorf("agent (no preview) and worker (no ingress) should be skipped: %+v %+v", by["agent"], by["worker"])
	}
	// ONE batched preview publish (2 targets) then ONE batched app publish (2).
	if pub.batchPreviewCalls != 1 || pub.batchPreviewTargets[0] != 2 {
		t.Errorf("preview batch = %d calls %v targets, want 1 call of 2", pub.batchPreviewCalls, pub.batchPreviewTargets)
	}
	if pub.batchAppCalls != 1 || pub.batchAppTargets[0] != 2 {
		t.Errorf("app batch = %d calls %v targets, want 1 call of 2", pub.batchAppCalls, pub.batchAppTargets)
	}
	if pub.previewCalls != 0 {
		t.Errorf("per-member preview publishes = %d, want 0 (batched)", pub.previewCalls)
	}

	// Restore across the stack.
	rec2 := deleteStackJSON(mux, dev, "/api/v1/projects/"+testProject+"/stacks/voiceai/route",
		stackSuspendRequest{TargetEnv: "staging"})
	if rec2.Code != http.StatusOK {
		t.Fatalf("unroute: expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	uby := stackResultsByApp(t, rec2)
	for _, n := range []string{"web", "api"} {
		if uby[n].Skipped || !uby[n].OK {
			t.Errorf("%s should be restored, got %+v", n, uby[n])
		}
		a, _ := store.GetApp(ctx, testProject, n)
		if got := a.Spec.EnvironmentDefaults["staging"].RoutedToPreview; got != "" {
			t.Errorf("%s should be unrouted, got %q", n, got)
		}
		env, _ := store.GetAppEnvironment(ctx, testProject, n, "pr-5")
		if len(env.URLs) != 1 || env.URLs[0] != "https://pr-5."+n+".preview.localhost" {
			t.Errorf("%s pr-5 URLs = %v, want its own preview URL", n, env.URLs)
		}
	}
	if !uby["agent"].Skipped || !uby["worker"].Skipped {
		t.Errorf("unrouted members should be skipped: %+v %+v", uby["agent"], uby["worker"])
	}
	if pub.batchAppCalls != 2 || pub.batchPreviewCalls != 2 {
		t.Errorf("restore should add one app batch + one preview batch: apps=%d previews=%d", pub.batchAppCalls, pub.batchPreviewCalls)
	}
}

func TestStackRoute_Rejections(t *testing.T) {
	mux, ah, store, stackStore, _ := newTestStackMuxPub(testProject, &recordingPublisher{})
	_ = stackStore.SaveStack(context.Background(), &domain.Stack{Name: "voiceai", ProjectName: testProject})
	seedRoutedStackMember(store, testProject, "web", "voiceai", true)
	dev := sessionCookieFor(ah, "bob", "developer")
	url := "/api/v1/projects/" + testProject + "/stacks/voiceai/route"

	if rec := postStackJSON(mux, dev, url, stackRouteRequest{FromPreview: "pr-5", TargetEnv: "prod"}); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "production") {
		t.Errorf("prod target: %d %s, want 422 mentioning production", rec.Code, rec.Body.String())
	}
	if rec := postStackJSON(mux, dev, url, stackRouteRequest{FromPreview: "pr-5", TargetEnv: "stagingX"}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("bogus targetEnv: %d, want 422", rec.Code)
	}
	if rec := postStackJSON(mux, dev, url, stackRouteRequest{TargetEnv: "staging"}); rec.Code != http.StatusBadRequest {
		t.Errorf("missing fromPreview: %d, want 400", rec.Code)
	}
	if rec := postStackJSON(mux, sessionCookieFor(ah, "carol", "viewer"), url, stackRouteRequest{FromPreview: "pr-5", TargetEnv: "staging"}); rec.Code != http.StatusForbidden {
		t.Errorf("viewer: %d, want 403", rec.Code)
	}
	if rec := postStackJSON(mux, dev, "/api/v1/projects/"+testProject+"/stacks/nope/route", stackRouteRequest{FromPreview: "pr-5", TargetEnv: "staging"}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown stack: %d, want 404", rec.Code)
	}
}

func TestStackRoute_PublishFailureRevertsMembers(t *testing.T) {
	pub := &routeFailPublisher{failApps: true}
	mux, ah, store, stackStore, _ := newTestStackMuxPub(testProject, pub)
	_ = stackStore.SaveStack(context.Background(), &domain.Stack{Name: "voiceai", ProjectName: testProject})
	seedRoutedStackMember(store, testProject, "web", "voiceai", true)
	seedRoutedStackMember(store, testProject, "api", "voiceai", true)
	rec := postStackJSON(mux, sessionCookieFor(ah, "bob", "developer"),
		"/api/v1/projects/"+testProject+"/stacks/voiceai/route",
		stackRouteRequest{FromPreview: "pr-5", TargetEnv: "staging"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 batch response, got %d: %s", rec.Code, rec.Body.String())
	}
	by := stackResultsByApp(t, rec)
	for _, n := range []string{"web", "api"} {
		if by[n].OK {
			t.Errorf("%s should report the publish error, got %+v", n, by[n])
		}
		a, _ := store.GetApp(context.Background(), testProject, n)
		if got := a.Spec.EnvironmentDefaults["staging"].RoutedToPreview; got != "" {
			t.Errorf("%s spec should be reverted, got %q", n, got)
		}
	}
}

func TestDeleteStackPreview_RestoresRouting(t *testing.T) {
	pub := &previewDeleterPublisher{}
	mux, ah, store, stackStore, _ := newTestStackMuxPub(testProject, pub)
	_ = stackStore.SaveStack(context.Background(), &domain.Stack{Name: "voiceai", ProjectName: testProject})
	seedRoutedStackMember(store, testProject, "web", "voiceai", true)
	seedRoutedStackMember(store, testProject, "api", "voiceai", true)
	seedRoutedStackMember(store, testProject, "agent", "voiceai", true) // has the preview, not routed
	dev := sessionCookieFor(ah, "bob", "developer")
	ctx := context.Background()

	rec := postStackJSON(mux, dev, "/api/v1/projects/"+testProject+"/stacks/voiceai/route",
		stackRouteRequest{FromPreview: "pr-5", TargetEnv: "staging", Apps: []string{"web", "api"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("route: %d %s", rec.Code, rec.Body.String())
	}
	pub.log = nil
	pub.batchAppCalls = 0

	rec2 := deleteStackJSON(mux, dev, "/api/v1/projects/"+testProject+"/stacks/voiceai/previews/pr-5", nil)
	if rec2.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec2.Code, rec2.Body.String())
	}
	by := stackResultsByApp(t, rec2)
	for _, n := range []string{"web", "api", "agent"} {
		if !by[n].OK {
			t.Errorf("%s preview should be deleted, got %+v", n, by[n])
		}
		if _, err := store.GetAppEnvironment(ctx, testProject, n, "pr-5"); err == nil {
			t.Errorf("%s pr-5 record should be gone", n)
		}
		a, _ := store.GetApp(ctx, testProject, n)
		if got := a.Spec.EnvironmentDefaults["staging"].RoutedToPreview; got != "" {
			t.Errorf("%s should be unrouted after the preview delete, got %q", n, got)
		}
	}
	// One batched restore publish for the two routed members, before the prunes.
	if pub.batchAppCalls != 1 || pub.batchAppTargets[len(pub.batchAppTargets)-1] != 2 {
		t.Errorf("restore batch = %d calls %v, want one call of 2", pub.batchAppCalls, pub.batchAppTargets)
	}
	if len(pub.deleted) != 3 {
		t.Errorf("prunes = %v, want 3", pub.deleted)
	}
}
