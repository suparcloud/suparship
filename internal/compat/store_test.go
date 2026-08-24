package compat_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/suparcloud/suparship/internal/compat"
	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/fake"
)

// ── test stubs ────────────────────────────────────────────────────────────────

// stubEmptyAppStore implements domain.AppStore and always returns empty/error
// results.  It is used to force the fallback path in ServiceBackedAppStore
// tests.
type stubEmptyAppStore struct{}

func (s *stubEmptyAppStore) ListApps(_ context.Context, _ string) ([]*domain.App, error) {
	return nil, fmt.Errorf("no native apps")
}

func (s *stubEmptyAppStore) GetApp(_ context.Context, _, _ string) (*domain.App, error) {
	return nil, fmt.Errorf("app not found")
}

func (s *stubEmptyAppStore) ListAppEnvironments(_ context.Context, _, _ string) ([]*domain.AppEnvironment, error) {
	return []*domain.AppEnvironment{}, nil
}

func (s *stubEmptyAppStore) GetAppEnvironment(_ context.Context, _, _, _ string) (*domain.AppEnvironment, error) {
	return nil, fmt.Errorf("env not found")
}

func (s *stubEmptyAppStore) ListAppPreviews(_ context.Context, _, _ string) ([]*domain.AppEnvironment, error) {
	return []*domain.AppEnvironment{}, nil
}

func (s *stubEmptyAppStore) SaveApp(_ context.Context, _ string, _ *domain.App) error {
	return fmt.Errorf("stub: SaveApp not implemented")
}

func (s *stubEmptyAppStore) SaveAppEnvironment(_ context.Context, _ string, _ *domain.AppEnvironment) error {
	return fmt.Errorf("stub: SaveAppEnvironment not implemented")
}

func (s *stubEmptyAppStore) DeleteAppEnvironment(_ context.Context, _, _, _ string) error {
	return fmt.Errorf("stub: DeleteAppEnvironment not implemented")
}

func (s *stubEmptyAppStore) DeleteApp(_ context.Context, _, _ string) error {
	return fmt.Errorf("stub: DeleteApp not implemented")
}

// ── store constructors ────────────────────────────────────────────────────────

// newPrimaryStore returns a store whose primary AppStore holds native seed data
// (DevRuntime).  The fallback stores are wired but should never be reached when
// the primary already has data.
func newPrimaryStore() *compat.ServiceBackedAppStore {
	r := fake.NewSeededDevRuntime()
	return compat.NewServiceBackedAppStore(r, r, r, r, r, nil)
}

// newFallbackStore returns a store whose primary AppStore is stubbed to return
// nothing, so every operation must fall back to the DevRuntime service stores.
func newFallbackStore() *compat.ServiceBackedAppStore {
	r := fake.NewSeededDevRuntime()
	return compat.NewServiceBackedAppStore(&stubEmptyAppStore{}, r, r, r, r, nil)
}

var storeCtx = context.Background()

// ── ListApps ──────────────────────────────────────────────────────────────────

func TestServiceBackedAppStore_ListApps_PrimaryDataUsed(t *testing.T) {
	store := newPrimaryStore()
	apps, err := store.ListApps(storeCtx, "demo")
	if err != nil {
		t.Fatalf("ListApps: %v", err)
	}
	if len(apps) == 0 {
		t.Fatal("expected apps from primary store")
	}
	// Native seed sets Template.Version = "1.0.0"; the fallback mapping leaves
	// it empty.  A non-empty version confirms the primary was used.
	if apps[0].Spec.Template.Version == "" {
		t.Error("expected non-empty template version from primary; got empty (fallback may have been used)")
	}
}

func TestServiceBackedAppStore_ListApps_FallsBackToServices(t *testing.T) {
	store := newFallbackStore()
	apps, err := store.ListApps(storeCtx, "demo")
	if err != nil {
		t.Fatalf("ListApps fallback: %v", err)
	}
	if len(apps) == 0 {
		t.Fatal("expected at least one app from fallback service mapping")
	}
	if !sliceHas(appNames(apps), "notes-web") {
		t.Errorf("expected 'hello' app in fallback list, got %v", appNames(apps))
	}
}

func TestServiceBackedAppStore_ListApps_FallbackVersionIsEmpty(t *testing.T) {
	// The service→app mapping must not invent a template version.
	store := newFallbackStore()
	apps, _ := store.ListApps(storeCtx, "demo")
	for _, app := range apps {
		if app.Spec.Template.Version != "" {
			t.Errorf("app %q: fallback should leave Template.Version empty, got %q",
				app.Name, app.Spec.Template.Version)
		}
	}
}

