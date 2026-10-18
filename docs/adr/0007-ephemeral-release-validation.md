# ADR-0007: Optional ephemeral release validation workloads

## Status

Accepted. Extends [ADR-0006](0006-optional-persistent-release-validation.md).

## Context

Independent validation removes the staging reservation bottleneck, but persistent
validation capacity costs resources between releases. Some apps need stable state
or endpoints; others can recreate disposable workloads on demand.

## Decision

Keep persistent mode as the default. Add an explicit per-app ephemeral lifecycle.
Use the same configured validation environment, values, and promotion source.
Prepare inactive Applications before Kargo activates the selected Freight. After
successful verification remove workload Application inputs from Git; Argo CD
cascades their deletion. Retain failed runs for one hour from observed failure.
Never clean up while a promotion or verification is executing.

Keep namespace, platform configuration, Git values, Stage, and verification history.
Persistent data belongs outside the disposable workload Applications. Reject apps
with declared stateful components. This is not a fresh namespace per release.

Suparship owns validation scheduling in this mode after acknowledged Kargo handoff.
Use a Kubernetes CAS lease and durable Stage progress for restart/multi-worker
safety, plus a Git dormancy marker to stop republishes resurrecting idle workloads.
Queue one active run per app, coalescing newer discoveries to the latest available
Freight when capacity becomes idle. Cleanup must complete before reuse.

## Consequences

Idle compute is released, but provisioning and cleanup add latency. External state,
resource retention policies inside arbitrary charts, and custom check timeouts
remain operator responsibilities. Production can promote historical verification
without needing the validator to remain running.

## Migration strategy

No automatic migration. Omitted lifecycle fields preserve the existing mode;
empty or persistent retains existing behavior. Enabling on an existing deployment
preserves it until handoff and verification are complete. Changes away from
an ephemeral lifecycle require the run to be idle and use the same lifecycle lease.

See [lifecycle and configuration](../release-validation.md#ephemeral-lifecycle-optional).
