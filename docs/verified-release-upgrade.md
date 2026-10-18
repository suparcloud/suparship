# Upgrade to verified-release promotion

This is the rollout guide for the unreleased verified-release workflow and
generator v0.4.0. It replaces the earlier
[separate integration environment proposal](release-pipeline-migration.md).
The [validation report](verified-release-e2e.md) records what has actually been
tested; an unattended installation upgrade and live multi-cluster/external-chart
rehearsal remain outstanding.

## Before upgrading

Follow the backup procedure in [Upgrading suparship](upgrading.md). Record the
current chart/server versions, GitOps revision, environment order, active pins,
rollback holds, current Freight, and any custom ProjectConfig policies. Preserve
references and metadata without copying secret values into the rollout report.

Confirm that CI publishes immutable main-branch image tags and that the intended
release has upstream `verifiedIn` evidence. Older explicit approvals do not
qualify. Configure existing AnalysisTemplates in the project's Kargo namespace
when acceptance checks beyond rollout health are required.

## Apply and observe

1. Upgrade the server and chart together. The service account now needs `get`
   access to `projectconfigs.kargo.akuity.io`, in addition to its existing Stage
   update and patch permissions.
2. Allow generator v0.4.0's startup republish to finish. If publishing fails,
   resolve the error and retry Sync to Git. A successful Git write is not proof
   that Argo CD applied the manifests.
3. Inspect the live ApplicationSet templates. Prepared targets must have no
   automatic sync; active targets retain their normal policy. For composed apps,
   verify the generated main and stateful Applications and Kargo authorization.
4. Inspect the live ProjectConfig. Kargo should auto-promote the first Stage;
   each downstream target should have an exact disabled policy. Kargo uses the
   first matching policy. Place that exact policy before custom label or pattern
   selectors; ambiguous earlier selectors keep Suparship's scheduler paused.
5. Observe the handoff: Suparship writes a `kargo.akuity.io/refresh` token beginning
   with `suparship-handoff-`. Kargo acknowledges it in
   `status.lastHandledRefresh`. The gate also checks that Kargo's reported
   auto-promotion is disabled. Configuration version changes require another
   acknowledgement. No manual acknowledgement or status editing is needed.
6. Test a previously verified release against a non-production target. Confirm
   the Promotion, Stage current Freight, Application health, and running image
   agree. Then test a first deployment and release → rollback → release.
7. Test a staging pin: an older verified release should remain selectable; an
   unverified build should remain ineligible. Confirm the staging pin and its
   running image are unchanged by production submission.

Do not remove holds or enable auto-promotion just to complete the upgrade. Retain
the app's existing manual/automatic policy and validate each deployment topology
used by the installation.

## Diagnose a paused or rejected operation

| Symptom | Check and recovery |
| --- | --- |
| Auto-promotion waits for scheduler handoff | Check service-account ProjectConfig access, Argo CD's applied revision, policy order, and the Stage refresh acknowledgement. Do not bypass the gate. |
| A manual release has no eligible candidates | Check Warehouse origin and upstream verification. Reverify legitimately; an approval is not sufficient. |
| First target returns a retryable conflict | Inspect the prepared Application and ApplicationSet reconciliation. Retry after preparation completes; do not enable automatic sync on the pending target manually. |
| Another promotion is active | Wait for its terminal state. Identical active requests reuse the operation; a competing release must wait. |
| Promotion succeeded but rollout is not ready | Inspect Argo CD health and Kargo Stage verification. "Applied" is not a readiness guarantee. |
| History sorts after a new compatible name | Check for experimental `verified-*` names or clock skew. Preserve evidence and resolve the history/clock issue; repeated requests will not fix ordering. |
| Git contains new manifests but live resources remain old | Inspect Argo CD reconciliation and repository caching. Record any manual refresh; it means an unattended upgrade has not been demonstrated. |

## Promotion history and recovery

Released `stage-UnixNano` names sort before the new `stage.ULID.freight` format.
Existing history is retained, and Stage status remains part of ordering even
when old Promotion objects have been garbage-collected.

Experimental `verified-*` names were never a compatible native naming scheme.
There is no automatic destructive migration for them. Preserve those records;
use a fresh fixture for development validation. Do not rename Promotion objects,
edit verification evidence, or clear Stage status as a routine upgrade step.

A downgrade also changes scheduler ownership. Before rolling back the server or
chart, pause downstream auto-promotion through the installation's app/stack
settings, let active operations finish, and review the GitOps revision and live
ProjectConfig together. Restoring the previous binary alone does not restore
its generated policies. Do not leave both schedulers managing the same target.

## Optional independent validation

Upgrades do not enable separate validation automatically. Existing apps keep their
ordered promotion route. See [setup and lifecycle](release-validation.md) before
selecting a persistent validation environment; disabling retains its resources.
