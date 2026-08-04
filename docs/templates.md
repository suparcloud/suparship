# Templates

suparship templates are the primary mechanism for creating and deploying **apps**. When a developer picks a template and fills in the form, suparship creates an **app** backed by that template's rendering configuration. The template does not create a raw Kubernetes Service or Deployment directly — it creates an app that suparship then renders into the appropriate GitOps manifests.

## What a template creates

A template creates an **app** — the primary user-facing deployment object in suparship. See [ADR-0001](adr/0001-app-as-primary-deployment-object.md) for the rationale behind the app-first model.

An app:

- belongs to a **project** (the team/product boundary)
- runs across one or more **environments** (`staging`, `prod`, ephemeral `preview-*`)
- is composed of one or more internal **runtime components** (e.g. `web`, `worker`, `cron`)

The template controls the default component topology. Most templates define a single implicit `web` component; more advanced templates may define multiple components.

```
org
└── project
    └── app              ← what the template creates
        ├── environment  (staging | prod | preview-*)
        └── component    (web | worker | cron — advanced / hidden by default)
```

## Component topology

### MVP guidance

**Most templates should produce a single `web` component.** This is the default for any template with `category: web`. The `web` component:

- is of type `web` (`ComponentType = "web"` in the domain model)
- is enabled in preview environments (`enabledInPreview: true`)
- maps to a Kubernetes Deployment + Service + optional Ingress

Some templates may define a `web` + `worker` pair when the workload naturally splits into an HTTP server and a background queue consumer. Example:

```yaml
# Derived automatically from template category; callers may override
components:
  - name: web
    type: web
    enabledInPreview: true
  - name: worker
    type: worker
    enabledInPreview: false   # workers are expensive; skip in previews
```

Workers and cron jobs are **not enabled in preview environments by default** to keep preview costs low. This behaviour is controlled by the `enabledInPreview` field on each component and can be overridden by the caller.

### Category → default component mapping

suparship derives the default component list from the template's `category` field when no explicit component list is provided:

| Template `category` | Default component | Preview enabled |
|---------------------|-------------------|-----------------|
| `web` (or any other) | `web` (type: web) | ✅ yes |
| `worker` | `worker` (type: worker) | ❌ no |
| `cron` | `cron` (type: cron) | ❌ no |

Templates that need custom topology (e.g. `web` + `worker`) should document this in their `spec.description` and set `category: web`; the component list is then specified explicitly at app-creation time or in a future `components` block on the template spec.

### Validation rules

- An app must have at least one component.
- Component names must be valid DNS labels.
- Component names must be unique within an app.
- At most one `web` component is allowed unless the template explicitly permits multiple HTTP endpoints.

## App inputs and template values

Template inputs are the curated parameters exposed in the UI form. They map to Helm values (or equivalent rendering engine values) via the `mappings` block in `template.yaml`. Inputs intentionally hide raw Helm complexity from the developer.

Supported input types:

| Type | Description |
|------|-------------|
| `string` | Free-form text; may have a `pattern` constraint |
| `number` | Numeric value; optional `min` / `max` |
| `boolean` | Toggle |
| `enum` | Dropdown from a fixed `options` list |

