# Contributing to suparShip

Thanks for your interest in contributing! We welcome PRs of all sizes.

## Getting Started

1. **Fork & clone** the repository
2. **Install Go 1.23+**
3. Run `make build` to verify everything compiles

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

Spin up a local k3d cluster and install suparShip components into it. Use
this when you need to test real ArgoCD sync, Kargo promotions, preview
environments, or any feature that reads/writes Kubernetes resources.

```bash
task dev:cluster   # provision k3d + install components
task seed          # populate demo org, project, service
task dev           # start backend + frontend against the cluster
```

### 3. E2E / CI mode

Full pipeline: create a disposable cluster, seed data, run the test suite,
and tear everything down. Designed for CI and pre-merge validation.

```bash
task dev:cluster
task seed
task test
task reset
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
