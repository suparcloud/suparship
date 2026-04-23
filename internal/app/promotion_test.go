package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
)

// --- minimal in-memory AppStore for promotion tests ---

type memStore struct {
	mu   sync.RWMutex
	apps map[string]map[string]*domain.App                            // project → app
	envs map[string]map[string]map[string]*domain.AppEnvironment      // project → app → envName
}

func newMemStore() *memStore {
	return &memStore{
		apps: make(map[string]map[string]*domain.App),
		envs: make(map[string]map[string]map[string]*domain.AppEnvironment),
	}
}

func (m *memStore) saveApp(app *domain.App) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.apps[app.ProjectName] == nil {
		m.apps[app.ProjectName] = make(map[string]*domain.App)
	}
	m.apps[app.ProjectName][app.Name] = app
}

func (m *memStore) saveEnv(projectName string, env *domain.AppEnvironment) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.envs[projectName] == nil {
		m.envs[projectName] = make(map[string]map[string]*domain.AppEnvironment)
	}
	if m.envs[projectName][env.AppName] == nil {
		m.envs[projectName][env.AppName] = make(map[string]*domain.AppEnvironment)
	}
	cp := *env
	m.envs[projectName][env.AppName][env.EnvName] = &cp
}

func (m *memStore) getEnv(projectName, appName, envName string) *domain.AppEnvironment {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.envs[projectName] == nil {
		return nil
	}
	if m.envs[projectName][appName] == nil {
		return nil
	}
	return m.envs[projectName][appName][envName]
}

// domain.AppStore implementation

