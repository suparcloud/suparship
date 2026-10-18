# Verified-release promotion: local end-to-end evidence

Tested on 2026-10-17 using `kind-suparship-dev`, Tilt, local Gitea, local
registry, Argo CD and Kargo v1.9.5. No remote cluster was modified.

## Passed

| Scenario | Observed result |
| --- | --- |
| PR → merge → CI | Local Gitea PR #3 merged; CI published `main-7924225` for frontend and API. |
| Unverified release | Production request returned HTTP 400 before upstream verification; no Promotion was created by that request. |
| Acceptance verification | Kargo created a successful AnalysisRun for `shipnotes-acceptance`; it read the database-backed `/api/notes` endpoint. |
| Manual promotion while staging pinned | UI promotion `verified-8582aa4202e7fd1b-1` succeeded. Production served `main-7924225`; staging kept serving `pr-2-5574c7d`. |
| Concurrent identical requests | Three simultaneous submissions returned the same operation, `verified-8582aa4202e7fd1b-3`. |
| Automatic promotion while staging pinned | After deliberately selecting the prior verified release, enabling auto-promotion caused Suparship to submit `verified-8582aa4202e7fd1b-4`. Production returned to the latest verified release; the staging pin remained. |
| Frontend validation | Production frontend build passed after picker-label and tracking-namespace corrections. |

The release used for the successful main promotion was Freight
`dc81d4326cb42c3625db83b1993e7a608b9ead54`. Its staging verification completed at
18:09:56 UTC. Automatic production submission was logged at 18:14:23 UTC.
HTTP checks and updated, ready Deployment replicas independently confirmed the
rollout; the conclusion does not rely only on a Kargo success badge.

## Limits and findings

- The five-minute Warehouse discovery interval still applies to new images.
  Historical promotion avoided restoring staging first. These are distinct delays.
- Root/ApplicationSet refreshes were used while setting up test fixtures and
  acceptance configuration. Do not claim this run proves zero-touch migration.
- Acceptance configuration does not retroactively revalidate old Freight.
- Completed-request reuse was fixed and retested: repeating the current successful
  release returns the original Promotion without creating another operation.
- The demo seed rebuilt an old mutable tag, producing a mixed-tag Freight.
  The UI diagnosed it. The successful test used a fresh, matching main tag.
  Warehouse discovery order and mutable tags are not Git commit ordering or
  enforced artifact immutability.
- Summary and selected-environment status now refresh periodically; configuration
  inputs and the selected environment are preserved.
- A synchronous settings request timed out after 120 seconds although its change
  persisted and auto-promotion completed. The UI uses asynchronous task polling.
- Safe first-time production provisioning was tested with `bootstrap-probe`.
  The original HTTP 409 blocker is fixed. At 18:35:32 UTC the prepared production
  Application had no automatic sync and no workload pods. Promotion
  `verified-c4f0a05b5c498851-1` committed the selected image and activation at
  18:35:34 UTC. Argo CD restored automatic sync and reached Healthy/Synced;
  Kargo subsequently recorded production verification. Repeating the request
  returned that same successful Promotion. The promoted Git revision was
  `e5663f806cdac30b8b1053ac208f019db4604e70`, and the initial experimental health check required it for both Git sources.
  A later unrelated commit exposed why a permanent exact-SHA check is incorrect
  for this shared branch. The final template uses the installed Kargo v1.9.5 `argocd-update` runner;
  its registered Stage health checks gate verification. Ongoing Stage checks
  do not retain that fixed SHA. The newer standalone `argocd-wait` runner is
  unavailable in this version and is not used. Promotion-applied and
  deployment-verified are deliberately distinct states in the UI.
- During development an invalid generator override was rejected by the installed
  Argo CD CRD. Required template fields were corrected and server dry-run passed.
  The old failed root operation was terminated so Argo CD could apply the corrected
  Git revision. This validates the fix, not an unattended upgrade rehearsal.

Original screenshots and structured evidence are in the local
`tmp/release-demo/` recording kit. That ignored directory is not a shipped test
suite. The kit includes a storyboard and publication caveats. No continuous
video was recorded.

## Final follow-up result

The final supported configuration was checked again at 19:13 UTC on October 17
(October 18 local time). All four Kargo Stages were Healthy. Shipnotes production
served `main-7924225` while staging continued serving `pr-2-5574c7d`.
Promotion `verified-8582aa4202e7fd1b-9` succeeded with both the app and database
Application included in health checks. The first-deployment probe was Healthy
and Synced, and its Stage recovered after rerunning its release with the final
health policy (`verified-c4f0a05b5c498851-2`).

