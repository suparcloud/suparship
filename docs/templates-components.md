# Apps and components

Components are **user-declared on the app** — templates declare none. A
template is a Helm chart (plus optional metadata); how many workloads an app
runs is decided by the person creating the app:

- **Plain app (the common case): zero components.** The app pins one
  template (`AppSpec.Template`) and renders that single chart. Whatever the
  chart deploys — a Deployment, a Deployment + CronJob, anything — is the
  chart's business; suparship publishes one values overlay for it.
- **Composed app: one component per chart.** Each component declares its own
  `template: {name, version}` pin and its own `values` overlay, and the app
  renders as **one multi-source ArgoCD Application** (one source per
  component, all sharing the app's namespace and sync policy). Composition is
  all-or-nothing: either every component carries a template ref, or none do
  (`domain.ValidateComposedComponents`).

The shipnotes demo (`task demo:shipnotes`) is the reference composed app:
`frontend` (web chart, exposed externally) + `api` (web chart, internal) +
`db` (postgres chart, stateful, curated env vars).

For the full app model see [`docs/app-model.md`](app-model.md); for the
template registry see [`docs/templates.md`](templates.md).

---

## ComponentSpec fields

```
internal/domain/app.go — ComponentSpec
```

| Field | Type | Purpose |
|-------|------|---------|
| `name` | string | Unique within the app (DNS label) |
| `type` | `web` \| `worker` \| `cron` \| `job` | Runtime role — drives UI grouping and the preview default |
| `enabled` | bool | Disabled components render nothing |
| `exposeMode` | `disabled` \| `internal` \| `external` | Which org routing profile the component's route resolves through (feeds the `((platform.routingHost))` / ingress tokens) |
| `template` | `{name, version}` | The component's own chart pin (composed apps) |
| `values` | map | The component's Helm values overlay, in **its chart's own shape** — image, port, command, resources, autoscaling, everything |
| `inheritAppVars` | `*bool` | `nil`/`true`: envFrom the app-wide config/secrets; `false`: curate (below) |
| `envVars` | list | The component's own variables: literal extend/override entries while inheriting, or the curated list when `inheritAppVars: false` |
| `images` | list | This component's CD image bindings, keyed by the chart's tag path (`tagKey`, e.g. `image.tag`) |
| `stateful` | bool | Renders as its own prune-disabled ArgoCD Application (databases/caches) |
| `previewEnabled` | `*bool` | Overrides whether the component deploys in previews |

There are no workload-shape fields (replicas, size presets, resources,
scaling, config) on a component — **all workload shape lives in the
component's `values`**, in whatever paths its chart defines. This is what
keeps bring-your-own charts first-class: suparship never has to understand a
chart's schema to configure it.

---

## Per-component environment variables

A component's environment takes one of three postures — all delivered through
platform-rendered objects (the chart only ever `envFrom`s the two token
names):

- **Inherit (default, `inheritAppVars` unset or `true`, no `envVars`).** The
  component `envFrom`s the app-wide `<app>-config` ConfigMap and
  `<app>-secrets` Secret — every app variable, exactly like a plain app.
- **Inherit + extend/override (inheriting, with literal `envVars`).**
  suparship renders `<app>-<component>-config` as the app/env resolved
  variables **merged with the component's literals (literal wins)** and
  points that component's `((platform.configMapName))` at it;
  `((platform.secretName))` keeps pointing at the app-wide secret. App
  variable changes keep flowing — the merge re-renders on every publish.
  Two limits: a literal overrides inherited *variables*, not secret-delivered
  keys (charts list secrets after configMaps in `envFrom`, and Kubernetes
  gives later sources precedence); and previews currently point every
  component at the app-level preview objects, so the extras don't apply
  inside previews. Source-mapped entries (`fromConfig`/`fromSecret`) are
  rejected in this posture — renaming while inheriting is ambiguous.
- **Curate (`inheritAppVars: false` + `envVars`).** suparship renders
  per-component objects — `<app>-<component>-config` and (when secret keys
  are selected) `<app>-<component>-secrets` — holding only the curated
  entries, and points `((platform.configMapName))` /
  `((platform.secretName))` at them for that component. Each entry is either
  a literal `value`, or a selected (optionally renamed) key of the app's
  config (`fromConfig`) or secrets (`fromSecret`). With no secret keys
  selected, `((platform.secretName))` resolves to `""` — no app secrets reach
  the component.

The shipnotes `db` component is the worked example: it curates
`POSTGRES_DB` from the app's variables (`fromConfig`) and
`POSTGRES_USER`/`POSTGRES_PASSWORD` from the app's secrets (`fromSecret`)
instead of inheriting every app variable.

---

## Stateful components (addons)

`stateful: true` marks a component (a database/cache — an "addon") whose
lifecycle must be decoupled from the app's shared auto-sync. Instead of a
source in the composed multi-source Application (one Application-level
`prune: true` policy), it renders as its **own** ArgoCD Application with
prune disabled. That protects against sync-time prune/drift — surviving PVCs
across component deletion additionally require the chart to mark them
`helm.sh/resource-policy: keep` (the example `postgres` chart does). Typically
paired with `inheritAppVars: false` and no `images` (deploys pinned/direct,
not Kargo-promoted).

---

## Previews

Preview support is an **app-level** switch (`AppSpec.PreviewsEnabled`); a
preview mirrors what the base env deploys. Within a composed app,
`previewEnabled` tunes which components are included — an explicit value
wins, otherwise the type default applies: long-running services (`web`,
`worker`) preview by default; one-shot (`job`, `cron`) and stateful
components do not (no throwaway DBs or re-run migrations per PR). Composed
previews build every included component at the **same** image tag. See
[`docs/previews.md`](previews.md).

---

## Images and CD

Image bindings are explicit and live where the values live: app-level
`AppSpec.Images` for a plain app, per-component `images` for a composed one.
Images are *discovered* from the effective values (chart defaults ⊕
overlays); a binding selects one by its `tagKey` — the dotted path in that
chart's own values where the promoted tag is written (`image.tag` in the
example charts). No bindings = no Kargo Warehouse. See
[`docs/apps-and-images.md`](apps-and-images.md).

---

## Related documents

- [`docs/app-model.md`](app-model.md) — App, Environment, and Component concepts
- [`docs/templates.md`](templates.md) — the template registry and `template.yaml` reference
- [`docs/byo-charts.md`](byo-charts.md) — the chart-side contract
- [`docs/adr/0001-app-as-primary-deployment-object.md`](adr/0001-app-as-primary-deployment-object.md) — decision record
