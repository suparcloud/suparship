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

## Managed validation (recommended)

Enable **Use a separate release validation environment** in the app's delivery
settings. New configurations use an app-owned validation instance; no organization
environment or duplicate cluster registration is needed. Its default lifecycle is
**Ephemeral**. Existing registered-environment configurations retain their previous
routing and persistent default.

The instance inherits the base environment's deployment targets, routing profiles,
template and stack overlays, app values, configuration variables and secret scopes.
`environmentDefaults.release-validation` adds validation-specific Helm values and
variables; the app's release-validation secret scope adds secret overrides in the
base environment's backend vault. Preview overrides are not included. This is live
inheritance at publication time, not a frozen copy: the next run picks up source
configuration changes. Base pins, image tags and suspension controls are not copied;
Kargo chooses the release candidate's image.

Its namespace is `{projectApp}-release-validation`, with an existing project prefix
folded into the app name: project `voiceai`, app `voiceai-lk-sh` produces
`voiceai-lk-sh-release-validation`. Long namespace identities receive a stable hash.
Application names include validation and the destination cluster, preserving the
identities of existing staging and production Applications. Namespaced chart
resources can retain their normal short names because the namespace isolates them.

Charts that expose HTTP should enable their HTTPRoute using the inherited Gateway
parent and `((platform.routingHost))` hostname, rather than a literal staging host.
The validation hostname includes the deduplicated project/app identity and
`release-validation`; component routes also include their component identity.
HTTPRoutes and Services belong to the disposable chart Application. Cleanup removes
them with workloads, while the namespace, platform ConfigMaps, ExternalSecrets,
backend override item and verification history remain. The platform does not invent
service names or rewrite arbitrary third-party chart route schemas.

The ExternalSecret reads base scope items plus the validation override item and
materializes a separate Kubernetes Secret in the validation namespace. Source items
are never modified or deleted by validation. Shared credentials still reach the same
external databases, queues and accounts; supply isolated credentials when needed.
Stateful components retain their data policy by default. Explicitly allow temporary
data only in ephemeral release validation and supply validation-only values that
disable persistent storage. Staging, production and persistent validation retain
their existing values and storage protection. Configure checks
for the validation source explicitly; staging checks are not silently copied.

API example:

```json
{"cd":{"managed":true,"autoPromote":true,"releaseValidationManaged":true,"releaseValidationMode":"ephemeral"}}
```

## Registered environment setup (legacy / advanced)


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
  Each declared stateful component requires `releaseValidationDataPolicy: disposable`
  and validation-only values using temporary storage such as `emptyDir`. The actual
  rendered chart is checked before publication: PVCs, volume claim templates and
  external storage mounts are rejected. Use persistent validation for durable data.
  This policy never changes pruning or values in stable environments.
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

### Temporary Redis during validation

Keep the component marked `stateful: true`. In delivery settings, select
**Dispose of redis data only in ephemeral release validation** and provide the
chart's validation-only persistence overrides, for example `persistence.enabled:
false` when supported by that chart. Charts using explicit volumes can instead
mount an `emptyDir`. Stable environment values remain unchanged.

The component policy is stored as `releaseValidationDataPolicy: disposable`; it
has effect only when the selected environment uses ephemeral validation. It is
not a general disposable flag. Persistent validation and stable environments keep
stateful Application pruning disabled. Each ephemeral run receives fresh temporary
data; successful checks remove its workloads and failed runs retain them for one
hour. The policy does not clean external database contents or shared credentials.

## Operator: end failed-run retention early

After correcting a provisioning or verification failure, an operator can skip the
remaining one-hour debugging window. This removes retained validation workloads;
collect any needed logs first. It does not unpin staging, approve Freight, or bypass
verification or production promotion policy. There is currently no UI retry button.

Use the tooling cluster that hosts Kargo. The following example targets
`voiceai/voiceai-lk-sh`; replace the context, namespace, and Stage for your app.
It changes only `failed` to `cleaning`, preserving the Freight cursor and other
run fields. It refuses to act while the controller holds a lifecycle lease and
uses a resource-version test to reject concurrent changes. If either guard fails,
inspect the current state and retry later; do not remove the lease or force the patch.

```bash
python3 - <<'PY'
import json
import subprocess

context = "shared-aks-01-southcentralus"
namespace = "kargo-voiceai"
stage = "voiceai-lk-sh-release-validation"
base = ["kubectl", "--context", context, "--request-timeout=15s", "-n", namespace]
obj = json.loads(subprocess.check_output(base + ["get", "stage", stage, "-o", "json"]))
annotations = obj["metadata"]["annotations"]
run = json.loads(annotations["suparship.io/validation-run"])
if run.get("phase") != "failed":
    raise SystemExit("Run is not failed; inspect its current phase instead.")
if annotations.get("suparship.io/validation-lease"):
    raise SystemExit("Controller lease present; retry after it is released.")
run["phase"] = "cleaning"
patch = [
    {"op": "test", "path": "/metadata/resourceVersion",
     "value": obj["metadata"]["resourceVersion"]},
    {"op": "replace", "path": "/metadata/annotations/suparship.io~1validation-run",
     "value": json.dumps(run, separators=(",", ":"))},
]
subprocess.run(base + ["patch", "stage", stage, "--type=json", "-p", json.dumps(patch)], check=True)
PY
```

Inspect progress:

```bash
kubectl --context shared-aks-01-southcentralus -n kargo-voiceai \
  get stage voiceai-lk-sh-release-validation -o json \
  | jq '.metadata.annotations["suparship.io/validation-run"] | fromjson'
```

Expected phases are `cleaning → deleting → idle → provisioning → waiting → running`.
The controller selects the newest queued Freight **after the saved cursor**, not
necessarily the failed build. Without newer Freight it remains idle; this procedure
is not a same-build retry. Do not reset the cursor manually. Successful verification
allows normal downstream promotion and triggers validation cleanup. Confirm the
actual production image and successful Kargo Promotion before declaring delivery
complete. If provisioning fails again, inspect its error and effective validation
values rather than repeatedly clearing the retention window.
