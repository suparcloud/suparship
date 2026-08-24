# Templates

A suparship **template** is a Helm chart served from the template registry,
optionally carrying platform-authored metadata (`template.yaml`). When a
developer picks a template, suparship creates an **app** backed by that
chart. The template does not create a raw Kubernetes Service or Deployment
directly — it creates an app that suparship then renders into the appropriate
GitOps manifests.

Every template is a plain, bring-your-own Helm chart — there are no built-in
templates and no platform values schema. See
[Bring your own Helm charts](byo-charts.md) for the chart-side story and the
production-ready starters in [`examples/charts/`](../examples/charts/). This
page is the reference for the registry and the optional `template.yaml`
metadata layer.

## Day one: register a template source

A fresh install has an empty template gallery — every template arrives
through the registry (a `git` / `gitcharts` / `oci` source) or a one-off BYO
chart upload, and is served live from cluster ConfigMaps. Under
**Settings → Templates → Sources**, add a source; the copy-paste starter is
this repo's example catalog as a **Git charts repo** (`gitcharts`):

- Repo URL: `https://github.com/suparcloud/suparship.git`
- Path: `examples/charts`

Sync it and `web`, `worker`, `cronjob`, `job`, `gateway`, and `postgres`
appear as templates. (The dev loop does this automatically: the Tilt
`seed-templates` resource runs `hack/dev/seed-example-charts.sh`, which
mirrors `examples/charts/` into the local Gitea and registers it as a
`gitcharts` source.)

## What a template creates

A template creates an **app** — the primary user-facing deployment object in suparship. See [ADR-0001](adr/0001-app-as-primary-deployment-object.md) for the rationale behind the app-first model.

An app:

- belongs to a **project** (the team/product boundary)
- runs across one or more **environments** (`staging`, `prod`, ephemeral `preview-*`)
- deploys one chart directly (the common case, zero components), or — as a
  **composed app** — declares several **components**, each pinned to its own
  template with its own values overlay

Templates declare no components: components are **user-declared on the app**.
A single-chart app has none; a composed app lists each component with its own
template ref and Values. See
[templates-components.md](templates-components.md) for the app-component
model.

```
org
└── project
    └── app              ← what the template creates
        ├── environment  (staging | prod | preview-*)
        └── component    (composed apps only — each with its own template + values)
```

## Configuring apps: values, not form inputs

