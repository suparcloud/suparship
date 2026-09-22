package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/tpl"
)

// --- helpers ---

// routedTestApp is previewTestAppForProject with an EXPOSED web component, so
// there is a hostname to route. (The base fixture exposes nothing — that's the
// natural "no ingress → 422" case.)
func routedTestApp(projectName string) *domain.App {
	app := previewTestAppForProject(projectName)
	app.Spec.Components[0].ExposeMode = domain.ExposeExternal
	return app
}

// seedRouteEnvs seeds staging (base), prod and a pr-42 preview cloned from
// staging for "my-app", plus a pr-7 preview cloned from prod (for the mismatch
// case).
func seedRouteEnvs(store *memAppStore, projectName string) {
	ctx := context.Background()
	_ = store.SaveAppEnvironment(ctx, projectName, &domain.AppEnvironment{
		AppName: "my-app", ProjectName: projectName, EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1,
		Namespace: projectName + "-my-app-staging", Release: &domain.AppReleaseRef{Tag: "v1"},
	})
	_ = store.SaveAppEnvironment(ctx, projectName, &domain.AppEnvironment{
		AppName: "my-app", ProjectName: projectName, EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2,
		Namespace: projectName + "-my-app-prod",
	})
	_ = store.SaveAppEnvironment(ctx, projectName, &domain.AppEnvironment{
		AppName: "my-app", ProjectName: projectName, EnvName: "pr-42", EnvType: domain.AppEnvPreview, BaseEnv: "staging",
		Namespace: projectName + "-my-app-preview-pr-42", Release: &domain.AppReleaseRef{Tag: "pr-42-abc"},
		URLs: []string{"https://pr-42.my-app.preview.localhost"},
	})
	_ = store.SaveAppEnvironment(ctx, projectName, &domain.AppEnvironment{
		AppName: "my-app", ProjectName: projectName, EnvName: "pr-7", EnvType: domain.AppEnvPreview, BaseEnv: "prod",
		Namespace: projectName + "-my-app-preview-pr-7", Release: &domain.AppReleaseRef{Tag: "pr-7-abc"},
		URLs: []string{"https://pr-7.my-app.preview.localhost"},
	})
}

func routeAppEnvReq(mux *http.ServeMux, cookie *http.Cookie, project, app, env, fromPreview string) *httptest.ResponseRecorder {
	data, _ := json.Marshal(routeAppEnvRequest{FromPreview: fromPreview})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+project+"/apps/"+app+"/environments/"+env+"/route", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func unrouteAppEnvReq(mux *http.ServeMux, cookie *http.Cookie, project, app, env string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/"+project+"/apps/"+app+"/environments/"+env+"/route", nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func routedTo(t *testing.T, store *memAppStore, project, app, env string) string {
	t.Helper()
	a, err := store.GetApp(context.Background(), project, app)
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	return a.Spec.EnvironmentDefaults[env].RoutedToPreview
}

func previewURLs(t *testing.T, store *memAppStore, project, app, preview string) []string {
	t.Helper()
	env, err := store.GetAppEnvironment(context.Background(), project, app, preview)
	if err != nil {
		t.Fatalf("GetAppEnvironment(%s): %v", preview, err)
	}
	return env.URLs
}

// --- route ---

func TestRouteAppEnv_SetsSpecAndPublishesEnvThenPreview(t *testing.T) {
	pub := &recordingPublisher{}
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, pub)
	store.addApp(routedTestApp(testProject))
	seedRouteEnvs(store, testProject)
	dev := sessionCookieFor(ah, "bob", "developer")

	rec := routeAppEnvReq(mux, dev, testProject, "my-app", "staging", "pr-42")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp["host"] != "my-app.staging.localhost" || resp["from"] != "pr-42" {
		t.Errorf("response = %v, want host=my-app.staging.localhost from=pr-42", resp)
	}
	if got := routedTo(t, store, testProject, "my-app", "staging"); got != "pr-42" {
		t.Errorf("staging RoutedToPreview = %q, want pr-42", got)
	}
	// Webhook order: the env releases the host first (moves to -origin, one
	// batched app publish carrying the staging focus env), then the preview
	// claims it.
	if want := []string{"apps:1", "preview:pr-42"}; strings.Join(pub.log, ",") != strings.Join(want, ",") {
		t.Errorf("publish order = %v, want %v", pub.log, want)
	}
	if len(pub.previewInsts) != 1 || pub.previewInsts[0].Namespace != testProject+"-my-app-preview-pr-42" {
		t.Errorf("preview republished with wrong instance: %+v", pub.previewInsts)
	}
	// The preview's stored URL is now the donated stable URL.
	if urls := previewURLs(t, store, testProject, "my-app", "pr-42"); len(urls) != 1 || urls[0] != "https://my-app.staging.localhost" {
		t.Errorf("pr-42 URLs = %v, want [https://my-app.staging.localhost]", urls)
	}
	// Pin state is untouched: routing never freezes the image.
	a, _ := store.GetApp(context.Background(), testProject, "my-app")
	if ov := a.Spec.EnvironmentDefaults["staging"]; ov.PinnedFrom != "" || ov.PinnedImageTag != "" {
		t.Errorf("routing must not pin: %+v", ov)
	}
}

