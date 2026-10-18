# Fast UI read model (0.2 beta)

The server maintains shared, disposable JSON projections indexed by resource
path and the cluster-config `env` selector. Dashboard metadata, app lists and
details, app environments, deployment history, plain configuration/resolved
configuration, variable catalogs, and cluster summaries use these views.
App-list `brief` and `stack` hints share the full project view; their result
membership is unchanged. Metrics, logs, secret-value reads, promotion gates,
mutations and authorization continue using their dedicated authoritative paths.

## Runtime contract

- `Server.Run` starts eight background workers. Embedders using `Handler` call
  `StartReadModel(ctx)` with their server lifecycle context.
- Initial access registers a view and returns `503`, `Retry-After: 1`, and
  `X-Read-State: synchronizing`. It never performs synchronous data fan-out.
  The UI retries for up to 30 seconds and then displays an error.
- Workers refresh requested views every five seconds, with an eight-second
  deadline. Idle views expire after five minutes. There are at most 1,024 keys,
  2 MiB per response, and 64 MiB of retained payloads. Saturation is an explicit
  unavailable/stale state, never a fallback to live reads.
- Responses include `X-Read-State` (`fresh`, `stale`, `synchronizing`, or
  `unavailable`), `X-Read-Observed-At` (UTC timestamp), and `X-Read-Revision`
  (content hash, **not** an authoritative Git/Kubernetes revision).
- A source failure retains the last good result and marks it stale. Typed
  not-found errors permit removal; a source outage does not imply deletion.
  Partial runtime failures also mark the view stale. Cold failures without a
  usable result return an actionable unavailable response.
- Successful mutations invalidate views. A generation check rejects observations
  that started before the invalidation. External changes arrive through periodic
  refresh. Background mutations may become visible on the next refresh.
- `?watch=1` on a projected read returns SSE `view` events containing freshness
  and revision metadata only. Streams close within 15 seconds so reconnection
  rechecks session/role access. Every connection sends the current state;
  consumers GET the latest snapshot instead of relying on a replay log.
- Browser app, project and stack views refresh runtime rows on revision changes.
  App editors retain their configuration/drafts. Dashboard metadata and excess
  subscriptions use five-second polling to reserve browser connections for writes.
  No more than three SSE connections are opened per page. Visibility restoration
  resynchronizes the view. Configuration editors do not auto-replace drafts.
- A stale view cannot authorize a mutation. Authentication and authorization run
  outside the projection on every GET and SSE reconnect. Their own store latency
  remains part of request latency; unavailable authority fails closed.

The Kubernetes server gives data clients a bounded budget of 50 requests/second
with a burst of 100 (`--kube-qps`, `--kube-burst`). The role authorizer uses an
independent client at 100 requests/second, burst 200 (`--auth-kube-qps`,
`--auth-kube-burst`), so background enrichment cannot consume its tokens. Every
role check still reads the current org ConfigMap; there is no permission cache.
The Helm chart exposes these under `server.kubernetes` as `qps`, `burst`,
`authorizationQps`, and `authorizationBurst`, and validates positive numeric
limits (integer bursts) before deployment. These are per-client, per-replica
limits, not an aggregate cluster limit. Tune them
against API-server capacity and replica count. Remote workload-cluster clients
retain their existing limits. Debug logs include the first partial source error
for a refresh. The chart grants read access to StatefulSets and DaemonSets as well
as Deployments so runtime discovery can complete.

This incremental model uses bounded polling over existing reader interfaces.
It has no new database dependency or persisted schema; every replica rebuilds
its own views. No read-projection backup/restore is needed. Replicas can briefly
show different observations. The durable task queue remains separate work.
See [ADR 0008](adr/0008-shared-read-projections.md).

## Reproducible latency and observation fixture

Run:

```sh
go test ./internal/server -run 'TestReadProjection(LatencyFixture|PeriodicObservationAndRestart)' -count=1 -v
```

