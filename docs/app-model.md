# App model

suparship organises every deployable workload around a four-level hierarchy:

```
org
└── project          (team / product boundary)
    └── app          (primary developer-owned object)
        ├── environment  (staging | prod | preview-*)
        └── component    (internal; web | worker | cron — advanced view only)
```

This document explains the role of each level, the guardrails enforced in code,
and how the model maps to Kubernetes at runtime.

---

## App — the unit of deployment

An **app** is the thing a developer creates, previews, and promotes. It is:

- The top-level navigation object within a project.
- The unit of ownership — an app belongs to exactly one project.
- The unit of deployment — you deploy an app; its internal components deploy as a set.
- The scope for preview creation and environment promotion.

### What an app contains

An app is described by `domain.App` + `domain.AppSpec` in
`internal/domain/app.go`:

| Field | Purpose |
|-------|---------|
| `template` | The template (chart) this app renders — the only pin for a plain single-chart app |
| `values` | The app's Helm values overlay, in the chart's own shape (no secrets) |
| `secretRefs` | Secret references resolved at runtime from Kubernetes Secrets |
| `components` | User-declared chart-backed workloads (composed apps only; empty for a plain app) |
| `environmentDefaults` | Per-environment overlays (values + env vars) |

### What an app is NOT

- An app is **not** a Kubernetes Service or a Helm release. Those are
  implementation details that an app renders into.
- An app is **not** an environment. Environments are runtime views of the same
  app, not separate containers of apps.
- An app is **not** a component. Components are the chart-backed workloads a
  composed app declares; a plain single-chart app has none.

### Guardrails

```
internal/domain/app.go   — canonical App, AppSpec, ComponentSpec types
internal/domain/types.go — Service (deprecated), kept for compatibility only
```

New code MUST use `domain.App` and `domain.AppStore`. Do not add features to
the deprecated `domain.Service` or `domain.ServiceStore` types.

### App sizing & the stack boundary

There is **no cap** on how many components an app has. An app with one `web` +
several `worker` + `cron` components is normal and fully supported. The boundary
is **shape, not size**:

> **One app = one atomic-release unit** (one Kargo pipeline / one image-tag
> promotion) with **at most one HTTP surface**, plus the workers/crons that ship
> and roll out with it.

The forcing function is the **single-exposed-component invariant**: an app may
have at most one component with a non-disabled `exposeMode` (and at most one `web`
component). Reach for **separate apps grouped in a [stack](stacks.md)** when you
need **multiple HTTP surfaces**, **independent release cadences**, or to **clone a
collection** with config diffs. See [ADR-0002](adr/0002-app-vs-stack-boundary.md).

### Deployment variants

Pick the primitive by the need:

| Need | Primitive |
|---|---|
| Test a branch/PR in isolation | **Preview** — ephemeral env instance (below) |
| Differ config staging ↔ prod | **Per-env config** (`EnvironmentDefaults`) + promotion |
| A second long-lived copy (tenant/region, cloud-vs-self-hosted) | **Clone** (stack-clone) |
| Canary/stable split within one env | **Release channels** — *post-0.1* (Gateway API) |

---

## Environment — runtime context, not a navigation object

An **environment** is a lens applied to an app, not a top-level container.
A developer navigates to an app and then switches the environment view
(`staging`, `prod`, `pr-42`).

Each environment instance is modelled by `domain.EnvironmentInstance`:

| Field | Purpose |
|-------|---------|
| `envName` | Logical name (`staging`, `prod`, `pr-42`) |
| `envType` | Classified as `staging`, `prod`, or `preview` |
| `namespace` | Kubernetes namespace for this instance |
| `release` | The release currently targeted here |
| `url` | Primary ingress hostname for this instance |
| `status` | Live runtime health summary from the cluster |
| `clusterName` | Name of the registered cluster this instance runs on |
| `clusterServer` | Kubernetes API server URL for this instance's cluster |

The per-environment configuration lives in `domain.Environment` (project store):