Apps are configured through **Helm values overlays** in the chart's own
shape — there is no input/mapping layer between the form and the chart. The
legacy `inputs` / `advancedInputs` / `mappings` / `presets` blocks are
retired; the developer-facing form is declared with
[`developerValues`](#developervalues--the-values-projection), and
platform-engineer defaults ship as `defaultValues` / `envValues` /
`previewDefaultValues` overlays.

Secret inputs remain a separate category — see [Secrets](#secrets) below.

## Environment overrides in the app model

When an app is created from a template, two default environment instances are provisioned: `staging` and `prod`. Each instance is an `AppEnvironment` record that tracks:

- the **desired release** (`release` — image, tag, commit)
- the **live runtime status** (`status` — phase, replicas, available)
- the **Kubernetes namespace** (`namespace`)
- the **ingress URLs** (`urls`)

Environment instances are created with `StatusNotDeployed` and a `nil` release; they become active once a release is promoted or synced via ArgoCD.

### Namespace convention

| Instance | Namespace pattern |
|----------|-------------------|
| Stable (`staging`, `prod`) | `{appName}-{envName}` — e.g. `myapp-staging` |
| Preview | `{appName}-preview-{previewName}` — e.g. `myapp-preview-pr-42` |

### Environment-level overrides (future)

In a future iteration, environment-specific overrides (e.g. replica count, resource size, feature flags) will be stored on the `AppEnvironment` record. For MVP these are driven by the template defaults and values set at app-creation time.

## Secrets

Secret values **must never** be stored in Git or in the app spec. suparship enforces this through the `secretInputs` block in the template schema.

### Secret input declaration

```yaml
secretInputs:
  - name: database_url
    title: Database URL
    description: >-
      Connection string for the primary database. Injected as an
      environment variable from a Kubernetes Secret.
    secretRef: db-credentials.url
```

Each secret input carries a `secretRef` in `secret-name.key` format. This is the reference that suparship stores in the app spec (`AppSecretRef.SecretRef`). The actual secret value is resolved at runtime by the cluster from the named Kubernetes Secret — it is never written to Git or stored in the suparship database.

### Reference format

```
ref:my-secret.MY_KEY
```

The target Kubernetes Secret is expected to exist in the app's environment namespace (`{appName}-{envName}`) and is provisioned out-of-band (manually, via ExternalSecrets, or via Vault — all future integrations).

### What is never allowed

- Literal secret values in `template.yaml`
- Literal secret values in app `values` maps
- Literal secret values in GitOps-committed manifests
- Env vars with plaintext passwords in Helm `values.yaml`

## Template schema reference

A `template.yaml` file must conform to the following structure:

```yaml
apiVersion: suparship.io/v1alpha1
kind: Template
metadata:
  name: <dns-label>          # template identifier
  version: "<semver>"        # e.g. "1.0.0"
spec:
  title: <string>            # human-readable name shown in UI
  description: <string>      # optional one-paragraph description
  category: <string>         # catalog metadata (e.g. web | worker | cron | database)
  engine:
    type: helm               # only "helm" is supported in MVP
    chart: ./chart           # path relative to the template directory
  # Platform-Engineer-authored Helm values overlays, layered below the
  # developer's own overrides. Free-form; string leaves may use
  # ((platform.*)) / ((vars.*)) tokens resolved at publish.
  defaultValues: {...}       # applies to every environment
  envValues: {...}           # per environment name, after defaultValues
  previewDefaultValues: {...}  # previews only
  developerValues: [...]     # the values projection a developer sees + edits
  images: [...]              # declared image slots for external-CD (Kargo) wiring
  deliveryMode: <string>     # pipeline (default) | direct
  secretInputs: [...]        # secret-reference parameters (no literal values)
  inputs: [...]              # RETIRED — superseded by developerValues
  advancedInputs: [...]      # RETIRED
  mappings: {...}            # RETIRED
  presets: [...]             # RETIRED
```

Org platform engineers can layer their own `defaultValues` / `envValues` /
`clusterValues` / `previewDefaultValues` on top of a template without forking it,
via `PUT /api/v1/templates/{name}/overrides`. Those are stored separately from the
template, so an external sync can't clobber them. `clusterValues` (keyed by cluster
ref) exists only at that org level, not in `template.yaml`.

### `developerValues` — the values projection

A chart plus its platform overlays can carry far more than a developer should have
to read. `developerValues` declares the small, ordered subset that is theirs:

```yaml
  developerValues:
    - path: image.repository                  # dotted Helm values path — the CHART's own shape
      title: Image Repository
      type: string                            # string | number | boolean | enum
      required: true
      description: Container image, e.g. ghcr.io/org/app
    - path: containerPort
      title: Port
      type: number
    - path: schedule                          # e.g. for a cronjob chart
      title: Schedule
      type: string
```

The app-creation editor seeds from exactly these paths, prefilled with each key's
current effective value. **Required** entries (and any whose effective value is
empty) are seeded live; the rest are seeded **commented out** showing what they
inherit. Only what the developer actually writes is saved, so an untouched key keeps
tracking the chart/platform default rather than being frozen into the app.

Declaring none keeps the previous behaviour: the editor seeds from the full platform
base.

This is a **view, not a permission boundary** — the editor offers a "Show all
platform values" escape hatch and the API still accepts any key.

Operators can curate the projection for a read-only synced/built-in template with
`PATCH /api/v1/templates/{name}` (`developerValues`); it is stored sync-safe and
REPLACES the template's own list.

`path` is dotted, so it cannot express a key containing a dot — the same constraint
`images[].tagKey` carries. A path resolving to a map projects that whole subtree.

### Validation rules enforced at load time

- `apiVersion` must be `suparship.io/v1alpha1`
- `kind` must be `Template`
- `metadata.name` and `metadata.version` are required
- `spec.title`, `spec.category`, and `spec.engine.type` are required
- `developerValues[].path` is required and unique; an `enum` entry needs at least
  one `options` entry; `min` must not exceed `max`; `pattern` must compile
- `images[]` entries need `name`, `repository`, and `tagKey`; names are unique
- `secretRef` fields must be in `secret-name.key` format

## The example charts catalog

[`examples/charts/`](../examples/charts/) is the reference catalog — plain
Helm charts registered like any other source (see
[Day one](#day-one-register-a-template-source)). Highlights beyond the table
in [`examples/charts/README.md`](../examples/charts/README.md):

- **`web`** — Deployment, Service, optional Ingress **or** Gateway API
  HTTPRoute, optional HPA. Production hardening built in: zero-downtime
  `RollingUpdate` (surge 1 / unavailable 0), a `preStop` sleep so rollouts
  don't 502 while load balancers drop pods from rotation, and a default-on
  `PodDisruptionBudget` (`maxUnavailable: 1`, so single-replica apps still
  drain).
- **`worker`** — a long-running background worker: no Service, no Ingress, no
  ports. Set the entrypoint via `command`/`args` in the values overlay when
  the image's own entrypoint isn't the worker loop. Shutdown is a drain:
  SIGTERM, then a 60s grace budget (`terminationGracePeriodSeconds`).
- **`cronjob`** — a task on a cron schedule (`schedule` in values). Unlike
  `job` (a one-shot ArgoCD PreSync hook that gates a release), `cronjob` runs
  independent of deploys. Defaults chosen for safety: `concurrencyPolicy:
  Forbid`, missed runs skipped after 300s rather than fired late, failed runs
  kept for debugging (`failedJobsHistoryLimit: 3`). The platform suspend flag
  maps onto `CronJob.spec.suspend`, so suspending the app pauses the schedule
  without deleting anything.

## Disabling a template

Org admins can retire any template — including read-only synced ones — with
`PATCH /api/v1/templates/{name}` `{"disabled": true}` (or the Disable button
on the template detail page). A disabled template stays listed (marked) so it
can be found and re-enabled, but the create flow doesn't offer it and the
server refuses new apps from it with a 422. **Existing apps are untouched**:
they pin chart versions, not gallery entries, and keep publishing/editing/
upgrading. The flag lives in the template's sync-safe override, so re-syncs
can't resurrect a retired template.

## Writing a new template

1. Put your Helm chart in the repo your chart source scans (or package it as
   a `.tgz` for **Templates → Import**).
2. Optionally add a `template.yaml` next to the chart, following the schema
   above, to ship curated metadata (overlays, `developerValues`, `images`)
   with it. Without one, the chart imports with its own `values.yaml` as the
   base and everything is curated from the UI.
3. Test loading with `go test ./internal/tpl/...` if you're developing
   against this repo; otherwise push and hit **Sync now** on the source.

> **Tip:** Keep the developer-facing surface small. A `developerValues`
> projection with a handful of well-titled paths beats exposing the whole
> values document.

## Roadmap

Captured here so they aren't lost between sessions. Order is rough priority,
not dependency.

### Already shipped

- **BYO Helm chart wizard** — operators upload a `.tgz` via `/templates/import`; the chart is introspected (`Chart.yaml` + `values.schema.json`, with `values.yaml` fallback) and turned into a starter `template.yaml` they can edit before saving. Backend in `internal/tpl/chartimport`, UI at `/templates/import`.
- **External chart registries** — Git-hosted chart libraries are pulled and indexed on a configurable interval (`SUPARSHIP_TEMPLATE_SYNC_INTERVAL`, default 5m). Backend in `internal/tpl/registrysync`, UI at `/templates/sources`.

### Env vars from template defaults

Two natural extensions of the values overlays:

- **Render-into-envconfig defaults** — let a template declare a value that materialises into the merged env-config map (org → env-type → project → app → app-env → cluster), so a chart can declare `LOG_LEVEL` once and bind it across scopes.
- **`${envvar:KEY}` references in defaults** — resolve at publish time so a template default can read an upper-scope env var.

**Before implementing:** decide whether to surface these in two distinct categories (`buildtime` vs `runtime`) or stay flat. Mixing the two paths under one shape will confuse operators.

### Catalog UX

The gallery is currently a flat grid. Worth adding:

- Search + filter by `category` and a new `tags: []` field on `TemplateSpec`.
- Per-project "starred" templates (separate ConfigMap or annotation on the project).
- Version history — list previous `metadata.version` values when the same template name is reimported.
- Deprecation badge — new boolean `deprecated` + optional `deprecatedMessage` on `TemplateSpec`.
- Render `README.md` from the chart bundle on the template-detail page (we already store `chart.tgz` in the template ConfigMap; just untar and convert).

### Versioning + upgrades

**Where an upgrade is applied: at component level, mirrored to the app.** Every
component carries its own `template: {name, version}` pin (`ComponentSpec.Template`),
because a composed app mixes charts — api→web, worker→worker,
migrate→job — and two components can even sit on different versions of the same
template. That per-component pin is what the composed publisher renders from.
`AppSpec.Template` is the mirror of the primary component, and it is what the
*single-source* path (a 0- or 1-component app) renders from, so both are written
together via `AppSpec.SyncPrimaryTemplate()`. Never author the app-level pin on
its own.

`POST /api/v1/projects/{p}/apps/{a}/upgrade-template` takes either shape:

```jsonc
{"version": "2.0.0"}                            // the app's PRIMARY template:
                                                // every component on that template
                                                // moves; others are returned in
                                                // "skipped"
{"components": {"api": "2.0.0", "web": "1.4.0"}} // per component, each validated
                                                // against ITS OWN template
```

Both forms are atomic: every target version is validated before anything is
written, then one save + one publish, and a publish failure restores every pin.
`GET .../apps/{a}` reports `components[].templateVersion` / `latestVersion` /
`upgradeAvailable`, plus app-level `upgradesAvailable` and `templateVersions`
(archived versions per template, newest first) so the picker needs no extra calls.

Note an editing invariant: a component PATCH that omits `template.version`
*preserves* the stored pin rather than re-pinning to the registry's current
version. Only an explicit version, a brand-new component, or a retemplate onto a
different chart lands on latest.

Still open:

- Generate a dry-run diff — render Helm values before/after the version bump so
  the operator sees what changes before approving. The dangerous case is silent:
  a values key the new chart renamed or removed leaves the developer's overlay
  inert and deploys the chart default instead. `kube.LoadChartBundleVersion` +
  `chartimport.ParseArchive` already give both sides of that comparison.
- Schema-migration rules for `developerValues` paths that change between
  template versions (rename/removal/type-change).
- **Only the chart is pinned.** Template *metadata* — `defaultValues`,
  `envValues`, `previewDefaultValues`, `images`, `suspendKey` — is resolved
  by NAME at latest on every publish
  (`server.ResolveTemplates`), so a pinned app already tracks the newest
  template's overlays. Making that version-aware needs a
  `kube.LoadTemplateVersion` and is a behaviour change for every running app.
- **Archives are not immutable.** `kube.SaveTemplate` overwrites the per-version
  archive, and `PATCH /templates/{name}` re-saves without bumping — so version X's
  bytes can change under a pinned app. Refuse to overwrite a differing archive.

### Smaller follow-ups

- **Validation hooks** — let templates declare a CEL/Go validator for cross-value rules (e.g. `enable_db=true ⇒ db_size required`). Today `required`/`min`/`max`/`pattern` only validate one `developerValues` field at a time.
- **Test fixtures** — let a template ship example-values YAMLs + golden rendered manifests so CI verifies the chart still renders cleanly. Plug into `go test ./internal/tpl/...` (`task charts:verify` covers the in-repo example charts already).
- **Engine pluralism** — `engine.type` already hints at this. Kustomize, raw-manifest, and Crossplane Composition engines are credible alternatives for non-Helm shops.
- **Sync error persistence** — periodic-sync errors are logged but not stored. Add per-source error fields to the registry document so `/templates/sources` can show "last sync failed: <reason>" days later.