func TestRouteAppEnv_Rejections(t *testing.T) {
	cases := []struct {
		name, env, from string
		app             *domain.App
		want            int
		msg             string
	}{
		{"prod target", "prod", "pr-7", routedTestApp(testProject), http.StatusUnprocessableEntity, "production"},
		{"preview of another base env", "staging", "pr-7", routedTestApp(testProject), http.StatusUnprocessableEntity, "based on"},
		{"no ingress", "staging", "pr-42", previewTestAppForProject(testProject), http.StatusUnprocessableEntity, "no HTTP route"},
		{"unknown preview", "staging", "pr-999", routedTestApp(testProject), http.StatusNotFound, "preview"},
		{"target is a preview", "pr-42", "pr-7", routedTestApp(testProject), http.StatusUnprocessableEntity, "preview"},
		{"unknown env", "qa", "pr-42", routedTestApp(testProject), http.StatusNotFound, "environment"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pub := &recordingPublisher{}
			mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, pub)
			store.addApp(tc.app)
			seedRouteEnvs(store, testProject)
			rec := routeAppEnvReq(mux, sessionCookieFor(ah, "bob", "developer"), testProject, "my-app", tc.env, tc.from)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.want, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.msg) {
				t.Errorf("error %q should mention %q", rec.Body.String(), tc.msg)
			}
			if pub.previewCalls != 0 || pub.batchAppCalls != 0 {
				t.Errorf("a rejected route must not publish: %v", pub.log)
			}
			if got := routedTo(t, store, testProject, "my-app", tc.env); got != "" {
				t.Errorf("spec must stay unrouted, got %q", got)
			}
		})
	}
}

func TestRouteAppEnv_MissingBodyFieldAndUnknownApp(t *testing.T) {
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, &recordingPublisher{})
	store.addApp(routedTestApp(testProject))
	seedRouteEnvs(store, testProject)
	dev := sessionCookieFor(ah, "bob", "developer")
	if rec := routeAppEnvReq(mux, dev, testProject, "my-app", "staging", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("empty fromPreview: status = %d, want 400", rec.Code)
	}
	if rec := routeAppEnvReq(mux, dev, testProject, "nope", "staging", "pr-42"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown app: status = %d, want 404", rec.Code)
	}
}

func TestRouteAppEnv_RBAC(t *testing.T) {
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, &recordingPublisher{})
	store.addApp(routedTestApp(testProject))
	seedRouteEnvs(store, testProject)
	// Viewers can't route; developers can (same gate as launching previews).
	if rec := routeAppEnvReq(mux, sessionCookieFor(ah, "carol", "viewer"), testProject, "my-app", "staging", "pr-42"); rec.Code != http.StatusForbidden {
		t.Errorf("viewer: status = %d, want 403", rec.Code)
	}
	if rec := unrouteAppEnvReq(mux, sessionCookieFor(ah, "carol", "viewer"), testProject, "my-app", "staging"); rec.Code != http.StatusForbidden {
		t.Errorf("viewer unroute: status = %d, want 403", rec.Code)
	}
	if rec := routeAppEnvReq(mux, sessionCookieFor(ah, "bob", "developer"), testProject, "my-app", "staging", "pr-42"); rec.Code != http.StatusOK {
		t.Errorf("developer: status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
}

func TestRouteAppEnv_ReplacesExistingRoute(t *testing.T) {
	pub := &recordingPublisher{}
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, pub)
	store.addApp(routedTestApp(testProject))
	seedRouteEnvs(store, testProject)
	// A second preview also based on staging.
	_ = store.SaveAppEnvironment(context.Background(), testProject, &domain.AppEnvironment{
		AppName: "my-app", ProjectName: testProject, EnvName: "pr-43", EnvType: domain.AppEnvPreview, BaseEnv: "staging",
		Namespace: testProject + "-my-app-preview-pr-43", URLs: []string{"https://pr-43.my-app.preview.localhost"},
	})
	dev := sessionCookieFor(ah, "bob", "developer")
	if rec := routeAppEnvReq(mux, dev, testProject, "my-app", "staging", "pr-42"); rec.Code != http.StatusOK {
		t.Fatalf("first route: %d %s", rec.Code, rec.Body.String())
	}
	pub.log = nil
	if rec := routeAppEnvReq(mux, dev, testProject, "my-app", "staging", "pr-43"); rec.Code != http.StatusOK {
		t.Fatalf("re-route: %d %s", rec.Code, rec.Body.String())
	}
	if got := routedTo(t, store, testProject, "my-app", "staging"); got != "pr-43" {
		t.Errorf("RoutedToPreview = %q, want pr-43", got)
	}
	// Old preview releases the host first, then the new one claims it. The env
	// is already on its -origin host, so it is not republished.
	if want := "preview:pr-42,preview:pr-43"; strings.Join(pub.log, ",") != want {
		t.Errorf("publish order = %v, want %s", pub.log, want)
	}
	if urls := previewURLs(t, store, testProject, "my-app", "pr-42"); len(urls) != 1 || urls[0] != "https://pr-42.my-app.preview.localhost" {
		t.Errorf("pr-42 URLs = %v, want its own preview URL back", urls)
	}
	if urls := previewURLs(t, store, testProject, "my-app", "pr-43"); len(urls) != 1 || urls[0] != "https://my-app.staging.localhost" {
		t.Errorf("pr-43 URLs = %v, want the staging URL", urls)
	}
}

