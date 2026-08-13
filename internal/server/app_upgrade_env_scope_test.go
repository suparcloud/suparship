package server

import (
	"context"
	"net/http"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/suparcloud/suparship/internal/domain"
)

// seedStableEnvs records staging + prod environment rows for "my-app" so the
// env-scoped upgrade's environment validation and convergence check see them.
func seedStableEnvs(store *memAppStore, projectName string) {
	ctx := context.Background()
	_ = store.SaveAppEnvironment(ctx, projectName, &domain.AppEnvironment{
		AppName: "my-app", EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1,
	})
	_ = store.SaveAppEnvironment(ctx, projectName, &domain.AppEnvironment{
		AppName: "my-app", EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2,
	})
	_ = store.SaveAppEnvironment(ctx, projectName, &domain.AppEnvironment{
		AppName: "my-app", EnvName: "pr-1", EnvType: domain.AppEnvPreview, Order: 0,
	})
}

// An env-scoped upgrade writes ONLY the chosen env's override: the app-wide
// component pin (and with it every other environment) stays put.
func TestUpgradeTemplate_EnvScopedWritesOverrideOnly(t *testing.T) {
	kc := fake.NewSimpleClientset(archiveCM("web-service", "1.0.0"), archiveCM("web-service", "1.1.0"))
	mux, ah, store := newTestAppUpgradeMuxWithKube(testProject, &recordingPublisher{}, kc)
	store.addApp(upgradeTestApp(testProject, comp("web", "web-service", "1.0.0")))
	seedStableEnvs(store, testProject)
	cookie := sessionCookieFor(ah, "alice", "org_admin")

	rec := postUpgradeTemplateJSON(mux, cookie, testProject, "my-app",
		map[string]any{"version": "1.1.0", "environment": "staging"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	app, _ := store.GetApp(context.Background(), testProject, "my-app")
	if got := componentVersions(app)["web"]; got != "1.0.0" {
		t.Errorf("app-wide pin moved to %q; must stay 1.0.0", got)
	}
	if got := app.Spec.Template.Version; got != "1.0.0" {
		t.Errorf("app-level mirror moved to %q; must stay 1.0.0", got)
	}
	if got := app.Spec.EnvironmentDefaults["staging"].TemplateVersions["web"]; got != "1.1.0" {
		t.Errorf("staging override = %q, want 1.1.0", got)
	}
	if tv := app.Spec.EnvironmentDefaults["prod"].TemplateVersions; len(tv) != 0 {
		t.Errorf("prod must carry no override, got %v", tv)
	}
}

// Once every stable env pins the same version, the overrides fold into the
// app-wide pin and the spec reads as if the upgrade had been app-wide.
func TestUpgradeTemplate_EnvScopedConvergenceCollapses(t *testing.T) {
	kc := fake.NewSimpleClientset(archiveCM("web-service", "1.0.0"), archiveCM("web-service", "1.1.0"))
	mux, ah, store := newTestAppUpgradeMuxWithKube(testProject, &recordingPublisher{}, kc)
	store.addApp(upgradeTestApp(testProject, comp("web", "web-service", "1.0.0")))
	seedStableEnvs(store, testProject)
	cookie := sessionCookieFor(ah, "alice", "org_admin")

	for _, env := range []string{"staging", "prod"} {
		rec := postUpgradeTemplateJSON(mux, cookie, testProject, "my-app",
			map[string]any{"version": "1.1.0", "environment": env})
		if rec.Code != http.StatusOK {
			t.Fatalf("env %s: expected 200, got %d: %s", env, rec.Code, rec.Body.String())
		}
	}

	app, _ := store.GetApp(context.Background(), testProject, "my-app")
	if got := componentVersions(app)["web"]; got != "1.1.0" {
		t.Errorf("converged pin = %q, want 1.1.0", got)
	}
	if got := app.Spec.Template.Version; got != "1.1.0" {
		t.Errorf("app-level mirror = %q, want 1.1.0", got)
	}
	for _, env := range []string{"staging", "prod"} {
		if tv := app.Spec.EnvironmentDefaults[env].TemplateVersions; len(tv) != 0 {
			t.Errorf("%s override must be folded away, got %v", env, tv)
		}
	}
}

// Re-upgrading an env back to the app-wide pin clears its override instead of
// storing a redundant one — the env simply follows the pin again.
func TestUpgradeTemplate_EnvScopedBackToPinClearsOverride(t *testing.T) {
	kc := fake.NewSimpleClientset(archiveCM("web-service", "1.0.0"), archiveCM("web-service", "1.1.0"))
	mux, ah, store := newTestAppUpgradeMuxWithKube(testProject, &recordingPublisher{}, kc)
	store.addApp(upgradeTestApp(testProject, comp("web", "web-service", "1.0.0")))
	seedStableEnvs(store, testProject)
	cookie := sessionCookieFor(ah, "alice", "org_admin")

	for _, v := range []string{"1.1.0", "1.0.0"} {
		rec := postUpgradeTemplateJSON(mux, cookie, testProject, "my-app",
			map[string]any{"version": v, "environment": "staging"})
		if rec.Code != http.StatusOK {
			t.Fatalf("to %s: expected 200, got %d: %s", v, rec.Code, rec.Body.String())
		}
	}

	app, _ := store.GetApp(context.Background(), testProject, "my-app")
	if tv := app.Spec.EnvironmentDefaults["staging"].TemplateVersions; len(tv) != 0 {
		t.Errorf("staging override must be cleared, got %v", tv)
	}
	if got := componentVersions(app)["web"]; got != "1.0.0" {
		t.Errorf("app-wide pin = %q, want 1.0.0", got)
	}
}

// An app-wide upgrade supersedes env-scoped pins for the moved components —
// leaving them would silently hold those envs on the old version.
func TestUpgradeTemplate_AppWideClearsEnvPins(t *testing.T) {
	kc := fake.NewSimpleClientset(
		archiveCM("web-service", "1.0.0"), archiveCM("web-service", "1.1.0"), archiveCM("web-service", "1.2.0"))
	mux, ah, store := newTestAppUpgradeMuxWithKube(testProject, &recordingPublisher{}, kc)
	store.addApp(upgradeTestApp(testProject, comp("web", "web-service", "1.0.0")))
	seedStableEnvs(store, testProject)
	cookie := sessionCookieFor(ah, "alice", "org_admin")

	rec := postUpgradeTemplateJSON(mux, cookie, testProject, "my-app",
		map[string]any{"version": "1.1.0", "environment": "staging"})
	if rec.Code != http.StatusOK {
		t.Fatalf("env-scoped: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	rec = postUpgradeTemplateJSON(mux, cookie, testProject, "my-app",
		map[string]any{"version": "1.2.0"})
	if rec.Code != http.StatusOK {
		t.Fatalf("app-wide: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	app, _ := store.GetApp(context.Background(), testProject, "my-app")
	if got := componentVersions(app)["web"]; got != "1.2.0" {
		t.Errorf("app-wide pin = %q, want 1.2.0", got)
	}
	if tv := app.Spec.EnvironmentDefaults["staging"].TemplateVersions; len(tv) != 0 {
		t.Errorf("staging env pin must be superseded, got %v", tv)
	}
}

// Unknown environments and previews are rejected before anything is mutated.
func TestUpgradeTemplate_EnvScopedRejectsUnknownAndPreviewEnv(t *testing.T) {
	kc := fake.NewSimpleClientset(archiveCM("web-service", "1.1.0"))
	mux, ah, store := newTestAppUpgradeMuxWithKube(testProject, &recordingPublisher{}, kc)
	store.addApp(upgradeTestApp(testProject, comp("web", "web-service", "1.0.0")))
	seedStableEnvs(store, testProject)
	cookie := sessionCookieFor(ah, "alice", "org_admin")

	for _, env := range []string{"nope", "pr-1"} {
		rec := postUpgradeTemplateJSON(mux, cookie, testProject, "my-app",
			map[string]any{"version": "1.1.0", "environment": env})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("env %q: expected 400, got %d: %s", env, rec.Code, rec.Body.String())
		}
	}
	app, _ := store.GetApp(context.Background(), testProject, "my-app")
	if got := componentVersions(app)["web"]; got != "1.0.0" {
		t.Errorf("pin moved to %q on a rejected request", got)
	}
}
