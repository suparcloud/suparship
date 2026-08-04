package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/session"
)

func postUpgradeTemplateJSON(mux *http.ServeMux, cookie *http.Cookie, project, app string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	url := "/api/v1/projects/" + project + "/apps/" + app + "/upgrade-template"
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// upgradeTestApp builds an app whose components carry explicit pins. Pass pairs
// of "component=template@version"; the first component's template becomes the
// app-level primary mirror.
func upgradeTestApp(projectName string, components ...domain.ComponentSpec) *domain.App {
	app := &domain.App{
		Name:        "my-app",
		ProjectName: projectName,
		Spec:        domain.AppSpec{Components: components},
	}
	if len(components) > 0 && components[0].Template != nil {
		app.Spec.Template = *components[0].Template
	}
	return app
}

func comp(name, tmplName, version string) domain.ComponentSpec {
	return domain.ComponentSpec{
		Name:     name,
		Type:     domain.ComponentWeb,
		Enabled:  true,
		Template: &domain.AppTemplateRef{Name: tmplName, Version: version},
	}
}

// archiveCM builds the per-version template archive ConfigMap the version check
// reads through kube.ListTemplateVersions.
func archiveCM(name, version string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "suparship-template-" + name + "-" + version,
			Namespace: "suparship-system",
			Labels: map[string]string{
				"suparship.io/template-name":    name,
				"suparship.io/template-version": version,
			},
		},
	}
}

// newTestAppUpgradeMuxWithKube wires a kubernetes client into the handler so the
// per-component archive-existence check actually runs. Without one the check is
// skipped by design (test harnesses, fake mode).
func newTestAppUpgradeMuxWithKube(projectName string, pub GitOpsPublisher, kc kubernetes.Interface) (*http.ServeMux, *authHandler, *memAppStore) {
	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	ah.registerRoutes(mux)

	store := newMemAppStore()
	store.mu.Lock()
	store.apps[projectName] = make(map[string]*domain.App)
	store.mu.Unlock()

	appH := newAppHandler(store, nil, nil, nil)
	appH.gitOpsPublisher = pub
	appH.kubeClient = kc

	rh := &rbacHandler{
		auth:       ah,
		orgStore:   &staticOrgProvider{org: testRBACOrg()},
		appHandler: appH,
	}
	rh.registerRoutes(mux)
	return mux, ah, store
}

func componentVersions(app *domain.App) map[string]string {
	out := map[string]string{}
	for _, c := range app.Spec.Components {
		if c.Template != nil {
			out[c.Name] = c.Template.Version
		}
	}
	return out
}

// A single-component app must have BOTH levels moved: the single-source render
// path reads AppSpec.Template, and leaving Components[0] behind would let the
// next component edit quietly restore the old pin.
func TestUpgradeTemplate_SingleComponentWritesBothLevels(t *testing.T) {
	pub := &updatePublisher{}
	mux, ah, store := newTestAppUpdateMuxWithTemplates(testProject, pub, nil)
	store.addApp(upgradeTestApp(testProject, comp("web", "web-service", "1.0.0")))

	rec := postUpgradeTemplateJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		upgradeAppTemplateRequest{Version: "2.0.0"})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if pub.publishApps != 1 {
		t.Errorf("expected one PublishApp call, got %d", pub.publishApps)
	}
	got, _ := store.GetApp(context.Background(), testProject, "my-app")
	if v := got.Spec.Components[0].Template.Version; v != "2.0.0" {
		t.Errorf("component pin = %q, want 2.0.0", v)
	}
	if v := got.Spec.Template.Version; v != "2.0.0" {
		t.Errorf("app mirror = %q, want 2.0.0", v)
	}
}

// The composed no-op bug: writing only AppSpec.Template left the chart sources
// untouched, because the composed path renders from each component's own pin.
func TestUpgradeTemplate_ComposedSameTemplateMovesEveryComponent(t *testing.T) {
	pub := &updatePublisher{}
	mux, ah, store := newTestAppUpdateMuxWithTemplates(testProject, pub, nil)
	store.addApp(upgradeTestApp(testProject,
		comp("web", "web-service", "1.0.0"),
		comp("worker", "web-service", "1.0.0"),
	))

	rec := postUpgradeTemplateJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		upgradeAppTemplateRequest{Version: "2.0.0"})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := store.GetApp(context.Background(), testProject, "my-app")
	for name, v := range componentVersions(got) {
		if v != "2.0.0" {
			t.Errorf("component %q pin = %q, want 2.0.0", name, v)
		}
	}
	if v := got.Spec.Template.Version; v != "2.0.0" {
		t.Errorf("app mirror = %q, want 2.0.0", v)
	}
}

