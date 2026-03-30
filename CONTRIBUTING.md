# Contributing to suparShip

Thanks for your interest in contributing! We welcome PRs of all sizes.

## Getting Started

1. **Fork & clone** the repository
2. **Install Go 1.23+** and **Node.js 20+**
3. Run `make build` to verify everything compiles
4. Run `task dev` for instant local development (no cluster required)

## Development Workflow

```bash
# Build the binary
make build

# Run tests
make test

# Format code (uses gofumpt if available, gofmt otherwise)
make fmt

# Run linters (requires golangci-lint)
make lint
```

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
