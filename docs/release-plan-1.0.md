# Suparship 1.0 scope and release plan

Planning baseline: 2026-10-18. This defines intended scope and acceptance criteria,
not current implementation status. Dates and named owners require capacity planning.

## Product contract

A developer can connect a repository, deploy an application and supporting services,
preview a change, submit a reviewable production change, release through a canary,
diagnose failures, and recover without routine operator manifest editing. Platform
administrators configure infrastructure, policy, and service plans.

Core workflows operate self-hosted without a required vendor-hosted control plane.
Kubernetes, Helm, OCI, GitOps, OIDC, Gateway API, Prometheus-compatible queries, and
OpenTelemetry provide documented integration boundaries. State supported versions
and limitations; do not promise universal compatibility or effortless data portability.

### Required capabilities and initial provider choices

Provider selections below are planning defaults, not claims of implemented support.
Validate feasibility before the relevant milestone. A failed feasibility check
requires a recorded replacement decision, not silent removal of a capability.

| Area | Required for 1.0 | Initial supported path |
| --- | --- | --- |
| Delivery | Web/API apps, workers, scheduled jobs, migration jobs, composed apps | Existing images and isolated Dockerfile builds; GitHub |
| Developer experience | Guided setup, default HTTPS URL, custom domains, variables, secrets, previews | Approved templates and existing Helm-chart onboarding |
| Responsiveness | Fast list/detail reads, visible freshness, incremental UI updates | Background-maintained read model; no request-time infrastructure fan-out for ordinary browsing |
| Governance | PR changes, required approval, rendered impact, Argo CD diff, drift visibility | GitHub PRs and protected branches |
| Releases | Verified immutable artifacts, policy-driven promotion, history, rollback, manual HTTP canaries | Argo CD/Kargo; Gateway API with Envoy Gateway |
| Databases | Provision PostgreSQL; attach existing PostgreSQL/MySQL | CloudNativePG; qualified restore and retention |
| Cache | Provision cache, attach existing endpoints, scoped credentials and lifecycle | Valkey; compatible Redis/Valkey attachments |
| Messaging | One provisioned messaging service with explicit durability contract | NATS with JetStream |
| Storage | Object-store attachment and persistent volumes | Existing S3-compatible buckets; approved StorageClasses |
| Observability | Resource/usage dashboards, saved query panels, logs, traces, deployment correlation | Prometheus-compatible metrics, Loki, Tempo, OpenTelemetry guidance |
| Multi-cluster/cloud | Registration, placement, overrides, ordered rollout, partial-failure status | Existing clusters; EKS and AKS live qualification |
| Global traffic | Endpoint health, weights, failover, approved configuration | Cloudflare first; extension interfaces for AWS/Azure alternatives |
| Private networking | Local discovery, service bindings, access policies, one cross-cluster pattern | Administrator-provisioned private connectivity |
| Operations | Durable queue/workers, quotas, preview expiry, notifications, upgrade, backup/restore | PostgreSQL-backed operation store/queue as the planning default; recoverable workers and tested runbooks |

Attachments support only documented protocols/authentication modes. SQS, RabbitMQ,
and NATS require distinct contracts; buckets and volumes are separate resources.
Document required customer accounts, credentials, provider plans, and network access.

### Outside the 1.0 requirement

- Buildpacks, framework-specific edge execution, and additional Git-provider parity.
- Managed RDS, MySQL-operator, ElastiCache, RabbitMQ, CloudAMQP, and SQS provisioning.
- Operating object-storage infrastructure or provisioning every bucket provider.
- Automatic metric-driven canaries, worker canaries, cohorts, or multiple candidates.
- AWS/Azure global-traffic parity, automatic cluster provisioning, universal network
  overlays, mandatory service mesh, or automatic workload relocation.
- General active-active databases or automatic cross-cloud data failover.
- Cloud billing/cost allocation or a general-purpose telemetry backend/dashboard builder.
- Mutually untrusted tenant hosting and VoiceAI-specific templates as launch blockers.
- Stable stacks and ephemeral validation: these may remain explicitly beta, but
  enabled features must still obey security, data protection, and approval policies.

## Shared design contracts

1. **Review before apply:** protected mutations produce proposals and PRs. Required
   approvals/checks apply to the current revision; merge accepts desired state.
   Reconciliation and health are tracked separately. Apps, addons, releases, and
   traffic share this mechanism. Preview policies may permit automatic changes.