// routeFailPublisher fails the preview publish so the spec revert is exercised.
type routeFailPublisher struct {
	recordingPublisher
	failPreview bool
	failApps    bool
}

func (p *routeFailPublisher) PublishAppPreview(ctx context.Context, app *domain.App, inst *domain.EnvironmentInstance, baseEnv, tag string) error {
	if p.failPreview {
		return errTestGitops
	}
	return p.recordingPublisher.PublishAppPreview(ctx, app, inst, baseEnv, tag)
}

func (p *routeFailPublisher) PublishApps(ctx context.Context, targets []AppPublishTarget) error {
	if p.failApps {
		return errTestGitops
	}
	return p.recordingPublisher.PublishApps(ctx, targets)
}

func TestRouteAppEnv_PublishFailureRevertsSpec(t *testing.T) {
	for _, tc := range []struct {
		name string
		pub  *routeFailPublisher
	}{
		{"preview publish fails", &routeFailPublisher{failPreview: true}},
		{"env publish fails", &routeFailPublisher{failApps: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, tc.pub)
			store.addApp(routedTestApp(testProject))
			seedRouteEnvs(store, testProject)
			rec := routeAppEnvReq(mux, sessionCookieFor(ah, "bob", "developer"), testProject, "my-app", "staging", "pr-42")
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500 (%s)", rec.Code, rec.Body.String())
			}
			if got := routedTo(t, store, testProject, "my-app", "staging"); got != "" {
				t.Errorf("spec should be reverted after a publish failure, got %q", got)
			}
			if urls := previewURLs(t, store, testProject, "my-app", "pr-42"); len(urls) != 1 || urls[0] != "https://pr-42.my-app.preview.localhost" {
				t.Errorf("pr-42 URL must be untouched after failure, got %v", urls)
			}
		})
	}
}

// --- unroute ---

func TestUnrouteAppEnv_RestoresPreviewThenEnv(t *testing.T) {
	pub := &recordingPublisher{}
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, pub)
	store.addApp(routedTestApp(testProject))
	seedRouteEnvs(store, testProject)
	dev := sessionCookieFor(ah, "bob", "developer")
	if rec := routeAppEnvReq(mux, dev, testProject, "my-app", "staging", "pr-42"); rec.Code != http.StatusOK {
		t.Fatalf("route: %d %s", rec.Code, rec.Body.String())
	}
	pub.log = nil

	rec := unrouteAppEnvReq(mux, dev, testProject, "my-app", "staging")
	if rec.Code != http.StatusOK {
		t.Fatalf("unroute: status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if got := routedTo(t, store, testProject, "my-app", "staging"); got != "" {
		t.Errorf("RoutedToPreview = %q, want cleared", got)
	}
	// Webhook order: the preview releases the host first (back to its own),
	// then the env takes it back.
	if want := "preview:pr-42,apps:1"; strings.Join(pub.log, ",") != want {
		t.Errorf("publish order = %v, want %s", pub.log, want)
	}
	if urls := previewURLs(t, store, testProject, "my-app", "pr-42"); len(urls) != 1 || urls[0] != "https://pr-42.my-app.preview.localhost" {
		t.Errorf("pr-42 URLs = %v, want its own preview URL", urls)
	}

	// Not routed → no-op 200, nothing published.
	pub.log = nil
	rec2 := unrouteAppEnvReq(mux, dev, testProject, "my-app", "staging")
	if rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), "not routed") {
		t.Errorf("no-op unroute: %d %s", rec2.Code, rec2.Body.String())
	}
	if len(pub.log) != 0 {
		t.Errorf("no-op unroute must not publish: %v", pub.log)
	}
}