// A bare version means "the app's primary template". Components rendered by a
// different chart must be left byte-identical and reported back.
func TestUpgradeTemplate_HeterogeneousSkipsOtherTemplates(t *testing.T) {
	pub := &updatePublisher{}
	mux, ah, store := newTestAppUpdateMuxWithTemplates(testProject, pub, nil)
	store.addApp(upgradeTestApp(testProject,
		comp("web", "web-service", "1.0.0"),
		comp("migrate", "job", "3.0.0"),
	))

	rec := postUpgradeTemplateJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		upgradeAppTemplateRequest{Version: "2.0.0"})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := store.GetApp(context.Background(), testProject, "my-app")
	vers := componentVersions(got)
	if vers["web"] != "2.0.0" {
		t.Errorf("web pin = %q, want 2.0.0", vers["web"])
	}
	if vers["migrate"] != "3.0.0" {
		t.Errorf("migrate pin = %q, want 3.0.0 untouched", vers["migrate"])
	}

	var resp struct {
		Skipped    []string `json:"skipped"`
		Components []struct {
			Name      string `json:"name"`
			ToVersion string `json:"toVersion"`
		} `json:"components"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Skipped) != 1 || resp.Skipped[0] != "migrate" {
		t.Errorf("skipped = %v, want [migrate]", resp.Skipped)
	}
	if len(resp.Components) != 1 || resp.Components[0].Name != "web" {
		t.Errorf("components = %+v, want just web", resp.Components)
	}
}

// The general form: name one component and only it moves.
func TestUpgradeTemplate_PerComponentTargetsOnlyThatComponent(t *testing.T) {
	pub := &updatePublisher{}
	mux, ah, store := newTestAppUpdateMuxWithTemplates(testProject, pub, nil)
	store.addApp(upgradeTestApp(testProject,
		comp("web", "web-service", "1.0.0"),
		comp("migrate", "job", "3.0.0"),
	))

	rec := postUpgradeTemplateJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		upgradeAppTemplateRequest{Components: map[string]string{"migrate": "3.2.0"}})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := store.GetApp(context.Background(), testProject, "my-app")
	vers := componentVersions(got)
	if vers["migrate"] != "3.2.0" {
		t.Errorf("migrate pin = %q, want 3.2.0", vers["migrate"])
	}
	if vers["web"] != "1.0.0" {
		t.Errorf("web pin = %q, want 1.0.0 untouched", vers["web"])
	}
	// The mirror tracks web-service (match-by-name), so upgrading job leaves it.
	if got.Spec.Template.Name != "web-service" || got.Spec.Template.Version != "1.0.0" {
		t.Errorf("mirror = %+v, want web-service@1.0.0", got.Spec.Template)
	}
}

// Atomicity: one bad version in the batch must leave the whole app untouched and
// never reach the publisher.
func TestUpgradeTemplate_RejectsBatchWhenOneVersionMissing(t *testing.T) {
	pub := &updatePublisher{}
	// A real kubeClient turns the archive-existence check on. Seed web-service
	// @2.0.0 and job@3.0.0 — job@9.9.9 deliberately does not exist.
	kc := fake.NewSimpleClientset(archiveCM("web-service", "2.0.0"), archiveCM("job", "3.0.0"))
	mux, ah, store := newTestAppUpgradeMuxWithKube(testProject, pub, kc)
	store.addApp(upgradeTestApp(testProject,
		comp("web", "web-service", "1.0.0"),
		comp("migrate", "job", "3.0.0"),
	))

	rec := postUpgradeTemplateJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		upgradeAppTemplateRequest{Components: map[string]string{"web": "2.0.0", "migrate": "9.9.9"}})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown version, got %d: %s", rec.Code, rec.Body.String())
	}
	if pub.publishApps != 0 {
		t.Errorf("publish must not run when any target is invalid, got %d calls", pub.publishApps)
	}
	got, _ := store.GetApp(context.Background(), testProject, "my-app")
	vers := componentVersions(got)
	if vers["web"] != "1.0.0" || vers["migrate"] != "3.0.0" {
		t.Errorf("pins mutated on a rejected batch: %v", vers)
	}
}

func TestUpgradeTemplate_RejectsAmbiguousAndUnknownInputs(t *testing.T) {
	pub := &updatePublisher{}
	mux, ah, store := newTestAppUpdateMuxWithTemplates(testProject, pub, nil)
	store.addApp(upgradeTestApp(testProject, comp("web", "web-service", "1.0.0")))
	cookie := sessionCookieFor(ah, "alice", "org_admin")

	tests := []struct {
		name string
		body upgradeAppTemplateRequest
	}{
		{"both version and components", upgradeAppTemplateRequest{
			Version: "2.0.0", Components: map[string]string{"web": "2.0.0"}}},
		{"neither", upgradeAppTemplateRequest{}},
		{"unknown component", upgradeAppTemplateRequest{
			Components: map[string]string{"nope": "2.0.0"}}},
		{"blank component version", upgradeAppTemplateRequest{
			Components: map[string]string{"web": "  "}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := postUpgradeTemplateJSON(mux, cookie, testProject, "my-app", tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
	if pub.publishApps != 0 {
		t.Errorf("publish must not run on rejected input, got %d calls", pub.publishApps)
	}
}

// A publish failure restores every component pin AND the mirror — otherwise the
// store drifts from what is actually in the gitops repo.
func TestUpgradeTemplate_RollsBackAllPinsOnPublishFailure(t *testing.T) {
	pub := &updatePublisher{failPublish: true}
	mux, ah, store := newTestAppUpdateMuxWithTemplates(testProject, pub, nil)
	store.addApp(upgradeTestApp(testProject,
		comp("web", "web-service", "1.0.0"),
		comp("worker", "web-service", "1.0.0"),
	))

	rec := postUpgradeTemplateJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		upgradeAppTemplateRequest{Version: "2.0.0"})

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on publish failure, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := store.GetApp(context.Background(), testProject, "my-app")
	for name, v := range componentVersions(got) {
		if v != "1.0.0" {
			t.Errorf("component %q pin = %q, want 1.0.0 restored", name, v)
		}
	}
	if v := got.Spec.Template.Version; v != "1.0.0" {
		t.Errorf("mirror = %q, want 1.0.0 restored", v)
	}
}

// A BYO/passthrough app stores NO components — AppSpec.Template is the only pin
// there is, and the single-source path renders straight from it. Upgrading must
// move that field and must NOT synthesize a component to hold it: persisting one
// would trip the publisher's len(Components)==1 canonical-key remap, silently
// changing how the app renders.
func TestUpgradeTemplate_TemplatelessAppUpgradesAppLevelPinOnly(t *testing.T) {
	pub := &updatePublisher{}
	mux, ah, store := newTestAppUpdateMuxWithTemplates(testProject, pub, nil)
	store.addApp(&domain.App{
		Name:        "my-app",
		ProjectName: testProject,
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "chartmuseum-app", Version: "1.0.0"}},
	})

	rec := postUpgradeTemplateJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		upgradeAppTemplateRequest{Version: "1.4.0"})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := store.GetApp(context.Background(), testProject, "my-app")
	if v := got.Spec.Template.Version; v != "1.4.0" {
		t.Errorf("app pin = %q, want 1.4.0", v)
	}
	if len(got.Spec.Components) != 0 {
		t.Errorf("upgrade must not materialize components, got %d", len(got.Spec.Components))
	}
	if pub.publishApps != 1 {
		t.Errorf("expected one PublishApp call, got %d", pub.publishApps)
	}
}

// The per-component form has nothing to address on a componentless app.
func TestUpgradeTemplate_TemplatelessAppRejectsComponentForm(t *testing.T) {
	pub := &updatePublisher{}
	mux, ah, store := newTestAppUpdateMuxWithTemplates(testProject, pub, nil)
	store.addApp(&domain.App{
		Name:        "my-app",
		ProjectName: testProject,
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "chartmuseum-app", Version: "1.0.0"}},
	})

	rec := postUpgradeTemplateJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		upgradeAppTemplateRequest{Components: map[string]string{"web": "1.4.0"}})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if pub.publishApps != 0 {
		t.Errorf("publish must not run, got %d calls", pub.publishApps)
	}
}

// A rollback must restore the app-level pin on the componentless path too.
func TestUpgradeTemplate_TemplatelessAppRollsBackOnPublishFailure(t *testing.T) {
	pub := &updatePublisher{failPublish: true}
	mux, ah, store := newTestAppUpdateMuxWithTemplates(testProject, pub, nil)
	store.addApp(&domain.App{
		Name:        "my-app",
		ProjectName: testProject,
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "chartmuseum-app", Version: "1.0.0"}},
	})

	rec := postUpgradeTemplateJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		upgradeAppTemplateRequest{Version: "1.4.0"})

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := store.GetApp(context.Background(), testProject, "my-app")
	if v := got.Spec.Template.Version; v != "1.0.0" {
		t.Errorf("pin = %q, want 1.0.0 restored", v)
	}
}

// Re-pinning to the current version is accepted but must not churn a publish.
func TestUpgradeTemplate_NoOpWhenAlreadyPinned(t *testing.T) {
	pub := &updatePublisher{}
	mux, ah, store := newTestAppUpdateMuxWithTemplates(testProject, pub, nil)
	store.addApp(upgradeTestApp(testProject, comp("web", "web-service", "1.0.0")))

	rec := postUpgradeTemplateJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app",
		upgradeAppTemplateRequest{Version: "1.0.0"})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if pub.publishApps != 0 {
		t.Errorf("a no-op re-pin must not publish, got %d calls", pub.publishApps)
	}
}