2. **Honest diff:** show source and rendered changes, affected clusters, deletion
   risks, and live drift separately. Never expose secret values in review artifacts.
   Refresh stale comparisons and approvals according to branch policy.
3. **Release identity:** link source commit, build, image digests, configuration,
   verification evidence, and targets. Define when configuration changes invalidate
   verification. A mutable image tag alone is not sufficient approval identity.
4. **Service ownership:** addons have independent identities and explicit bindings.
   App deletion detaches production services by default; destruction requires a
   separate approved action. Platform backup does not back up workload data.
5. **Isolation:** scope credentials to components; previews use isolated resources
   or approved test dependencies, never implicit production credentials. Document
   prerequisites for enforcing network policy and the single-organization trust model.
6. **Connections:** show destination, locality, authentication/encryption, and health.
   Cross-cloud access requires policy; data failover requires provider qualification.
7. **Traffic layers:** global routing selects clusters; local routing selects versions.
   Show combined exposure and DNS/proxy, session, and propagation limitations.
8. **Operations:** durable progress, bounded retries, duplicate protection, restart
   recovery, and accurate partial-failure states. Explain cancellation limitations.
9. **Outages:** existing workloads continue serving without Suparship. Qualify
   independent routing failover and document secret-refresh/credential dependencies.
10. **Emergency recovery:** optional break-glass policy defines authorized aborts,
    auditing, and reconciliation back to Git. Ordinary protected changes require approval.
11. **Read authority and freshness:** Git remains authoritative for GitOps desired
    configuration; Kubernetes/Argo CD/Kargo provide observed state. Suparship owns
    task and proposal records. Derived projections are rebuildable and record source
    revision, last observation, and synchronization health. Stale health is never
    presented as current; sensitive actions revalidate relevant authority and concurrency.
12. **Queued execution:** slow mutations are durably accepted before returning an
    operation ID. Workers use at-least-once execution, idempotent steps, leases, and
    reconciliation of uncertain external outcomes. Never promise exactly-once external
    effects. Authorization and approval are checked again before protected effects.

## Read model and asynchronous execution architecture

Measure request latency first: distinguish database/API time, sequential fan-out,
payload size, and browser rendering. Argo CD/Kubernetes latency is a working
hypothesis, not a verified root cause. Remove repeated and unnecessary calls.

Ordinary dashboard, app/detail, configuration, history, and cluster-summary reads
use a shared indexed projection. Background synchronization uses watches/informers
where supported, suitable Argo CD integrations, and bounded polling otherwise.
Handle initial synchronization, deletion, expired watches, reconnects, periodic
resync, and out-of-order updates. Cold or stale views expose their state instead
of falling back to unbounded request-time fan-out. Use authorized server-sent events
with reconnect/resync support to update affected views and preserve unsaved edits.
Metrics, logs, and traces continue querying their dedicated backends.

PostgreSQL is the planning default for durable operation records, the queue, and
persistent read projections. An in-memory read projection may be an incremental
0.2 implementation, but durable task acceptance and recovery are required for 0.2.
Record an ADR before implementation covering deployment dependency, retention,
schema migrations, multi-replica ownership, and the queue implementation. A separate
broker is not required initially; workload NATS addons are independent of this queue.
Suparship's own database must be installable and recoverable without its addon UI.

Task execution contract:
- Validate input and access synchronously; atomically persist the operation and its
  queue entry, then return an operation ID (normally HTTP 202). No acceptance on
  database failure. Use an outbox if later delivery crosses a transaction boundary.
- Separate execution state (queued/running/retrying/succeeded/failed/cancelled) from
  delivery phase (awaiting approval/applying/verifying). Report progress, timestamps,
  attempts, actionable errors, and whether desired state is actually healthy.
- Workers claim bounded batches with leases/heartbeats; recover abandoned claims.
  Use per-resource ordering and version/fencing checks so an expired worker or
  superseded task cannot overwrite newer intent. Reconcile an ambiguous external
  result before retrying; use provider idempotency keys where available.
- Persist step checkpoints; retry transient failures with bounded exponential
  backoff and jitter. Terminal failures enter a failed-task/dead-letter view with
  explicit authorized retry. Apply deadlines, concurrency limits, fair scheduling,
  and backpressure; prevent one unreachable cluster from exhausting all workers.
