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

  inputs:                    # curated user-facing parameters
    - name: service_name
      title: App Name
      type: string
      required: true
      pattern: "^[a-z][a-z0-9-]{0,61}[a-z0-9]$"
    # types: string | number | boolean | enum
    # constraints: required, default, min, max, pattern, options

  advancedInputs: []         # optional, shown in an Advanced accordion

  secretInputs:              # secret-reference parameters; never literal values
    - name: database_url
      secretRef: db-credentials.url

  mappings:                  # input name → Helm value path
    app.name: "{{ .inputs.service_name }}"

  presets: []                # named shortcuts that pre-fill inputs
```

Validation rules enforced at load time:

- `metadata.name` and `metadata.version` are required.
- Input names are unique across `inputs` and `advancedInputs`.
- `enum` inputs need at least one `options` entry.
- `secretRef` is `secret-name.key`.
- Preset values may only reference declared input names.
- `mappings` keys are dotted Helm value paths; values are Go-template
  expressions over `.inputs.<name>`.

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
3. Wire `inputs:` → `mappings:` so every Helm value the chart reads
   can be set from the form. If a value should NOT be user-configurable,
   leave it as a chart default and don't expose it.
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
