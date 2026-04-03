# Migration guide: service model → app model

suparShip is transitioning its primary UX and API object from **service** to
**app**. This document explains why the change is happening, what the old and
new models look like, and how existing callers should migrate.

The service-oriented endpoints remain fully functional and **are not being
removed in this release**. They emit a `Deprecation: true` HTTP response header
(RFC 8594) so API clients can surface a migration signal to their operators.

---

## Background

The original implementation modelled every deployable workload as a *service*
belonging to a project. This matched the internal data format
(`project.Spec.Services`) and was a reasonable MVP shortcut.

The product direction (see [`suparship-product-architecture.mdc`]
(../.cursor/rules/suparship-product-architecture.mdc)) defines the primary
top-level object as an **app**: a deployable unit that a developer creates,
previews, and promotes. An app may contain one or more *components* (web,
worker, cron). The environment where an app runs is modelled as an
`AppEnvironment`, not as a separately navigated environment object.

The transition is being done incrementally:

1. App-oriented domain types (`domain.App`, `domain.AppEnvironment`) and
   `domain.AppStore` were added alongside the legacy types.
2. A compatibility bridge (`internal/compat`) maps service data to app types
   for read-only app API responses while the underlying storage still speaks
   services.
3. The HTTP layer now exposes **both** sets of endpoints side by side.
4. Once native app persistence is complete, the service endpoints and
   compatibility bridge will be removed.

---

## Old model (service-oriented)

### Storage

Each project CR (`project.Project`) carries a `spec.services` list. Each entry
is a `project.Service`:

```yaml
# gitops-output/.../project.yaml
spec:
  services:
    - name: hello
      template:
        name: web-service
        version: "1.0.0"
      values:
        service_name: hello
```

### Domain types (deprecated)

| Type | Package | Status |
|------|---------|--------|
| `Service` | `internal/domain` | Deprecated — use `App` |
| `ServiceStatus` | `internal/domain` | Deprecated — use `AppRuntimeStatus` |
| `Preview.ServiceName` | `internal/domain` | Deprecated field — treat as `AppName` |
| `ServiceStore` | `internal/domain` | Deprecated interface — use `AppStore` |
| `RuntimeStatusReader` | `internal/domain` | Deprecated interface |
| `LogReader` | `internal/domain` | Deprecated interface |
| `Provider.GetServiceRuntime` | `internal/runtime` | Deprecated method |

### HTTP endpoints (deprecated, compatibility only)

All routes below emit `Deprecation: true` in every response.

| Method | Path | Purpose |
|--------|------|---------|
| `POST` | `/api/v1/projects/{project}/services` | Create a service (app) |
| `GET` | `/api/v1/environments` | List all environments across projects |
| `GET` | `/api/v1/projects/{project}/services` | List services in a project |
| `GET` | `/api/v1/projects/{project}/services/{service}` | Get service detail |
| `GET` | `/api/v1/projects/{project}/services/{service}/previews` | List service previews |
| `GET` | `/api/v1/previews` | List all previews (global) |
| `POST` | `/api/v1/previews` | Create a preview (body carries `service` field) |
| `DELETE` | `/api/v1/previews/{name}` | Delete a preview |
| `POST` | `/api/v1/projects/{project}/services/{service}/promote` | Promote a service |
| `GET` | `/api/v1/projects/{project}/services/{service}/logs` | Get service logs |

### Response DTOs (deprecated)

| DTO | Replacement |
|-----|-------------|
| `createServiceRequest` / `createServiceResponse` | `createAppRequest` / `createAppResponse` |
| `serviceResponseDTO` | `AppDetailDTO` |
| `ServiceRuntimeDTO` | `AppSummaryDTO` |
| `ServiceDetailResponse` | `AppDetailResponse` |
| `ServiceEnvDTO` | `AppEnvironmentSummaryDTO` |
| `EnvironmentsResponse` | per-app `AppEnvironmentsResponse` |
| `PreviewDTO` (with `service` field) | `AppPreviewSummaryDTO` (with `appName` field) |
| `CreatePreviewRequest` (with `service`) | `CreateAppPreviewRequest` (app in path) |
| `PromoteResponse` (with `service`) | `AppPromoteResponse` (with `app`) |
| `LogsResponse` (with `service`) | `AppLogsResponse` (with `app`) |

---

## New model (app-oriented)

### Domain types

