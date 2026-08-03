# suparship UI

React + Vite + TypeScript + Tailwind frontend for suparship.

---

## Running locally (against the local backend)

> The quickest path is `task dev` from the repo root — it starts the fake-mode
> backend and this Vite dev server together. For full-stack/cluster work, `task up`
> (Tilt) runs the backend in-cluster and Vite here proxies to its port-forward.
> See [docs/contributor-guide/hacking-on-suparship.md](../docs/contributor-guide/hacking-on-suparship.md).
> The manual two-terminal flow below is the underlying setup.

The Vite dev server proxies all `/api` requests to the Go backend on `:8080`
(the in-cluster pod's port-forward when using Tilt), so no manual CORS
configuration is needed on the frontend side.

### Terminal 1 — Go backend (fake / no-cluster mode)

```bash
# From the repo root — first time only
cp .env.example .env

# Build and start the backend with fake in-memory data
make dev-api
```

The `dev-api` target sets `SUPARSHIP_CORS_ORIGINS=http://localhost:5173`.
To activate fake (no-cluster) mode, make sure your `.env` has:

```
SUPARSHIP_DEV_MODE=local
```

You should see:

```
level=INFO msg="runtime mode: fake — in-memory seed data, no cluster required" trigger=SUPARSHIP_DEV_MODE=local login=admin@local password_env=SUPARSHIP_ADMIN_PASSWORD
level=INFO msg="suparship server listening" addr=:8080
```

### Terminal 2 — Vite dev server

```bash
cd ui
npm install   # first time only
npm run dev
```

Open **http://localhost:5173** in your browser.

**Default login** (fake mode):

| Field    | Value         | Override env var          |
|----------|---------------|---------------------------|
| Username | `admin@local` | `SUPARSHIP_ADMIN_EMAIL`   |
| Password | `admin123`    | `SUPARSHIP_ADMIN_PASSWORD`|

---

## API layer

All backend calls go through a single typed module tree under `src/lib/`.
There are no ad-hoc `fetch` calls in page or component files.

| Module                | Covers                                              |
|-----------------------|-----------------------------------------------------|
| `src/lib/api.ts`      | Base `fetch` wrapper, `ApiError`, `api.get/post/del`|
| `src/lib/auth.ts`     | `login`, `logout`, `fetchMe`                        |
| `src/lib/settings.ts` | org, teams, projects, project detail, RBAC          |
| `src/lib/services.ts` | environments, project services, service detail, logs, promote, create service |
| `src/lib/previews.ts` | global previews list, service-scoped previews, create, delete |
| `src/lib/templates.ts`| template list, template detail                      |
| `src/lib/onboarding.ts` | onboarding status                                 |

### Proxy configuration

`vite.config.ts` proxies `/api → http://127.0.0.1:8080`.  
This is the only place you need to change the backend address.

### Error handling

All functions throw `ApiError` (from `src/lib/api.ts`) on non-2xx responses.
`ApiError` carries the HTTP `status` and a `message` string extracted from the
response body's `{ error: "..." }` field.

---

## Types

All shared types live in `src/types/index.ts`.  
They mirror the JSON shapes returned by the Go backend DTOs.

Key types and their backend counterparts:

| TypeScript                | Backend DTO                    | Endpoint                                         |
|---------------------------|--------------------------------|--------------------------------------------------|
| `AuthUser`                | auth response                  | `GET /api/v1/auth/me`                            |
| `Project`                 | `ProjectDTO`                   | `GET /api/v1/projects`                           |
| `ProjectDetail`           | `ProjectDetailResponse`        | `GET /api/v1/projects/{project}`                 |
| `EnvironmentInfo`         | `EnvironmentDTO`               | `GET /api/v1/environments`                       |
| `ServiceRuntime`          | service list item              | `GET /api/v1/projects/{project}/services`        |
| `ServiceDetailInfo`       | service detail response        | `GET /api/v1/projects/{project}/services/{svc}`  |
| `PreviewEnvironment`      | `PreviewDTO`                   | `GET /api/v1/previews`                           |
| `TemplateSummary`         | template list item             | `GET /api/v1/templates`                          |
| `TemplateDetail`          | template detail response       | `GET /api/v1/templates/{name}`                   |
| `LogsResponse`            | logs response                  | `GET /api/v1/projects/{project}/services/{svc}/logs` |

---

## Scripts

Run from the `ui/` directory:

| Script              | Description                                  |
|---------------------|----------------------------------------------|
| `npm run dev`       | Start Vite dev server with HMR on `:5173`    |
| `npm run build`     | Type-check and build for production (`dist/`)|
| `npm run preview`   | Serve the production build locally           |
| `npm run typecheck` | Run TypeScript type checking only (no emit)  |

---

## Serving the built frontend from the backend

```bash
npm run build                        # outputs ui/dist/
suparship server --ui-dir ui/dist    # backend serves the SPA at /
```

The backend falls back to `index.html` for unknown paths, enabling client-side
routing without a separate web server.
