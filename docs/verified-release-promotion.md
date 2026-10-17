# Promote a verified release

An environment pin holds that environment's deployment. It does not invalidate
verification evidence for releases that already passed its checks.

In the app's Promote dialog, select a verified release and submit it to the
next environment. The selected Kargo Freight is promoted without unpinning or
republishing staging. Production still takes time to reconcile and roll out;
this avoids the extra staging restore and rollout before production submission.

## API

List matching releases, newest discovery first. The response includes release IDs
and details (image tags and discovery timestamps):

```http
GET /api/v1/projects/{project}/apps/{app}/promotion-candidates?targetEnvironment=prod
```

Submit a selected release:

```http
POST /api/v1/projects/{project}/apps/{app}/promote
Content-Type: application/json

{"targetEnvironment":"prod","freight":"<verified-freight-name>"}
```

The real Kargo adapter also checks verification when `freight` is omitted; it
resolves the current upstream Freight in that case. Explicit selection is needed
when promoting a historical release while the upstream environment is pinned.

Submission checks the app's pipeline source, target hold/decommission status,
Freight Warehouse origin, upstream `status.verifiedIn` evidence, and the target
Stage's requested origin and upstream verification policy. `approvedFor` alone
is not verification. Kargo performs its own execution-time checks, including
configured soak time. Submission creates a pending Promotion; it does not mean
the rollout succeeded. No explicit Freight approval is performed. A missing target is prepared in Git
with automatic sync disabled before the Promotion is submitted.

## Prerequisites and boundaries

- The destination Stage must exist. A missing Application is prepared without
  automatic sync. The pending state survives settings republishes and restarts.
  Kargo writes the selected image tags, clears the pending state, and restores
  the normal sync policy in one Git commit. Preparation checks the installed
  ApplicationSet generator before publishing, so an incomplete migration fails
  closed. A slow ApplicationSet reconciliation can return a retryable conflict;
  retrying resumes the prepared target without deploying a seed image.
- Kargo must have recorded upstream verification. Existing installations relying
  on Suparship's historical approval workaround may have no eligible releases.
  Configure/fix upstream verification and reverify; do not fabricate evidence.
- Configure acceptance checks in CD settings with existing AnalysisTemplate
  names per environment. These templates must exist in the project's Kargo
  namespace. Suparship renders Stage verification references; it does not create
  the test templates. With no templates configured, rollout health is the gate.
- Suparship schedules downstream automatic promotions; generated Kargo policy
  only schedules the first Stage. Generator contract v0.4.0 triggers
  the existing startup republish mechanism. Before rollout, verify that Argo CD
  has applied the generated ProjectConfig; Git publication alone does not prove
  that the old downstream scheduler is disabled.
- Automatic selection uses verified history even when the source is pinned or
  unhealthy. It only advances beyond the destination's current Freight discovery
  timestamp. Discovery order is not Git ancestry: restrict Warehouse discovery to
  immutable main-branch build tags, and do not backfill older builds into that
  stream. Manual release selection and rollback remain explicit operations.
- Active verified requests are reused. Competing submissions contend on a
  Stage-wide attempt name; failed attempts can be retried. This does not serialize
  external Kargo clients or legacy rollback submissions. A completed request
  reuses its successful Promotion while that Freight is still the destination's
  current release; it does not suppress an explicit redeployment after rollback.
- New releases that have never passed required verification remain blocked.
  They need access to the configured validation environment.
- No extra environments, reservation service, or routing changes are introduced.
- Local fake/in-store mode retains its existing behavior and does not expose
  verified release candidates. The existing rollback API remains separate.

## Merge and automatic promotion

1. Merge to the configured release branch. CI must build and publish an immutable
   image only after its required checks pass; Suparship does not infer CI success
   from an image appearing in a registry.
2. Kargo discovers the release through the configured Warehouse filters and
   deploys the first Stage unless that environment is pinned.
3. Kargo records upstream verification after rollout health and the configured
   AnalysisTemplates succeed. Failed or missing evidence cannot be promoted by
   this path; explicit approval alone is insufficient.
4. With auto-promotion enabled, Suparship submits the newest eligible release to
   the next Stage. Otherwise a user selects it in Promote. Kargo executes the
   promotion; a queued request is not proof of a successful rollout.

A pinned staging environment does not validate new builds. Already verified
releases can continue; new builds wait for validation capacity.

## Verification and operational limits

Promotion success means the Git change and sync request were applied. Deployment
health and Stage verification are separate checks; Kargo registers ongoing Argo CD health and sync checks before Stage
verification. Composed apps include stateful component Applications in those
checks. Ongoing Stage health follows the shared GitOps branch: it must not be
pinned to an earlier commit, since unrelated apps also commit to that branch. The dashboard refreshes deployment status
without resetting configuration edits or the selected environment.

Local end-to-end evidence covers the first-production preparation and activation,
completed-request reuse, acceptance execution, and manual/automatic promotion
with pinned staging. See [test evidence](verified-release-e2e.md). Multi-cluster
and external-chart path generation have regression coverage, but were not tested
against multiple live clusters. Acceptance-policy changes do not retroactively
revoke old Kargo verification; reverify or remove obsolete evidence as part of
changing a release policy. Soak-time enforcement remains Kargo's responsibility.
