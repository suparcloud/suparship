# suparShip

**Vercel-like developer experience for Kubernetes teams.**  
Open source. GitOps-native. Built on CNCF projects.

---

## What is suparShip?

**suparShip** is an open-source platform runtime from **suparCloud** that lets small SRE / DevOps teams provide a **simple, self-service PaaS-like experience** to developers — without building or maintaining a full platform team.

It standardizes the *golden paths* for:
- deploying applications
- creating preview environments
- promoting safely to production

…while using **proven open-source tools** like ArgoCD and Kargo under the hood.

> If you can run Kubernetes, you can run a great platform.

---

## Why suparShip?

Most teams want:
- preview environments for every change
- consistent deploy workflows
- safe promotions to production

But they:
- don’t have time to build an internal platform
- don’t want vendor lock-in
- eventually outgrow hosted PaaS solutions

**suparShip sits in the middle**:
- simple like a PaaS
- flexible like Kubernetes
- transparent like GitOps

---

## Key concepts

suparShip is built around a small set of primitives:

- **Environment** – `staging`, `prod`, `preview`
- **Project** – logical grouping (team / product)
- **Service** – a deployable workload
- **Template** – a golden path for deploying a service

Developers think in:
> *service → environment → preview → promote*

SREs define:
> *defaults, templates, and guardrails*

---

## Quickstart (no cloud, no paid services)

### Prerequisites

- `kubectl`
- `k3d`
- (optional) `docker` – required only for local build mode

### Install suparShip

```bash
curl -fsSL https://suparcloud.io/install/suparship.sh | sh
```

---

## Running the API Server

Start the built-in HTTP server:

```bash
suparship server              # listens on :8080
suparship server --addr :9090 # custom address
```

### Server flags

| Flag | Env var | Default | Description |
|------|---------|---------|-------------|
| `--addr` | `SUPARSHIP_ADDR` | `:8080` | Listen address |
| `--ui-dir` | `SUPARSHIP_UI_DIR` | | Path to built frontend assets |
| `--cors-origins` | `SUPARSHIP_CORS_ORIGINS` | | Comma-separated allowed origins |
| `--templates-dir` | `SUPARSHIP_TEMPLATES_DIR` | | Path to templates directory |
| `--cookie-secure` | `SUPARSHIP_COOKIE_SECURE` | `false` | Set `Secure` flag on session cookies (enable behind HTTPS) |

### Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/healthz` | — | Liveness probe — returns `ok` |
| GET | `/readyz` | — | Readiness probe — returns `ok` |
| GET | `/api/v1/meta` | — | JSON build metadata (app, version, commit, date) |
| POST | `/api/v1/auth/login` | — | Authenticate with username/password, returns session cookie |
| POST | `/api/v1/auth/logout` | — | Destroy session and clear cookie |
| GET | `/api/v1/auth/me` | session | Return current user identity and role |
| GET | `/api/v1/org` | session | Return org name, display name, created at |
| GET | `/api/v1/teams` | session | List all teams with members |
| GET | `/api/v1/projects` | session | List all projects |
| GET | `/api/v1/projects/{project}` | viewer | Get project (placeholder) |
| GET | `/api/v1/projects/{project}/rbac` | viewer | List role bindings for a project |
| PUT | `/api/v1/projects/{project}` | project_admin | Update project (placeholder) |
| POST | `/api/v1/projects/{project}/previews` | developer | Create preview (placeholder) |
| POST | `/api/v1/projects/{project}/services/{service}/promote` | developer | Promote service (placeholder) |
| GET | `/api/v1/templates` | session | List all available templates |
| GET | `/api/v1/templates/{name}` | session | Get full template detail for form generation |

Auth endpoints are enabled automatically when a Kubernetes cluster is
reachable (they validate against the `suparship-admin-auth` Secret).
RBAC-protected routes require both auth and an org config provider.
Session cookies are `HttpOnly` and `SameSite=Lax`.

---

## Admin Auth

suparShip uses a single bootstrap admin account stored as a Kubernetes Secret
in the `suparship-system` namespace.

### Bootstrap the admin user

```bash
suparship admin bootstrap                  # username defaults to "admin"
suparship admin bootstrap --username ops   # custom username
suparship admin bootstrap --force          # overwrite existing credentials
```

The generated password is printed once — save it immediately.

### Reset the admin password

```bash
suparship admin reset-password
```

The username is preserved; only the password is regenerated.

### Secret layout

The credentials are stored in `suparship-system/suparship-admin-auth`:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: suparship-admin-auth
  namespace: suparship-system
type: Opaque
stringData:
  username: admin
  password-hash: $2a$12$...   # bcrypt hash — never plaintext
```

---

## Authorization Model

suparShip uses a single-org RBAC model. The org configuration is stored in a
Kubernetes ConfigMap (`suparship-system/suparship-org-config`).

### Roles (highest → lowest privilege)

| Role | Description |
|------|-------------|
| `org_admin` | Full access to all projects and settings |
| `project_admin` | Full access within assigned projects |
| `developer` | Deploy and manage services in assigned projects |
| `viewer` | Read-only access to assigned projects |

Higher roles implicitly satisfy lower role requirements.

### Data structure

```yaml
# ConfigMap: suparship-system/suparship-org-config
# Key: org.yaml
name: default
displayName: My Organization
createdAt: "2026-03-27T00:00:00Z"
teams:
  - name: admins
    displayName: Administrators
    members: [admin]
  - name: backend
    displayName: Backend Team
    members: [alice, bob]
roleBindings:
  - project: "*"        # wildcard = all projects
    team: admins
    role: org_admin
  - project: api
    team: backend
    role: developer
```

### Role resolution

For a given user and project:

1. Find all teams the user belongs to.
2. Collect role bindings that match the project (exact match or wildcard `*`).
3. Return the highest-privilege role across all matching bindings.

---

## Development

### Prerequisites

- Go 1.23+
- Node.js 20+ and npm

### Quick start (two terminals)

```bash
# Terminal 1 — backend API (with CORS for the Vite dev server)
make dev-api

# Terminal 2 — frontend with HMR
make dev-ui
```

Open http://localhost:5173 in your browser. The Vite dev server proxies
`/api` requests to the Go backend on `:8080`.

### Serving the built frontend from the backend

```bash
cd ui && npm run build        # produces ui/dist/
suparship server --ui-dir ui/dist
```

### Make targets

| Target | Description |
|--------|-------------|
| `make build` | Build the `suparship` binary |
| `make test` | Run all Go tests |
| `make dev-api` | Build and run backend with CORS enabled for `localhost:5173` |
| `make dev-ui` | Run the Vite dev server |
| `make lint` | Run Go linters |
| `make fmt` | Format Go code |
| `make clean` | Remove build artifacts |

### Frontend scripts (run from `ui/`)

| Script | Description |
|--------|-------------|
| `npm run dev` | Start Vite dev server with HMR |
| `npm run build` | Type-check and build for production |
| `npm run preview` | Preview the production build locally |
| `npm run typecheck` | Run TypeScript type checking only |