func (m *memStore) ListApps(_ context.Context, projectName string) ([]*domain.App, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	pm, ok := m.apps[projectName]
	if !ok {
		return nil, fmt.Errorf("project %q not found", projectName)
	}
	out := make([]*domain.App, 0, len(pm))
	for _, a := range pm {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *memStore) GetApp(_ context.Context, projectName, appName string) (*domain.App, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	pm, ok := m.apps[projectName]
	if !ok {
		return nil, fmt.Errorf("project %q not found", projectName)
	}
	a, ok := pm[appName]
	if !ok {
		return nil, fmt.Errorf("app %q not found", appName)
	}
	return a, nil
}

func (m *memStore) ListAppEnvironments(_ context.Context, projectName, appName string) ([]*domain.AppEnvironment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	pm, ok := m.envs[projectName]
	if !ok {
		return []*domain.AppEnvironment{}, nil
	}
	em, ok := pm[appName]
	if !ok {
		return []*domain.AppEnvironment{}, nil
	}
	out := make([]*domain.AppEnvironment, 0, len(em))
	for _, e := range em {
		out = append(out, e)
	}
	return out, nil
}

func (m *memStore) GetAppEnvironment(_ context.Context, projectName, appName, envName string) (*domain.AppEnvironment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	pm, ok := m.envs[projectName]
	if !ok {
		return nil, fmt.Errorf("project %q not found", projectName)
	}
	em, ok := pm[appName]
	if !ok {
		return nil, fmt.Errorf("app %q not found", appName)
	}
	e, ok := em[envName]
	if !ok {
		return nil, fmt.Errorf("env %q not found", envName)
	}
	return e, nil
}

func (m *memStore) ListAppPreviews(_ context.Context, projectName, appName string) ([]*domain.AppEnvironment, error) {
	envs, _ := m.ListAppEnvironments(context.Background(), projectName, appName)
	var out []*domain.AppEnvironment
	for _, e := range envs {
		if e.EnvType == domain.AppEnvPreview {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *memStore) SaveApp(_ context.Context, projectName string, app *domain.App) error {
	app.ProjectName = projectName
	m.saveApp(app)
	return nil
}

func (m *memStore) SaveAppEnvironment(_ context.Context, projectName string, env *domain.AppEnvironment) error {
	env.ProjectName = projectName
	m.saveEnv(projectName, env)
	return nil
}

func (m *memStore) DeleteAppEnvironment(_ context.Context, projectName, appName, envName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.envs[projectName] == nil || m.envs[projectName][appName] == nil {
		return fmt.Errorf("env %q not found", envName)
	}
	if _, ok := m.envs[projectName][appName][envName]; !ok {
		return fmt.Errorf("env %q not found", envName)
	}
	delete(m.envs[projectName][appName], envName)
	return nil
}

func (m *memStore) DeleteApp(_ context.Context, projectName, appName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.apps[projectName] == nil {
		return fmt.Errorf("app %q not found in project %q", appName, projectName)
	}
	if _, ok := m.apps[projectName][appName]; !ok {
		return fmt.Errorf("app %q not found in project %q", appName, projectName)
	}
	delete(m.apps[projectName], appName)
	if m.envs[projectName] != nil {
		delete(m.envs[projectName], appName)
	}
	return nil
}

// --- test helpers ---

const (
	testProject = "acme"
	testApp     = "my-app"
)

func baseApp() *domain.App {
	return &domain.App{
		Name:        testApp,
		ProjectName: testProject,
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "web-service"},
			Components: []domain.ComponentSpec{
				{Name: "web", Type: domain.ComponentWeb, Enabled: true, PreviewEnabled: true},
				{Name: "worker", Type: domain.ComponentWorker, Enabled: true, PreviewEnabled: false},
			},
		},
	}
}

func previewEnv(name, tag string) *domain.AppEnvironment {
	e := &domain.AppEnvironment{
		AppName:   testApp,
		EnvName:   name,
		EnvType:   domain.AppEnvPreview,
		Namespace: testApp + "-" + name,
	}
	if tag != "" {
		e.Release = &domain.AppReleaseRef{Tag: tag, Commit: "abc123"}
	}
	return e
}

func stagingEnv(tag string) *domain.AppEnvironment {
	e := &domain.AppEnvironment{
		AppName:   testApp,
		EnvName:   "staging",
		EnvType:   domain.AppEnvStaging,
		Order:     1,
		Namespace: testApp + "-staging",
	}
	if tag != "" {
		e.Release = &domain.AppReleaseRef{Tag: tag, Commit: "def456"}
	}
	return e
}

func prodEnv() *domain.AppEnvironment {
	return &domain.AppEnvironment{
		AppName:   testApp,
		EnvName:   "prod",
		EnvType:   domain.AppEnvProd,
		Order:     2,
		Namespace: testApp + "-prod",
	}
}

// seedStore populates the store with the base app and supplied environments.
func seedStore(store *memStore, envs ...*domain.AppEnvironment) {
	store.saveApp(baseApp())
	for _, e := range envs {
		store.saveEnv(testProject, e)
	}
}

// --- Promote tests ---

func TestPromote_PreviewToStaging_CopiesRelease(t *testing.T) {
	store := newMemStore()
	seedStore(store,
		previewEnv("pr-42", "pr-42-sha1"),
		stagingEnv(""),
	)

	result, err := Promote(context.Background(), store, PromoteRequest{
		ProjectName: testProject,
		AppName:     testApp,
		FromEnv:     "pr-42",
		ToEnv:       "staging",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FromEnv != "pr-42" {
		t.Errorf("FromEnv = %q, want %q", result.FromEnv, "pr-42")
	}
	if result.ToEnv != "staging" {
		t.Errorf("ToEnv = %q, want %q", result.ToEnv, "staging")
	}
	if result.Release.Tag != "pr-42-sha1" {
		t.Errorf("Release.Tag = %q, want %q", result.Release.Tag, "pr-42-sha1")
	}

	// Verify the store was updated.
	saved := store.getEnv(testProject, testApp, "staging")
	if saved == nil || saved.Release == nil {
		t.Fatal("staging environment not updated in store")
	}
	if saved.Release.Tag != "pr-42-sha1" {
		t.Errorf("stored staging Release.Tag = %q, want %q", saved.Release.Tag, "pr-42-sha1")
	}
}

func TestPromote_StagingToProd_CopiesRelease(t *testing.T) {
	store := newMemStore()
	seedStore(store,
		stagingEnv("v1.2.3"),
		prodEnv(),
	)

	result, err := Promote(context.Background(), store, PromoteRequest{
		ProjectName: testProject,
		AppName:     testApp,
		FromEnv:     "staging",
		ToEnv:       "prod",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Release.Tag != "v1.2.3" {
		t.Errorf("Release.Tag = %q, want %q", result.Release.Tag, "v1.2.3")
	}
	if result.Release.Commit != "def456" {
		t.Errorf("Release.Commit = %q, want %q", result.Release.Commit, "def456")
	}

	saved := store.getEnv(testProject, testApp, "prod")
	if saved == nil || saved.Release == nil {
		t.Fatal("prod environment not updated in store")
	}
	if saved.Release.Tag != "v1.2.3" {
		t.Errorf("stored prod Release.Tag = %q, want %q", saved.Release.Tag, "v1.2.3")
	}
}

// TestPromote_AllComponentsMove verifies that the single AppReleaseRef covers
// all components — there is no per-component partial state possible.
func TestPromote_AllComponentsMove(t *testing.T) {
	store := newMemStore()
	// App has two components: web + worker.
	seedStore(store,
		stagingEnv("v2.0.0"),
		prodEnv(),
	)

	result, err := Promote(context.Background(), store, PromoteRequest{
		ProjectName: testProject,
		AppName:     testApp,
		FromEnv:     "staging",
		ToEnv:       "prod",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The release ref is app-scoped, so both components (web + worker) share it.
	if result.Release.Tag != "v2.0.0" {
		t.Errorf("Release.Tag = %q, want %q", result.Release.Tag, "v2.0.0")
	}

	app, _ := store.GetApp(context.Background(), testProject, testApp)
	if len(app.Spec.Components) != 2 {
		t.Errorf("expected 2 components after promotion, got %d", len(app.Spec.Components))
	}
}

// TestPromote_SourceReleaseUnchanged verifies that promoting does not mutate
// the source environment's release in the store.
func TestPromote_SourceReleaseUnchanged(t *testing.T) {
	store := newMemStore()
	seedStore(store,
		stagingEnv("v3.0.0"),
		prodEnv(),
	)

	_, err := Promote(context.Background(), store, PromoteRequest{
		ProjectName: testProject,
		AppName:     testApp,
		FromEnv:     "staging",
		ToEnv:       "prod",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	src := store.getEnv(testProject, testApp, "staging")
	if src == nil || src.Release == nil || src.Release.Tag != "v3.0.0" {
		t.Errorf("source release was mutated; got %+v", src.Release)
	}
}

// TestPromote_DeepCopy verifies that mutating the returned Release value does
// not affect the stored record.
func TestPromote_DeepCopy(t *testing.T) {
	store := newMemStore()
	seedStore(store,
		stagingEnv("v4.0.0"),
		prodEnv(),
	)

	result, _ := Promote(context.Background(), store, PromoteRequest{
		ProjectName: testProject,
		AppName:     testApp,
		FromEnv:     "staging",
		ToEnv:       "prod",
	})

	// Mutate the result value — stored record must be unaffected.
	result.Release.Tag = "MUTATED"

	saved := store.getEnv(testProject, testApp, "prod")
	if saved.Release.Tag == "MUTATED" {
		t.Error("stored Release.Tag was mutated via returned result")
	}
}

// --- Error path tests ---

func TestPromote_NoRelease(t *testing.T) {
	store := newMemStore()
	seedStore(store,
		stagingEnv(""),  // no release
		prodEnv(),
	)

	_, err := Promote(context.Background(), store, PromoteRequest{
		ProjectName: testProject,
		AppName:     testApp,
		FromEnv:     "staging",
		ToEnv:       "prod",
	})

	if !errors.Is(err, ErrNoRelease) {
		t.Errorf("expected ErrNoRelease, got %v", err)
	}
}

func TestPromote_SourceNotFound(t *testing.T) {
	store := newMemStore()
	seedStore(store, prodEnv())

	_, err := Promote(context.Background(), store, PromoteRequest{
		ProjectName: testProject,
		AppName:     testApp,
		FromEnv:     "staging",
		ToEnv:       "prod",
	})

	if err == nil {
		t.Fatal("expected error for missing source env")
	}
}

func TestPromote_TargetNotFound(t *testing.T) {
	store := newMemStore()
	seedStore(store, stagingEnv("v1.0.0"))

	_, err := Promote(context.Background(), store, PromoteRequest{
		ProjectName: testProject,
		AppName:     testApp,
		FromEnv:     "staging",
		ToEnv:       "prod",
	})

	if err == nil {
		t.Fatal("expected error for missing destination env")
	}
}

func TestPromote_ToPreviewFails(t *testing.T) {
	store := newMemStore()
	seedStore(store,
		stagingEnv("v1.0.0"),
		previewEnv("pr-1", ""),
	)

	_, err := Promote(context.Background(), store, PromoteRequest{
		ProjectName: testProject,
		AppName:     testApp,
		FromEnv:     "staging",
		ToEnv:       "pr-1",
	})

	if err == nil {
		t.Fatal("expected error when promoting to preview")
	}
}

func TestPromote_WrongOrder_StagingToPreview(t *testing.T) {
	store := newMemStore()
	// staging → preview is backwards (preview < staging in chain)
	seedStore(store,
		stagingEnv("v1.0.0"),
		previewEnv("pr-99", ""),
	)

	// pr-99 is preview type, so the "cannot promote to preview" error fires first.
	_, err := Promote(context.Background(), store, PromoteRequest{
		ProjectName: testProject,
		AppName:     testApp,
		FromEnv:     "staging",
		ToEnv:       "pr-99",
	})

	if err == nil {
		t.Fatal("expected error for wrong-order promotion")
	}
}

func TestPromote_WrongOrder_ProdToStaging(t *testing.T) {
	store := newMemStore()
	seedStore(store,
		stagingEnv(""),
		prodEnv(),
	)
	// Give prod a release so we can test pure direction validation.
	store.saveEnv(testProject, &domain.AppEnvironment{
		AppName:   testApp,
		EnvName:   "prod",
		EnvType:   domain.AppEnvProd,
		Order:     2,
		Namespace: testApp + "-prod",
		Release:   &domain.AppReleaseRef{Tag: "v5.0.0"},
	})

	_, err := Promote(context.Background(), store, PromoteRequest{
		ProjectName: testProject,
		AppName:     testApp,
		FromEnv:     "prod",
		ToEnv:       "staging",
	})

	if err == nil {
		t.Fatal("expected error: prod → staging violates promotion chain order")
	}
}

func TestPromote_SameEnvironmentFails(t *testing.T) {
	store := newMemStore()
	seedStore(store, stagingEnv("v1.0.0"))

	_, err := Promote(context.Background(), store, PromoteRequest{
		ProjectName: testProject,
		AppName:     testApp,
		FromEnv:     "staging",
		ToEnv:       "staging",
	})

	if err == nil {
		t.Fatal("expected error when FromEnv == ToEnv")
	}
}

func TestPromote_MissingFields(t *testing.T) {
	store := newMemStore()

	cases := []PromoteRequest{
		{AppName: testApp, FromEnv: "staging", ToEnv: "prod"},
		{ProjectName: testProject, FromEnv: "staging", ToEnv: "prod"},
		{ProjectName: testProject, AppName: testApp, ToEnv: "prod"},
		{ProjectName: testProject, AppName: testApp, FromEnv: "staging"},
	}

	for _, req := range cases {
		_, err := Promote(context.Background(), store, req)
		if err == nil {
			t.Errorf("expected error for incomplete request %+v", req)
		}
	}
}
