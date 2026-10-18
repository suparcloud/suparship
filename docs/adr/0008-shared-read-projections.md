# ADR 0008: Bounded, rebuildable UI read projections

Status: Accepted for the incremental 0.2 read model.

Ordinary app, environment, history, configuration and cluster reads currently
repeat remote calls on the HTTP request path. Introduce a process-local indexed
projection of explicitly allowlisted, identity-independent JSON views. Each key
includes the route and resource identifiers. Authorization runs outside the
projection on every read and SSE reconnect; no session or credential is retained
in the projection. Writes and protected decisions continue using source stores.

The first read registers a view and returns a synchronizing response. Eight
background workers refresh active views every five seconds with an eight-second
deadline. Idle views expire after five minutes; capacity and response-size limits
bound memory. A failed refresh retains the last result but marks it stale. A
confirmed 404 replaces the previous result. Writes invalidate existing views,
and generation checks discard refreshes started before that invalidation.
SSE carries revision/freshness metadata only, reconnecting at least every fifteen
seconds to recheck access. Clients resynchronize by reading the current snapshot;
no unbounded event log or replay is needed. Independent replicas rebuild their
own projections. No schema migration, backup, leader election or database is
required for this disposable data. This does not implement the durable task queue.

Bounded polling is the initial integration because the existing readers expose
request/response interfaces, not watch streams. A future source-level informer
can replace view loaders without changing the HTTP contract. Only requested
views are maintained; a cold start never falls back to synchronous fan-out.
The initial ingestion budget is 15 seconds for successful refreshes at the
published fixture size. Source reconciliation delay is separate. Failures or
capacity pressure can exceed that budget and must remain visible as stale.
