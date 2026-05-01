# Templates and components

Templates are the golden-path mechanism for creating apps in suparShip. A
template defines:

- The rendering engine and chart (Helm in MVP).
- The **component topology** — which runtime processes the app contains.
- The input schema rendered into a UI form.
- Secret input declarations (references only, never literals).
- Mappings from input values to Helm values.
- Preset bundles for common configurations.

This document focuses on the relationship between templates and components.
For the full template authoring guide see [`docs/templates.md`](templates.md).
For the app model see [`docs/app-model.md`](app-model.md).

---

## How templates define component topology

Every template can declare a `components` block listing the runtime units the
app contains. When a user creates an app from the template, these component
declarations seed the `AppSpec.Components` slice.

```yaml
# template.yaml — web-service
spec:
  components:
    - name: web
      type: web
      required: true
      defaultEnabled: true
      previewEnabled: true
      exposed: true
```

If the `components` block is absent the platform derives a single default
component from the template's `category` field for backward compatibility:

| `category` | Derived component name | Derived type |
|------------|----------------------|--------------|
| `web` | `web` | `web` |
| `worker` | `worker` | `worker` |
| `cron` | `cron` | `cron` |

A template with multiple components explicitly lists each one:

```yaml
spec:
  components:
    - name: web
      type: web
      required: true
      defaultEnabled: true
      previewEnabled: true
      exposed: true

    - name: worker
      type: worker
      required: false
      defaultEnabled: true
      previewEnabled: false   # skip heavy worker in preview environments
      exposed: false
```

---

## TemplateComponent fields

| Field | Type | Default | Purpose |
|-------|------|---------|---------|
| `name` | string | — | Unique identifier within the template (DNS label) |
| `type` | `web` \| `worker` \| `cron` | — | Runtime role |
| `required` | bool | `false` | When true, users cannot disable this component |
| `defaultEnabled` | bool | `true` | Whether the component is on by default |
| `previewEnabled` | bool | `false` | Whether this component deploys in preview environments |
| `exposed` | bool | `false` | Whether the component receives an ingress endpoint *by default* (UI initial state) |
| `produces` | `[]string` | `[]` | Resource Kinds the chart MUST render for this component (asserted by chart-validate) |
| `optionallyProduces` | `[]string` | `[]` | Kinds the chart MAY render based on values (informational) |
| `capabilities` | `ComponentCapabilities` | per type — see below | Which UI input groups apply to this component |

`defaultEnabled` uses a pointer in Go (`*bool`) so an omitted YAML field is
treated as `true` by `IsDefaultEnabled()`. Write `defaultEnabled: false`
explicitly to opt a component out by default.

### Capabilities

