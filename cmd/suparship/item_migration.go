package main

import (
	"context"
	"log/slog"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/secrets"
)

// item_migration.go — one-shot rename of app-tier secret vault items from the
// legacy unqualified name ({app}-{scopeSuffix}) to the project-qualified name
// ({project}-{app}-{scopeSuffix}). App names are unique only within a project,
// but app-tier items live in org-wide vaults (global vault; per-env vaults shared
// across projects), so the unqualified name let two same-named apps in different
// projects collide on one vault item. See the plan for the full rollout: copy at
// boot (this file, via copyLegacyAppItems) → republish (new names) → prune
// (prune-legacy-items CLI). The copy/delete run below the value-blind VaultStore
// via secrets.LegacyItemMigrator so values never surface.

// appItemTarget is one app-tier item to migrate: the scope's vault plus the
// legacy and project-qualified item names within it.
type appItemTarget struct {
	scope  secrets.Scope
	legacy string
	newer  string
}

// appItemTargets enumerates every app-tier item an app could own: its global
// item, one per org environment, and one per cluster bound to each env (cluster
// items live in the env vault). It over-enumerates deliberately — the copy/prune
// is a no-op when the legacy item is absent — so it needs no per-app knowledge of
// which items actually exist. Shared-tier items are excluded: they already carry
// the project (shared-project-*) or are legitimately org/env-wide.
func appItemTargets(app *domain.App, org *rbac.Org) []appItemTarget {
	project := app.ProjectName
	var out []appItemTarget
	add := func(base secrets.Scope) {
		out = append(out, appItemTarget{
			scope:  base,
			legacy: secrets.AppItemName(base, app.Name),
			newer:  secrets.AppItemName(base.WithProject(project), app.Name),
		})
	}
	add(secrets.GlobalScope())
	if org != nil {
		for _, e := range org.Environments {
			add(secrets.EnvScope(e.Name))
			for _, cluster := range e.ClusterRefs {
				if cluster != "" {
					add(secrets.ClusterScope(e.Name, cluster))
				}
			}
		}
	}
	return out
}

// forEachAppItem walks every app in every project and invokes fn for each
// app-tier item target. Returns the number of apps whose enumeration failed to
// load (not per-item errors — those are the caller's to count).
func forEachAppItem(
	ctx context.Context,
	appStore domain.AppStore,
	projectStore project.Store,
	org *rbac.Org,
	fn func(app *domain.App, t appItemTarget),
) (loadFailures int) {
	projects, err := projectStore.List(ctx)
	if err != nil {
		return 1
	}
	for _, p := range projects {
		apps, err := appStore.ListApps(ctx, p.Metadata.Name)
		if err != nil {
			loadFailures++
			continue
		}
		for _, app := range apps {
			for _, t := range appItemTargets(app, org) {
				fn(app, t)
			}
		}
	}
	return loadFailures
}

// copyLegacyAppItems copies every app's legacy-named app-tier items to their
// project-qualified names, so the republish that follows (emitting the new names)
// finds the items already present. Idempotent and copy-if-present: a missing
// legacy item or an already-copied item is a no-op. Returns the number of copy
// failures; a zero return means the whole fleet copied cleanly and the caller may
// record the migration marker. A nil migrator (no vault backend) is a no-op.
func copyLegacyAppItems(
	ctx context.Context,
	migrator secrets.LegacyItemMigrator,
	appStore domain.AppStore,
	projectStore project.Store,
	orgProvider rbac.OrgProvider,
	logger *slog.Logger,
) int {
	if migrator == nil || appStore == nil || projectStore == nil || orgProvider == nil {
		return 0
	}
	org, err := orgProvider.GetOrg(ctx)
	if err != nil || org == nil {
		logger.Warn("item migration: cannot load org; skipping copy", "err", err)
		return 1
	}
	failures := 0
	copied := 0
	loadFailures := forEachAppItem(ctx, appStore, projectStore, org, func(app *domain.App, t appItemTarget) {
		if t.legacy == t.newer {
			return // project empty (shouldn't happen for app tier) — nothing to rename
		}
		if err := migrator.CopyItem(ctx, t.scope, t.legacy, t.newer); err != nil {
			logger.Warn("item migration: copy failed",
				"app", app.Name, "project", app.ProjectName, "from", t.legacy, "to", t.newer, "err", err)
			failures++
			return
		}
		copied++
	})
	failures += loadFailures
	logger.Info("item migration: copy complete", "targets_copied", copied, "failures", failures)
	return failures
}

// pruneLegacyAppItems deletes every app's legacy-named app-tier items. Run only
// after the copy + republish cutover is verified (see the prune-legacy-items
// CLI). When dryRun is true it deletes nothing and returns the names it would
// remove. Returns the legacy names acted on and the number of delete failures.
func pruneLegacyAppItems(
	ctx context.Context,
	migrator secrets.LegacyItemMigrator,
	appStore domain.AppStore,
	projectStore project.Store,
	org *rbac.Org,
	dryRun bool,
	logger *slog.Logger,
) (names []string, failures int) {
	forEachAppItem(ctx, appStore, projectStore, org, func(app *domain.App, t appItemTarget) {
		if t.legacy == t.newer {
			return
		}
		names = append(names, t.legacy)
		if dryRun {
			return
		}
		if err := migrator.DeleteItem(ctx, t.scope, t.legacy); err != nil {
			logger.Warn("prune: delete failed",
				"app", app.Name, "project", app.ProjectName, "item", t.legacy, "err", err)
			failures++
		}
	})
	return names, failures
}