func TestUnrouteAppEnv_PreviewAlreadyGone(t *testing.T) {
	pub := &recordingPublisher{}
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, pub)
	app := routedTestApp(testProject)
	app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{"staging": {RoutedToPreview: "pr-gone"}}
	store.addApp(app)
	seedRouteEnvs(store, testProject)
	rec := unrouteAppEnvReq(mux, sessionCookieFor(ah, "bob", "developer"), testProject, "my-app", "staging")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if want := "apps:1"; strings.Join(pub.log, ",") != want {
		t.Errorf("publish order = %v, want only the env republish", pub.log)
	}
	if got := routedTo(t, store, testProject, "my-app", "staging"); got != "" {
		t.Errorf("RoutedToPreview = %q, want cleared", got)
	}
}

func TestUnrouteAppEnv_PublishFailureRevertsSpec(t *testing.T) {
	pub := &routeFailPublisher{}
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, pub)
	store.addApp(routedTestApp(testProject))
	seedRouteEnvs(store, testProject)
	dev := sessionCookieFor(ah, "bob", "developer")
	if rec := routeAppEnvReq(mux, dev, testProject, "my-app", "staging", "pr-42"); rec.Code != http.StatusOK {
		t.Fatalf("route: %d %s", rec.Code, rec.Body.String())
	}
	pub.failApps = true
	if rec := unrouteAppEnvReq(mux, dev, testProject, "my-app", "staging"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (%s)", rec.Code, rec.Body.String())
	}
	if got := routedTo(t, store, testProject, "my-app", "staging"); got != "pr-42" {
		t.Errorf("spec should still route to pr-42 after a failed restore, got %q", got)
	}
}

// --- lifecycle hooks ---

func TestDeleteAppPreview_RestoresRouting(t *testing.T) {
	pub := &previewDeleterPublisher{}
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, pub)
	store.addApp(routedTestApp(testProject))
	seedRouteEnvs(store, testProject)
	dev := sessionCookieFor(ah, "bob", "developer")
	if rec := routeAppEnvReq(mux, dev, testProject, "my-app", "staging", "pr-42"); rec.Code != http.StatusOK {
		t.Fatalf("route: %d %s", rec.Code, rec.Body.String())
	}
	pub.log = nil

	rec := deleteAppPreview(mux, dev, testProject, "my-app", "pr-42")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: status = %d, want 204 (%s)", rec.Code, rec.Body.String())
	}
	if got := routedTo(t, store, testProject, "my-app", "staging"); got != "" {
		t.Errorf("deleting the routed preview must clear the swap, got %q", got)
	}
	// Webhook order: the preview is pruned (releases the host) BEFORE the env
	// is republished to take it back.
	if want := "apps:1"; strings.Join(pub.log, ",") != want {
		t.Errorf("publish log = %v, want %s", pub.log, want)
	}
	if len(pub.deleted) != 1 {
		t.Errorf("preview should be pruned once, got %v", pub.deleted)
	}
	if _, err := store.GetAppEnvironment(context.Background(), testProject, "my-app", "pr-42"); err == nil {
		t.Error("preview record should be gone")
	}
}

func TestDeleteAppPreview_RestoreFailureReinstatesSwap(t *testing.T) {
	// routeFailPublisher (failApps) + AppPreviewDeleter.
	del := &routeFailDeleter{}
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, del)
	store.addApp(routedTestApp(testProject))
	seedRouteEnvs(store, testProject)
	dev := sessionCookieFor(ah, "bob", "developer")
	if rec := routeAppEnvReq(mux, dev, testProject, "my-app", "staging", "pr-42"); rec.Code != http.StatusOK {
		t.Fatalf("route: %d %s", rec.Code, rec.Body.String())
	}
	del.failApps = true
	rec := deleteAppPreview(mux, dev, testProject, "my-app", "pr-42")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("delete: status = %d, want 500 (%s)", rec.Code, rec.Body.String())
	}
	// The prune already happened (it releases the host); the failed env
	// republish reinstates the swap and keeps the record so a retry (or an
	// unroute, which tolerates a missing preview) republishes the env.
	if got := routedTo(t, store, testProject, "my-app", "staging"); got != "pr-42" {
		t.Errorf("swap must be reinstated after a failed restore, got %q", got)
	}
	if len(del.deleted) != 1 {
		t.Errorf("preview should have been pruned once before the restore: %v", del.deleted)
	}
	if _, err := store.GetAppEnvironment(context.Background(), testProject, "my-app", "pr-42"); err != nil {
		t.Error("preview record must be kept for a retry")
	}
	if !strings.Contains(rec.Body.String(), "unroute") {
		t.Errorf("error should point at unroute as the retry: %s", rec.Body.String())
	}
}

