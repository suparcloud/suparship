package main

import (
	"context"
	"log/slog"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/secrets"
)

func migrateTestOrg() *rbac.Org {
	return &rbac.Org{
		Environments: []rbac.OrgEnvironment{
			{Name: "staging", ClusterRefs: []string{"c-staging"}},
			{Name: "prod", ClusterRefs: []string{"c-prod"}},
		},
	}
}

func migrateTestWorld() ([]*project.Project, map[string][]*domain.App, map[string][]*domain.Stack) {
	projects := []*project.Project{{Metadata: project.ProjectMeta{Name: "acme"}}}
	apps := map[string][]*domain.App{"acme": {{Name: "web", ProjectName: "acme"}}}
	stacks := map[string][]*domain.Stack{}
	return projects, apps, stacks
}

func TestMigrationTargets_CoversAllBands(t *testing.T) {
	projects, apps, stacks := migrateTestWorld()
	targets := migrationTargets(migrateTestOrg(), projects, apps, stacks)

	want := map[string]bool{
		// Shared tier, global vault.
		"suparship-secrets-global/shared-global":        false,
		"suparship-secrets-global/shared-project-acme":  false,
		// Shared tier, env vaults (incl. cluster overrides + preview band).
		"suparship-secrets-env-staging/shared-env-staging":       false,
		"suparship-secrets-env-staging/shared-cluster-c-staging": false,
		"suparship-secrets-env-staging/shared-env-preview":       false,
		"suparship-secrets-env-prod/shared-project-acme-env-prod": false,
		// App tier, project-qualified.
		"suparship-secrets-global/acme-web-global":                false,
		"suparship-secrets-env-prod/acme-web-env-prod":            false,
		"suparship-secrets-env-prod/acme-web-cluster-c-prod":      false,
		"suparship-secrets-env-staging/acme-web-env-preview":      false,
	}
	for _, tgt := range targets {
		if _, tracked := want[tgt.label()]; tracked {
			want[tgt.label()] = true
		}
	}
	for label, seen := range want {
		if !seen {
			t.Errorf("expected target %s not enumerated", label)
		}
	}
}

func TestMigrateItems_CopiesAndMerges(t *testing.T) {
	ctx := context.Background()
	src := secrets.NewMemVaultStore()
	dst := secrets.NewMemVaultStore()
	logger := slog.Default()

	appScope := secrets.EnvScope("staging").WithProject("acme")
	if err := src.Upsert(ctx, secrets.GlobalScope(), secrets.TierShared, "", map[string][]byte{
		"G": []byte("1"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := src.Upsert(ctx, appScope, secrets.TierApp, "web", map[string][]byte{
		"DB_URL": []byte("postgres://src"),
	}); err != nil {
		t.Fatal(err)
	}
	// Destination has a pre-existing sibling key that must survive the merge.
	if err := dst.Upsert(ctx, appScope, secrets.TierApp, "web", map[string][]byte{
		"KEEP": []byte("dst"),
	}); err != nil {
		t.Fatal(err)
	}

	projects, apps, stacks := migrateTestWorld()
	targets := migrationTargets(migrateTestOrg(), projects, apps, stacks)

	// Dry-run writes nothing.
	res := migrateItems(ctx, src, dst, targets, true, logger)
	if res.Migrated != 2 || res.Keys != 2 || res.Failures != 0 {
		t.Fatalf("dry-run result = %+v", res)
	}
	if keys, _ := dst.ListKeys(ctx, secrets.GlobalScope(), secrets.TierShared, ""); len(keys) != 0 {
		t.Fatal("dry run wrote to the destination")
	}

	// Real run copies, merges, and leaves the source untouched.
	res = migrateItems(ctx, src, dst, targets, false, logger)
	if res.Migrated != 2 || res.Failures != 0 {
		t.Fatalf("result = %+v", res)
	}
	got, _ := dst.ExportItem(ctx, appScope, secrets.TierApp, "web")
	if string(got["DB_URL"]) != "postgres://src" || string(got["KEEP"]) != "dst" {
		t.Errorf("merged destination = %v", got)
	}
	srcData, _ := src.ExportItem(ctx, appScope, secrets.TierApp, "web")
	if len(srcData) != 1 || string(srcData["DB_URL"]) != "postgres://src" {
		t.Errorf("source was modified: %v", srcData)
	}

	// Idempotent: a re-run converges to the same state.
	res = migrateItems(ctx, src, dst, targets, false, logger)
	if res.Failures != 0 {
		t.Errorf("re-run failures = %d", res.Failures)
	}
	again, _ := dst.ExportItem(ctx, appScope, secrets.TierApp, "web")
	if len(again) != 2 {
		t.Errorf("re-run changed the destination: %v", again)
	}
}

// An item existing on the source with no keys must be ENSURED on the target,
// not skipped — an ExternalSecret may already reference it, and ESO errors on
// extract of a missing item.
func TestMigrateItems_EnsuresEmptyItems(t *testing.T) {
	ctx := context.Background()
	src := secrets.NewMemVaultStore()
	dst := secrets.NewMemVaultStore()
	scope := secrets.EnvScope("staging")
	if err := src.EnsureItem(ctx, scope, secrets.TierShared, ""); err != nil {
		t.Fatal(err)
	}

	targets := []migrationTarget{{scope: scope, tier: secrets.TierShared}}
	res := migrateItems(ctx, src, dst, targets, false, slog.Default())
	if res.Empty != 1 || res.Migrated != 0 {
		t.Fatalf("result = %+v", res)
	}
	// Present on the destination (exists, no keys).
	if data, _ := dst.ExportItem(ctx, scope, secrets.TierShared, ""); data == nil {
		t.Error("empty item was not ensured on the destination")
	}
}

func TestFilterTargets(t *testing.T) {
	projects, apps, stacks := migrateTestWorld()
	all := migrationTargets(migrateTestOrg(), projects, apps, stacks)

	globals := filterTargets(all, "global")
	envs := filterTargets(all, "env")
	if len(globals)+len(envs) != len(all) {
		t.Errorf("bands overlap or drop targets: %d + %d != %d", len(globals), len(envs), len(all))
	}
	for _, tgt := range globals {
		if got := secrets.VaultName(tgt.scope); got != secrets.GlobalVaultName() {
			t.Errorf("global band target in vault %s", got)
		}
	}
	for _, tgt := range envs {
		if got := secrets.VaultName(tgt.scope); got == secrets.GlobalVaultName() {
			t.Errorf("env band target %s in the global vault", tgt.label())
		}
	}
	if got := len(filterTargets(all, "all")); got != len(all) {
		t.Errorf("scope all filtered: %d != %d", got, len(all))
	}
}

func TestValidateMigrateBackends(t *testing.T) {
	if err := validateMigrateBackends("k8s", "vault"); err != nil {
		t.Errorf("k8s→vault: %v", err)
	}
	if err := validateMigrateBackends("k8s", "k8s"); err == nil {
		t.Error("same-backend migrate should be rejected")
	}
	if err := validateMigrateBackends("nope", "vault"); err == nil {
		t.Error("unknown source should be rejected")
	}
	if err := validateMigrateBackends("vault", "aws"); err == nil {
		t.Error("unknown destination should be rejected")
	}
}
