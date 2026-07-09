package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/secrets"
)

// migAppStore embeds domain.AppStore (nil) and implements only ListApps, the one
// method the migration walk uses.
type migAppStore struct {
	domain.AppStore
	byProject map[string][]*domain.App
}

func (s *migAppStore) ListApps(_ context.Context, projectName string) ([]*domain.App, error) {
	return s.byProject[projectName], nil
}

// migProjectStore is a minimal project.Store returning a fixed project list.
type migProjectStore struct{ projects []*project.Project }

func (s *migProjectStore) List(context.Context) ([]*project.Project, error) {
	return s.projects, nil
}
func (s *migProjectStore) Get(context.Context, string) (*project.Project, error) { return nil, nil }
func (s *migProjectStore) Save(context.Context, *project.Project) error          { return nil }
func (s *migProjectStore) Delete(context.Context, string) error                  { return nil }

func migLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestCopyAndPruneLegacyAppItems is the end-to-end migration check on the mem
// backend: seed an app's legacy-named app-tier items (global + env + cluster),
// copy them to the project-qualified names, verify the new items carry the
// values, then prune and verify the legacy items are gone and the new ones stay.
func TestCopyAndPruneLegacyAppItems(t *testing.T) {
	ctx := context.Background()
	vault := secrets.NewMemVaultStore()

	const proj, app, env, cluster = "voiceai", "biglysales-tts", "production", "eks-aws"
	// Seed legacy items (project-less scopes → unqualified names).
	seed := map[secrets.Scope]map[string][]byte{
		secrets.GlobalScope():              {"GLOBAL_TOKEN": []byte("g")},
		secrets.EnvScope(env):              {"TTS_AUTH_BEARER_TOKEN": []byte("e")},
		secrets.ClusterScope(env, cluster): {"CLUSTER_KEY": []byte("c")},
	}
	for scope, data := range seed {
		if err := vault.Upsert(ctx, scope, secrets.TierApp, app, data); err != nil {
			t.Fatal(err)
		}
	}

	appStore := &migAppStore{byProject: map[string][]*domain.App{
		proj: {{Name: app, ProjectName: proj}},
	}}
	projectStore := &migProjectStore{projects: []*project.Project{
		{Metadata: project.ProjectMeta{Name: proj}},
	}}
	org := &rbac.Org{Environments: []rbac.OrgEnvironment{
		{Name: env, ClusterRefs: []string{cluster}},
	}}
	orgProvider := &fakeOrgStore{org: org}

	// Copy phase.
	if failures := copyLegacyAppItems(ctx, vault, appStore, projectStore, orgProvider, migLogger()); failures != 0 {
		t.Fatalf("copy reported %d failures", failures)
	}

	// New project-qualified items now carry the values.
	assertKey := func(scope secrets.Scope, wantKey string) {
		t.Helper()
		keys, _ := vault.ListKeys(ctx, scope.WithProject(proj), secrets.TierApp, app)
		if len(keys) != 1 || keys[0].Key != wantKey {
			t.Errorf("scope %v new item = %+v, want single key %q", scope.Kind, keys, wantKey)
		}
	}
	assertKey(secrets.GlobalScope(), "GLOBAL_TOKEN")
	assertKey(secrets.EnvScope(env), "TTS_AUTH_BEARER_TOKEN")
	assertKey(secrets.ClusterScope(env, cluster), "CLUSTER_KEY")

	// Idempotent: a second copy is a clean no-op.
	if failures := copyLegacyAppItems(ctx, vault, appStore, projectStore, orgProvider, migLogger()); failures != 0 {
		t.Fatalf("second copy reported %d failures", failures)
	}

	// Prune phase: legacy items removed, new items retained.
	names, failures := pruneLegacyAppItems(ctx, vault, appStore, projectStore, org, false, migLogger())
	if failures != 0 {
		t.Fatalf("prune reported %d failures", failures)
	}
	if len(names) == 0 {
		t.Fatal("prune reported no legacy items")
	}
	for scope := range seed {
		if legacy, _ := vault.ListKeys(ctx, scope, secrets.TierApp, app); len(legacy) != 0 {
			t.Errorf("legacy item for scope %v survived prune: %+v", scope.Kind, legacy)
		}
	}
	assertKey(secrets.GlobalScope(), "GLOBAL_TOKEN") // new item still intact
}