Secret inputs are a separate category — see [Secrets](#secrets) below.

### Presets

A preset is a named shortcut that pre-fills a set of input values. Presets are optional but strongly recommended for common configurations:

```yaml
presets:
  - name: starter
    title: Starter
    description: Single replica, small resources, no ingress.
    values:
      replicas: 1
      size: small
      ingress_enabled: false
  - name: production
    title: Production
    description: Multiple replicas, larger resources, ingress enabled.
    values:
      replicas: 3
      size: large
      ingress_enabled: true
```

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
  category: <string>         # web | worker | cron (controls default component)
  engine:
    type: helm               # only "helm" is supported in MVP
    chart: ./chart           # path relative to the template directory
  components: [...]          # named runtime units (optional; see templates-components.md)
  # Platform-Engineer-authored Helm values overlays, layered below the
  # developer's own overrides. Free-form; string leaves may use
  # ((platform.*)) / ((vars.*)) tokens resolved at publish.
  defaultValues: {...}       # applies to every environment
  envValues: {...}           # per environment name, after defaultValues
  previewDefaultValues: {...}  # previews only
  developerValues: [...]     # the values projection a developer sees + edits
  images: [...]              # declared image slots for external-CD (Kargo) wiring
  injectCanonicalValues: <bool>  # false = passthrough/BYO chart
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
    - path: components.web.image.repository   # dotted Helm values path
      title: Image Repository
      type: string                            # string | number | boolean | enum
      required: true
      description: Container image, e.g. ghcr.io/org/app
    - path: components.web.resources.size
      title: Resource Size
      type: enum
      options: [small, medium, large]
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
- Input names must be unique across `inputs` and `advancedInputs`
- `enum` inputs must have at least one `options` entry
- `secretRef` fields must be in `secret-name.key` format
- Preset values may only reference declared input names

## Built-in templates

### `web-service`

A general-purpose template for HTTP apps. Creates a single `web` component (Deployment, Service, optional Ingress, optional HPA). Suitable for APIs, frontends, and general HTTP workloads.

**Category:** `web`  
**Default component topology:** single `web` component, preview-enabled  
**Engine:** Helm (`./chart`)

Key inputs:

| Input | Description |
|-------|-------------|
| `service_name` | App name; used in Kubernetes resource names and selectors |
| `image_repository` | Container image repository |
| `image_tag` | Container image tag |
| `port` | TCP port the container listens on |
| `ingress_enabled` | Expose the app via an Ingress resource |
| `size` | Resource profile: `small` / `medium` / `large` |
| `health_path` | HTTP path for liveness and readiness probes |

Secret inputs:

| Input | Description |
|-------|-------------|
| `database_url` | Database connection string; injected from a Kubernetes Secret |

Presets: `starter` (single replica, small, no ingress) and `production` (three replicas, large, ingress enabled).

## Writing a new template

1. Create `templates/<name>/template.yaml` following the schema above.
2. Add the Helm chart under `templates/<name>/chart/`.
3. Test loading with `go test ./internal/tpl/...`.
4. Add the template to the `LoadDir` path used at startup (no code change required — the loader picks up all subdirectories automatically).
5. Document the template in this file under [Built-in templates](#built-in-templates).

> **Tip:** Keep templates focused. A template that does one thing well is better than a template with 30 inputs. Use presets to cover the common cases; advanced users can override via `advancedInputs`.

## Roadmap

Captured here so they aren't lost between sessions. Order is rough priority,
not dependency.

### Already shipped

- **BYO Helm chart wizard** — operators upload a `.tgz` via `/templates/import`; the chart is introspected (`Chart.yaml` + `values.schema.json`, with `values.yaml` fallback) and turned into a starter `template.yaml` they can edit before saving. Backend in `internal/tpl/chartimport`, UI at `/templates/import`.
- **External chart registries** — Git-hosted chart libraries are pulled and indexed on a configurable interval (`SUPARSHIP_TEMPLATE_SYNC_INTERVAL`, default 5m). Backend in `internal/tpl/registrysync`, UI at `/templates/sources`.

### Env vars as template inputs

Today inputs feed `helm template` values. Two natural extensions:

- **Render-into-envconfig inputs** — let a template declare an input that materialises into the merged env-config map (org → env-type → project → app → app-env → cluster), so a chart can declare `LOG_LEVEL` once and bind it across scopes.
- **`${envvar:KEY}` references in defaults** — resolve at publish time so a template default can read an upper-scope env var.

**Before implementing:** decide whether to surface inputs in two distinct categories (`buildtime` vs `runtime`) or stay flat. Mixing the two paths under one Input shape will confuse operators.

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
because a composed app mixes charts — api→web-service, worker→worker,
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
- Schema-migration rules for inputs that change between template versions
  (rename/removal/type-change).
- **Only the chart is pinned.** Template *metadata* — `defaultValues`,
  `envValues`, `previewDefaultValues`, `images`, `suspendKey`,
  `injectCanonicalValues` — is resolved by NAME at latest on every publish
  (`server.ResolveTemplates`), so a pinned app already tracks the newest
  template's overlays. Making that version-aware needs a
  `kube.LoadTemplateVersion` and is a behaviour change for every running app.
- **Archives are not immutable.** `kube.SaveTemplate` overwrites the per-version
  archive, and `PATCH /templates/{name}` re-saves without bumping — so version X's
  bytes can change under a pinned app. Refuse to overwrite a differing archive.

### Smaller follow-ups

- **Validation hooks** — let templates declare a CEL/Go validator for cross-input rules (e.g. `enable_db=true ⇒ db_size required`). Today `Required`/`Min`/`Max`/`Pattern` only validate one input at a time.
- **Component composition** — multi-component templates today are flat. A "compose templates" mechanism (web + worker + cron in one app, each declaring its own component) would keep individual charts smaller.
- **Test fixtures** — let a template ship example-input JSONs + golden rendered manifests so CI verifies the chart still renders cleanly. Plug into `go test ./internal/tpl/...`.
- **Engine pluralism** — `engine.type` already hints at this. Kustomize, raw-manifest, and Crossplane Composition engines are credible alternatives for non-Helm shops.
- **OCI-backed registries** — `registrysync` is Git-only today. Adding an OCI source would let suparship pull from ArtifactHub-indexed OCI registries directly.
- **Built-in/external name collisions** — built-ins win in the live-merge today; a synced chart with a colliding name silently disappears. Surface this as a warning on the gallery + a server log entry.
- **Sync error persistence** — periodic-sync errors are logged but not stored. Add per-source error fields to the registry document so `/templates/sources` can show "last sync failed: <reason>" days later.
