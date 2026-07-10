# ADR-0001: App as Primary User-Facing Deployment Object

**Status:** Accepted  
**Date:** 2026-04-03  
**Authors:** suparCloud engineering

---

## Context

The initial MVP used **service** as the top-level deployable entity inside a project. This mirrored common DevOps terminology (`kubectl`, Helm, microservice language) and was a reasonable starting point.

However, the product vision for suparship is a **Vercel-like developer experience**. Products like Vercel, Railway, and Render orient the developer around an **app** — the thing you create, preview, and promote — not around a Kubernetes "service" or a Helm release.

Several problems emerged with the service-centric model:

1. **Semantic mismatch.** Developers think of "the app I'm working on". They do not think of "the service that runs in a namespace". Calling it a service leaks Kubernetes implementation details into the UX.

2. **Wrong primary navigation.** Environments (`staging`, `prod`) were implicitly top-level objects that services lived inside. In practice, developers navigate to *an app* and then switch the environment lens, not the other way around.

3. **No room for internal complexity.** Real apps have multiple runtime processes (web server, background worker, cron job). The service model has no natural place for these as first-class entities without either creating multiple peer services (wrong) or inventing a new sub-object named "component" outside the established hierarchy (confusing).

4. **Preview and promotion semantics are app-scoped.** You preview *the app*. You promote *the app* from staging to prod. Scoping these operations at the service level was technically workable but semantically wrong and harder to explain.

---

## Decision

Adopt a four-level user-facing hierarchy:

```
org
└── project          (team / product boundary)
    └── app          (primary developer-owned object)
        ├── environment  (staging | prod | preview-*)
        └── component    (internal; web | worker | cron — advanced only)
```

### 1. App replaces service as the primary UX object

**App** is what a developer creates, configures, previews, and promotes. It is the unit of ownership, the unit of deployment, and the top-level navigation object within a project.

