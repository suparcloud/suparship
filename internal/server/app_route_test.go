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

func TestRouteAppEnv_SetsSpecAndPublishesPreviewThenEnv(t *testing.T) {
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
	// Preview publishes first (takes the host), then the env (moves to -origin)
	// — one batched app publish carrying the staging focus env.
	if want := []string{"preview:pr-42", "apps:1"}; strings.Join(pub.log, ",") != strings.Join(want, ",") {
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
	// Old preview moves back to its own host first, then the new one takes over.
	if want := "preview:pr-42,preview:pr-43,apps:1"; strings.Join(pub.log, ",") != want {
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

func TestUnrouteAppEnv_RestoresEnvThenPreview(t *testing.T) {
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
	// Env first (takes its host back), then the preview (back to its own).
	if want := "apps:1,preview:pr-42"; strings.Join(pub.log, ",") != want {
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
	// The env is republished (gets its host back) BEFORE the preview is pruned.
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

func TestDeleteAppPreview_RestoreFailureKeepsSwapAndPreview(t *testing.T) {
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
	if got := routedTo(t, store, testProject, "my-app", "staging"); got != "pr-42" {
		t.Errorf("swap must be reinstated after a failed restore, got %q", got)
	}
	if len(del.deleted) != 0 {
		t.Errorf("preview must not be pruned when the restore failed: %v", del.deleted)
	}
	if _, err := store.GetAppEnvironment(context.Background(), testProject, "my-app", "pr-42"); err != nil {
		t.Error("preview record must be kept for a retry")
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
