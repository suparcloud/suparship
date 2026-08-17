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

The loop works on **macOS** and **Linux** (Ubuntu/Debian, RHEL/CentOS/Fedora).

| Tool | Why | macOS | Linux |
|------|-----|-------|-------|
| **Docker** (rootful) | kind cluster + image builds; the ingress binds host port **80**, which rootless docker can't | OrbStack / Docker Desktop | [docker-ce](https://docs.docker.com/engine/install/) |
| **kind**, **ctlptl** | declarative kind cluster + local registry | `brew install kind tilt-dev/tap/ctlptl` | release binaries |
| **tilt** | the dev loop | `brew install tilt-dev/tap/tilt` | curl installer |
| **kubectl**, **helm** | cluster + chart ops | `brew install kubectl helm` | vendor repos |
| **Node.js / npm** | frontend (Vite) | `brew install node` | <https://nodejs.org> |
| **htpasswd** | one-time dev admin secret | preinstalled | `apache2-utils` (deb) / `httpd-tools` (rpm) |

`task up` runs `hack/preflight.sh tilt` first and tells you exactly what (if
anything) is missing, with per-platform hints. (`go` is **not** required on
the host — suparShip is compiled inside the dev container.)

Linux specifics, all handled but worth knowing: `task dev:dns` is a no-op on
systemd-resolved distros (they already resolve `*.localhost`) and configures
dnsmasq elsewhere; CI job containers get `host.docker.internal` via an
explicit `host-gateway` mapping (native Linux docker doesn't provide it).

---

## Quickstart

```bash
git clone https://github.com/suparcloud/suparship.git
cd suparship
task dev:dns   # once per machine: *.localhost → 127.0.0.1 (no-op if it already resolves)
task up
```

`task up` does three things:

1. `ctlptl apply -f hack/dev/cluster.yaml` — creates the `suparship-dev` kind
   cluster wired to a local image registry on `localhost:5001`, with host
   port 80 mapped so app URLs like `http://myapp.staging.localhost` work
   without a port suffix.
2. `tilt up` — installs every prerequisite (including the NGINX ingress and
   the Vault secrets backend, both on by default), builds suparShip from
   source, and deploys it in-cluster.
3. Opens the **Tilt UI at <http://localhost:10350>** — watch resources go green.

First run pulls a lot of images and installs Helm charts, so it takes a few
minutes. Subsequent runs are fast (the cluster and images are cached).

> **Upgrading an existing dev cluster:** the port-80 mapping is part of the
> kind cluster definition and can't be added live — run
> `task cluster:delete && task up` once (destroys dev cluster state: seeded
> apps, Gitea repos, Vault data; also
> `docker volume rm suparship-act-runner-data` so the CI runner re-registers).

When everything is green:

One dev password everywhere a human logs in: **`admin123`** (the `gitops`
Gitea account is machinery and keeps its own).

| Service | URL | Credentials |
|---------|-----|-------------|
| **suparShip UI** (Vite HMR) | <http://localhost:5173> | `admin@local` / `admin123` (same as fake mode) |
| suparShip API (in-cluster pod) | <http://localhost:8080> | `admin@local` / `admin123` |
| Tilt UI | <http://localhost:10350> | — |
| ArgoCD | <http://localhost:8081> | `admin` / `admin123` |
| Gitea | <http://localhost:3000> | `gitops` / `gitops-dev-only` |
| Kargo UI / API | <http://localhost:8083> | password `admin123` (admin login is password-only; plain **http**) |
| Registry (kind, unauthenticated) | `localhost:5001` | — (Tilt builds + CD demo pushes) |
| Registry (private, authenticated) | `localhost:5010` | `admin` / `admin123` (`docker login localhost:5010`) |
| Vault (default; absent with `task up:lean`) | <http://localhost:8200> | token `admin123` |

This table also prints in your terminal when `task up` starts (before Tilt
takes over the screen), and lives on the **`endpoints`** resource in the Tilt
UI — it goes green once every user-facing service is ready, with all the
links clickable. One source of truth: `hack/dev/endpoints.sh`.

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

## Ingress + `*.localhost` routing (default)

The NGINX ingress (v1.10.1) is part of plain `task up` — without it no app
has a browsable URL, which made every first look at suparship a port-forward
hunt. App and preview URLs (`http://<app>.<env>.localhost`,
`http://pr-<n>.<app>.preview.localhost`) resolve straight to the cluster via
the host port-80 mapping; the platform services keep their port-forwards
(the endpoints table is the source of truth).

The one host prerequisite is `*.localhost → 127.0.0.1`: one-time
`task dev:dns` on any platform — it's a no-op where the resolver already
handles it (systemd-resolved distros), configures Homebrew dnsmasq on macOS,
and dnsmasq via apt/dnf elsewhere ([docs/local-dns.md](../local-dns.md) for
the details). The endpoints table warns when it doesn't resolve.

Opt out with `task up:lean` (drops ingress + Vault) or
`tilt up -- --no-ingress`; `task up:ingress` remains as a deprecated alias of
`task up`.

---

## The CD golden path, locally

The dev loop wires the ctlptl kind registry in as the org registry
(`hack/dev/values-dev.yaml` sets `registry.url: kind-registry:5000` with
`insecure: true` — the registry is plain HTTP, and without that flag Kargo's
Warehouse attempts TLS and never resolves a single tag). That makes the whole
image-driven flow real:

```bash
task demo:color-app:release VERSION=0.2.0 COLOR=green
# → pushes localhost:5001/demo/color-app:0.2.0
# → the color-app Warehouse (kargo-demo namespace) discovers the tag as Freight
# → promote staging → prod from the UI (or watch auto-promote, if enabled)
```

The host pushes to `localhost:5001`; everything in-cluster (nodes pulling,
Kargo polling) reaches the same registry as `kind-registry:5000` via ctlptl's
docker-network alias — including the `task up:multi` workload clusters. Check
Freight discovery with:

```bash
kubectl --context kind-suparship-dev -n kargo-demo get warehouses,freight
```

`insecure` is read into the publisher at server boot; if you change it later,
restart the suparship pod and re-sync apps so Warehouses are re-rendered.

### The private (authenticated) registry

Two registries run in the dev loop, with a deliberate division of labor:

- **kind registry** (`localhost:5001` host / `kind-registry:5000` in-cluster,
  no auth) — the workhorse: Tilt pushes dev images through it, kind nodes
  pull from it, and the CD golden path above uses it as the org registry.
- **private registry** (`localhost:5010` host /
  `private-registry.registry.svc.cluster.local:5000` in-cluster,
  `admin` / `admin123`) — a stand-in for a real authenticated registry
  (ghcr/ECR/Harbor), for exercising credential flows: `docker login`,
  registry settings with credentials in suparship, Kargo warehouse auth.

```bash
docker login localhost:5010 -u admin -p admin123
docker tag alpine:3 localhost:5010/demo/alpine:3 && docker push localhost:5010/demo/alpine:3
curl -su admin:admin123 http://localhost:5010/v2/_catalog
```

Plain HTTP is fine from the host — Docker treats `localhost` registries as
insecure-allowed. One caveat by design: kind nodes' containerd is **not**
wired to trust this HTTP registry, so cluster workloads can't pull images
from it — it exists to test the credential paths, not to serve workload
images (that's the kind registry's job).

---

## The BYO-chart flow, locally (example charts)

[`examples/charts/`](../../examples/charts/) holds plain, production-ready
charts (web / worker / cronjob / gateway) meant to be registered as a chart
source — the no-templates path described in
[docs/byo-charts.md](../byo-charts.md). To try the whole flow against the dev
cluster, publish them to the in-cluster Gitea and register that repo:

```bash
# 1. Push the charts to dev Gitea as a public repo (charts under charts/,
#    the gitcharts default scan path). Only web + gateway: template names
#    are global and the dev demo templates repo already claims `worker` and
#    `cronjob`, so the sync collision guard would refuse those two with a
#    per-source error (see docs/byo-charts.md). Re-run the git lines to
#    publish local chart edits.
tmp=$(mktemp -d) && mkdir -p "$tmp/charts" && cp -R examples/charts/web examples/charts/gateway "$tmp/charts/"
git -C "$tmp" init -q -b main && git -C "$tmp" add -A && git -C "$tmp" commit -qm "example charts"
curl -s -u gitops:gitops-dev-only -H 'Content-Type: application/json' \
  -d '{"name":"example-charts","private":false,"default_branch":"main"}' \
  http://localhost:3000/api/v1/user/repos >/dev/null
git -C "$tmp" push -qf http://gitops:gitops-dev-only@localhost:3000/gitops/example-charts.git main
```

2. In the UI: **Templates → Sources → Add source**, type **Git charts repo**:
   - Name: `example-charts`
   - Repo URL: `http://gitea-http.gitea.svc.cluster.local:3000/gitops/example-charts.git`
     (the *in-cluster* URL — the suparship pod does the cloning, so
     `localhost:3000` won't resolve)
   - Ref / Path: leave empty (`main` / `charts` defaults)

3. Hit **Sync now** on the source — `web` and `gateway` appear as templates
   (passthrough: no injected values schema).

4. Create an app from `web`. The chart's defaults deploy a standalone
   `nginx-unprivileged` demo, so it goes green with no inputs. From there,
   follow [docs/byo-charts.md](../byo-charts.md): wire `envFrom` /
   routing with `((platform.*))` tokens in the app's values overlay, author
   developer values on the template page, and bind the CD image to
   `image.tag`.

---

## The shipnotes demo (CI-driven golden path)

The color-app loop above pushes images by hand. The full production shape —
CI from PRs and main driving previews, staging, and promotion, on a real
composed app — is **one command** on top of `task up`:

```bash
task demo:shipnotes   # second terminal — or press ▶ on the demo-shipnotes
                      # resource in the Tilt UI
```

**The user-facing guided tour lives in
[docs/try-suparship.md](../try-suparship.md)** — this section is the
contributor's view of the machinery.

The command is idempotent and does everything: mirrors
[suparship-demo](https://github.com/suparcloud/suparship-demo) into the dev
Gitea + wires its Gitea Actions CI, registers the example-charts template
source (for the `postgres` template), waits for the first `main-<7sha>`
images, creates the composed app (`frontend` external + `api` internal +
`db` stateful) with CD managed and per-component image bindings, sets the
`DATABASE_URL` app secret (Vault-backed), and prints the tour.

Tag scheme (deliberate): immutable, prefix + 7-char sha. There are **no
separate release tags locally** — Kargo freight promotes the *same* immutable
tag through staging → prod; the `main-`/`pr-` prefixes carry provenance so
tag filters can separate promotable builds from preview builds. `v*` git tags
still produce semver image tags for parity with the GitHub workflow.

How the plumbing works (and what to check when it doesn't):

- The **runner** (`act-runner` resource) is an `act_runner` container on the
  HOST docker daemon, registered against Gitea automatically. Job containers
  get the host daemon's socket, so workflow `docker push localhost:5001/...`
  takes the same path as `task demo:color-app:release`. If runs sit queued,
  re-trigger `act-runner`; after `task cluster:delete` + recreate, first
  `docker volume rm suparship-act-runner-data` (the old registration points
  at a dead instance).
- Job containers reach Gitea and the suparship API at
  `host.docker.internal` (native on Docker Desktop / OrbStack; the runner
  adds a `host-gateway` mapping for Linux).
- `task demo:shipnotes` is idempotent — re-run it to push a fresh mirror or
  rotate the CI token.

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

> **Known gap (and why `k8s` is deprecated).** The `k8s` secret backend is
> hub-only: `_infra/secret-stores/` syncs to the tooling cluster, so a remote
> workload cluster never receives its `suparship-store-*` and app
> ExternalSecrets there will dangle. The 1Password and Vault backends publish
> per-cluster stores through the seal pipeline and reach remote clusters
> correctly — `task up:multi` (Vault included by default) is the end-to-end
> test of exactly that. Surfacing the contrast is part of the point of running
> multi-cluster locally.

Tear-down removes all three: `task cluster:delete`.

---

## The HashiCorp Vault secrets backend (default)

`task up` includes a Vault and wires it in as the org's secret backend — the
same backend a production install is expected to run. (The `k8s` backend is
deprecated, demo-only, and its stores never reach the `up:multi` workload
clusters, so defaulting to Vault keeps the dev loop on the real secrets path.)
Opt out with **`task up:lean`** (`tilt up -- --no-vault`) for the leanest
loop; `task up:vault` remains as a deprecated alias of `task up`.

Alongside the Vault itself, a `vault-bootstrap` resource initialises and
unseals it, enables the `suparship` KV v2 mount, creates the write-token
Secret (`suparship-vault-token` in `suparship-system`) and ESO's read-token
Secret (`vault-token` in `external-secrets`), then switches the org's secret
backend to vault through `PUT /org/secret-backend`.

**The data persists.** Vault runs standalone with file storage on a PVC, so
secrets you enter survive a pod restart, a `tilt down`/`tilt up`, and image
rebuilds. (It used to run in dev mode, where storage was in-memory — a pod
restart took not just your values but the KV mount itself, which made the
backend tedious to work on.) State still dies with the cluster, since the PVC is
hostPath-backed on the kind node.

**After a Vault pod restart it comes back SEALED.** That is the cost of
persistence: dev-mode Vault auto-unsealed, a real storage backend does not.
Re-trigger `vault-bootstrap` in the Tilt UI (or re-run the script) — it unseals
from the stashed key and no-ops everything already in place. Nothing unseals it
automatically, because that would mean handing the unseal key to an in-cluster
controller, which isn't worth building for a dev loop.

The root token is generated at init, not fixed. Both it and the 1-of-1 unseal
key are stashed in `vault/vault-dev-keys`; the bootstrap output prints the token:

```bash
kubectl --context kind-suparship-dev -n vault get secret vault-dev-keys \
  -o jsonpath='{.data.root-token}' | base64 -d
```

DEV ONLY, and not just the 1-of-1 key: that one root token is reused as both
suparship's write token and ESO's read token, so nothing here is
least-privilege. A real install mints per-scope read policies — see
[secrets.md](../secrets.md#least-privilege-vault).

Start over with a clean Vault:

```bash
kubectl --context kind-suparship-dev -n vault delete pvc data-vault-0 secret vault-dev-keys
kubectl --context kind-suparship-dev -n vault delete pod vault-0
# then re-trigger vault-bootstrap
```

The org switch goes through the API on purpose: the handler *merges* onto the
stored config, so it is safe to run after `seed`/`seed-multi` rewrite the org
ConfigMap wholesale. If you re-run `task seed` by hand afterwards, re-trigger
`vault-bootstrap` in the Tilt UI to restore the backend selection.

Composes with multi-cluster out of the box (`task up:multi` gets Vault too —
which it effectively requires, since the deprecated k8s backend's stores never
reach the workload clusters). Poke at it directly:

```bash
TOKEN=$(kubectl --context kind-suparship-dev -n vault get secret vault-dev-keys \
  -o jsonpath='{.data.root-token}' | base64 -d)
kubectl --context kind-suparship-dev -n vault exec vault-0 -- \
  env VAULT_TOKEN="$TOKEN" vault kv list suparship/   # suparship's containers in the mount
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
- **Can't log in to suparShip** — `admin@local` / `admin123`, in both fake and
  cluster mode. In fake mode it comes from `.env`; against a real cluster the
  server authenticates against the Secret
  `suparship-system/suparship-admin-auth`, created by the
  `suparship-admin-secret` resource with the same defaults (override with
  `$SUPARSHIP_DEV_USER` / `$SUPARSHIP_DEV_PASSWORD`).
  See [config/dev/README.md](../../config/dev/README.md).
- **Linux: apps unreachable on port 80** — the ingress publishes host port
  80, which needs a **rootful** docker daemon; rootless docker can't bind it.
  `*.localhost` resolution comes free on systemd-resolved distros; elsewhere
  run `task dev:dns` (dnsmasq). CI job containers already get
  `host.docker.internal` via an explicit `host-gateway` mapping.
- **Logged in but everything says "insufficient permissions"** —
  authentication and authorization are separate: the login Secret only proves
  who you are; project access comes from the ORG CONFIG's teams and role
  bindings (seeded with `members: [admin, admin@local]`). If you changed
  `$SUPARSHIP_DEV_USER`, add that username to the `admins` team on the
  Teams settings page (or re-seed).
- **Can't log in to Kargo** — <http://localhost:8083>, password `admin123` (the
  admin login has no username field). Note it is **http**, not https: the
  Tiltfile sets `api.tls.enabled=false` so there's no self-signed cert prompt —
  `https://localhost:8083` fails with `ERR_SSL_PROTOCOL_ERROR`. If you
  re-enable TLS, be aware the container port is named `h2c` but serves https.
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