The term **service** is retired from user-facing language. It remains as an internal implementation detail in backend code during the transition period (see [Migration strategy](#migration-strategy)).

### 2. Environment is app-scoped, not a top-level navigation object

Environments (`staging`, `prod`, and ephemeral `preview-*` instances) are **lenses on a single app**, not independent containers. The user navigates to an app first and then selects or switches the environment within that view.

This does not change how environments map to Kubernetes namespaces (`{project}-{environment}`) or how the project config enumerates them. It changes the UX framing and the primary API hierarchy.

### 3. Component is internal and advanced by default

An **component** represents a distinct runtime process within an app — for example, `web` (HTTP server), `worker` (background queue consumer), or `cron` (scheduled job).

Components are:
- **Hidden from the default UI.** The average developer sees "app status", not "component status". Health aggregation happens at the app level.
- **Surfaced in advanced / detail views** only when the template or the operator exposes them explicitly.
- **Not a top-level navigation object.** There is no `/components` list page. Components appear inside an app's environment view when relevant.

Templates remain the mechanism by which component topology is defined. A simple template produces one implicit component (e.g., `web`). A more complex template can define multiple components; the UI exposes them progressively.

### 4. Preview is an ephemeral environment instance of an app

A preview is not a separate object type — it is an ephemeral `environment` instance of a specific app, created on demand (typically for a branch or pull request) and deleted when no longer needed.

Namespace convention: `{project}-preview-{name}` (unchanged from current implementation).

Preview creation and deletion are app-scoped operations. The API shape moves from `POST /projects/{project}/services/{service}/previews` toward `POST /projects/{project}/apps/{app}/previews`.

### 5. Promotion is an app-level release operation

Promoting an app advances a coherent release bundle across the ordered environment chain (`dev → staging → prod`). It is not a service-level operation.

The API shape moves from `POST /projects/{project}/services/{service}/promote` toward `POST /projects/{project}/apps/{app}/promote`.

---

## Consequences

### Backend API

| Current path (service-oriented) | Target path (app-oriented) | Notes |
|---|---|---|
| `POST /api/v1/projects/{p}/services` | `POST /api/v1/projects/{p}/apps` | New external path |
| `GET /api/v1/projects/{p}/services` | `GET /api/v1/projects/{p}/apps` | List apps in project |
| `GET /api/v1/projects/{p}/services/{s}` | `GET /api/v1/projects/{p}/apps/{app}` | App detail with env runtime state |
| `GET /api/v1/projects/{p}/services/{s}/previews` | `GET /api/v1/projects/{p}/apps/{app}/previews` | App-scoped previews |
| `POST /api/v1/projects/{p}/services/{s}/promote` | `POST /api/v1/projects/{p}/apps/{app}/promote` | App-scoped promotion |
| `GET /api/v1/projects/{p}/services/{s}/logs` | `GET /api/v1/projects/{p}/apps/{app}/logs` | App-scoped logs |

Old paths MUST remain registered and functional during the migration period so existing integrations are not broken.

Internal Go types and function names (e.g., `ServiceSpec`, `getService`, `listServices`) may retain their names until a dedicated refactor commit. New externally visible DTOs MUST use `app` terminology.

### UI

- Primary navigation within a project changes from "Services" to "Apps".
- The sidebar and breadcrumbs reference "Apps", not "Services".
- App detail has an environment switcher (not the environment as the primary page).
- Component health is surfaced inside the environment view, aggregated by default.
- No top-level "Environments" page listing all environments across all apps — environments are navigated through the app.
- The existing `/api/v1/environments` endpoint remains for cross-project status aggregation but is not the primary navigation surface.

### Templates

- Template metadata (`template.yaml`) uses `app` in descriptions and UI labels.
- The `components` block in a template defines the internal runtime topology and remains implementation-level; templates do not need to change structure.
- Template inputs and presets are unchanged.

### GitOps layout

Namespace naming remains deterministic and backward compatible:

```
{project}-{environment}       →  app environment instance
{project}-preview-{name}      →  ephemeral preview instance
```

ArgoCD Application names and Kargo Stage names follow the same convention. No rename is required in GitOps-committed manifests.

### Onboarding / seed data

The default seeded demo data SHOULD use "app" terminology in display names. The seeded `hello` object becomes "Hello App" rather than "Hello Service". Internal identifiers (`name: hello`) are unchanged.

---

## Migration strategy

This ADR defines the *target model* and *intent*. It does not require an immediate big-bang rename.

### Phase 1 — Documentation and intent (this commit)

- ADR written and merged.
- README and docs updated to introduce "app" terminology alongside "service" with a clear note that "service" is the current internal name.
- No code changes. No API changes.

### Phase 2 — New external API paths (follow-up commit)

- Register `/api/v1/projects/{p}/apps/...` routes pointing to the same handlers as the existing `/services/...` routes.
- Return `app`-keyed fields in JSON responses (`"app": {...}`) while keeping `"service"` as a deprecated alias if needed for a grace period.
- Annotate legacy routes with a deprecation comment in code.

### Phase 3 — UI migration ✅ (this commit)

- Updated all user-facing labels, headings, stat cards, table headers, empty states, and helper text from "service" to "app" across Dashboard, Onboarding, Templates, and ServiceDetail pages.
- Onboarding checklist and CTA copy updated to explain that apps run across staging, prod, and previews, and may include multiple runtime components.
- Internal variable names and route paths (e.g. `ServiceRow`, `/projects/:p/services/:s`) are preserved in this commit to avoid excess churn; they will be cleaned up in Phase 4.
- Frontend API client still calls existing `/services/...` paths; the new `/apps/...` paths will be wired in a follow-up once Phase 2 API routes land.

### Phase 4 — Internal rename (follow-up commit)

- Rename Go types: `ServiceSpec → AppSpec`, `getService → getApp`, etc.
- Remove legacy `/services/...` route aliases (or keep indefinitely for compatibility — decision deferred).
- Update seeded fake data display names.

### Backward compatibility guarantee

- Legacy `service`-oriented API paths (`/projects/{p}/services/...`) MUST continue to work until Phase 4 is explicitly committed and released.
- Config stored as `services:` in project ConfigMaps remains readable; the backend maps it to the app model on read.
- No breaking change is introduced to callers relying on the service-oriented paths until a deliberate deprecation release.

---

## Alternatives considered

### Keep "service" and add "app" as an alias

Rejected. Having two names for the same concept increases confusion. A clean migration with backward-compatible aliases during the transition is preferable.

### Make environment the top-level navigation object

Rejected. Developers own apps, not environments. Navigating to `staging` and then asking "which app?" is counter to how people think about their work. Vercel, Railway, and Render all confirm this: environment is a property of an app, not a container of apps.

### Expose components as first-class UX objects from day one

Rejected for MVP. Most apps have one or two components. Forcing every developer to think about components from the first click adds cognitive overhead. Progressive disclosure is the right model: start with "app health", reveal components when needed.
