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

#### After bootstrap

ArgoCD and application installs are handled in subsequent steps (coming soon):

```bash
task dev:cluster:bootstrap   # create cluster + namespaces
task seed                    # populate demo data (coming soon)
task dev                     # start backend + frontend
```

### 3. E2E / CI mode

Full pipeline: create a disposable cluster, seed data, run the test suite,
and tear everything down. Designed for CI and pre-merge validation.

```bash
task dev:cluster:bootstrap
task seed
task test
task dev:cluster:delete
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
