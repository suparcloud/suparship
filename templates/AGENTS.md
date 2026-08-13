# AGENTS.md — suparship templates repo

Guidance for AI coding agents (Claude Code, Cursor, …) working in a
suparship *external templates repo*. Read this before editing template
or chart files.

## What this repo is

A library of suparship **templates**. A template is a `template.yaml`
plus an optional Helm chart. suparship's `registrysync` engine clones
this repo on a schedule (default 5m), packages each chart it finds, and
saves the result as a cluster-side ConfigMap. End users pick a template
in the suparship UI, fill out the form (driven by `inputs:`), and an
**app** is created and rendered into a GitOps repo by suparship's
publisher.

You are *not* writing app manifests directly. You are writing the
**template** and the **chart** that the publisher feeds.

## Directory layout

```
templates/
  <template-name>/
    template.yaml             # suparship metadata: inputs, mappings, presets
    chart/                    # Helm chart (inline mode) — optional in external mode
      Chart.yaml
      Chart.lock              # commit this
      values.yaml
      values.schema.json      # optional but recommended
      templates/
      charts/                 # vendored deps, e.g. suparship-common-*.tgz
```

Two valid modes:

- **Inline mode** — sibling `chart/` directory present. Most templates
  use this. The chart is packaged at sync time and shipped alongside the
  template.