// routeFailDeleter is routeFailPublisher + AppPreviewDeleter.
type routeFailDeleter struct {
	routeFailPublisher
	deleted []string
}

func (p *routeFailDeleter) DeleteAppPreview(_ context.Context, project, previewName, appName, baseEnv string) error {
	p.deleted = append(p.deleted, baseEnv+"/"+project+"/"+previewName+"/"+appName)
	return nil
}

func TestUndeployAppEnv_RejectedWhileRouted(t *testing.T) {
	pub := &recordingPublisher{}
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, pub)
	app := routedTestApp(testProject)
	// Route a NON-base env (qa) so the base-env guard doesn't fire first.
	app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{"qa": {RoutedToPreview: "pr-42"}}
	store.addApp(app)
	seedRouteEnvs(store, testProject)
	_ = store.SaveAppEnvironment(context.Background(), testProject, &domain.AppEnvironment{
		AppName: "my-app", ProjectName: testProject, EnvName: "qa", EnvType: domain.AppEnvStaging, Order: 3,
		Namespace: testProject + "-my-app-qa",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+testProject+"/apps/my-app/environments/qa/undeploy", nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "restore routing") {
		t.Fatalf("status = %d body = %s, want 422 mentioning restore routing", rec.Code, rec.Body.String())
	}
	if len(pub.removedEnvs) != 0 {
		t.Errorf("env must not be removed while routed: %v", pub.removedEnvs)
	}
}

func TestPrepAppPreview_KeepsDonatedURLOnRelaunch(t *testing.T) {
	pub := &recordingPublisher{}
	// The stack mux wires an org provider, which the preview create path needs
	// to resolve the base env.
	mux, ah, store, _, _ := newTestStackMuxPub(testProject, pub)
	store.addApp(routedTestApp(testProject))
	seedRouteEnvs(store, testProject)
	dev := sessionCookieFor(ah, "bob", "developer")
	if rec := routeAppEnvReq(mux, dev, testProject, "my-app", "staging", "pr-42"); rec.Code != http.StatusOK {
		t.Fatalf("route: %d %s", rec.Code, rec.Body.String())
	}
	// CI re-POSTs the preview with a new tag: the stored URL must stay the
	// donated staging URL (the chart renders the preview on that host).
	rec := postAppPreviewJSON(mux, dev, testProject, "my-app", CreateAppPreviewRequest{Name: "pr-42", ImageTag: "pr-42-def"})
	if rec.Code != http.StatusOK {
		t.Fatalf("relaunch: status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if urls := previewURLs(t, store, testProject, "my-app", "pr-42"); len(urls) != 1 || urls[0] != "https://my-app.staging.localhost" {
		t.Errorf("pr-42 URLs after relaunch = %v, want the staging URL", urls)
	}
}

// --- DTO ---

func TestBuildEnvSummaryDTOs_RoutedFlags(t *testing.T) {
	app := routedTestApp(testProject)
	app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{
		"staging":                 {RoutedToPreview: "pr-42"},
		domain.PreviewOverrideKey: {RoutedToPreview: "pr-42"}, // reserved band: ignored
	}
	envs := []*domain.AppEnvironment{
		{AppName: "my-app", EnvName: "pr-42", EnvType: domain.AppEnvPreview, BaseEnv: "staging", URLs: []string{"https://my-app.staging.localhost"}},
		{AppName: "my-app", EnvName: "pr-7", EnvType: domain.AppEnvPreview, BaseEnv: "staging", URLs: []string{"https://pr-7.my-app.preview.localhost"}},
		{AppName: "my-app", EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, URLs: []string{}},
		{AppName: "my-app", EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, URLs: []string{"https://my-app-origin.staging.localhost"}},
	}
	dtos := buildEnvSummaryDTOs(app, envs)
	by := map[string]AppEnvironmentSummaryDTO{}
	for _, d := range dtos {
		by[d.EnvName] = d
	}
	if s := by["staging"]; s.RoutedToPreview != "pr-42" || s.RoutedHost != "https://my-app.staging.localhost" || s.RoutedFromEnv != "" {
		t.Errorf("staging DTO = %+v, want RoutedToPreview=pr-42 RoutedHost=https://my-app.staging.localhost", s)
	}
	if p := by["pr-42"]; p.RoutedFromEnv != "staging" || p.RoutedToPreview != "" {
		t.Errorf("pr-42 DTO = %+v, want RoutedFromEnv=staging", p)
	}
	if p := by["pr-7"]; p.RoutedFromEnv != "" {
		t.Errorf("pr-7 must not be marked routed: %+v", p)
	}
	if p := by["prod"]; p.RoutedToPreview != "" || p.RoutedHost != "" {
		t.Errorf("prod must not be marked routed: %+v", p)
	}
}

func TestGetAppEnvironment_RoutedFields(t *testing.T) {
	mux, ah, store := newTestAppPromoteMuxWithPublisher(testProject, &recordingPublisher{})
	store.addApp(routedTestApp(testProject))
	seedRouteEnvs(store, testProject)
	dev := sessionCookieFor(ah, "bob", "developer")
	if rec := routeAppEnvReq(mux, dev, testProject, "my-app", "staging", "pr-42"); rec.Code != http.StatusOK {
		t.Fatalf("route: %d %s", rec.Code, rec.Body.String())
	}
	get := func(env string) AppEnvironmentSummaryDTO {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+testProject+"/apps/my-app/environments/"+env, nil)
		req.AddCookie(dev)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", env, rec.Code, rec.Body.String())
		}
		var resp AppEnvironmentResponse
		_ = json.NewDecoder(rec.Body).Decode(&resp)
		return resp.Environment
	}
	if s := get("staging"); s.RoutedToPreview != "pr-42" || s.RoutedHost != "https://my-app.staging.localhost" {
		t.Errorf("staging = %+v, want routed to pr-42 with the staging URL", s)
	}
	if p := get("pr-42"); p.RoutedFromEnv != "staging" {
		t.Errorf("pr-42 = %+v, want RoutedFromEnv=staging", p)
	}
}

