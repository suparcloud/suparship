# Contributing to suparShip

Thanks for your interest in contributing! We welcome PRs of all sizes.

## Getting Started

1. **Fork & clone** the repository
2. **Install Go 1.23+** and **Node.js 20+**
3. Copy the example env file and start the stack:

```bash
cp .env.example .env   # sets SUPARSHIP_DEV_MODE=local (no cluster needed)
task dev               # builds backend + starts Vite frontend
```

4. Open **http://localhost:5173** and log in:

| Field    | Default       | Override via               |
|----------|---------------|----------------------------|
| Username | `admin@local` | `SUPARSHIP_ADMIN_EMAIL`    |
| Password | `admin123`    | `SUPARSHIP_ADMIN_PASSWORD` |

You should immediately see seeded demo data — no empty state:

| What you'll see | Value |
|-----------------|-------|
| Org             | `default` — My Organization |
| Project         | `demo` — Demo Project |
| Environments    | `staging`, `prod` |
| Service         | `hello` (web-service template) |
| Preview         | `pr-42` |
| Runtime status  | `healthy` with fake ingress URLs |
| Logs            | Sample log lines |

Press **Ctrl+C** to stop both servers. All writes are in-memory and reset on
the next `task dev` — no cleanup needed.

> **Preflight checks** — `task dev` and `task dev:cluster` verify required
> tools before doing anything else. If a tool is missing you'll see a clear
> error with an install link rather than a cryptic build failure.

---

## Which mode for which work?

Pick your development mode based on what you're working on:

| I'm working on…                         | Use          | Command              |
|-----------------------------------------|--------------|----------------------|
| UI components, frontend layout          | fake mode    | `task dev`           |
| Backend API handlers, RBAC, auth        | fake mode    | `task dev`           |
| Template parsing, service model logic   | fake mode    | `task dev`           |
| Real K8s resource reads/writes          | cluster mode | `task dev:cluster`   |
| Preview creation / deletion             | cluster mode | `task dev:cluster`   |
| Service promotion (staging → prod)      | cluster mode | `task dev:cluster`   |
| ArgoCD sync / health integration        | cluster mode | `task dev:cluster`   |
| Real pod log streaming                  | cluster mode | `task dev:cluster`   |

**Start with fake mode.** Most contributions — including all UI and API work —
never need a cluster. Reach for cluster mode only when the feature you're
building genuinely reads or writes Kubernetes resources.

---

## Development Workflow

```bash
# Build the binary
make build

# Run all tests
make test

# Run API smoke tests only (login + seeded data — no cluster required)
make test-smoke

# Format code (uses gofumpt if available, gofmt otherwise)
make fmt

# Run linters (requires golangci-lint)
make lint
```

Smoke tests live in `test/smoke/` and exercise the fully assembled server
backed by the same fake deps that `task dev` uses. They are included in
`make test` and can be run independently with `make test-smoke`.

## Development Modes

suparShip supports three development modes depending on what you are working on:

### 1. Fast local dev

Run the backend API and frontend UI on your host machine with no cluster
required. Best for UI work, API handler logic, and template parsing.

```bash
task dev
```

Backend runs on `:8080`, frontend on `:5173` with HMR. Kubernetes-dependent
features will degrade gracefully (returning placeholder data or connection
errors).

### 2. Cluster dev

