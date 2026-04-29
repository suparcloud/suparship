# Templates

suparShip templates are the primary mechanism for creating and deploying **apps**. When a developer picks a template and fills in the form, suparShip creates an **app** backed by that template's rendering configuration. The template does not create a raw Kubernetes Service or Deployment directly — it creates an app that suparShip then renders into the appropriate GitOps manifests.

## What a template creates

A template creates an **app** — the primary user-facing deployment object in suparShip. See [ADR-0001](adr/0001-app-as-primary-deployment-object.md) for the rationale behind the app-first model.

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

suparShip derives the default component list from the template's `category` field when no explicit component list is provided:

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

Secret values **must never** be stored in Git or in the app spec. suparShip enforces this through the `secretInputs` block in the template schema.

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

Each secret input carries a `secretRef` in `secret-name.key` format. This is the reference that suparShip stores in the app spec (`AppSecretRef.SecretRef`). The actual secret value is resolved at runtime by the cluster from the named Kubernetes Secret — it is never written to Git or stored in the suparShip database.

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
  inputs: [...]              # curated user-facing parameters
  advancedInputs: [...]      # shown in an "Advanced" accordion (optional)
  secretInputs: [...]        # secret-reference parameters (no literal values)
  mappings: {...}            # input name → Helm value path expression
  presets: [...]             # named shortcut value sets (optional)
```

### Validation rules enforced at load time

- `apiVersion` must be `suparship.io/v1alpha1`
- `kind` must be `Template`
- `metadata.name` and `metadata.version` are required
- `spec.title`, `spec.category`, and `spec.engine.type` are required
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

- Pin `app.spec.template.version` (currently `template.name` only).
- Surface "upgrade available" on `AppDetail` when the cluster has a newer template version than the app pins.
- Generate a dry-run diff — render Helm values before/after the version bump so the operator sees what changes before approving.
- Schema-migration rules for inputs that change between template versions (rename/removal/type-change).

### Smaller follow-ups

- **Validation hooks** — let templates declare a CEL/Go validator for cross-input rules (e.g. `enable_db=true ⇒ db_size required`). Today `Required`/`Min`/`Max`/`Pattern` only validate one input at a time.
- **Component composition** — multi-component templates today are flat. A "compose templates" mechanism (web + worker + cron in one app, each declaring its own component) would keep individual charts smaller.
- **Test fixtures** — let a template ship example-input JSONs + golden rendered manifests so CI verifies the chart still renders cleanly. Plug into `go test ./internal/tpl/...`.
- **Engine pluralism** — `engine.type` already hints at this. Kustomize, raw-manifest, and Crossplane Composition engines are credible alternatives for non-Helm shops.
- **OCI-backed registries** — `registrysync` is Git-only today. Adding an OCI source would let suparship pull from ArtifactHub-indexed OCI registries directly.
- **Built-in/external name collisions** — built-ins win in the live-merge today; a synced chart with a colliding name silently disappears. Surface this as a warning on the gallery + a server log entry.
- **Sync error persistence** — periodic-sync errors are logged but not stored. Add per-source error fields to the registry document so `/templates/sources` can show "last sync failed: <reason>" days later.
