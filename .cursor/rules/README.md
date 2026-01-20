# Cursor Rules for suparship

This directory contains Cursor IDE rules that enforce coding standards, architectural decisions, and best practices for the suparship repository.

## MDC File Format

Each `.mdc` file uses YAML frontmatter to control when Cursor applies the rule:

```yaml
---
description: Brief explanation of when the AI should use this rule
globs: ["**/*.go"]     # File patterns this rule applies to
alwaysApply: false     # If true, always included in context
---

# Rule Content (Markdown)
...
```

| Field | Purpose |
|-------|---------|
| `description` | Helps Cursor understand when to use this rule |
| `globs` | File patterns that trigger this rule (e.g., `**/*.go`) |
| `alwaysApply` | If `true`, rule is always injected regardless of file type |

## Rule Files

| File | Globs | Purpose |
|------|-------|---------|
| [`core.mdc`](./core.mdc) | `**/*` (always) | Project identity, MVP scope, architectural constraints, determinism rules |
| [`go.mdc`](./go.mdc) | `**/*.go` | Go coding standards, package layout, error handling, logging |
| [`cli.mdc`](./cli.mdc) | `**/cmd/**/*.go`, `**/internal/cli/**/*.go` | CLI UX conventions, command naming, flags, output standards |
| [`k8s-gitops.mdc`](./k8s-gitops.mdc) | `**/*.yaml`, `**/*.yml`, `**/templates/**` | Kubernetes and GitOps conventions, ArgoCD/Kargo integration |
| [`security.mdc`](./security.mdc) | `**/*` (always) | Security policies, secrets handling, supply chain security |
| [`pr-checklist.mdc`](./pr-checklist.mdc) | `**/.github/**` | Pull request template and review checklist |
| [`commit-style.mdc`](./commit-style.mdc) | (reference only) | Conventional Commits format and scope guidelines |
| [`testing.mdc`](./testing.mdc) | `**/*_test.go`, `**/test/**` | Testing requirements, strategies, and commands |
| [`docs.mdc`](./docs.mdc) | `**/*.md`, `**/docs/**` | Documentation standards, README requirements |

## How Rules Work

Cursor uses these `.mdc` (Markdown with Context) files to understand project conventions and provide contextually aware assistance. When you work in this repository, Cursor will:

1. **Enforce conventions** — Suggest code that follows our Go style, CLI patterns, and GitOps conventions
2. **Catch violations** — Flag commits that don't follow conventional commits, PRs missing checklist items
3. **Provide context** — Understand that this is a GitOps platform, not a generic Go project

## Quick Reference

### Key Principles

- **CLI-first**: All operations via `suparship` binary
- **Git as source of truth**: All state in Git, synced by ArgoCD
- **Deterministic output**: Same input = same manifests (no timestamps, random IDs)
- **No secrets in Git**: Only `ref:secret.key` references

### MVP Scope

✅ In scope: `demo`, `install --profile`, `preview`, `promote`, `status`, `open`

❌ Out of scope: UI, multi-cloud, CI engine, custom controllers

### Profiles

| Profile | For |
|---------|-----|
| `demo` | Local dev, requires nothing but Docker |
| `core` | Minimal ArgoCD + Kargo |
| `full` | Core + monitoring |

## Contributing to Rules

When modifying rules:

1. Keep rules **specific and actionable** (not generic advice)
2. Include **examples** of good and bad patterns
3. Use **MUST/SHOULD/MAY** language consistently
4. Keep each file **80-200 lines** — dense but readable

## Related Documentation

- Main project README: [`../README.md`](../../README.md)
- Contributing guide: [`../../CONTRIBUTING.md`](../../CONTRIBUTING.md) (if exists)
- License: [`../../LICENSE`](../../LICENSE)