| Field | Purpose |
|-------|---------|
| `name` | Logical name (`staging`, `prod`) |
| `order` | Position in the promotion chain |
| `clusterRef` | Name of the registered cluster apps in this env deploy to |
| `baseDomain` | Root ingress domain (e.g. `acme.com`). Defaults to `localhost` |
| `namespacePattern` | Template for Kubernetes namespace generation. Tokens: `{app}`, `{env}`, `{project}`. Defaults to `{app}-{env}` |

### Namespace convention

Namespaces are derived by `GenerateNamespaceFromPattern` using the environment's
`namespacePattern`. The default (`{app}-{env}`) works for both stable and preview
environments on shared clusters:

```
{app}-{envName}    →  any environment (default pattern)
```

Examples:
- app `hello`, environment `staging` → namespace `hello-staging`
- app `hello`, preview `pr-42` → namespace `hello-pr-42`

On dedicated clusters where a single namespace per app is preferred, set
`namespacePattern: "{app}"` so all environments for an app share one namespace
on that cluster.

### Environment types

| `AppEnvironmentType` | Constant | Description |
|---------------------|----------|-------------|
| `staging` | `AppEnvStaging` | Standard pre-production target |
| `prod` | `AppEnvProd` | Production target |
| `preview` | `AppEnvPreview` | Ephemeral branch/PR instance |

Only these three values are valid in MVP. `ParseAppEnvironmentType` returns an
error for any other string.

A `preview` env clones a stable base env (default: the first by promotion order)
plus the app's preview overrides, and is excluded from the promotion chain. See
[previews.md](previews.md) for the preview lifecycle, overlays, and how to launch
one from the UI, the API, or CI.

### Desired config vs runtime state

**Desired config** lives in `App`/`AppSpec` and is stored in Git-backed
Kubernetes ConfigMaps. It describes *what should be running*.

**Runtime state** lives in `AppEnvironment.Status` (`AppRuntimeStatus`). It is
derived from live cluster observations and MUST NOT be stored as desired config.

### Guardrails

- `AppEnvironment` values are **read-only** runtime observations — never persist
  them back to the project store.
- `EnvironmentOverride` (inside `AppSpec.EnvironmentDefaults`) holds the
  *desired* per-environment tuning and is the only place to store env-specific
  config.

---

## Component — a chart-backed workload in a composed app

A **component** is a user-declared, chart-backed workload inside a composed
app. Common types (`ComponentType`): `web` (HTTP server), `worker`
(background consumer), `cron` (scheduled), `job` (one-shot).

### Components are user-declared, not template-derived

Templates declare no components. A plain single-chart app stores **zero**
components — `AppSpec.Template` is its only pin and the single-source
publisher renders straight from it. A composed app declares every component
explicitly, each with its own `template: {name, version}` pin and its own
`values` overlay, and renders as one multi-source ArgoCD Application.
Composition is all-or-nothing (`domain.ValidateComposedComponents`): either
every component carries a template ref, or none do.

### ComponentSpec fields