// --- ArgoCD sequencing ---

// settleGate fakes the ArgoCD gate + nudger: the env's Applications report
// "settled" only after settleAfter reads, and the preview Application reports
// "gone" only after goneAfter reads. Every call is logged so a test can assert
// the claim publish waited for the release to be applied.
type settleGate struct {
	settleAfter, goneAfter int
	settleReads, goneReads int
	refreshed              []string
	appsets                []string
	log                    []string
}

func (g *settleGate) HasAppForEnv(_ context.Context, _, _, env string) (bool, error) {
	g.goneReads++
	g.log = append(g.log, "gone?:"+env)
	return g.goneReads <= g.goneAfter, nil
}

func (g *settleGate) EnvAppsSettled(_ context.Context, _, _, env string, _ time.Time) (bool, []string, error) {
	g.settleReads++
	g.log = append(g.log, "settled?:"+env)
	return g.settleReads > g.settleAfter, []string{"demo-my-app-" + env}, nil
}

func (g *settleGate) RefreshAppsByName(_ context.Context, names []string) error {
	g.refreshed = append(g.refreshed, names...)
	return nil
}

func (g *settleGate) RefreshAppSets(_ context.Context, names []string) error {
	g.appsets = append(g.appsets, names...)
	return nil
}

func TestRouteAppEnv_WaitsForEnvToReleaseBeforePreviewClaims(t *testing.T) {
	routeSettlePoll = time.Millisecond
	pub := &recordingPublisher{}
	mux, ah, store, _, appH := newTestStackMuxPub(testProject, pub)
	gate := &settleGate{settleAfter: 2}
	appH.argoAppGate = gate
	appH.argoChainNudger = gate
	store.addApp(routedTestApp(testProject))
	seedRouteEnvs(store, testProject)
	dev := sessionCookieFor(ah, "bob", "developer")

	if rec := routeAppEnvReq(mux, dev, testProject, "my-app", "staging", "pr-42"); rec.Code != http.StatusOK {
		t.Fatalf("route: %d %s", rec.Code, rec.Body.String())
	}
	// Env published, then polled until settled (3 reads), then the preview.
	if want := "apps:1,preview:pr-42"; strings.Join(pub.log, ",") != want {
		t.Errorf("publish order = %v, want %s", pub.log, want)
	}
	if gate.settleReads != 3 {
		t.Errorf("settle reads = %d, want 3 (two not-yet, one settled)", gate.settleReads)
	}
	if len(gate.refreshed) == 0 || gate.refreshed[0] != "demo-my-app-staging" {
		t.Errorf("ArgoCD should have been nudged to refresh the env's Application, got %v", gate.refreshed)
	}

	// Restore: the preview releases, polled until settled, then the env.
	gate.settleReads, gate.settleAfter, pub.log = 0, 1, nil
	if rec := unrouteAppEnvReq(mux, dev, testProject, "my-app", "staging"); rec.Code != http.StatusOK {
		t.Fatalf("unroute: %d %s", rec.Code, rec.Body.String())
	}
	if want := "preview:pr-42,apps:1"; strings.Join(pub.log, ",") != want {
		t.Errorf("restore publish order = %v, want %s", pub.log, want)
	}
	if gate.settleReads != 2 {
		t.Errorf("restore settle reads = %d, want 2", gate.settleReads)
	}
}