Spin up a local [kind](https://kind.sigs.k8s.io/) cluster and configure it as
the suparShip dev target. Use this when you need to test any feature that
reads or writes real Kubernetes resources (namespaces, deployments, ingresses).

#### Required tools

| Tool | Version | Install |
|------|---------|---------|
| `kind` | 0.20+ | `brew install kind` · [kind.sigs.k8s.io](https://kind.sigs.k8s.io/docs/user/quick-start/#installation) |
| `kubectl` | 1.28+ | `brew install kubectl` · [kubernetes.io](https://kubernetes.io/docs/tasks/tools/) |
| `helm` | 3.12+ | `brew install helm` · [helm.sh](https://helm.sh/docs/intro/install/) |
| `docker` | any | [docs.docker.com](https://docs.docker.com/get-docker/) |

#### Bootstrap the cluster

```bash
task dev:cluster:bootstrap   # create cluster + foundational namespaces
```

This command is **idempotent** — safe to run multiple times. On first run it:

1. Verifies `kind`, `kubectl`, and `helm` are installed
2. Creates a kind cluster named `suparship-dev` using `config/kind/cluster.yaml`
3. Sets `kubectl` context to `kind-suparship-dev`
4. Creates foundational namespaces: `suparship-system`, `argocd`

Cluster config lives at `config/kind/cluster.yaml`. The node is labelled
`ingress-ready=true` and ports 80 → 8880 and 443 → 8443 are mapped for a
future ingress controller.

#### Verify

```bash
kubectl get nodes          # should show suparship-dev-control-plane
kubectl get namespaces     # should include suparship-system and argocd
kubectl config current-context   # should be kind-suparship-dev
```

#### Tear down

```bash
task dev:cluster:delete    # delete the kind cluster
```

#### ArgoCD (optional — for runtime integration work)

> **You do not need ArgoCD for most contributions.**
> `task dev` (fake/in-memory mode) is sufficient for UI work, API handlers,
> template parsing, and anything that does not require a real Kubernetes runtime.
>
> Install ArgoCD only when you need to test features that read or drive real
> ArgoCD Application resources: sync status, health checks, GitOps reconciliation.

```bash
task dev:cluster:argocd   # install ArgoCD dev profile into the argocd namespace
```

This command (idempotent — safe to re-run):

1. Adds the official argo Helm repository
2. Installs `argo-cd` chart `7.7.0` (ArgoCD v2.13.x) with the dev values from
   `config/argocd/values-dev.yaml` — single replica, no SSO, no TLS, no notifications
3. Waits for `argocd-server` rollout to complete
4. Prints the admin password and access instructions

**Access the UI** after install:

```bash
kubectl port-forward svc/argocd-server -n argocd 8180:80
# open http://localhost:8180
# username: admin
# password: printed by the install script, or retrieve with:
kubectl get secret argocd-initial-admin-secret \
  -n argocd -o jsonpath='{.data.password}' | base64 -d
```

**Verify ArgoCD is healthy:**

```bash
kubectl get pods -n argocd           # all pods should be Running
kubectl get deployments -n argocd    # server, repo-server, applicationset-controller
```

#### One-command cluster dev

Once the cluster exists, `task dev:cluster` is the preferred daily workflow
for integration work. It is idempotent — safe to re-run at any time:

```bash
task dev:cluster
```

Internally this:
1. Runs `hack/bootstrap-cluster.sh` (creates the kind cluster if absent)
2. Checks whether ArgoCD is installed; installs it if needed
3. Seeds demo data — org, project, preview — via `hack/seed.sh` (idempotent)
4. Builds the Go binary
5. Starts the backend **in Kubernetes mode** (talks to the kind cluster)
6. Starts the Vite frontend dev server

Use `task dev:cluster` when you need a real Kubernetes runtime for:

| Use case | Why cluster mode is needed |
|----------|---------------------------|
| Preview environment creation / deletion | Reads / writes namespaces and ConfigMaps in the cluster |
| Service promotion (staging → prod) | Drives Kargo / ArgoCD promotions |
| Runtime status against real workloads | Reads live Deployment replica counts and Ingress URLs |
| Real pod log streaming | Proxies logs via the Kubernetes API |
| ArgoCD sync and health integration | Queries ArgoCD Application resources |

`task dev` (fake mode) remains the fastest path for UI work, API handler
logic, and template parsing — no cluster required.

#### First-time setup (one-off)

```bash
task dev:cluster:bootstrap   # create kind cluster + namespaces (~30 s)
task dev:cluster:argocd      # install ArgoCD dev profile (~3–5 min)
task seed                    # seed demo org, project, preview into the cluster
# Bootstrap admin credentials (printed once — save the password):
go build -o bin/suparship ./cmd/suparship
./bin/suparship admin bootstrap
# Subsequent runs: just task dev:cluster
```

#### What gets seeded

`task seed` (also run automatically by `task dev:cluster`) applies three
idempotent ConfigMaps to `suparship-system`:

| Resource | ConfigMap | Contents |
|----------|-----------|---------|
| Org | `suparship-org-config` | `default` org, `admins` team, org_admin binding |
| Project | `suparship-project-demo` | `demo` project, `hello` service, `staging` + `prod` envs |
| Preview | `suparship-preview-pr-42` | preview `pr-42` for `demo/hello` |

The seeded data matches `internal/fake/seed.go` so the UI looks the same
in both fake mode and cluster mode.

Admin credentials are **not** seeded — they require a bcrypt hash and are
created by `suparship admin bootstrap` (run once per cluster).

### 3. E2E / CI mode

Full pipeline: create a disposable cluster, install components, run the test
suite, and tear everything down. Designed for CI and pre-merge validation.

```bash
task dev:cluster:bootstrap   # create cluster
task dev:cluster:argocd      # install ArgoCD
task seed                    # load demo data (coming soon)
task test                    # run all tests
task dev:cluster:delete      # clean up
```

> All three modes share the same `Taskfile.yml` tasks. The difference is
> which tasks you run and in what order.

---

## When things go wrong

| Symptom | Fix |
|---------|-----|
| Binary is stale or build artifacts are cluttering the workspace | `task reset` |
| Seeded demo data is corrupted or you want a clean slate | `task dev:cluster:reset` then `task seed` |
| Cluster is in an unrecoverable state | `task dev:cluster:delete` then `task dev:cluster:bootstrap` |
| Login fails in cluster mode (no admin Secret) | `go build -o bin/suparship ./cmd/suparship && ./bin/suparship admin bootstrap` |
| Frontend refuses to start | `rm -rf ui/node_modules && cd ui && npm install` |

### `task reset` — clean local artifacts

Removes `bin/` and `tmp/`. Safe and reversible — does not touch the
cluster, the database, or any Kubernetes resources.

```bash
task reset
```

Fake mode state is in-memory and resets automatically the next time
`task dev` starts — no explicit cleanup needed.

### `task dev:cluster:reset` — remove seeded demo data

Removes only the three ConfigMaps that `task seed` creates:

```
suparship-system/suparship-org-config
suparship-system/suparship-project-demo
suparship-system/suparship-preview-pr-42
```

It does **not** delete the cluster, ArgoCD, namespaces, or admin credentials.
Idempotent — safe to run if some resources are already gone.

```bash
task dev:cluster:reset   # delete the three seed ConfigMaps
task seed                # restore them, or use task dev:cluster which seeds automatically
```

To wipe everything and start from scratch:

```bash
task dev:cluster:delete      # delete the kind cluster entirely
task dev:cluster:bootstrap   # recreate it
task dev:cluster             # install ArgoCD + seed + start servers
```

---

## Why these tools?

### Fake / in-memory runtime

Most features — UI, API handlers, RBAC, template logic — don't actually
need a Kubernetes cluster. Running against a real cluster adds several
minutes of bootstrap time, requires Docker, and makes tests flaky when
the cluster is slow or unavailable.

`internal/fake` provides an in-process, deterministic implementation of
every store and runtime interface. It starts in milliseconds, resets on
restart, and produces the same data every time — which makes it excellent
for unit tests and rapid UI iteration. The seeded data intentionally
matches what `task seed` puts into a real cluster, so the UI looks
identical in both modes.

### kind for real integration

When you do need a real Kubernetes runtime — for preview lifecycle, actual
pod log streaming, or ArgoCD integration — [kind](https://kind.sigs.k8s.io/)
gives you a full Kubernetes API server on your laptop in under a minute.
It's reproducible, free, doesn't require a cloud account, and is easy to
throw away (`task dev:cluster:delete`) when something goes wrong.

The backend always runs as a local process (not inside the cluster), so
you get fast Go rebuild cycles even in cluster mode. Only the Kubernetes
API calls go to the kind cluster.

---

## Submitting a Pull Request

1. Create a feature branch from `main`
2. Write clear commit messages
3. Add tests for new functionality
4. Ensure `make test` and `make lint` pass
5. Open a PR with a description of changes

### Commit Messages

We follow conventional commits:

```
feat: add new demo subcommand
fix: handle missing kubeconfig gracefully
docs: update README with install instructions
```

### Sign-off (Optional)

We appreciate (but don't require) a Developer Certificate of Origin sign-off:

```bash
git commit -s -m "feat: add cool feature"
```

## Code Style

- Follow Go conventions and `gofumpt` formatting
- Wrap errors with context: `fmt.Errorf("doing X: %w", err)`
- Add comments for exported functions and types

## Questions?

See [SUPPORT.md](SUPPORT.md) for where to ask questions.