The composed-app failure exposed missing Kargo authorization on stateful
Applications; generated manifests now include it while retaining `prune: false`.
Retries no longer reuse an older success after a newer partial failure.
That checkpoint used a fixed-width attempt suffix. The subsequent ordering fix
below replaces it with Kargo-compatible ULID names.

Validation: `go test ./internal/gitops ./internal/server ./internal/kube
./cmd/suparship`, the UI production build, and `git diff --check` passed.
The live run used one local cluster; multi-cluster/external-chart generation is
covered by unit tests, not a multi-cluster deployment rehearsal.

## Release-blocker fixes (October 18 local time)

The ordering implementation now uses Kargo's `stage.ULID.freight` format for
verified releases and rollbacks. A Stage annotation reserved with Kubernetes
resourceVersion keeps simultaneous Suparship requests on the same operation.
Regression coverage includes verified release → rollback → native-format
promotion → verified release, duplicate requests, resourceVersion conflicts,
restart recovery, failed retries, released legacy timestamp names, and
Promotion garbage collection with retained Stage status.

Automatic promotion now checks the live ProjectConfig before selecting a
release. It requests a Kargo refresh tied to that configuration's version and
waits for the Stage's acknowledgement. It fails closed on missing configuration,
read failures, ambiguous policies, or an enabled Kargo downstream scheduler.
The chart includes the required ProjectConfig read permission.

### Live handoff validation

An isolated `kargo-handoff-validation` project exercised the actual Kargo v1.9.5
controller with the new gate:

| State | Gate result |
| --- | --- |
| Legacy downstream automatic promotion enabled | Closed |
| Disabled policy applied, acknowledgement pending | Closed |
| Kargo acknowledged that configuration | Open |

No manual Stage refresh or status editing was used; the gate requested its own
refresh. The fixture had no Freight, so it tested ownership acknowledgement,
not a deployment race under load. Server regression tests separately prove that
no promotion or retry cooldown is created while the gate is closed, and that
automatic promotion resumes when it opens. The isolated project was removed.

### Live release ordering validation

A fresh `release-order-probe` app used two actual registry images and normal
upstream Kargo verification. Production operations were:

| Operation | Promotion name | Freight |
| --- | --- | --- |
| Initial production deployment | `release-order-probe-prod.01m55np7zy5nxcc9g3zha1fk4w.8d9bf27` | `8d9bf272d82d0dbd7e6b8f25cc8b367e1f6a4876` |
| Second verified release | `release-order-probe-prod.01m55nsb030yvhv76rkw6qvadt.d9808c2` | `d9808c203193115c64db156f18b53542e82f0079` |
| Rollback | `release-order-probe-prod.01m55nt3g66gwkc2nnzjh4mkew.8d9bf27` | `8d9bf272d82d0dbd7e6b8f25cc8b367e1f6a4876` |
| Re-promotion | `release-order-probe-prod.01m55ntw2m7v1057a5s1qwkap3.d9808c2` | `d9808c203193115c64db156f18b53542e82f0079` |

All four operations succeeded. Each operation was checked against the Stage's
lastPromotion, current Freight, and rollout health before continuing. The first
submission used the HTTP API, including first-target preparation; subsequent
operations exercised the shared KargoStore release and rollback primitives.
This was not a browser-level rollback/Resume CD test.

### Remaining validation limits

- Root Application hard refreshes were needed to load the new fixture's Git
  revisions. Periodic reconciliation had continued to report the older revision.
  This run does not prove a fully unattended upgrade of an existing installation.
- The fixture inherited references to an empty Secret that ESO did not create.
  Empty Secrets were supplied in its staging and production namespaces to let
  the test workload start. No verification evidence or Stage status was fabricated.
- Earlier `verified-*` development records were preserved on the original demos.
  Those experimental names may sort after native names; submission now rejects
  that incompatible history rather than silently accepting an ignored operation.
  Released `stage-UnixNano` history is covered by the upgrade regression test.
- Multi-cluster and external-chart paths still require a live rehearsal.

The Go package checks and `git diff --check` passed. Structured local evidence is
in `tmp/release-demo/evidence/scheduler-handoff-live.json` and
`tmp/release-demo/evidence/release-ordering-live.json`. These checks validate the
two code fixes but are not blanket public release approval.