- Cancellation is cooperative; explain completed side effects and irreversible
  operations. Use explicit compensation/reconciliation, not assumed rollback.
- Recheck permissions and approval revision before effects, including delayed tasks.
  Store credential references rather than secret values in payloads and errors.
- Support graceful shutdown, restart, multiple workers, task retention, and audit
  history. Monitor queue depth/age, task duration, retries, lease recovery, and failures.

Migrate existing slow publish/republish, pin/unpin, promotion/rollback preparation,
validation lifecycle, and cleanup paths in 0.2 after inventorying which already
support async operation. Reuse existing controllers rather than creating competing
execution owners. Later builds, addons, traffic, and notifications use this substrate.

Provisional performance target: p95 below 300 ms for warm ordinary list/detail API
reads at a declared dataset size and concurrency. Publish the benchmark fixture and
baseline before evaluating this gate. Track browser interaction latency separately.
Set an observation-lag budget for each source before 0.2 acceptance; distinguish
source reconciliation delay from Suparship ingestion delay. API responsiveness is
not a claim that infrastructure reconciliation became faster.

## Efficient Git repository access

Make repository access a shared backend capability for GitOps publishing, PR
preparation, chart sources, and later builds. Profile clone, fetch, checkout,
rendering, and push separately before choosing optimizations.

- Reuse worker-local bare repository/object caches scoped by canonical remote,
  organization, and authorization boundary. Fetch incrementally instead of cloning
  the full repository for each task. Support explicit invalidation and cold rebuild.
- Resolve the required remote ref, fetch it as needed, and pin each operation to
  an immutable commit. A cache hit alone does not establish freshness or permission.
  Revalidate the expected branch head before publishing; handle non-fast-forward
  conflicts without overwriting other changes or silently force-pushing.
- Use isolated per-task worktrees or checkouts. Never share a mutable working tree
  between concurrent jobs. Coordinate fetch, ref updates, worktree metadata, and
  garbage collection; serialize publication to the same destination as needed.
- Use targeted refs, shallow/partial clones, or sparse checkout only where supported
  and correct for the task. Deepen/fetch missing history for merge bases, diffs, tags,
  submodules, LFS, and chart dependencies; retain a reliable full-fetch fallback.
- Deduplicate concurrent fetches within an authorized cache scope. Apply network
  timeouts, bounded retry, cancellation, disk quotas, cache eviction, and cleanup
  of abandoned workspaces without deleting active worker state.
- Keep credentials out of remote URLs, cached Git config, logs, and artifacts.
  Disable untrusted hooks/config execution and validate remote access. Untrusted
  builds must not receive writable shared caches, other jobs' worktrees, or access
  to cached private refs; export isolated source snapshots into build sandboxes.
- Cache rendered results only with keys covering source revision and all effective
  inputs/dependency versions. Preserve secret redaction and scope isolation.

The 0.2 benchmark records cold clone, warm incremental fetch plus workspace
preparation, publication, network bytes, disk use, and concurrent-worker behavior
on declared repository sizes and histories. Set numerical budgets from the baseline
before acceptance; no unsupported universal checkout-time promise. Routine warm
operations must reuse objects rather than full-clone, and missing/corrupt caches
must recover safely. UI browsing never waits for a checkout.

## 0.2 — Fast UI and recoverable delivery foundation

Scope:
- Reconcile implementation, changelog, docs, and tests into a capability inventory.
- Package verified promotion and persistent/ephemeral validation with stable/beta
  labels and migration instructions.
- Qualify first production deployment, staging pins, promotion/rollback, duplicate
  requests, retries, and restart recovery.
- Establish release identity and durable-operation contracts; measure setup and recovery.
- Profile UI/API paths and implement the shared read model, freshness indicators,
  bounded background synchronization, and incremental UI notifications.
- Introduce the durable queue and worker lifecycle described above; migrate existing
  slow infrastructure mutations and return operation progress immediately after acceptance.
- Define database deployment/backup requirements and test schema migration, startup,
  projection rebuild, and worker ownership before making this a required dependency.
- Implement reusable Git repository access, incremental fetch, isolated task workspaces,
  safe concurrent publication, and bounded cache lifecycle as defined above.

