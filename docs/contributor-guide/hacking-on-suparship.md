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
| Kargo UI / API | <http://localhost:8083> | `admin` / `devpass` |
| Vault (only with `task up:vault`) | <http://localhost:8200> | token `root` |

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

## Optional: real multi-cluster (`task up:multi`)

By default everything runs on one kind cluster wearing three hats — tooling,
staging and prod. `config/seed/clusters.yaml` registers two cluster records that
both point at `https://kubernetes.default.svc`. That is fine for most work, but
it is fiction, and some behaviour genuinely cannot be exercised that way.

```bash
task up:multi     # tooling cluster + kind-staging + kind-prod
```

This adds two **workload** clusters running only External Secrets and
sealed-secrets, registers each with suparship (writing its kubeconfig Secret and
its ArgoCD cluster Secret), and rebinds the seeded environments onto them. The
tooling cluster keeps suparship, ArgoCD, Kargo, Gitea and the registry — the
split [docs/install.md](../install.md) prescribes. Measured cost is about
**+2 GiB** of RAM — roughly 1 GiB per workload cluster, against ~2.5 GiB for the
tooling cluster — which is why it is opt-in; plain `task up` is unchanged.

Namespaces switch from `{app}-{env}` to `{app}` in this mode, because the cluster
boundary now provides the isolation the `-{env}` suffix was compensating for.

**What this unlocks that one cluster cannot reach:**

- kubeconfig registration and the `suparship-cluster-kubeconfig-*` Secret
- ArgoCD remote cluster Secrets and a non-in-cluster `destination.server`
- `deployMode: all` fan-out producing one Application per cluster, with
  per-cluster values under `_clusters/<cluster>/`
- cross-cluster pod-log streaming through `internal/k8s/cluster_pool.go`, which
  has no kubeconfig to work with in single-cluster mode
- the acceptance scenarios in [docs/acceptance.md](../acceptance.md), which
  require "at least one **remote** workload cluster … not in-cluster"

**What it does *not* change: promotion.** Kargo promotion is a git operation —
`git-clone → yaml-update → git-commit → git-push → argocd-update`. It never
touches a workload cluster, so it is already fully testable on one cluster. What
differs here is only where the resulting Application lands.

> **Known gap.** The `k8s` secret backend is hub-only: `_infra/secret-stores/`
> syncs to the tooling cluster, so a remote workload cluster never receives its
> `suparship-store-*` and app ExternalSecrets there will dangle. The 1Password
> backend does publish per-cluster stores. Surfacing this is part of the point of
> running multi-cluster locally.

Tear-down removes all three: `task cluster:delete`.

---

## Optional: HashiCorp Vault backend (`task up:vault`)

For working on the `vault` secrets backend. Adds a **dev-mode** Vault
(in-memory, auto-unsealed, fixed root token `root` — everything it stores dies
with the pod) and a `vault-bootstrap` resource that enables the `suparship`
KV v2 mount, creates the write-token Secret (`suparship-vault-token` in
`suparship-system`) and ESO's read-token Secret (`vault-token` in
`external-secrets`), then switches the org's secret backend to vault through
`PUT /org/secret-backend`.

The org switch goes through the API on purpose: the handler *merges* onto the
stored config, so it is safe to run after `seed`/`seed-multi` rewrite the org
ConfigMap wholesale. If you re-run `task seed` by hand afterwards, re-trigger
`vault-bootstrap` in the Tilt UI to restore the backend selection.

Composes with multi-cluster: `tilt up -- --vault --multi` (or set
`SUPARSHIP_VAULT=1`). Poke at it directly:

```bash
kubectl --context kind-suparship-dev -n vault exec vault-0 -- \
  vault kv list suparship/          # list suparship's containers in the mount
```

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
- **Can't log in to suparShip** — cluster mode is `admin` / `devpass`, *not* the
  `admin@local` / `admin123` pair sitting in your `.env`. Those two env vars are
  read only in fake mode; against a real cluster the server authenticates
  against the Secret `suparship-system/suparship-admin-auth`, created by the
  `suparship-admin-secret` resource (override with `$SUPARSHIP_DEV_PASSWORD`).
  See [config/dev/README.md](../../config/dev/README.md).