func TestServiceBackedAppStore_ListApps_FallbackComponentIsWeb(t *testing.T) {
	store := newFallbackStore()
	apps, _ := store.ListApps(storeCtx, "demo")
	for _, app := range apps {
		if len(app.Spec.Components) != 1 {
			t.Errorf("app %q: expected 1 component, got %d", app.Name, len(app.Spec.Components))
			continue
		}
		if app.Spec.Components[0].Type != domain.ComponentWeb {
			t.Errorf("app %q: component type = %q, want %q",
				app.Name, app.Spec.Components[0].Type, domain.ComponentWeb)
		}
	}
}

func TestServiceBackedAppStore_ListApps_FallbackUnknownProject(t *testing.T) {
	store := newFallbackStore()
	_, err := store.ListApps(storeCtx, "nonexistent")
	if err == nil {
		t.Error("expected error for unknown project on fallback path")
	}
}

// ── GetApp ────────────────────────────────────────────────────────────────────

func TestServiceBackedAppStore_GetApp_PrimaryDataUsed(t *testing.T) {
	store := newPrimaryStore()
	app, err := store.GetApp(storeCtx, "demo", "notes-web")
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	if app.Spec.Template.Version == "" {
		t.Error("expected non-empty template version from primary")
	}
}

func TestServiceBackedAppStore_GetApp_FallsBackToService(t *testing.T) {
	store := newFallbackStore()
	app, err := store.GetApp(storeCtx, "demo", "notes-web")
	if err != nil {
		t.Fatalf("GetApp fallback: %v", err)
	}
	if app.Name != "notes-web" {
		t.Errorf("Name = %q, want %q", app.Name, "notes-web")
	}
	if app.Spec.Template.Name != "web" {
		t.Errorf("Template.Name = %q, want %q", app.Spec.Template.Name, "web")
	}
	if len(app.Spec.Components) == 0 {
		t.Error("fallback app must have at least one component")
	}
}

func TestServiceBackedAppStore_GetApp_FallbackNotFound(t *testing.T) {
	store := newFallbackStore()
	_, err := store.GetApp(storeCtx, "demo", "ghost")
	if err == nil {
		t.Error("expected error for unknown app on fallback path")
	}
}

// ── ListAppEnvironments ───────────────────────────────────────────────────────

func TestServiceBackedAppStore_ListAppEnvironments_PrimaryDataUsed(t *testing.T) {
	store := newPrimaryStore()
	envs, err := store.ListAppEnvironments(storeCtx, "demo", "notes-web")
	if err != nil {
		t.Fatalf("ListAppEnvironments: %v", err)
	}
	if len(envs) == 0 {
		t.Fatal("expected environments from primary store")
	}
}

func TestServiceBackedAppStore_ListAppEnvironments_FallbackHasStableEnvs(t *testing.T) {
	store := newFallbackStore()
	envs, err := store.ListAppEnvironments(storeCtx, "demo", "notes-web")
	if err != nil {
		t.Fatalf("ListAppEnvironments fallback: %v", err)
	}
	names := envNames(envs)
	for _, want := range []string{"staging", "prod"} {
		if !sliceHas(names, want) {
			t.Errorf("expected environment %q in fallback list, got %v", want, names)
		}
	}
}

func TestServiceBackedAppStore_ListAppEnvironments_FallbackIncludesPreviews(t *testing.T) {
	store := newFallbackStore()
	envs, _ := store.ListAppEnvironments(storeCtx, "demo", "notes-web")
	var previewCount int
	for _, e := range envs {
		if e.EnvType == domain.AppEnvPreview {
			previewCount++
		}
	}
	if previewCount == 0 {
		t.Error("expected at least one preview environment in fallback list")
	}
}

func TestServiceBackedAppStore_ListAppEnvironments_FallbackEnvTypesCorrect(t *testing.T) {
	store := newFallbackStore()
	envs, _ := store.ListAppEnvironments(storeCtx, "demo", "notes-web")
	for _, e := range envs {
		if !e.EnvType.Valid() {
			t.Errorf("environment %q has invalid EnvType %q", e.EnvName, e.EnvType)
		}
	}
}

// ── GetAppEnvironment ─────────────────────────────────────────────────────────

func TestServiceBackedAppStore_GetAppEnvironment_PrimaryDataUsed(t *testing.T) {
	store := newPrimaryStore()
	env, err := store.GetAppEnvironment(storeCtx, "demo", "notes-web", "staging")
	if err != nil {
		t.Fatalf("GetAppEnvironment: %v", err)
	}
	if env.EnvName != "staging" {
		t.Errorf("EnvName = %q, want %q", env.EnvName, "staging")
	}
	// Primary seed has a Release; fallback may not.
	if env.Release == nil {
		t.Error("expected Release from primary seed data")
	}
}

