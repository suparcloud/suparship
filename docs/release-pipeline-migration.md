# Release pipeline and integration reservation migration

> Historical proposal: the implementation direction is now [verified release promotion](verified-release-promotion.md). Separate integration reservations are not being implemented. This document is retained as design history, not the current rollout plan.

**Status:** Superseded by the [verified-release upgrade runbook](verified-release-upgrade.md). No separate integration reservation migration was implemented.
**Architecture:** [ADR-0004](adr/0004-release-pipeline-and-integration-reservations.md).

## Target behavior

Merged main releases deploy to staging, pass acceptance verification, and become
eligible for production approval. PR releases can occupy a separate integration
environment for days without participating in this pipeline.

## Phase 1: Inventory and additive configuration

Inventory project environment order, bindings, apps/stacks, active preview pins,
rollback holds, Kargo version/CRDs, Warehouse image filters, and existing auto-
promotion settings. Identify external callback URLs, credentials, test tenants,
and data stores that staging users depend on. Preserve current Git revisions
and release selections for recovery; exclude secret values from the inventory.

Add explicit pipeline/integration participation and source configuration to
domain models, persistence, import/export, API DTOs, and UI. Use a shared policy
resolver in the publisher and every promotion path. Existing projects stay in
legacy mode until explicitly migrated; never reinterpret environment order or
change production on upgrade. New projects can default to the golden path only
after its gates are implemented.

Generate a migration diff that maps the existing ordered chain to explicit
sources. Do not infer integration participation from its name. Preserve custom
multi-stage pipelines rather than rewriting all projects to two environments.
Reject unresolved or invalid sources. Test round-trip persistence and recovery
from partial migration before changing release behavior.

## Phase 2: Provision integration and reservations

Create a bound integration target with its own namespace, stable hostname,
secret references, resource quotas, and external-service isolation. Its manifests
are reconciled by Argo CD, but it has no downstream release subscription and
does not consume main releases automatically. Provision only the apps and
dependencies required for the integration workflow.

Implement reservation ownership, release snapshots, renewal, conflict handling,
and audit records. Validate policy for app and stack APIs as well as UI controls.
Show reservation status separately from release pipeline health. Preserve
direct-delivery support for dependencies without adding them to app promotion.

Migrate active staging reservations individually:

1. Capture the exact held release and non-secret configuration references.
2. Deploy it to integration and verify health with external side effects disabled.
3. Configure separate provider credentials/callbacks, or arrange an explicit
   endpoint cutover with the reservation owner. Never run duplicate consumers
   against a shared tenant without confirming the provider's behavior.
4. Validate the external flow on integration, then retire the old reservation.
5. Restore staging to a known main release and rerun acceptance verification.

If a provider endpoint cannot move, wait for that reservation to finish before
migrating it. Do not silently rename DNS, rotate credentials, or unpin staging.
During this transitional period, the affected legacy pipeline may still block.
Retain production rollback holds through their existing operational workflow.

## Phase 3: Release identity and verification

Capture image digests, merged source commit, build provenance, component images,
chart revisions, and relevant configuration revisions as a release. Resolve
historical tags once where possible; ambiguous or unavailable artifacts require
a fresh build and staging validation, not an invented digest or success record.
Constrain release Warehouse discovery to the permitted main-build artifacts;
PR builds must not become candidates merely because they share a registry.

Configure explicit staging verification with Kargo AnalysisTemplates. Cover
readiness, smoke tests, and required external integration/contract tests as
appropriate for each app. Test compatibility with the installed Kargo version.
Do not backfill verification success solely from current Argo CD health.

Store evidence against the release and staging configuration. Define revocation,
freshness, and configuration-change rules. Decide how required tests obtain
isolated provider tenants; if isolation is unavailable, document their required
exclusive-access schedule and resulting release delay.

## Phase 4: Enforce protected promotion

Replace normal freight approval overrides with verified release eligibility.
Keep emergency overrides and rollback on separate permission-checked paths.
Default production to manual approval for new golden-path projects; preserve
existing auto-promotion choices until an explicit policy migration changes them.
Apply identical release gates to automatic and manual delivery.

Check policy and release identity before writing an active destination release
to Git. Remove protected production's publish-first and success-on-Kargo-error
fallbacks. First-promotion materialization must not itself deploy the candidate
before verification. Local fake/in-store behavior must not become an escape
path for protected production in a real installation.

Make retry handling idempotent and reject stale requests: a newer promotion,
hold, policy change, or revocation must not be overwritten by a delayed request.
Bind approval to the selected release, target, and relevant configuration; do
not switch to a newer staging release between approval and execution. Recheck
eligibility at execution. Ensure a failed request leaves Git desired state and
stored release selection unchanged.

Run the acceptance suite in a disposable project, including controller-level
Argo CD/Kargo tests, before enabling protected mode in the pilot project.
During rollout, make legacy versus enforced mode visible. Do not advertise
verification guarantees for projects still using the approval workaround.

## Phase 5: Pilot, rollout, and recovery

Pilot one app/stack whose provider resources can be isolated. Reserve PR A for
72 hours while deploying merged release B through staging into production.
Record both release digests and verify that the integration reservation is
unchanged. Repeat the failure scenarios below, then migrate projects in batches.

If rollout fails, pause further promotions, retain deployed release selections,
and repair or revert the versioned configuration changes. Do not automatically
restore the old verification-bypass behavior. Preserve new schema fields when
rolling back application code; if the old version would ignore participation
and re-chain integration into production, keep its publisher/reconciler disabled
or use a backward-compatible version until the topology is safely restored.

## Acceptance criteria

| Scenario | Required outcome |
|----------|------------------|
| PR A reserved for 72 hours; main release B passes staging | B can reach production; A and its integration configuration stay unchanged |
| New main releases arrive during reservation | Integration does not advance automatically |
| PR update, close, or preview deletion | Reserved release remains available and unchanged |
| Two developers reserve the same target | Conflict reported; no silent overwrite |
| Reservation expires | Owner notified, workload unchanged until explicit release/update |
| Staging verification fails or remains pending | Production Git revision/release selection unchanged |
| Kargo unavailable or freight missing | Promotion fails/retries visibly; no deployable fallback commit |
| Production has never been deployed | Scaffolding cannot deploy an unverified image |
| User pins preview to staging/prod through app or stack API | Rejected under golden-path policy |
| Integration is reordered, rebound, or decommissioned | Production's source remains staging |
| Staging upstream is disabled or missing | Production blocks; no direct Warehouse fallback |
| Release C reaches staging after B was verified | Eligible B remains selectable; approval cannot silently switch to C |
| Evidence revoked or relevant configuration changed | Promotion blocked pending applicable revalidation |
| Concurrent requests or retries | No duplicate/older request overwrites a newer release or hold |
| Production rollback hold exists | Auto-promotion stays paused; integration migration does not clear it |
| Stack operation partially fails | Per-app outcomes shown; no claim of cross-app atomic success |
| Integration shares the only required provider tenant | Required production tests wait; no fabricated success or gate bypass |

Use focused unit tests for policy/eligibility, API tests for all entry points,
GitOps manifest tests for subscriptions and digest preservation, and an end-to-
end controller test for actual reconciliation and verification. Include negative
assertions that rejected promotions do not alter production Git state.