## Optional persistent validation while staging is pinned

Tested on 2026-10-17 UTC in `kind-suparship-dev` with `release-order-probe`:

- Staging was pinned to `pr-validation-hold`, image `pr-2-5574c7d`.
- Selected `release-validation`, bound to the local `validation-cluster` registration,
  with managed delivery and automatic promotion enabled.
- Published fixture tag `validation-check-1792267491`. This is a fresh registry tag
  referencing an existing image digest, not a new CI build or main-merge rehearsal.
- Production promotion before verification returned HTTP 400: the release had not
  been verified in `release-order-probe-release-validation`.
- Freight `cfcfbdb25b6caa59823aa1a9e69931c16594dd8c` was then verified in validation,
  with no staging verification entry.
- At 20:07:48 UTC, Suparship automatically submitted
  `release-order-probe-prod.01m55qmj0dqdq8khvezjn3a9qj.cfcfbdb` from validation to prod.
  The Promotion succeeded. Both validation and production Deployments reached
  two updated, ready replicas using the new tag. Staging retained two ready
  replicas running its preview tag.

Local evidence is saved at `tmp/release-demo/evidence/separate-validation.json`.
The test used rollout-health verification, not a new custom acceptance AnalysisRun.
Existing acceptance-template execution is covered by the earlier Shipnotes run;
route selection and checked-source guards have unit coverage.

The first setup attempt exposed an Application naming collision when staging and
validation shared a cluster registration with the default naming pattern. The API
now rejects that configuration before saving, with a regression test. The live
fixture uses a distinct logical cluster registration targeting the same local kind
cluster; it does not demonstrate separate physical clusters. Its initial host-only
API address and kubeconfig were corrected to the in-cluster address; the local
server was restarted to clear its cached cluster client. Root/ApplicationSet refreshes
and empty fixture Secrets were needed during setup. No verification status was
fabricated. This run does not prove unattended upgrades, external-chart delivery,
or ephemeral resource lifecycle behavior.

Backend tests passed for domain, GitOps, server, Kubernetes adapters, and command
wiring. The UI production build and whitespace checks passed. The UI shows the
configured promotion sources; browser automation could inspect the overview but
could not activate the settings tab reliably, so settings save was exercised through
the API rather than claimed as a completed browser interaction test.

## Ephemeral validation lifecycle

Tested on 2026-10-17 UTC in `kind-suparship-dev`, using the existing
`release-order-probe` fixture and its independent validation namespace:

| Event | Evidence |
| --- | --- |
| Opt-in and safe handoff | Enabled ephemeral mode; existing validation was preserved until Kargo's disabled auto-scheduler policy was acknowledged. |
| Initial cleanup | Controller reached Idle at 20:28:46 UTC; validation had zero pods. Staging and production remained deployed. |
| Fresh discovery | Published fixture tag `ephemeral-check-1792268926`, Freight `50b5519877e2ac2bf190251c0b119afb9bcc5728`. This reuses an existing image digest, not a new CI build. |
| Provisioning | Observed provisioning → waiting → running, with Kargo activation of the selected Freight. |
| Verification | Kargo verified the Freight in release validation at 20:30:28 UTC. No staging verification entry existed. |
| Restart recovery | Development server restarted at 20:30:28 UTC during the run; lifecycle resumed from the durable Stage record. |
| Cleanup | Observed cleaning → deleting → Idle by 20:31:15 UTC; the validation namespace had zero pods. |
| Promotion after cleanup | Automatic production promotion `release-order-probe-prod.01m55s01zx8hrrvkjd3rhygtpz.50b5519` submitted at 20:31:33 UTC and succeeded. Production reached two updated, ready replicas of the new image tag. |
| Staging reservation | Staging retained `pr-2-5574c7d`, pinned to `pr-validation-hold`. |
| UI | Overview displayed Healthy production, the staging pin, and Idle validation, without a false deploying banner. |

Local evidence: `tmp/release-demo/evidence/ephemeral-validation.json` and
`tmp/release-demo/screenshots/14-ephemeral-validation-idle.jpg`.

Backend tests passed for domain, publisher, Kubernetes adapters, server, and command
wiring; the UI production build passed. Focused tests cover competing lifecycle
leases, lost-owner protection, persisted progress after an error/restart, cleanup
retry, failed-run retention, preparation timeout, shared-namespace rejection, and
preventing idle workload resurrection on republish.

