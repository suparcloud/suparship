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