| Type | Package | Notes |
|------|---------|-------|
| `App` | `internal/domain/app.go` | Primary top-level entity |
| `AppSpec` | `internal/domain/app.go` | Template ref, curated values, components |
| `AppEnvironment` | `internal/domain/app.go` | One instance of an app in an env |
| `AppRuntimeStatus` | `internal/domain/app.go` | Replica / phase / ingress state |
| `Component` | `internal/domain/app.go` | Named component (web, worker, cron) |
| `AppStore` | `internal/domain/interfaces.go` | Replaces `ServiceStore` |

### HTTP endpoints (current, non-deprecated)

| Method | Path | Purpose |
|--------|------|---------|
| `GET` | `/api/v1/projects/{project}/apps` | List apps in a project |
| `GET` | `/api/v1/projects/{project}/apps/{app}` | Get app detail |
| `POST` | `/api/v1/projects/{project}/apps` | Create an app from a template |
| `GET` | `/api/v1/projects/{project}/apps/{app}/environments` | List app environments |
| `GET` | `/api/v1/projects/{project}/apps/{app}/environments/{env}` | Get one environment |
| `GET` | `/api/v1/projects/{project}/apps/{app}/previews` | List app previews |
| `POST` | `/api/v1/projects/{project}/apps/{app}/previews` | Create a preview |
| `DELETE` | `/api/v1/projects/{project}/apps/{app}/previews/{name}` | Delete a preview |
| `POST` | `/api/v1/projects/{project}/apps/{app}/promote` | Promote an app |
| `GET` | `/api/v1/projects/{project}/apps/{app}/logs` | Get app logs |

---

## Endpoint mapping

Use this table to update your API calls:

| Old (service) endpoint | New (app) endpoint |
|------------------------|--------------------|
| `POST .../services` | `POST .../apps` |
| `GET .../services` | `GET .../apps` |
| `GET .../services/{svc}` | `GET .../apps/{app}` |
| `GET .../services/{svc}/previews` | `GET .../apps/{app}/previews` |
| `POST .../services/{svc}/promote` | `POST .../apps/{app}/promote` |
| `GET .../services/{svc}/logs` | `GET .../apps/{app}/logs` |
| `GET /api/v1/previews` | `GET .../apps/{app}/previews` |
| `POST /api/v1/previews` (body: `service`) | `POST .../apps/{app}/previews` (app in URL) |
| `DELETE /api/v1/previews/{name}` | `DELETE .../apps/{app}/previews/{name}` |
| `GET /api/v1/environments` | `GET .../apps/{app}/environments` |

### Request body changes

**Create preview** — move `service` from body to the URL path:

```bash
# Old
POST /api/v1/previews
{ "name": "pr-42", "project": "myapi", "service": "hello" }

# New
POST /api/v1/projects/myapi/apps/hello/previews
{ "name": "pr-42" }
```

**Create service → create app** — rename the top-level wrapper key:

```bash
# Old
POST /api/v1/projects/myapi/services
{ "name": "hello", "template": "web-service", "values": { ... } }

# New
POST /api/v1/projects/myapi/apps
{ "name": "hello", "template": "web-service", "values": { ... } }
```

The `values` and `secretRefs` shapes are identical. The response changes the
outer key from `"service"` to `"app"`.

---

## Deprecation signal

Every legacy service endpoint now includes this HTTP response header:

```
Deprecation: true
```

This follows [RFC 8594](https://www.rfc-editor.org/rfc/rfc8594). Clients can
inspect the header to emit a warning to their operators without breaking
existing integrations.

---

## Compatibility bridge

The package `internal/compat` contains `ServiceBackedAppStore`, which
implements `domain.AppStore` on top of `domain.ServiceStore` and
`domain.PreviewStore`. This is what powers the app-oriented read endpoints in
the current release — they serve app-shaped responses backed by the existing
service data.

When native app persistence is implemented:
- `ServiceBackedAppStore` will be wired out and replaced by the native store.
- The `internal/compat` package will be deleted.
- The legacy service HTTP routes will be removed.

---

## Internal code migration checklist

For contributors updating service-oriented code paths to the app model:

- [ ] Replace `domain.ServiceStore` with `domain.AppStore` in the feature
- [ ] Replace `domain.RuntimeStatusReader.GetServiceStatus` with app-native
      runtime reading
- [ ] Replace `domain.LogReader.GetLogs` with the app-scoped logs provider
- [ ] Update `project.Spec.Services` persistence to use the app storage layer
- [ ] Remove uses of `internal/compat.ServiceBackedAppStore` once real
      app persistence is complete
- [ ] Delete the deprecated HTTP handlers from `services.go`, `inventory.go`,
      `previews.go`, `promote.go`, and `logs.go`
- [ ] Remove the `legacyServiceRoute` wrapper from `compat.go`
- [ ] Delete `internal/compat` when no callers remain