The one-hour failure-retention boundary was tested with a controlled clock in unit
tests, not by waiting an hour in the cluster. This live run used rollout health,
not a new custom acceptance template. Separate physical clusters, external-chart
cleanup, lease-holder death during a Git push, and multi-replica live contention
remain unrehearsed. Settings were exercised through the API; browser automation
could inspect the overview but could not reliably activate the settings tab.
Root refresh was used for configuration discovery; workload activation and cleanup
then reconciled through the generated ApplicationSet and Argo CD.

Raw Kargo Stage health reports the deleted validator Application as unhealthy after
cleanup; Suparship exposes the durable Idle lifecycle separately. Direct Kargo
monitoring must account for that lifecycle. Failed runs occupy the single validation
slot during their one-hour retention, so newer builds wait until cleanup.

## Managed validation rehearsal — 2026-10-18 (UTC)

The local Tilt stack (`kind-suparship-dev`) validated the app-owned setup without
adding an organization environment or cluster registration. Fixture app
`demo-managed-check` inherited staging's existing cluster and used namespace
`demo-managed-check-release-validation`, without a repeated `demo-` prefix.

A local Envoy Gateway v1.3.0 installation supplied Gateway API CRDs and a test
Gateway. The app's web chart inherited an enabled HTTPRoute whose hostname used
`((platform.routingHost))`. The generated validation route was accepted with
resolved references at `demo-managed-check-release-validation.localhost` and
returned HTTP 200 through a local Gateway service port-forward.

For candidate `managed-check-1792347540`, Freight
`e32cd74756d469d11da714144a98cdff6beda174`, the rehearsal confirmed:

- API setup automatically created the app-owned validation instance, with no
  environment selector or duplicate cluster registration.
- Base Helm annotations and validation overrides both reached the Deployment.
- The namespace-local ConfigMap inherited base variables and applied validation
  overrides. Its Secret inherited base backend items and applied the independent
  validation override item. No secret values were written to the evidence summary.
- Kargo recorded verification in the validation Stage, without staging verification
  for that candidate. This used rollout health verification, not a new custom
  acceptance AnalysisRun; the harness separately checked the HTTP endpoint.
- Cleanup returned to zero validation pods and HTTPRoutes, retaining the namespace,
  ConfigMap, ExternalSecret and verification history.
- Production automatically promoted the verified image and reached two ready pods.
  Staging remained pinned to `validation-hold` / `main-7924225`.
- Disabling validation removed the managed instance from the app and restored the
  ordered pipeline. Re-enabling reused the dedicated namespace and base cluster.

The harness waited for the reservation's disabled scheduling policy and pinned
Deployment to become live before publishing the candidate. A fixture-wide nonempty
baseline secret was provisioned through the secrets API so production's ExternalSecret
could materialize; persistent production dependencies remain independently configured.

Saved local evidence: `tmp/release-demo/evidence/managed-validation.json` and
`tmp/release-demo/screenshots/15-managed-validation-settings.png` (ignored artifacts).
Backend package tests, UI production build and golangci-lint passed. Additional
regressions cover missing-Stage recovery after interrupted setup, override precedence,
namespace ownership, safe disable/rollback, and retained-platform cleanup on app deletion.
This rehearsal does not establish live multi-cluster or arbitrary external-chart
compatibility; charts must support namespace isolation and platform-derived route hosts.

## Validation-only disposable Redis (2026-10-19)

Rehearsed against the local Tilt stack in `kind-suparship-dev` using `demo-cache`.
The stateful Redis component kept a PVC in staging and production. Ephemeral
validation alone used an explicit disposable policy and an `emptyDir` override.

- Default retention rejected ephemeral setup without explicit consent.
- Marking the component disposable while inheriting its PVC mount failed rendered
  chart preflight before validation workloads were activated.
- Two validation runs each started with empty Redis data, without a PVC.
- Each run removed validation pods, HTTPRoutes and stateful child Applications.
- Reserved staging retained its original PVC UID and Redis sentinel value.
- The second verified image automatically reached a healthy production deployment.

The harness was resumed after accommodating Kargo's temporary auto-sync changes;
unit tests separately assert pruning stays disabled in stable and persistent
validation environments. Evidence: `tmp/release-demo/evidence/disposable-redis.json`
(ignored local artifact). Go package tests, golangci-lint and the UI build passed.
