# Local Development Configuration

suparShip uses environment variables for local development settings.
A root `.env.example` file contains sensible defaults — copy it to get
started:

```bash
cp .env.example .env
```

## Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `SUPARSHIP_DEV_MODE` | `local` | Development mode. `local` disables real cluster integrations. |
| `SUPARSHIP_AUTH_MODE` | `basic` | Auth strategy. `basic` uses the bootstrap admin account. |
| `SUPARSHIP_ADMIN_EMAIL` | `admin@local` | Admin email for local login. |
| `SUPARSHIP_ADMIN_PASSWORD` | `admin123` | Admin password for local login (**dev only**). |
| `SUPARSHIP_BACKEND_ADDR` | `:8080` | HTTP listen address for the Go backend. |
| `SUPARSHIP_FRONTEND_PORT` | `5173` | Port for the Vite dev server. |
| `SUPARSHIP_CLUSTER_MODE` | `fake` | Cluster integration mode. `fake` stubs Kubernetes calls. |

## How `.env` files work

- `.env` — primary local overrides (git-ignored).
- `.env.local`, `.env.*.local` — additional local overrides (git-ignored).
- `.env.example` — tracked reference with safe defaults. Never put real
  secrets here.

The `.env` file is **not** loaded automatically by the Go binary. It is
consumed by Task (`Taskfile.yml`) and by the Vite dev server. If you run
the backend directly, export the variables yourself or use a tool like
[direnv](https://direnv.net/).

## Cluster modes

| Mode | Behaviour |
|------|-----------|
| `fake` | No real Kubernetes calls. API returns stubbed responses. Ideal for frontend-only or offline work. |
| `local` | Connects to the current kubeconfig context (e.g. k3d). |

## Security notes

- The values in `.env.example` are **for local development only**.
- Never copy development credentials into production.
- `.env` is git-ignored to prevent accidental secret commits.
