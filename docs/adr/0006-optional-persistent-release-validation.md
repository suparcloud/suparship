# ADR-0006: Optional persistent release validation

## Status

Accepted. Extends [ADR-0005](0005-verified-release-promotion.md).

## Context

Historical verified-release promotion lets previously tested releases advance
while staging is pinned. It cannot validate a new build without runnable capacity.
Teams needing long integration reservations need independent validation without
changing the default workflow for every installation.

## Decision

Add an optional per-app `cd.releaseValidationEnvironment` setting selecting a
cluster-bound, enabled, non-production environment. The base environment and
validator independently receive Warehouse builds. The first downstream release
environment requires verification in the validator; later gates remain ordered.
Use one shared route resolver for publication, automatic/manual promotion, and UI.
Do not route conditionally on pin state or silently skip acceptance checks.

Validation capacity is persistent in this implementation. Use normal GitOps
publication and Kargo/Argo CD reconciliation, preserving scheduler handoff guards.
Reject ambiguous Application names and changes during observed active promotions
or verification. The API reports resolved sources for UI presentation.

## Consequences

New builds can advance while staging stays pinned. Users pay for an additional
running workload and must isolate dependencies and external-service credentials.
Acceptance templates must be configured explicitly. Resource provisioning and
Warehouse discovery still incur their normal delays. This adds no ephemeral
lifecycle, automatic resource cleanup, or arbitrary user-editable promotion graph.

## Migration strategy

The setting defaults to empty; existing installations retain their ordered route.
Selecting a new organization environment attaches only that environment to the app.
An omitted API field preserves existing configuration; an explicit empty string
restores the ordered route without deleting workloads or data. Naming changes and
resource cleanup remain explicit administrative operations.

See the [setup and lifecycle guide](../release-validation.md).
