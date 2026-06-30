# Contributing to suparship

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

> **Preflight checks** — `task dev` and `task up` verify required
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
| Real K8s resource reads/writes          | cluster mode | `task up`            |
| Preview creation / deletion             | cluster mode | `task up`            |
| Service promotion (staging → prod)      | cluster mode | `task up`            |
| ArgoCD sync / health integration        | cluster mode | `task up`            |
| Real pod log streaming                  | cluster mode | `task up`            |

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

suparship supports three development modes depending on what you are working on:

### 1. Fast local dev

Run the backend API and frontend UI on your host machine with no cluster
required. Best for UI work, API handler logic, and template parsing.

```bash
task dev
```

Backend runs on `:8080`, frontend on `:5173` with HMR. Kubernetes-dependent
features will degrade gracefully (returning placeholder data or connection
errors).

### 2. Cluster dev (Tilt)

When you need a real Kubernetes runtime — previews, promotions, ArgoCD/Kargo,
secrets, GitOps — bring up the whole stack with one command.

#### Required tools

| Tool | Version | Install |
|------|---------|---------|
| `docker` | any | [docs.docker.com](https://docs.docker.com/get-docker/) |
| `ctlptl` | 0.8+ | `brew install tilt-dev/tap/ctlptl` · [github.com/tilt-dev/ctlptl](https://github.com/tilt-dev/ctlptl) |
| `tilt` | 0.33+ | `brew install tilt-dev/tap/tilt` · [tilt.dev](https://docs.tilt.dev/install.html) |
| `kind` | 0.20+ | `brew install kind` · [kind.sigs.k8s.io](https://kind.sigs.k8s.io/docs/user/quick-start/#installation) |
| `kubectl` | 1.28+ | `brew install kubectl` · [kubernetes.io](https://kubernetes.io/docs/tasks/tools/) |
| `helm` | 3.12+ | `brew install helm` · [helm.sh](https://helm.sh/docs/intro/install/) |

`hack/preflight.sh tilt` checks all of these for you.

#### Bootstrap the cluster

```bash
task up
```

This uses **ctlptl + Tilt** to create a kind cluster + local registry, install
every prerequisite (cert-manager, Argo Rollouts, Kargo, ArgoCD, Gitea, External
Secrets, Replicator, Reloader), and deploy suparShip **in-cluster with
hot-reload**. Edit `cmd/` or `internal/` and Tilt rebuilds in seconds; edit
`ui/src` and Vite HMR updates the browser. Tilt UI is at
<http://localhost:10350>; the app at <http://localhost:5173>
(login `admin` / `devpass`).

The full walkthrough — prerequisites, port map, the inner loop, optional
ingress, troubleshooting — lives in
**[docs/contributor-guide/hacking-on-suparship.md](docs/contributor-guide/hacking-on-suparship.md)**.

Tear down:

```bash
task down            # stop Tilt, keep the cluster
task cluster:delete  # delete the kind cluster + registry
```

> **Legacy script path.** The older `task dev:cluster` (provision via ~15 shell
> scripts) + `task dev:cluster:serve` (run the server on the host) flow still
> works and is handy for targeted re-installs — individual components remain
> available as `task dev:cluster:<component>` (e.g. `task dev:cluster:argocd`,
> `task dev:cluster:seed`). But `task up` is the recommended path.

### 3. CI mode

CI runs the test suite and chart checks without a local cluster:

```bash
make test          # unit + smoke tests
task charts:verify # schemas in sync, vendored libs in sync, helm lint clean
```

---

## When things go wrong

| Symptom | Fix |
|---------|-----|
| Binary is stale or build artifacts are cluttering the workspace | `task reset` |
| Seeded demo data is corrupted or you want a clean slate | `task dev:cluster:reset` then `task dev:cluster:seed` |
| Cluster is in an unrecoverable state | `task down && task cluster:delete && task up` |
| Login fails in cluster mode (no admin Secret) | Tilt: re-trigger the `suparship-admin-secret` resource (`hack/dev/admin-secret.sh`). Legacy: `go build -o bin/suparship ./cmd/suparship && ./bin/suparship admin bootstrap` |
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
task dev:cluster:reset     # delete the three seed ConfigMaps
task dev:cluster:seed      # restore them (Tilt's `seed` resource also re-applies them)
```

To wipe everything and start from scratch:

```bash
task down            # stop Tilt
task cluster:delete  # delete the kind cluster + registry entirely
task up              # recreate cluster, prereqs, suparship, seed
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
throw away (`task cluster:delete`) when something goes wrong.

With `task up`, suparShip runs **inside** the cluster (as the
`charts/suparship` Deployment) and Tilt keeps it in sync with your working
tree — on a Go change it recompiles in the container and restarts in a few
seconds, so you get a fast loop while exercising the real in-cluster
ServiceAccount, RBAC, and ConfigMaps. (The legacy `task dev:cluster:serve`
path instead runs the server as a host process against the cluster.)

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