| Field | Purpose |
|-------|---------|
| `name` | Unique identifier within the app (e.g. `api`, `db`) |
| `type` | Runtime role (`web`, `worker`, `cron`, `job`) |
| `enabled` | Whether this component is active across all environments |
| `exposeMode` | Routing profile: `disabled`, `internal`, or `external` |
| `template` | The component's own chart pin |
| `values` | The component's Helm values overlay, in its chart's own shape — image, port, replicas, resources, everything |
| `inheritAppVars` | `true`/unset: envFrom the app-wide config/secrets; `false`: curated `envVars` only |
| `envVars` | Curated per-component env (literals or selected/renamed app config/secret keys) |
| `images` | Per-component CD image bindings (by the chart's tag path) |
| `stateful` | Renders as its own prune-disabled Application (databases/caches) |
| `previewEnabled` | Whether this component deploys in preview environments |

All workload shape (replicas, resources, scaling, config) lives in the
component's `values`, in whatever paths its chart defines — there are no
typed workload fields. See
[templates-components.md](templates-components.md) for the full model.

### Preview opt-out

Heavy or non-essential components can opt out of preview deployments with
`previewEnabled: false`. Unset, the type default applies: `web`/`worker`
preview, `cron`/`job` and stateful components don't.

### Guardrails

- Secret values MUST NOT appear in component `values` or `envVars` literals.
  Use `AppSpec.SecretRefs` / the secrets vault and curate keys with
  `fromSecret` instead.

---

## Preview — ephemeral environment instance

A preview is not a separate object type. It is an ephemeral `AppEnvironment`
with `EnvType = AppEnvPreview`, created on demand for a branch or pull request
and deleted when no longer needed.

Preview creation and deletion are **app-scoped operations**:

```
POST   /api/v1/projects/{project}/apps/{app}/previews
DELETE /api/v1/projects/{project}/apps/{app}/previews/{name}
```

Namespace convention: `{app}-{previewName}` (default pattern)
(e.g. app `hello`, preview `pr-42` → `hello-pr-42`)

Components with `previewEnabled: false` are not deployed in preview instances.

---

## Promotion — app-level release operation

Promoting an app advances a coherent release bundle across the ordered
environment chain (`dev → staging → prod`). It is an **app-scoped operation**:

```
POST /api/v1/projects/{project}/apps/{app}/promote
Body: { "targetEnvironment": "prod" }
```

The source environment is automatically determined as the environment immediately
preceding the target in the project's ordered chain.

Promotion requires `project_admin` role or above.

---

## API hierarchy

The canonical API routes follow the object hierarchy:

```
/api/v1/projects/{project}/apps                         list / create apps
/api/v1/projects/{project}/apps/{app}                   app detail
/api/v1/projects/{project}/apps/{app}/environments      list environments
/api/v1/projects/{project}/apps/{app}/environments/{e}  environment detail
/api/v1/projects/{project}/apps/{app}/previews          list / create previews
/api/v1/projects/{project}/apps/{app}/promote           promote
/api/v1/projects/{project}/apps/{app}/logs              logs

/api/v1/projects/{project}/environments                 list / create project environments
/api/v1/projects/{project}/environments/{env}           get / update / delete a project environment

/api/v1/clusters                                        list / register clusters
/api/v1/clusters/{name}                                 get / remove a cluster
```

Legacy `/projects/{project}/services/...` paths remain registered for backward
compatibility but emit `Deprecation: true` in every response. See
[`docs/migration-app-model.md`](migration-app-model.md) for the migration guide.

---

## Code map

| Concern | Location |
|---------|----------|
| Canonical app model types | `internal/domain/app.go` |
| Project / Environment / Cluster types | `internal/domain/types.go` |
| Deprecated service types | `internal/domain/types.go` — `Service` (do not extend) |
| App store interface | `internal/domain/interfaces.go` — `AppStore` |
| Cluster store interface | `internal/domain/interfaces.go` — `ClusterStore` |
| App creation use-case | `internal/app/creator.go` |
| Preview use-case | `internal/app/preview.go` |
| Promotion use-case | `internal/app/promotion.go` |
| Service→App compat bridge | `internal/compat/` (temporary; will be deleted) |
| HTTP handlers — apps (canonical) | `internal/server/app_handler.go` |
| HTTP handlers — clusters | `internal/server/cluster_handler.go` |
| HTTP handlers — org environments | `internal/server/org_env_handler.go` |
| HTTP handlers — project environments | `internal/server/project_env_handler.go` |
| HTTP handlers (legacy) | `internal/server/services.go`, `inventory.go` |
| Env instance helpers | `internal/domain/env_instance.go` |
| Namespace / pattern generation | `internal/domain/env_instance.go` — `GenerateNamespaceFromPattern` |
| Namespace validation | `internal/domain/validate.go` |
| Kubernetes cluster store | `internal/kube/cluster_store.go` |
| Per-cluster client pool | `internal/k8s/cluster_pool.go` |

---

## Related documents

- [`docs/adr/0001-app-as-primary-deployment-object.md`](adr/0001-app-as-primary-deployment-object.md) — decision record
- [`docs/migration-app-model.md`](migration-app-model.md) — service → app migration guide
- [`docs/templates.md`](templates.md) — template system overview
- [`docs/templates-components.md`](templates-components.md) — app components: composed apps, per-component values and env vars
