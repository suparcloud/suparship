# Separate release validation

Separate release validation is an optional, per-app setting. It keeps new releases
moving while the base staging environment is pinned to an integration preview.
Persistent validation is the default: workloads keep running between releases.
Ephemeral validation is an explicit opt-in that creates workloads for each run and
removes them after successful checks. Existing apps do not change on upgrade.

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
selected environment. Existing staging pins are preserved. Persistent validation workloads
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
    "releaseValidationMode": "persistent",
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

## Ephemeral lifecycle (optional)

Choose **Ephemeral — create for each release** under the validation lifecycle
setting, or set `cd.releaseValidationMode` to `"ephemeral"`. An omitted mode
preserves the existing value; empty or `"persistent"` means persistent behavior.

Persistent remains the default because stable endpoints, stateful integrations,
and predictable startup latency are common requirements. Ephemeral is useful for
stateless validation when saving idle compute outweighs provisioning time.

The lifecycle is:

`idle → prepare inactive Application → activate selected image → verify → cleanup → idle`

- Validation runs independently of the production auto-promotion toggle. Passing
  releases remain eligible for manual or automatic promotion after cleanup.
- Only one run uses an app's validation environment at a time. Queued discoveries
  are coalesced to the newest available Warehouse Freight after cleanup. Failed-run
  retention also occupies this slot for one hour and delays later validation runs. Discovery
  time is not Git commit ordering.
- Successful runs are cleaned up after verification, without waiting for production
  approval. Failed runs stay for one hour from observed failure for debugging.
  The controller never tears down an actively executing promotion or verification;
  configure bounded timeouts for custom checks. Preparation that cannot reach
  activation within 30 minutes becomes a failed run and receives the same debug
  retention. Cleanup failures retry.
- Cleanup removes the workload Applications through GitOps and Argo CD cascading
  deletion. It retains namespace, platform configuration/secret resources, values
  in Git, Kargo Stage, and release/verification history. This is ephemeral workload
  capacity, not a fresh namespace per build or a full environment reset.
- The validator must have a distinct namespace from the app’s other environments.
- All chart-managed resources in this environment must be disposable. Keep
  databases, queues with durable state, and other persistent data outside it.
  Apps with declared stateful components are rejected; use persistent mode for
  those apps. Arbitrary external charts can contain additional stateful resources,
  so they must be configured appropriately before opting in.
- The environment uses its own values and secrets, as in persistent mode. Staging
  overrides are not inherited. External-service registrations and state are not
  automatically created or cleaned up.
- A durable Stage lease and run record allow restart recovery and serialize workers.
  A Git dormancy marker prevents ordinary republishes from recreating idle workloads.
  Native Kargo auto-scheduling is disabled and acknowledged before this controller
  starts operating; Kargo still executes promotions and verification.
- Pins and rollbacks into the ephemeral validator are rejected. Suspension pauses
  new runs; it does not cancel an executing run or its cleanup.
- Wait for **Idle** before changing lifecycle or selecting a different validator.
  Switching to persistent mode restores its workloads on publication. Disabling
  separate validation also requires `releaseValidationMode: "persistent"` and
  returns to the configured ordered route.

ApplicationSet and Argo CD reconciliation still take time. Deletion must finish
before the next run provisions, so ephemeral mode trades release latency for lower
idle compute usage. It does not eliminate control-plane or external-service costs.

### Monitoring idle capacity

Suparship reports the intentionally absent validator as **Idle** and does not show
it as a deployment in progress. Raw Kargo Stage health can report an unhealthy or
missing Application after cleanup because it retains the last promotion's health
check references. Alerts that read Kargo directly should account for the durable
`suparship.io/validation-run` lifecycle annotation; idle capacity is not a failed
release. Verification evidence remains in Freight status and is not fabricated or
cleared to conceal the missing workload.