func TestDeleteAppPreview_WaitsForPruneBeforeRestoring(t *testing.T) {
	routeSettlePoll = time.Millisecond
	pub := &previewDeleterPublisher{}
	mux, ah, store, _, appH := newTestStackMuxPub(testProject, pub)
	gate := &settleGate{goneAfter: 2}
	appH.argoAppGate = gate
	appH.argoChainNudger = gate
	store.addApp(routedTestApp(testProject))
	seedRouteEnvs(store, testProject)
	dev := sessionCookieFor(ah, "bob", "developer")
	if rec := routeAppEnvReq(mux, dev, testProject, "my-app", "staging", "pr-42"); rec.Code != http.StatusOK {
		t.Fatalf("route: %d %s", rec.Code, rec.Body.String())
	}
	pub.log, gate.log = nil, nil

	if rec := deleteAppPreview(mux, dev, testProject, "my-app", "pr-42"); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	// Prune, then poll until the preview Application is gone (3 reads, the
	// previews ApplicationSet nudged), then the env republish.
	if gate.goneReads != 3 {
		t.Errorf("gone reads = %d, want 3", gate.goneReads)
	}
	if len(gate.appsets) == 0 || gate.appsets[0] != "previews" {
		t.Errorf("the previews ApplicationSet should have been nudged, got %v", gate.appsets)
	}
	if want := "apps:1"; strings.Join(pub.log, ",") != want {
		t.Errorf("publish log = %v, want %s", pub.log, want)
	}
	if got := routedTo(t, store, testProject, "my-app", "staging"); got != "" {
		t.Errorf("swap should be cleared, got %q", got)
	}
}

// --- async by default ---

// Route is always slow (it waits on ArgoCD), so it accepts by default: 202 +
// a task the caller polls, which reports the phase while running and the
// sync payload when done. ?async=0 still returns the inline result.
func TestRouteAppEnv_AsyncByDefaultWithPhases(t *testing.T) {
	routeSettlePoll = time.Millisecond
	pub := &recordingPublisher{}
	mux, ah, store, _, appH := newTestStackMuxPub(testProject, pub)
	var wg sync.WaitGroup
	appH.async = newAsyncRunner(context.Background(), &wg)
	gate := &settleGate{settleAfter: 1}
	appH.argoAppGate = gate
	appH.argoChainNudger = gate
	store.addApp(routedTestApp(testProject))
	seedRouteEnvs(store, testProject)
	dev := sessionCookieFor(ah, "bob", "developer")

	rec := routeAppEnvReq(mux, dev, testProject, "my-app", "staging", "pr-42")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 by default (%s)", rec.Code, rec.Body.String())
	}
	var acc acceptedResponse
	_ = json.NewDecoder(rec.Body).Decode(&acc)
	if acc.TaskID == "" || acc.StatusURL != "/api/v1/projects/"+testProject+"/tasks/"+acc.TaskID {
		t.Fatalf("202 body = %+v, want taskId + statusUrl", acc)
	}
	wg.Wait()

	req := httptest.NewRequest(http.MethodGet, acc.StatusURL, nil)
	req.AddCookie(dev)
	srec := httptest.NewRecorder()
	mux.ServeHTTP(srec, req)
	if srec.Code != http.StatusOK {
		t.Fatalf("task status = %d (%s)", srec.Code, srec.Body.String())
	}
	var task asyncTask
	_ = json.NewDecoder(srec.Body).Decode(&task)
	if task.State != asyncSucceeded || task.Status != http.StatusOK || task.Kind != "route-app" {
		t.Errorf("task = %+v, want succeeded/200/route-app", task)
	}
	// The last reported phase is the claim; the terminal result is the sync payload.
	if task.Phase != "claim" {
		t.Errorf("task phase = %q (%s), want claim", task.Phase, task.Message)
	}
	res, _ := task.Result.(map[string]any)
	if res["host"] != "my-app.staging.localhost" {
		t.Errorf("task result = %v, want the route payload", task.Result)
	}
	if got := routedTo(t, store, testProject, "my-app", "staging"); got != "pr-42" {
		t.Errorf("RoutedToPreview = %q, want pr-42", got)
	}

	// Opting out of async returns the inline result.
	req2 := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/"+testProject+"/apps/my-app/environments/staging/route?async=0", nil)
	req2.AddCookie(dev)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), "serves its own hostname again") {
		t.Errorf("sync opt-out: %d %s", rec2.Code, rec2.Body.String())
	}
}