- **External mode** — no sibling `chart/`. `template.yaml` declares
  `engine.chart` as a registry ref (`{repository, name, version}`) and
  the chart is pulled from the internal ChartMuseum (see
  [Hosting](#hosting-this-org)) by Argo's repo-server at publish time.

A `template.yaml` with neither a sibling `chart/` nor a registry ref is
a hard error — `registrysync` will skip the template and surface the
error on `/templates/sources` in the UI.

## Hosting (this org)

- **Internal ChartMuseum:** `https://chartmuseum.internal.azure.exampleshared.com`
  Canonical home for the `suparship-common` library chart and for any
  external-mode template charts published from this org. Prefer
  ChartMuseum over OCI here so operators learn one publish path.
- **`suparship-common` dependency** in an external repo's
  `Chart.yaml`:

  ```yaml
  dependencies:
    - name: suparship-common
      version: "<pinned-version>"
      repository: "https://chartmuseum.internal.azure.exampleshared.com"
  ```

  Pin to an explicit version. Run `helm repo add suparship-common
  https://chartmuseum.internal.azure.exampleshared.com && helm
  search repo suparship-common --versions` to see what's published.

## `template.yaml` essentials

Minimal shape:

```yaml
apiVersion: suparship.io/v1alpha1
kind: Template
metadata:
  name: <dns-label>          # template identifier
  version: "<semver>"        # bump on every release
spec:
  title: <human-readable>
  description: <one paragraph>
  category: web              # web | worker | cron — drives default component
  engine:
    type: helm               # only "helm" today
    chart: ./chart           # inline mode
    # OR (external mode):
    # chart:
    #   repository: oci://ghcr.io/<org>
    #   name: <chart-name>
    #   version: <chart-semver>

  components:                # optional; usually one entry
    - name: web
      type: web
      required: true
      defaultEnabled: true
      previewEnabled: true
      exposed: true
      produces: [Deployment, Service]
      optionallyProduces: [Ingress, ScaledObject, PodDisruptionBudget]

  # Platform-Engineer-authored Helm values overlays. All four are free-form
  # and layer BELOW the developer's own overrides:
  defaultValues: {}          # every environment
  envValues: {}              # per env name, after defaultValues
  previewDefaultValues: {}   # previews only
  # (org-level clusterValues live in the sync-safe TemplateOverride, not here)

  developerValues:           # the values projection a developer sees + edits
    - path: components.web.image.repository   # dotted Helm values path
      title: Image Repository
      type: string
      required: true
      description: Container image, e.g. ghcr.io/org/app
    - path: containerPort    # one question, many keys: mirrors receive
      title: Port            # the SAME value when the developer sets it
      type: number
      mirrors: [service.port, healthCheck.port]
    # types: string | number | boolean | enum (all optional)
    # constraints: required, default, min, max, pattern, options

  inputs: []                 # RETIRED — see below
  advancedInputs: []         # RETIRED — see below

  secretInputs:              # secret-reference parameters; never literal values
    - name: database_url
      secretRef: db-credentials.url

  mappings:                  # input name → Helm value path
    app.name: "{{ .inputs.service_name }}"

  presets: []                # named shortcuts that pre-fill inputs
```

Validation rules enforced at load time:

- `metadata.name` and `metadata.version` are required.
- `developerValues[].path` is required and unique; `mirrors` share that
  namespace (no key may appear twice across any path or mirror); an
  `enum` entry needs at least one `options` entry; `pattern` must
  compile.
- Input names are unique across `inputs` and `advancedInputs`.
- `enum` inputs need at least one `options` entry.
- `secretRef` is `secret-name.key`.
- Preset values may only reference declared input names.
- `mappings` keys are dotted Helm value paths; values are Go-template
  expressions over `.inputs.<name>`.

### `developerValues` — the values projection

A template's chart and its `defaultValues` / `envValues` /
`previewDefaultValues` can carry a great deal that a developer should
never have to read: routing internals, cluster annotations, preview
sizing. `developerValues` declares the small, ordered subset that IS
theirs.

The app-creation editor seeds from exactly those paths, prefilled with
each key's current effective value:

- **Required** entries (and any whose effective value is empty) are
  seeded live — the developer must fill them in.
- Everything else is seeded **commented out**, showing what it inherits.
  Uncomment a line to override it. This matters: only what the developer
  actually writes is saved, so an untouched key keeps tracking the chart
  or platform default instead of being frozen into the app at creation.

A field may declare `mirrors`: additional dotted paths that receive the
same value (ask for a port once, fill `containerPort` and
`service.port`). The fan-out is purely an editor concern — the saved
overlay is plain Helm values at every path, and until the developer sets
the field each path keeps its own inherited value, which may
legitimately differ.

Declare no `developerValues` and the editor falls back to seeding from
the full platform base — the pre-projection behaviour.

**It is a view, not a permission boundary.** The editor offers a "Show
all platform values" escape hatch for BYO charts, and the API still
accepts any key. Use it to guide, not to lock down.

Operators can curate the projection for a read-only synced or built-in
template via `PATCH /api/v1/templates/{name}` with `developerValues` —
stored in the sync-safe override, so a re-sync can't drop it, and it
REPLACES the template's own list.

`path` is dotted (`components.web.image.repository`) and so cannot
express a key containing a dot — the same constraint `images[].tagKey`
and `mappings` keys already carry. A path that resolves to a map
projects that whole subtree.

### Retired: `inputs` / `advancedInputs` / `mappings` / `presets`

Apps are configured through the values editor now, not a generated
form. These fields still parse and are still served, but nothing in the
UI renders them, passthrough/BYO charts strip them, and app creation no
longer validates against them. `developerValues` supersedes them: it is
keyed directly by the Helm values path instead of a synthetic input name
plus a `mappings` indirection. Don't add new ones. `secretInputs` is
NOT retired — it is still rendered.

Never put literal secret values in `template.yaml`, in `values.yaml`,
or in mappings. Use `secretInputs` + `secretRef`.

## Chart conventions

Charts MUST follow the conventions documented in suparship's
`docs/chart-conventions.md`. The non-negotiables:

1. **Depend on `suparship-common`** (the shared library chart) and use
   its helpers for fullname / labels / selectorLabels / resources /
   envFrom / reloader. Do not re-implement these.
2. **Read publisher-injected values** at the canonical paths:
   - `.Values.app.{name,env}`
   - `.Values.components.<name>.{enabled,image,replicas,expose,port,healthCheck,env,resources,ingress}`
   - `.Values.routing.{host,component}`
   - `.Values.suparship.{envFromConfigMaps,envFromSecrets}`
   Chart-defined extras (autoscaling, pdb, gateway, lifecycle) live on
   top-level keys and merge naturally on top.
3. **Gate `envFrom`** through `suparship-common.envFrom` — empty lists
   must produce no `envFrom:` key (kubectl rejects otherwise).
4. **KEDA `ScaledObject` only.** No HPA v2 in new charts.
5. **Per-component label** `suparship.io/component: <name>` on every
   workload (Deployment / StatefulSet / CronJob) and pod template.
   Never put this in selectors.
6. **Resource size presets** are `small | medium | large` — use
   `suparship-common.resources` / `componentResources`.
7. **`reloader: "true"`** by default in `values.yaml`; gate the
   annotation through `suparship-common.reloaderAnnotation`.
8. **imagePullPolicy** = `IfNotPresent` for stateless web/worker;
   `Always` only when tags are mutable.
9. **Strategy** = `RollingUpdate` with `maxSurge: 1, maxUnavailable: 0`
   for web; `maxUnavailable: 0` for inbound-style "must-not-drop" workloads.
10. **preStop sleep** so the routing fabric stops sending traffic
    before the process exits. Default 15s for web; longer for stateful.

Multi-component charts use one file per component
(`<component>-deployment.yaml`, `<component>-pdb.yaml`,
`<component>-scaledobject.yaml`) and chart-local helpers named
`<chart>.<component>.<aspect>`. Avoid `range`-over-components in a
single template.

## Releases — how a new version reaches suparship

This repo has **no automated release pipeline**. A release is a
versioned git commit on the branch the cluster's `ExternalTemplateRepo`
points at (typically `main` or a pinned tag).

### Inline-mode template

1. Edit `templates/<name>/template.yaml` → bump `metadata.version`.
2. Edit `templates/<name>/chart/Chart.yaml` → bump `version`. Bump
   `appVersion` only when behavior shipped in the chart changes.
3. If `dependencies:` changed, run `helm dep update <chart-dir>` and
   commit the updated `Chart.lock` and any new `charts/*.tgz`.
4. Run `helm lint <chart-dir>` and `helm template <chart-dir>` against
   a representative values file. Fix until clean.
5. Commit with a conventional-ish message
   (`feat(template/<name>): …`, `fix(chart/<name>): …`).
6. Push to the tracked ref. The cluster picks it up on the next
   periodic sync (or on a manual trigger from `/templates/sources`).

### External-mode template (chart in ChartMuseum)

1. `helm package <chart-dir>` → `<name>-<version>.tgz`.
2. Push to the internal ChartMuseum:

   ```sh
   curl --data-binary "@<name>-<version>.tgz" \
     https://chartmuseum.internal.azure.exampleshared.com/api/charts
   ```

   (or `helm cm-push` if the plugin is installed). ChartMuseum rejects
   re-uploads of an existing `name-version` pair — bump the version,
   don't overwrite.
3. Edit `templates/<name>/template.yaml` → bump `metadata.version` and
   `engine.chart.version` to match the published chart. The
   `engine.chart.repository` should be
   `https://chartmuseum.internal.azure.exampleshared.com`.
4. Commit and push.

### What "discovered" means

The `registrysync` engine clones the repo at the configured `Ref`,
walks for `Chart.yaml` (inline) and `template.yaml` without sibling
`chart/` (external), packages each, and writes a ConfigMap via
`kube.SaveTemplate`. Per-template parse errors don't block siblings —
"3 of 4 imported, 1 failed" is the partial-success shape. Apps
currently pin templates **by name only**, so a new version is the new
default for new apps; existing apps surface an "upgrade available"
prompt on the app-detail page.

### Versioning rules of thumb

- Patch (`1.0.0 → 1.0.1`): bug fix, no manifest shape change, no input
  shape change.
- Minor (`1.0.0 → 1.1.0`): additive — new input with a default, new
  optional manifest, new preset.
- Major (`1.0.0 → 2.0.0`): breaking — input renamed/removed/retyped,
  required manifest removed, default behavior changed in a way that
  could disrupt running apps. There's no automatic input migration
  today, so majors must be applied to apps deliberately.

Keep `template.yaml` `metadata.version` and `Chart.yaml` `version` in
lockstep when both change in the same release. Drift is allowed (a
docs-only template change without a chart bump) but call it out in the
commit message.

## Common tasks for an AI agent

### Adding a new template

1. Scaffold `templates/<name>/template.yaml` and
   `templates/<name>/chart/`. Copy `web-service` or
   `gateway-web-service` as a starting point — they're the canonical
   inline-mode shapes.
2. Add the `suparship-common` dependency in `Chart.yaml` pointing at
   the internal ChartMuseum (see [Hosting](#hosting-this-org)), not
   the monorepo `file://` path. Run `helm dep update <chart-dir>`
   after adding the dep so `Chart.lock` and `charts/suparship-common-*.tgz`
   are written.
3. Declare `developerValues:` — the handful of Helm value paths a
   developer owns. Everything you leave out stays platform-owned and
   never reaches the app-creation editor. (`inputs:` → `mappings:` is
   the retired predecessor; see below.)
4. Add at least one preset (`starter` or `production`).
5. Run `helm lint`, `helm template` against a fixture values file,
   `helm dep update`, then commit.

### Modifying an existing template

1. Decide the version bump (patch / minor / major) — see above.
2. Make the change. If renaming an input, also update `mappings:` and
   any preset that references it.
3. Update the chart in lockstep when manifest shape changes.
4. Bump `metadata.version` AND `Chart.yaml` `version`.
5. `helm dep update` if dependencies changed; commit `Chart.lock` and
   `charts/*.tgz`.
6. Run lint + template, commit, push.

### Things NOT to do

- Don't introduce a `template.yaml` without bumping `metadata.version`
  if the spec changed. The cluster will overwrite the existing
  ConfigMap silently and operators lose the ability to see "what
  changed".
- Don't edit chart `templates/` without bumping `Chart.yaml` `version`.
- Don't hardcode hostnames, image registries, or env-specific values
  in `values.yaml`. Those come from the publisher.
- Don't add Ingress + HTTPRoute in the same chart — pick one routing
  fabric per template (capability `routing: ingress` vs `routing: gateway`).
- Don't add HPA — use KEDA `ScaledObject`.
- Don't put literal secrets anywhere. Use `secretInputs` and
  `secretRef`.
- Don't bypass `suparship-common`. If a helper is missing, raise it
  upstream rather than re-implementing it.

## Quick reference

- `template.yaml` schema: `docs/templates.md` in the suparship repo.
- Chart conventions: `docs/chart-conventions.md` in the suparship repo.
- Sync engine: `internal/tpl/registrysync/` — read `sync.go` to
  understand exactly what gets packaged and persisted.
- Source-type semantics: `internal/tpl/model.go` (`SourceTypeGit`,
  `SourceTypeOCI`, `SourceTypeChartMuseum`, `SourceTypeGitTgz`).
- Library chart: `charts/lib/suparship-common/`.
- Worked examples: `templates/web-service/`,
  `templates/gateway-web-service/`, `templates/voiceai-agent/`.