Measured on 2026-10-18 on the development machine, without the race detector:
100 apps, 300 environments, 16 concurrent clients, 1,600 loopback HTTP requests
per endpoint. Each simulated runtime read takes 10 ms. The warm HTTP path includes
real session/role middleware with in-memory authority. The baseline is one
uncached handler read; it is not a baseline percentile.

| Endpoint | Uncached handler baseline | Warm p50 | Warm p95 | Warm p99 | Initial materialization |
| --- | ---: | ---: | ---: | ---: | ---: |
| App list | 416.49 ms | 0.47 ms | 1.26 ms | 2.07 ms | 414.74 ms |
| App detail | 10.44 ms | 0.27 ms | 0.88 ms | 1.23 ms | 11.11 ms |

The periodic change test measured 4.98 seconds from simulated source change to
read visibility, below the declared 15-second ingestion budget. This budget is
for successful source reads and the declared fixture, independent of Argo CD or
Kubernetes reconciliation time. Worker saturation or unavailable sources can
exceed it; freshness becomes stale after 15 seconds.

Tests cover cold reads, warm reads during a blocked refresh, source failure,
deletion, query/resource isolation, write fencing, unauthorized/revoked reads
and streams, reconnect resynchronization, capacity limits, cancellation, periodic
updates, and rebuilding after restart.

These results pass the provisional 300 ms warm-read gate **for this fixture**.
Production cluster/auth latency, multi-replica/load-balancer behavior, browser
interaction latency and larger response sizes still require deployment-specific
rehearsal before declaring the release performance gate complete.

## Tilt stack rehearsal — 2026-10-18

Reproduce against the running local `kind-suparship-dev` Tilt stack:

```sh
python3 hack/test-read-projections.py --requests 1600 --exercise-sync
```

The script uses the local dev credentials (overridable with
`SUPARSHIP_DEV_USER` / `SUPARSHIP_DEV_PASSWORD`) and rejects non-loopback URLs.
The synchronization exercise creates only a uniquely named app ConfigMap,
changes/corrupts/restores it, then deletes it in cleanup. It publishes no workload.
The default report path is `/tmp/suparship-live-read-report.json`.

This run used the real Kubernetes-backed API through Tilt's localhost:8080 port
forward, live session authorization against the org ConfigMap, and Argo CD/runtime
reads: **3 existing apps, 9 environments, 16 concurrent clients, 1,600 requests
per endpoint**. The Vite UI was also open during the run.

| Endpoint | Warm p50 | Warm p95 | Warm p99 | Successful / fresh |
| --- | ---: | ---: | ---: | ---: |
| App list | 159.55 ms | 163.12 ms | 173.57 ms | 1,600 / 1,600 |
| App detail | 160.24 ms | 171.10 ms | 180.30 ms | 1,600 / 1,600 |

All nine exercised routes became fresh: org, projects, environments, clusters,
app list, app detail, app environments, staging history, and resolved staging
configuration. Cold reads returned `503 synchronizing`; unauthenticated GET and
SSE requests returned `401`. An external ConfigMap update arrived through SSE in
4.82 seconds; reconnect returned the current revision. A malformed source retained
the last good response with a stale indicator and recovered after restoration.
Deletion became `404` in 5.25 seconds. No test ConfigMaps remained afterward.

The browser showed fresh dashboard/project/app data and preserved an unsaved
settings draft across background observation updates; the draft was discarded.
This was a functional browser check, not a browser interaction latency benchmark.

The first live run exposed contention on client-go's default request budget,
missing StatefulSet/DaemonSet read permissions, and legacy fallback handling that
misclassified native empty apps and obscured source failures/deletions. The fixes
reserve authorization capacity, grant the required workload reads, and preserve
native absence/error semantics through the compatibility layer. The final run
passes the provisional 300 ms gate **for this local stack and load**. It does not
establish production fleet-scale or remote-cluster performance.
