# Upgrading suparShip

How to move a running install to a newer release safely, and the per-version
migration notes. Read the notes for **every version between your current one
and the target** before upgrading.

## Versioning model

suparShip carries three version numbers; `suparship version` prints all of them:

| Version | What it tracks | Bumps when |
|---|---|---|
| **release** (chart `appVersion` / image tag, e.g. `v0.1.0`) | the build | every release |
| **config-schema** (`version.Schema`, e.g. `1`) | the persisted org-config format | the org ConfigMap format changes incompatibly |
| **generator** (`version.Generator`, e.g. `v0.1.0`) | the emitted GitOps manifest/label contract | generated manifests or the `…/generator-version` label change |

The config-schema version is stamped into the org ConfigMap on every save and
**checked on startup**: the server logs an advisory when the stored schema is
older (upgrade — review notes), unversioned (pre-`v1` install), or newer than
the binary (you're running an older suparShip than wrote the config — a
downgrade risk). It does not auto-migrate; it points you here.

Generated manifests carry `…/generator-version` so you (or a migration tool)
can tell which generator produced a file in the GitOps repo.

## Upgrade procedure

1. **Back up first.** `suparship backup -o suparship-backup.yaml` (store it
   encrypted — it contains Secrets). See [install.md](install.md) recovery.
2. **Read the migration notes** below for your jump.
3. **Bump the chart and apply:** `helm upgrade suparship ./charts/suparship -n suparship-system --reuse-values` (or your values file).
4. **Watch startup logs** for the `org config schema check` line and any
   migration warnings.
5. **Re-publish if the generator version changed** — trigger a Sync to Git on
   apps so manifests are regenerated to the new contract, then let ArgoCD sync.
6. **Verify** the onboarding Platform-setup checklist is still all-green and a
   test app stays Healthy.

If anything looks wrong, restore the backup (`suparship restore -i
suparship-backup.yaml`) and roll the chart back.

---

## Migration notes

### Unreleased (next)

These changes shipped on `main` after the initial `v0.1.0` cut and **require
one-time operator action** on the 1Password secret backend. The k8s secret
backend is unaffected. (config-schema unchanged at `v1` — the changes are
operational, and the config decoder tolerates the removed fields.)

**Per-cluster ArgoCD Application names (`{project}-{app}-{cluster}` by default).**
ArgoCD Application names are now per-cluster and configurable via the org
ResourceNaming `argoAppName` pattern (Settings → Namespace Naming → ArgoCD
Application name pattern; tokens `{project}/{app}/{env}/{cluster}`, must contain
`{app}` and `{cluster}`). Previously single-cluster envs used `{project}-{app}-{env}`
and only multi-cluster fan-out appended the cluster; now every env names its
Application(s) per cluster, so adding a second cluster to an env never renames
the first cluster's Application.
- *Action:* none required — on startup the server **re-publishes every app**,
  writing the new per-cluster ApplicationSet names. ArgoCD will **delete the old
  `{project}-{app}-{env}` Application and create the new per-cluster one** (a
  one-time recreate; the workload is re-synced from git, not deleted). Status,
  deployment history, and diagnostics now resolve Applications by suparship
  identity labels, so they keep working across the rename. There is no
  cluster-less option (multi-cluster requires `{cluster}`); the closest to the
  old layout that keeps the env segment is `{project}-{app}-{env}-{cluster}`.

**0. Version-scoped chart layout (generator `v0.1.0` → `v0.2.0`).** Bundled
charts moved from `charts/{template}/` to `charts/{template}/{version}/`, and
each app's `app.yaml` gained a `chartPath` key the inline ApplicationSet now
sources (`charts/{{chartPath}}`). This lets two apps pin different versions of
the same template without colliding (and makes the existing template-upgrade
flow safe).
- *Action:* none — on startup the server **re-publishes every app**, writing
  the new versioned chart dirs + `chartPath`. Until that runs, an app whose
  `app.yaml` predates this change has an unresolved `{{chartPath}}`; the
  auto-republish closes the window. The old version-less `charts/{template}/`
  directories become **harmless orphans** — safe to delete from the gitops repo
  once every app shows the new `charts/{template}/{version}/` layout.
- Apps can now also have their template input **Values** + display
  name/description edited in place (App → Config → Edit), re-published like
  create. No migration needed.

**1. Per-cluster vaults removed — cluster secrets now live in the env vault.**
Cluster-scope overrides moved from a dedicated `suparship-secrets-cluster-<c>`
vault into items inside the env vault (`<app>-cluster-<c>` in
`suparship-secrets-env-<e>`), keyed per `(env, cluster)`.
- *Action:* none required if you weren't using cluster-scope secrets. If you
  were, re-enter those overrides under the env in Cluster Settings → Overrides;
  the old per-cluster vault is no longer read.

**2. One ClusterSecretStore + one Connect token per cluster (was one per
vault).** Each cluster now runs a single `suparship-store` listing every vault
it reads, authenticated by **one** Connect token (covering the global + its env
vaults) instead of a sealed token per vault.
- *Action (required):* in the 1Password console, issue **one** Connect token
  per cluster with access to the global vault plus that cluster's env vaults,
  and paste it under Settings → Secrets Backend → **Cluster Connect Tokens**.
  The old per-vault tokens are ignored; the per-cluster store shows
  "pending token" until you do. After deploying the build, the next seal
  collapses `_secret-stores/<cluster>/` from the old per-scope files down to
  `sealed-token.yaml` + `store.yaml` (legacy files are auto-pruned).
- *Recovery:* if you wiped the GitOps `_secret-stores/` tree, restarting the
  server now re-seals each cluster from its stashed token automatically (no
  re-paste needed) — see startup self-heal.

**Also in this range (no action needed):** app status now surfaces ArgoCD/ESO
failure reasons; cluster API server / Connect URL / registry URL are validated
at save time; project deletion is two-phase (no more stuck-Terminating apps
from the delete path), and stuck apps can be unstuck from the dashboard.

### v0.1.0 — initial release

Baseline. New installs start here; `suparship admin bootstrap` creates the
admin credential (see [install.md](install.md)).