func TestServiceBackedAppStore_GetAppEnvironment_FallbackStableEnvs(t *testing.T) {
	store := newFallbackStore()
	for _, envName := range []string{"staging", "prod"} {
		t.Run(envName, func(t *testing.T) {
			env, err := store.GetAppEnvironment(storeCtx, "demo", "notes-web", envName)
			if err != nil {
				t.Fatalf("GetAppEnvironment(%q) fallback: %v", envName, err)
			}
			if env.EnvName != envName {
				t.Errorf("EnvName = %q, want %q", env.EnvName, envName)
			}
			if env.AppName != "notes-web" {
				t.Errorf("AppName = %q, want %q", env.AppName, "notes-web")
			}
		})
	}
}

func TestServiceBackedAppStore_GetAppEnvironment_FallbackPreview(t *testing.T) {
	// "pr-42" is seeded in the DevRuntime preview store.
	store := newFallbackStore()
	env, err := store.GetAppEnvironment(storeCtx, "demo", "notes-web", "pr-42")
	if err != nil {
		t.Fatalf("GetAppEnvironment(pr-42) fallback: %v", err)
	}
	if env.EnvType != domain.AppEnvPreview {
		t.Errorf("EnvType = %q, want %q", env.EnvType, domain.AppEnvPreview)
	}
	if env.EnvName != "pr-42" {
		t.Errorf("EnvName = %q, want %q", env.EnvName, "pr-42")
	}
}

// ── ListAppPreviews ───────────────────────────────────────────────────────────

func TestServiceBackedAppStore_ListAppPreviews_PrimaryDataUsed(t *testing.T) {
	store := newPrimaryStore()
	previews, err := store.ListAppPreviews(storeCtx, "demo", "notes-web")
	if err != nil {
		t.Fatalf("ListAppPreviews: %v", err)
	}
	if len(previews) == 0 {
		t.Fatal("expected previews from primary store")
	}
	for _, p := range previews {
		if p.EnvType != domain.AppEnvPreview {
			t.Errorf("EnvType = %q, want %q", p.EnvType, domain.AppEnvPreview)
		}
	}
}

func TestServiceBackedAppStore_ListAppPreviews_FallsBackToLegacyPreviews(t *testing.T) {
	store := newFallbackStore()
	previews, err := store.ListAppPreviews(storeCtx, "demo", "notes-web")
	if err != nil {
		t.Fatalf("ListAppPreviews fallback: %v", err)
	}
	if len(previews) == 0 {
		t.Fatal("expected at least one preview from legacy fallback")
	}
	for _, p := range previews {
		if p.EnvType != domain.AppEnvPreview {
			t.Errorf("EnvType = %q, want %q", p.EnvType, domain.AppEnvPreview)
		}
		if p.AppName != "notes-web" {
			t.Errorf("AppName = %q, want %q (ServiceName must map to AppName)", p.AppName, "notes-web")
		}
	}
}

func TestServiceBackedAppStore_ListAppPreviews_FallbackFiltersCorrectly(t *testing.T) {
	store := newFallbackStore()
	// No previews should be returned for an app that has none.
	previews, err := store.ListAppPreviews(storeCtx, "demo", "ghost")
	if err != nil {
		t.Fatalf("ListAppPreviews(ghost) should not error: %v", err)
	}
	if len(previews) != 0 {
		t.Errorf("expected empty slice for unknown app, got %d", len(previews))
	}
}

// ── Determinism ───────────────────────────────────────────────────────────────

func TestServiceBackedAppStore_FallbackIsDeterministic(t *testing.T) {
	s1 := newFallbackStore()
	s2 := newFallbackStore()

	a1, err1 := s1.ListApps(storeCtx, "demo")
	a2, err2 := s2.ListApps(storeCtx, "demo")
	if err1 != nil || err2 != nil {
		t.Fatalf("ListApps errors: %v / %v", err1, err2)
	}
	if len(a1) != len(a2) {
		t.Fatalf("app counts differ: %d vs %d", len(a1), len(a2))
	}
	for i := range a1 {
		if a1[i].Name != a2[i].Name {
			t.Errorf("app[%d] name differs: %q vs %q", i, a1[i].Name, a2[i].Name)
		}
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func appNames(apps []*domain.App) []string {
	out := make([]string, len(apps))
	for i, a := range apps {
		out[i] = a.Name
	}
	return out
}

func envNames(envs []*domain.AppEnvironment) []string {
	out := make([]string, len(envs))
	for i, e := range envs {
		out[i] = e.EnvName
	}
	return out
}

func sliceHas(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
