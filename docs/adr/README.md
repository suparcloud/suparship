# Architecture Decision Records

This directory contains Architecture Decision Records (ADRs) for suparShip.

ADRs document significant decisions that shaped the system's design, API surface, and UX model. They are meant to be concise, implementation-oriented, and written at the time the decision is made.

## Index

| ADR | Title | Status |
|-----|-------|--------|
| [ADR-0001](0001-app-as-primary-deployment-object.md) | App as Primary User-Facing Deployment Object | Accepted |

## Related docs

- [docs/templates.md](../templates.md) — Template authoring guide: app-first model, component topology, environment overrides, secrets.

## Format

Each ADR follows the structure:

- **Status** — `Proposed` / `Accepted` / `Superseded by ADR-XXXX`
- **Context** — What situation or tension motivated the decision
- **Decision** — What was decided
- **Consequences** — Impact on subsystems and developer/user experience
- **Migration strategy** — How existing code and behaviour transitions

## Contributing

When a decision meaningfully changes the product model, API shape, UX hierarchy, or GitOps layout, add a new ADR rather than editing an existing one. Reference the superseded ADR and update the table above.
