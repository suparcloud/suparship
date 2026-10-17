# Separate release validation

Separate release validation is an optional, per-app setting. It keeps new releases
moving while the base staging environment is pinned to an integration preview.
It provisions a persistent validation workload; resources keep running between
releases. Ephemeral environments and automatic cleanup are not implemented.

## Flow

With the setting disabled, the configured environment order defines promotion:

`main build → staging → verification → downstream promotion`

With `release-validation` selected:

```text
main build ──→ staging (independent; preserves its preview pin)
           └→ release-validation → verification → production
```

Both roots receive Warehouse builds. The first downstream environment uses the
selected validator's evidence; later downstream gates remain in order. Routing
does not change when staging is pinned or unpinned. Production auto-promotion
requires auto-promotion to be enabled, successful upstream verification, and the
acknowledged Kargo scheduler handoff. Manual promotion uses the same source.
A failed check blocks that release. Pinning or suspending validation prevents new
builds from being validated; previously verified releases remain eligible under
the normal promotion rules.

## Setup

1. Create a non-production environment in Settings → Environments and bind it to
   a registered cluster. Keep staging first in the configured order and retain a
   downstream release environment.
2. Ensure Application names are unique across environments. With the default
   cluster-based naming, use distinct cluster registrations. Sharing a registration
   requires an organization naming pattern containing `{env}`; changing naming for
   existing deployments is a separate migration and must not be done casually.
3. Configure isolated databases, queues, secrets, and external-service test accounts
   for validation. A separate workload does not isolate shared external state.
4. In the app's CD settings, enable **Use a separate release validation environment**,
   select the environment, and configure its acceptance checks before saving.
   Checks are not copied from staging. If staging has acceptance checks, validation
   must have checks configured too. Referenced AnalysisTemplates must exist in the
   project's Kargo namespace. Empty checks mean rollout health verification only.
5. Enable auto-promotion if production should advance without manual approval.

Selecting an organization environment not yet attached to the app adds only that
selected environment. Existing staging pins are preserved. Validation workloads
and their dependencies consume resources continuously, including while idle.

The UI shows each environment's promotion source. Changing the source while a
stage is promoting or verifying is rejected; retry after that work finishes.
An unbound environment or colliding Application name is rejected before saving.
The selected validator cannot be undeployed until the setting is changed or disabled.

## API

Patch the app's `cd` configuration, retaining the desired managed/auto-promotion
and verification values:

```json
{
  "cd": {
    "managed": true,
    "autoPromote": true,
    "releaseValidationEnvironment": "release-validation",
    "verification": {
      "release-validation": ["release-acceptance"]
    }
  }
}
```

The app response includes a read-only resolved map:

```json
{
  "promotionSources": {
    "staging": "",
    "release-validation": "",
    "prod": "release-validation"
  }
}
```

An empty source means direct Warehouse intake. Omitting
`releaseValidationEnvironment` in an update preserves its existing value, allowing
older clients to update other settings safely. Sending an explicit empty string
disables it. Direct-delivery apps cannot enable this setting.

## Disabling and upgrading

Disabling restores promotion according to the configured environment order. It
retains the validation environment, workloads, and data, so inspect the resulting
order before saving. For example, staging → prod → release-validation becomes that
ordered chain again. Disabling is not a resource cleanup operation.

Existing apps are unchanged on upgrade: the setting defaults to empty. Enabling
requires an explicit save and GitOps reconciliation. Existing verified-release
history remains visible; eligibility is evaluated against the configured source.
This is a supported optional topology, not a CNCF-mandated environment model.