Exit: automated checks and clean live rehearsal pass, including upgrade from 0.1.0,
failed-verification rejection, pinned staging, first production activation, and
restart/duplicate handling. The supported upgrade path needs no undocumented repair.
Additional gates: publish warm-read p95 and observation-lag results against the
declared fixture; slow/unreachable upstreams do not block ordinary projected reads.
Test worker crashes before and after an external effect, lease expiry, duplicate
submission, database outage, concurrent/superseded tasks, exhausted retries, and
restart recovery. Accepted tasks must not disappear; retries must not duplicate
logical external actions. A stale display or delayed task cannot bypass authorization,
approval, or release verification. Confirm pending/failed state remains understandable.
Git gates: publish cold/warm benchmarks; verify concurrent jobs, moved branch heads,
credential-scope isolation, interrupted fetch/checkout, cache corruption/eviction,
and recovery without lost commits or cross-task working-tree contamination.

## 0.3 — Govern changes through Git

Depends on 0.2 read model, durable queue/workers, and release contracts.

Scope:
- Shared proposal lifecycle: draft, validating, review, merged, reconciling, healthy,
  failed, superseded/closed; represent approval state explicitly.
- Persist proposal/approval state and link it to queued operations. Resume webhook
  processing and post-merge reconciliation after restart, with transaction/outbox
  boundaries and deduplication. Never hold a worker lease while waiting for human approval.
- GitHub integration, required reviews/checks, approver roles, environment policy,
  audit events, branch protection, webhook authentication and replay handling.
- Rendered impact, Argo CD live/desired diff, drift ownership, stale-base handling,
  conflict resolution, and emergency-abort policy.
- Route existing protected mutations through proposals; prevent API, reconciler,
  and auto-promotion bypasses. Define approved provider-action interfaces.

Exit: rejected/unmerged requests cannot change protected desired state or invoke
provider mutations. Only the approved current revision applies. Test conflicts,
replays, concurrent edits, permission denial, and secret redaction. External pilot begins.

## 0.4 — Repository to application

Depends on 0.3 policy enforcement.

Scope:
- GitHub repository selection, webhooks, isolated Dockerfile builds, resource limits,
  scoped build secrets, registry access, logs, and digest-based deployment.
- Reuse the 0.2 Git access layer for exact-revision build sources while preserving
  build sandbox isolation and qualifying supported submodule/LFS behavior.
- Prevent untrusted fork PRs from receiving privileged credentials or production access.
- Automatic preview lifecycle, TTL, quotas, retry, and orphan cleanup.
- Guided first app, default HTTPS, custom-domain verification/TLS, variables/secrets,
  supported workload templates, and outbound deployment/approval event notifications.

Exit: an unfamiliar developer completes repository connection, build, preview,
approved production deployment, and cleanup using docs. Failed builds preserve the
previous release; duplicate events converge; untrusted PR isolation is verified.

## 0.5 — Application services and storage

Depends on 0.3 approved provider actions and 0.4 onboarding.

Scope:
- Catalog, administrator plans, service resources, component bindings, credentials,
  readiness, quotas, retention, and deletion protection.
- CloudNativePG, Valkey, NATS/JetStream, S3-compatible bucket attachment, and PVCs.
- Existing database/cache attachments with explicit lifecycle ownership.
- Provider capability matrix for create, resize, rotate, upgrade, backup, restore,
  and destroy; unsupported operations remain visible.
- Isolated preview dependencies and binding/access reviews.

Exit: reference workloads use the catalog. Qualify PostgreSQL and durable messaging
recovery against declared objectives, credential rotation, failed provisioning,
detach/destruction, and volume retention. Record snapshot prerequisites and cache
persistence/eviction semantics. Existing attachment does not imply managed backup.

## 0.6 — Understand application behavior

Depends on app/component identities and service bindings.

Scope:
- Resource/usage views by component, app, environment, and cluster.
- Prometheus-compatible connections, saved PromQL panels, filters, and access control.
- Loki logs, Tempo traces, OpenTelemetry setup, and deployment/trace correlation.
- Backend health, useful empty/error states, query limits, and failure notifications.
- Server-side telemetry authorization, including custom queries.

Exit: a developer diagnoses seeded application/resource failures from Suparship;
unauthorized cross-project queries fail. Telemetry outage is distinguishable from
workload outage and does not disable deployment control.

