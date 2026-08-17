# Local Development Configuration

suparship uses environment variables for local development settings.
A root `.env.example` file contains sensible defaults — copy it to get
started:

```bash
cp .env.example .env
```

## Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `SUPARSHIP_DEV_MODE` | `local` | Set to exactly `local` for fake mode. Any other value (or unset) means real Kubernetes. |
| `SUPARSHIP_CLUSTER_MODE` | `fake` | Set to exactly `fake` for fake mode. Any other value means real Kubernetes. |
| `SUPARSHIP_ADMIN_EMAIL` | `admin@local` | **Fake mode only.** Login username. Ignored against a real cluster. |
| `SUPARSHIP_ADMIN_PASSWORD` | `admin123` | **Fake mode only.** Login password — dev only, never production. |
| `SUPARSHIP_ADDR` | `:8080` | HTTP listen address for the Go backend. |
| `SUPARSHIP_FRONTEND_PORT` | `5173` | Port for the Vite dev server (read by `hack/dev.sh`). |
| `VITE_DEBUG` | unset | Log the Vite dev-server's `/api` proxy calls (`ui/vite.config.ts`). |

## Runtime mode

There are exactly two modes, and only two literal values select the fake one
(`internal/config/config.go`):

```
SUPARSHIP_DEV_MODE=local      → fake
SUPARSHIP_CLUSTER_MODE=fake   → fake
anything else                 → real Kubernetes
```

| Mode | Behaviour |
|------|-----------|
| fake | No Kubernetes calls at all. Stores are in-memory and reseed on restart. Ideal for UI, API-handler and template work. |
| Kubernetes | Builds a real client — in-cluster config when running as a pod, otherwise your current kubeconfig context. |

> **`SUPARSHIP_CLUSTER_MODE=local` does not mean "local/offline".** It is not a
> recognised value, so it falls through to **real Kubernetes** — the opposite of
> what it reads like. If you want fake mode, the value is `fake`.

## Login credentials differ by mode

Both modes use the SAME login — `admin@local` / `admin123` — but it comes
from different places:

| Command | Mode | Credentials | Source |
|---------|------|-------------|--------|
| `task dev` | fake | `admin@local` / `admin123` | the env vars above (`internal/fake/adapters.go`) |
| `task up` | Kubernetes | `admin@local` / `admin123` | Secret `suparship-system/suparship-admin-auth` (created by `hack/dev/admin-secret.sh` with the same defaults) |

In Kubernetes mode the admin env vars are ignored entirely. The Secret holds a
username and a **bcrypt** `password-hash`, created by `hack/dev/admin-secret.sh`
(override the password with `SUPARSHIP_DEV_PASSWORD`, then re-trigger the
`suparship-admin-secret` resource in Tilt). With no Secret present, login
returns `503 admin credentials not configured`.

## How `.env` files work

- `.env` — primary local overrides (git-ignored).
- `.env.local`, `.env.*.local` — additional local overrides (git-ignored).
- `.env.example` — tracked reference with safe defaults. Never put real
  secrets here.

The `.env` file is **not** loaded by the Go binary. Task loads it
(`Taskfile.yml` `dotenv:`) and exports the values into whatever it starts. Two
consequences worth internalising:

1. Running `./bin/suparship server` directly picks up **none** of it — export
   the variables yourself or use [direnv](https://direnv.net/).
2. It does not reach the in-cluster pod that `task up` deploys. That pod's
   environment comes from `charts/suparship`, so editing `.env` cannot change
   how the cluster-mode server behaves.

## Security notes

- The values in `.env.example` are **for local development only**.
- Never copy development credentials into production.
- `.env` is git-ignored to prevent accidental secret commits.
