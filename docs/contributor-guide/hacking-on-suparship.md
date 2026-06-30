# Hacking on suparShip

This guide gets you from a fresh clone to a **full suparShip cluster with
hot-reload** in a few commands, using [Tilt](https://tilt.dev) and
[ctlptl](https://github.com/tilt-dev/ctlptl). It is the recommended way to work
on anything that touches the cluster (ArgoCD, Gitea, Kargo, External Secrets,
previews, GitOps).

> Just want to poke at the UI/API with no cluster? Skip to
> [No-cluster fast mode](#no-cluster-fast-mode) — it is a one-liner.

---

## Prerequisites

| Tool | Why | Install |
|------|-----|---------|
| **Docker** (or OrbStack / Docker Desktop) | runs the kind cluster + builds images | <https://docs.docker.com/get-docker/> |
| **ctlptl** | declarative kind cluster + local registry | `brew install tilt-dev/tap/ctlptl` |
| **tilt** | the dev loop | `brew install tilt-dev/tap/tilt` |
| **kubectl**, **helm** | cluster + chart ops | `brew install kubectl helm` |
| **Node.js / npm** | frontend (Vite) | <https://nodejs.org> |
| **htpasswd** | one-time dev admin secret | macOS: preinstalled · Linux: `apache2-utils` |

`task up` runs `hack/preflight.sh tilt` first and tells you exactly what (if
anything) is missing. (`go` is **not** required on the host — suparShip is
compiled inside the dev container.)

---

## Quickstart

```bash
git clone https://github.com/suparcloud/suparship.git
cd suparship
task up
```

`task up` does three things:

1. `ctlptl apply -f hack/dev/cluster.yaml` — creates the `suparship-dev` kind
   cluster wired to a local image registry on `localhost:5001`.
2. `tilt up` — installs every prerequisite, builds suparShip from source, and
   deploys it in-cluster.
3. Opens the **Tilt UI at <http://localhost:10350>** — watch resources go green.

First run pulls a lot of images and installs Helm charts, so it takes a few
minutes. Subsequent runs are fast (the cluster and images are cached).

When everything is green:

| Service | URL | Credentials |
|---------|-----|-------------|
| **suparShip UI** (Vite HMR) | <http://localhost:5173> | `admin` / `devpass` |
| suparShip API (in-cluster pod) | <http://localhost:8080> | — |
| Tilt UI | <http://localhost:10350> | — |
| ArgoCD | <http://localhost:8081> | `admin` / (see below) |
| Gitea | <http://localhost:3000> | `gitops` / `gitops-dev-only` |
| Kargo API | <https://localhost:8083> | — |

> ArgoCD admin password:
> `kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' | base64 -d`

Tear down:

```bash
task down            # stop Tilt, remove workloads (keeps the cluster)
task cluster:delete  # delete the kind cluster + registry entirely
```

---

## The inner loop

suparShip runs **in the cluster** (as the `suparship` Deployment from
`charts/suparship`), and Tilt keeps it in sync with your working tree:

- **Backend (Go).** Edit anything under `cmd/` or `internal/`. Tilt syncs the
  changed files into the running container and runs `go build` **in place**
  (the Go build cache stays warm in the container), then restarts the process.
  Typical edit → live is a few seconds. Changing `go.mod`/`go.sum` triggers a
  full image rebuild (expected).
- **Frontend (React/Vite).** The `ui-dev` resource runs `npm run dev` on the
  host (port `5173`) with full HMR. `ui/vite.config.ts` proxies `/api` to
  `http://127.0.0.1:8080`, which is Tilt's port-forward to the in-cluster pod —
  so the host UI talks to the in-cluster API. Edit `ui/src/**` and the browser
  updates instantly.
- **Bundled UI** (for testing the production "server serves the UI from disk"
  path, or ingress): trigger the **`ui-build`** resource in the Tilt UI. It runs
  `npm run build --watch` → `ui/dist`, which Tilt syncs into the pod's
  `/app/ui`.

Use the Tilt UI to read logs, trigger a manual rebuild, or disable a resource.

---

## Optional: ingress + `*.localhost` routing

By default everything is reached via **port-forwards** — no `/etc/hosts`,
`dnsmasq`, or `sudo`. If you need to exercise real ingress / preview-URL routing:

```bash
task up:ingress     # installs NGINX ingress (v1.10.1) and enables Ingress objects
```

Services are then also reachable at `http://suparship.localhost:8880`,
`http://argocd.localhost:8880`, `http://gitea.localhost:8880`. On macOS this
needs `*.localhost` to resolve to `127.0.0.1` — see [docs/local-dns.md](../local-dns.md)
(only required for the ingress path).

---

## No-cluster fast mode

For UI/API work that doesn't need a real cluster, the in-memory **fake mode**
needs no Docker, no Kubernetes:

```bash
cp .env.example .env     # sets SUPARSHIP_DEV_MODE=local
task dev                 # backend (fake) on :8080 + Vite UI on :5173
```

Open <http://localhost:5173>, log in `admin@local` / `admin123`. Data is seeded
in memory and resets on restart. `Ctrl+C` stops both.

---

## Tests, lint, format

```bash
make test         # go test -race ./...
make test-smoke   # API smoke tests, no cluster
make lint         # golangci-lint (if installed)
make fmt          # gofumpt / gofmt
task charts:lint  # helm lint the library + template charts
```

---

## Troubleshooting

- **`Tilt is pointed at … but expected kind-suparship-dev`** — the cluster isn't
  selected. Run `ctlptl apply -f hack/dev/cluster.yaml` (or `task up`, which does it).
- **A prereq is stuck `Pending`** — open it in the Tilt UI and read the logs.
  Kargo waits on cert-manager's webhook; ESO waits on its CRDs — both are gated
  with `resource_deps`, but a slow image pull can still time out a `--wait`.
  Re-trigger the resource.
- **`exec format error` in the suparship pod** — the dev image was built for the
  wrong arch. It builds natively for the kind node; don't override `GOARCH`.
- **Can't log in** — the dev admin Secret is created by the `suparship-admin-secret`
  resource (`admin` / `devpass`, or `$SUPARSHIP_DEV_PASSWORD`). Re-trigger it if needed.
- **Frontend can't reach the API** — make sure the `suparship` resource is green
  (its port-forward on `:8080` is what Vite proxies to).
- **Start over** — `task down && task cluster:delete && task up`.

---

## How it fits together

```
task up
 └─ ctlptl ──────────► kind cluster "suparship-dev" + registry localhost:5001
 └─ tilt up ─► Tiltfile
      ├─ helm_resource: cert-manager → argo-rollouts → kargo
      ├─ helm_resource: external-secrets → eso-reader (RBAC + ClusterSecretStore)
      ├─ helm_resource: kubernetes-replicator, reloader
      ├─ helm_resource: argocd, gitea → init-gitops (repo skeleton + root App-of-Apps)
      ├─ docker_build_with_restart: Dockerfile.dev  (live_update: go build in-container)
      ├─ helm(charts/suparship): the suparship Deployment  ◄── your code, hot-reloaded
      ├─ local_resource: suparship-admin-secret, seed
      └─ local_resource: ui-dev (Vite HMR), ui-build (opt-in bundled UI)
```

Chart/version pins live in the `Tiltfile`; the dev image is `Dockerfile.dev`;
cluster + registry are `hack/dev/cluster.yaml`; dev chart overrides are
`hack/dev/values-dev.yaml`.