`capabilities` declares which UI input groups apply to a component, replacing
the prior frontend hardcoding ("every web has autoscaling, every cron has
schedule"). Authors override only the fields that differ from the type-based
defaults; the API resolves and serves the fully filled-in shape (no nils) at
`GET /api/v1/templates/{name}` under `components[].capabilities`.

| Field | Type | Purpose |
|-------|------|---------|
| `expose` | `*bool` | Show the externally-expose toggle |
| `routing` | `"" \| "none" \| "ingress" \| "gateway"` | Which routing fabric to surface inputs for |
| `autoscaling` | `"" \| "none" \| "hpa" \| "keda"` | Which scaling backend (drives input shape) |
| `pdb` | `*bool` | Show PodDisruptionBudget inputs (advanced) |
| `resources` | `*bool` | Show the small/medium/large size dropdown |
| `replicas` | `*bool` | Show the replicas slider |
| `schedule` | `*bool` | Show the cron schedule input |

Type-based defaults (used when a field is omitted):

| Type | expose | routing | autoscaling | pdb | resources | replicas | schedule |
|------|--------|---------|-------------|-----|-----------|----------|----------|
| `web` | true | ingress | keda | true | true | true | false |
| `worker` | false | none | keda | true | true | true | false |
| `cron` | false | none | none | false | true | false | true |

Pointer-typed bool fields distinguish "not declared" (use type default) from
"explicitly false" (override). String fields use the empty string for
"not declared".

Examples:

```yaml
# Web with HTTPRoute instead of Ingress
components:
  - name: web
    type: web
    capabilities:
      routing: gateway      # surfaces parentRef inputs

# Stateful worker with hardcoded resources
components:
  - name: livekit-agent
    type: worker
    capabilities:
      resources: false      # chart owns sizing; suppress dropdown

# Demo template — strip the form down
components:
  - name: web
    type: web
    capabilities:
      autoscaling: none
      pdb: false
      replicas: false
```

---

## Component types

```
internal/tpl/model.go  — TemplateComponentType constants
internal/domain/app.go — ComponentType constants (runtime)
```

The three MVP component types:

### `web`

An HTTP server that receives external traffic. Typical defaults:
- `exposed: true` — an Ingress or Service is created.
- `previewEnabled: true` — preview environments include this component.
- `required: true` on single-component templates.

### `worker`

A background process that consumes from a queue or processes async work.
- `exposed: false` — no ingress.
- `previewEnabled: false` recommended — preview environments skip it to save resources.

### `cron`

A scheduled job that runs on a time interval.
- `exposed: false` — no ingress.
- `previewEnabled: false` recommended — scheduled jobs typically do not need to
  run in short-lived preview environments.

---

## Input scoping to a component

By default, template inputs apply at the app level. When a template has
multiple components, an input can be scoped to a specific component by setting
its `component` field:

```yaml
inputs:
  - name: image_repository
    title: Image Repository
    type: string
    required: true
    # no component field — applies to the whole app / shared chart values

  - name: worker_concurrency
    title: Worker Concurrency
    type: number
    default: 4
    component: worker   # scoped to the worker component only
```

The `component` value must match a name declared in `spec.components`. The
loader rejects templates where the `component` reference is unresolved.

---

## Size presets

Size presets abstract CPU/memory requests into named t-shirt sizes. They are
surfaced as an `enum` input in templates and map to a `SizePreset` value on the
`ComponentSpec`:

| Preset | CPU request | Memory request | Typical use |
|--------|------------|---------------|-------------|
| `small` | 250m | 256Mi | Development, low traffic |
| `medium` | 500m | 512Mi | Moderate production workloads |
| `large` | 1 | 1Gi | High-throughput services |

Use `size` as the input name to match the mapping convention:

```yaml
inputs:
  - name: size
    title: Resource Size
    type: enum
    options: [small, medium, large]
    default: small
```

`SizePreset` and `Replicas` are mutually exclusive on a `ComponentSpec`. Setting
both is a validation error.

---

## Preview opt-out per component

Mark non-essential components with `previewEnabled: false` to keep preview
environments lightweight:

```yaml
spec:
  components:
    - name: web
      type: web
      previewEnabled: true    # always included in previews

    - name: worker
      type: worker
      previewEnabled: false   # omitted from previews — saves cluster resources
```

When suparShip creates a preview environment it skips every component whose
`previewEnabled` is false. This is checked at the `ComponentSpec` level in
`internal/domain/app.go`.

---

## App creation flow: template → AppSpec

When a user creates an app from a template via the UI or API:

1. The template is loaded from `templates/{name}/template.yaml`.
2. The user's input values are validated against `spec.inputs`.
3. An `AppSpec` is constructed:
   - `AppSpec.Template` ← template name + version.
   - `AppSpec.Values` ← validated user input values (no secrets).
   - `AppSpec.SecretRefs` ← user-supplied secret references.
   - `AppSpec.Components` ← seeded from `spec.components` in the template, with
     the user's per-component overrides applied on top. If the user did not
     customise any component defaults, `Components` may be left empty (the
     template topology is re-derived at render time).
4. The `App` is persisted to the project store.
5. At render time, the Helm values mapper (`internal/helmvalues`) applies
   `spec.mappings` to produce the final `values.yaml` passed to the chart.

See `internal/app/creator.go` for the creation orchestration and
`internal/helmvalues/mapper.go` for the values mapping.

---

## Adding a new template

1. Create a directory under `templates/{name}/`.
2. Write `templates/{name}/template.yaml` following the schema above.
3. Include the Helm chart under `templates/{name}/chart/`.
4. Declare `spec.components` explicitly if the app has more than one runtime
   process.
5. Validate with `tpl.Parse` in a test — see `internal/tpl/validate_test.go`.

The template loader discovers templates by scanning the directory tree; no
registration step is required.

---

## Related documents

- [`docs/app-model.md`](app-model.md) — App, Environment, and Component concepts
- [`docs/templates.md`](templates.md) — full template authoring reference
- [`docs/adr/0001-app-as-primary-deployment-object.md`](adr/0001-app-as-primary-deployment-object.md) — decision record
