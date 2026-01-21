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