## 0.7 — Controlled canary releases

Depends on 0.3 governance, 0.4 artifact identity, and 0.6 diagnostics.

Scope:
- Stable and candidate versions for HTTP services, Gateway API/Envoy Gateway weights,
  readiness gates, version diagnostics, promotion, and abort.
- Approval policies for start/weights/promotion/rollback; explicit emergency policy.
- Preserve stable capacity; explain sessions and request-weight semantics.
- Require compatible database migrations; abort cannot reverse schema changes.

Exit: advance and abort under load; no intended routing to unready candidates;
restart and conflicts preserve state. Measure convergence/abort time and verify
approval and verification cannot be bypassed.

## 0.8 — Operate across clusters and clouds

Existing multi-cluster support remains available earlier. This qualifies the
complete experience and depends on 0.3, 0.5, 0.6, and 0.7.

Scope:
- Cluster capabilities/health, placement, overrides, secret/addon bindings, network
  prerequisites, ordered rollout, stop-on-failure, and aggregate/per-cluster status.
- Cloudflare endpoint health/weights/failover through approved changes.
- Local discovery plus a documented cross-cluster pattern over administrator-provisioned
  private networking; validate reachability and access.
- Regional/canary traffic view, credential management, and disconnection recovery.

Exit: EKS/AKS rehearsal covers deployment, partial rollout failure, unreachable
cluster, secret isolation, regional failure, routing recovery, and Suparship outage.
Record routing propagation/failover behavior. Stateless routing tests do not prove
cross-region database availability or stateful disaster recovery.

## 0.9 — Feature freeze and production qualification

Depends on all required capability gates. Stop new feature work.

Scope:
- Close security, reliability, accessibility, and usability gaps throughout the workflows.
- Freeze supported versions and stable API/config/chart/extension contracts;
  publish versioning/deprecation policy.
- Rehearse supported upgrades, platform recovery, data restore, credential rotation,
  and independent operation after removal/export.
- Validate quotas, operation recovery, docs, release artifacts, and compatibility.
- Qualify read latency and freshness at the supported scale, task throughput and
  fairness across clusters, database failover/recovery, and projection rebuild.
  Back up authoritative workflow records separately from rebuildable projections.
- Independent onboarding and sustained production pilots; produce release candidates.

Exit: no known critical/high exploitable vulnerabilities or unresolved defects
causing data loss, unauthorized access, approval bypass, or unverified promotion.
Required paths pass against the frozen compatibility matrix.

## 1.0 — General availability

Release only after evidence demonstrates:
- Three representative workloads: web/API with PostgreSQL, worker with messaging,
  and multi-component app with migrations.
- Two unfamiliar users complete installation and developer walkthroughs without
  undocumented operator intervention.
- Two external design-partner teams complete real pilots; production observation
  covers at least 14 consecutive days with no unresolved release blockers.
- Reproducible two-cloud, policy, upgrade, restore, canary-abort, and outage evidence.
- Published UI/API performance and freshness results plus task crash-recovery
  evidence; no lost accepted tasks or duplicate logical effects in the supported scenarios.
- Final artifacts pass required checks; publish limitations, stable/beta labels,
  support/security reporting policy, supported upgrade paths, and recovery runbooks.

Seek paid pilots alongside engineering, but keep commercial validation distinct
from technical readiness and the open-source release gates.

## Execution and scheduling

Before committing a date for each milestone:
1. Assign an accountable release owner and owners for deliverables and evidence.
2. Inventory existing functionality, gaps, risks, and acceptance scenarios.
3. Estimate against actual capacity, explicitly reserving time for defects,
   documentation, migration, security, and operational rehearsals.
4. Resolve provider feasibility and record architecture decisions for governance,
   addon lifecycle, release identity, routing, networking, read projections, and
   durable queue/storage ownership.
5. Publish a forecast with assumptions and update it when evidence changes.

Maintain a gate ledger: scenario, configuration, owner, evidence link, result,
unresolved defects, and release decision. Measure onboarding time, operator
interventions, deployment lead time, recovery, preview cleanup, and repeat adoption.
Set performance thresholds from pilot requirements before claiming an SLO.

If capacity is constrained, reduce provider breadth and experimental scope before
weakening approvals, data protection, or recovery. Changing a required capability
needs an explicit roadmap decision. Patch releases handle defects between milestones.