func TestStackRoute_AsyncByDefault(t *testing.T) {
	pub := &recordingPublisher{}
	mux, ah, store, stackStore, appH := newTestStackMuxPub(testProject, pub)
	var wg sync.WaitGroup
	appH.async = newAsyncRunner(context.Background(), &wg)
	_ = stackStore.SaveStack(context.Background(), &domain.Stack{Name: "voiceai", ProjectName: testProject})
	seedRoutedStackMember(store, testProject, "web", "voiceai", true)
	rec := postStackJSON(mux, sessionCookieFor(ah, "bob", "developer"),
		"/api/v1/projects/"+testProject+"/stacks/voiceai/route",
		stackRouteRequest{FromPreview: "pr-5", TargetEnv: "staging"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (%s)", rec.Code, rec.Body.String())
	}
	wg.Wait()
	var acc acceptedResponse
	_ = json.NewDecoder(rec.Body).Decode(&acc)
	task, ok := appH.async.store.get(acc.TaskID)
	if !ok || task.State != asyncSucceeded || task.Kind != "route-stack" {
		t.Fatalf("task = %+v ok=%v, want succeeded route-stack", task, ok)
	}
	res, _ := task.Result.(stackBatchResponse)
	if len(res.Results) != 1 || !res.Results[0].OK {
		t.Errorf("task result = %+v, want one ok row", task.Result)
	}
}

// --- literal-host guard ---

// A component whose host is a literal (no routing token anywhere in its
// effective values) cannot be moved by the swap: route refuses with 422 and
// names it. A tokenized host passes. Templates come from the built-in registry
// the handler resolves through lookupTemplate.
func TestRouteAppEnv_RefusesLiteralHost(t *testing.T) {
	mk := func(host string) *tpl.Template {
		return &tpl.Template{
			APIVersion: tpl.CurrentAPIVersion, Kind: tpl.TemplateKind,
			Metadata: tpl.Metadata{Name: "web-service", Version: "1.0.0"},
			Spec: tpl.TemplateSpec{Title: "Web", Engine: tpl.Engine{Type: tpl.EngineHelm},
				DefaultValues: map[string]any{"ingress": map[string]any{"enabled": true, "host": host}}},
		}
	}
	for _, tc := range []struct {
		name string
		host string
		want int
	}{
		{"literal host", "voiceai.acme.com", http.StatusUnprocessableEntity},
		{"legacy routingHost token", "((platform.routingHost))", http.StatusOK},
		{"composed appRoutingName", "((platform.appRoutingName)).((platform.externalBaseDomain))", http.StatusOK},
		{"legacy delimiter", "[[platform.appComponentRoutingName]].acme.com", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pub := &recordingPublisher{}
			mux, ah, store, _, appH := newTestStackMuxPub(testProject, pub)
			appH.builtin = []*tpl.Template{mk(tc.host)}
			store.addApp(routedTestApp(testProject)) // single component "web", template web-service
			seedRouteEnvs(store, testProject)
			rec := routeAppEnvReq(mux, sessionCookieFor(ah, "bob", "developer"), testProject, "my-app", "staging", "pr-42")
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want == http.StatusUnprocessableEntity {
				if !strings.Contains(rec.Body.String(), "web") || !strings.Contains(rec.Body.String(), "appRoutingName") {
					t.Errorf("error should name the component and the tokens: %s", rec.Body.String())
				}
				if pub.previewCalls != 0 || pub.batchAppCalls != 0 {
					t.Errorf("a refused route must not publish: %v", pub.log)
				}
			}
		})
	}
	// The app's own values can supply the token even when the template doesn't.
	t.Run("app values override a literal template host", func(t *testing.T) {
		pub := &recordingPublisher{}
		mux, ah, store, _, appH := newTestStackMuxPub(testProject, pub)
		appH.builtin = []*tpl.Template{mk("voiceai.acme.com")}
		app := routedTestApp(testProject)
		app.Spec.RawValues = map[string]any{"ingress": map[string]any{"host": "((platform.appRoutingName)).acme.com"}}
		store.addApp(app)
		seedRouteEnvs(store, testProject)
		rec := routeAppEnvReq(mux, sessionCookieFor(ah, "bob", "developer"), testProject, "my-app", "staging", "pr-42")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
		}
	})
}
