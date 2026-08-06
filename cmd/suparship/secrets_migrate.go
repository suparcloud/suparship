package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/secrets"
)

// secrets_migrate.go — cross-backend secret migration (`suparship secrets
// migrate --from X --to Y`). Copies every item the org could own from one
// backend's storage into another's, so an operator can move k8s → vault (or
// any pair) without re-entering values.
//
// Values flow source → process memory → target and nowhere else: the source
// must implement secrets.ItemExporter (the package's only value-returning
// read), nothing is logged beyond item names and counts, and there is no HTTP
// surface. Additive and idempotent: the source is never modified, the target
// is Upserted (merge), and a re-run converges. Deliberately NOT run at boot —
// unlike the item-rename migration this crosses a trust boundary between two
// credentialed systems, and an operator chooses that moment.

// migrationTarget is one (scope, tier, app) item to migrate.
type migrationTarget struct {
	scope secrets.Scope
	tier  secrets.Tier
	app   string
}

// scopeBand classifies a target for the --scope filter: items living in the
// global vault vs items living in an env vault.
func (t migrationTarget) scopeBand() string {
	switch t.scope.Kind {
	case secrets.ScopeGlobal, secrets.ScopeProject, secrets.ScopeStack:
		return "global"
	default:
		return "env"
	}
}

// label renders the target for human output — the vault-qualified item name,
// which is safe to print (resource names, never values or key names).
func (t migrationTarget) label() string {
	return secrets.VaultName(t.scope) + "/" + secrets.ItemName(t.scope, t.tier, t.app)
}

// migrationTargets enumerates every item the org could own, shared tier and
// app tier, across global/env/cluster/project/stack/preview-band scopes. It
// over-enumerates deliberately — migration is a no-op for absent items — so it
// needs no knowledge of which items exist. Per-PR preview overrides
// (ScopePreviewPR) are excluded: they are transient CI-written values,
// re-created on the next push, and enumerating live PRs is not worth coupling
// the migration to the preview store.
func migrationTargets(
	org *rbac.Org,
	projects []*project.Project,
	appsByProject map[string][]*domain.App,
	stacksByProject map[string][]*domain.Stack,
) []migrationTarget {
	var out []migrationTarget
	shared := func(scope secrets.Scope) {
		out = append(out, migrationTarget{scope: scope, tier: secrets.TierShared})
	}
	app := func(scope secrets.Scope, proj, name string) {
		out = append(out, migrationTarget{scope: scope.WithProject(proj), tier: secrets.TierApp, app: name})
	}

	// Global-vault shared items: org-global, per-project, per-stack.
	shared(secrets.GlobalScope())
	for _, p := range projects {
		shared(secrets.ProjectScope(p.Metadata.Name))
		for _, s := range stacksByProject[p.Metadata.Name] {
			shared(secrets.StackScope(p.Metadata.Name, s.Name))
		}
	}

	// Env-vault shared items: per-env, per-(env,cluster), per-(project,env),
	// per-(stack,env), plus the env's shared preview band.
	for _, e := range org.Environments {
		shared(secrets.EnvScope(e.Name))
		shared(secrets.PreviewScope(e.Name))
		for _, cluster := range e.ClusterRefs {
			if cluster != "" {
				shared(secrets.ClusterScope(e.Name, cluster))
			}
		}
		for _, p := range projects {
			shared(secrets.ProjectEnvScope(p.Metadata.Name, e.Name))
			for _, s := range stacksByProject[p.Metadata.Name] {
				shared(secrets.StackEnvScope(p.Metadata.Name, s.Name, e.Name))
			}
		}
	}

	// App-tier items, project-qualified: global, per-env, per-(env,cluster),
	// plus the app's preview band in each env (its base env holds the real one;
	// the others are no-ops).
	for _, p := range projects {
		for _, a := range appsByProject[p.Metadata.Name] {
			app(secrets.GlobalScope(), p.Metadata.Name, a.Name)
			for _, e := range org.Environments {
				app(secrets.EnvScope(e.Name), p.Metadata.Name, a.Name)
				app(secrets.PreviewScope(e.Name), p.Metadata.Name, a.Name)
				for _, cluster := range e.ClusterRefs {
					if cluster != "" {
						app(secrets.ClusterScope(e.Name, cluster), p.Metadata.Name, a.Name)
					}
				}
			}
		}
	}
	return out
}

// migrationResult summarizes one run.
type migrationResult struct {
	// Migrated counts items that existed on the source and were written (or,
	// in dry-run, would be). Keys is their total key count. Empty counts
	// source items that exist with no keys (ensured on the target so ESO refs
	// resolve). Failures counts per-item errors.
	Migrated, Keys, Empty, Failures int
	// Labels lists the vault-qualified names of migrated items (dry-run report).
	Labels []string
}

// migrateItems copies each target present on the source into the destination.
// Merge semantics on the destination; the source is never written.
func migrateItems(
	ctx context.Context,
	from secrets.ItemExporter,
	to secrets.VaultStore,
	targets []migrationTarget,
	dryRun bool,
	logger *slog.Logger,
) migrationResult {
	var res migrationResult
	for _, t := range targets {
		data, err := from.ExportItem(ctx, t.scope, t.tier, t.app)
		if err != nil {
			logger.Warn("migrate: export failed", "item", t.label(), "err", err)
			res.Failures++
			continue
		}
		if data == nil {
			continue // absent on the source
		}
		res.Labels = append(res.Labels, t.label())
		if len(data) == 0 {
			res.Empty++
			if !dryRun {
				if err := to.EnsureItem(ctx, t.scope, t.tier, t.app); err != nil {
					logger.Warn("migrate: ensure failed", "item", t.label(), "err", err)
					res.Failures++
				}
			}
			continue
		}
		res.Migrated++
		res.Keys += len(data)
		if dryRun {
			continue
		}
		if err := to.Upsert(ctx, t.scope, t.tier, t.app, data); err != nil {
			logger.Warn("migrate: upsert failed", "item", t.label(), "err", err)
			res.Failures++
			res.Migrated--
			res.Keys -= len(data)
		}
	}
	return res
}

// filterTargets applies the --scope flag: "global", "env", or "all".
func filterTargets(targets []migrationTarget, scope string) []migrationTarget {
	if scope == "" || scope == "all" {
		return targets
	}
	var out []migrationTarget
	for _, t := range targets {
		if t.scopeBand() == scope {
			out = append(out, t)
		}
	}
	return out
}

// validateMigrateBackends checks the pair makes sense before any store is built.
func validateMigrateBackends(from, to string) error {
	fb, tb := secrets.BackendType(from), secrets.BackendType(to)
	if !secrets.ValidBackendTypes[fb] {
		return fmt.Errorf("unknown --from backend %q", from)
	}
	if !secrets.ValidBackendTypes[tb] {
		return fmt.Errorf("unknown --to backend %q", to)
	}
	if fb == tb {
		return fmt.Errorf("--from and --to are both %q — nothing to migrate", from)
	}
	return nil
}