- **Can't log in to Kargo** — <http://localhost:8083>, `admin` / `devpass`. Note
  it is **http**: the Tiltfile sets `api.tls.enabled=false` so there's no
  self-signed cert prompt. If you re-enable TLS, be aware the container port is
  named `h2c` but serves https.
- **Frontend can't reach the API** — make sure the `suparship` resource is green
  (its port-forward on `:8080` is what Vite proxies to).
- **A resource is green but its port is dead** — a Tilt resource binds to the
  *newest* pod matching the release, so a `Completed` Job/CronJob pod can
  silently take over a resource that also owns long-running Deployments. Tilt
  keeps reporting it healthy (the job did succeed) while its port-forward points
  at a dead pod: the port accepts connections and proxies nowhere, so every
  request fails at the transport layer rather than returning an HTTP error.
  Check what Tilt actually attached to:

  ```bash
  tilt get uiresource <name> -o json | jq '.status.k8sResourceInfo | {podName, podStatus}'
  ```

  If `podStatus` is `Completed` **and that resource owns a port**, that's the
  bug — forward the Deployment explicitly instead of relying on pod discovery
  (see `kargo-api-forward` in the `Tiltfile` for the pattern, including the
  retry loop that survives the pod being replaced by a `helm upgrade`).

  Expect the `kargo` resource itself to keep reporting `Completed` — its hourly
  garbage-collector CronJob wins pod attribution and always will. That is
  harmless now precisely *because* the port lives on `kargo-api-forward`
  instead. Nothing to fix there.
- **Start over** — `task down && task cluster:delete && task up`.

### Safety: this loop cannot touch a real cluster

Contributors often carry production contexts in their kubeconfig, so the dev
loop is pinned in two independent places:

- The `Tiltfile` refuses to load unless `k8s_context()` is `kind-suparship-dev`,
  and aborts with a non-zero exit.
- Tilt cannot police the shell scripts it invokes via `local_resource`, so those
  source [`hack/dev/lib.sh`](../../hack/dev/lib.sh), which shadows `kubectl` and
  `helm` with functions that always pass `--context kind-suparship-dev`, and
  fails fast if that context is missing. Override with `DEV_KUBE_CONTEXT`.

`tilt up` may leave your *current* context set to `kind-suparship-dev`. Check
before running ad-hoc `kubectl` against something you assume is elsewhere.

---

## How it fits together

```
task up
 └─ ctlptl ──────────► kind cluster "suparship-dev" + registry localhost:5001
 └─ tilt up ─► Tiltfile
      ├─ helm_resource: cert-manager → argo-rollouts → kargo → kargo-api-forward
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

### Why prerequisites are `helm_resource` blocks, not a Helm umbrella chart

The obvious alternative — one chart that installs suparShip *and* its
dependencies — was tried and rejected. Two Helm limitations rule it out:

1. **Subcharts get no namespace of their own.** Everything lands in the release
   namespace, and Kargo and Gitea expose no `namespaceOverride` (argo-cd does).
2. **Kargo creates cert-manager `Certificate`/`Issuer` CRs at install time**,
   which need cert-manager's webhook *running* to be admitted. Helm does not
   wait mid-release, so a single umbrella release races and fails
   intermittently. Kargo's own docs install cert-manager beforehand.

`resource_deps` expresses exactly what Helm cannot — the parsed graph shows
`kargo deps=[cert-manager, argo-rollouts]`. So `charts/suparship` stays an
app-only chart (its `Chart.yaml` records the same reasoning), and each
prerequisite is declared once in the `Tiltfile` with its own namespace, a pinned
version and explicit ordering. To bump a version, edit the `Tiltfile` — there is
no second place to keep in sync.
