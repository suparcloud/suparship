# Stacks

A **stack** is a logical grouping of tightly-coupled apps inside a project (e.g. a
"voiceai" service = `web` + `agent-server` + `capacity-manager`). Apps are meant
to be small; a real service is a *collection* of apps that ship, scale, and route
together. A stack lets you group them, share config/secrets across the group,
co-locate them, act on them as a unit, and clone the whole collection with
variations.

**When a stack vs one multi-component app?** An app can already hold one HTTP
surface + many workers/crons (no component cap). Use a **stack of apps** when you
need **multiple HTTP surfaces**, **independent release cadences**, or to **clone a
collection**. See the boundary rule in [ADR-0002](adr/0002-app-vs-stack-boundary.md)
and [app-model.md](app-model.md#app-sizing--the-stack-boundary).

**Status:** all four phases are implemented (grouping + cascade, shared namespace,
batch lifecycle, clone), shipping **beta for 0.1** — the feature is **optional**
(apps deploy fully without it) and its automated test coverage is still thin, so
0.1 hardens it before promoting it to stable. The remaining items are under "Out
of scope (future)".

## Design principle: config-cascade + orchestration, not a deployment engine

A stack is **lightweight**. Apps remain the deployment unit — each keeps its own
identity, ArgoCD Application (`{project}-{app}-{env}`), Kargo pipeline, ingress,
and owned namespace. A stack adds, on top of independent apps:

1. an **override layer** between project and app,
2. an optional **shared namespace** for intra-stack DNS,
3. **batch lifecycle** actions,
4. **clone**.

This reuses all the per-app gitops/ArgoCD/Kargo machinery and the existing
override-layering, secret-scope, and namespace-ownership work. The override
hierarchy is:

```
org → org-env → project → STACK → app → app-env → cluster
```

Membership is a label on the app (`AppSpec.Stack`); apps move in/out of a stack
without changing identity. The stack record (`domain.Stack`) holds the stack's
metadata + shared overrides, stored as ConfigMap `suparship-stack-{project}-{stack}`.

Rejected alternative: stack as a single deployment/promotion atom (one grouped
ArgoCD/Kargo pipeline) — too large a rework and weaker per-app isolation.

## Phase 1 — Grouping + override cascade ✅ (shipped)

- `domain.Stack`/`StackSpec` + `AppSpec.Stack`; `kube.K8sStackStore`.
- Stack CRUD + membership API (`/projects/{p}/stacks…`, `PUT …/apps/{app}/stack`);
  membership/override edits republish member apps.
- Override cascade between project and app across all three channels:
  - **env vars** — `mergeAllEnvVars` (cmd/suparship/server.go),
  - **Helm values** — `AppPublishEnv.StackRawValues`/`StackEnvRawValues` in
    `envOverlay` (internal/gitops/publisher.go),
  - **secrets** — `secrets.ScopeStack`/`ScopeStackEnv` (items
    `shared-stack-{proj}-{stack}[-env-{env}]`, global/env vault like project
    scopes), layered between project-shared and app in `ResolveScopes` +
    `BuildAppExternalSecret`; routes `/projects/{p}/stacks/{s}/secrets/{global,env/{env}}`.
- UI: `lib/stacks.ts`, `StackDetail` page (members, variables, secrets, delete),
  project page Stacks section + grouping + inline create.

## Phase 2 — Shared stack namespace + intra-stack DNS ✅ (shipped)

Member apps optionally co-locate in one `{project}-{stack}-{env}` namespace so
`web` reaches `agent-server-web` by in-cluster DNS without cross-namespace
plumbing.

- `{stack}` token in `secrets.RenderPattern`; `ResolveNamespace` gains
  `StackName`/`StackShared`/`StackPattern` and a shared-stack branch that wins
  over app/project scope (default `{project}-{stack}-{env}`, dedicated
  `{project}-{stack}`). `resolveEnvNamespaces` loads the app's stack (appHandler
  gained `stackStore`) and applies it.
- Shared stack namespaces carry the `suparship.io/stack` ownership label
  (`ownedNamespaceLabels`), so stack delete can reclaim them and rename/move
  never reclaim them as app-exclusive.
- `relocateApp` (namespace_ownership.go): recompute + persist namespaces, ensure
  new, republish, and reclaim the previous **app-exclusive** namespace — used by
  membership change (`handleSetAppStack`) and SharedNamespace toggle
  (`republishStackMembers`). `reclaimAppExclusiveNamespace` never deletes a
  shared namespace siblings rely on.
- UI: a SharedNamespace toggle on the StackDetail page.

## Phase 3 — Batch lifecycle ✅ (shipped)

Act on the whole collection in one call — orchestration over the existing
per-app flows, **no new ArgoCD/Kargo generators**. Every op is best-effort and
returns a per-app result summary (`stackBatchResponse` — `{app, ok, message,
error}` rows) so partial failures are visible.

- `POST .../stacks/{stack}/sync` — `republishApp` for every member.
- `POST .../stacks/{stack}/promote {targetEnvironment}` — `promoteAppEnv` for
  every member. The promote core was extracted from `handlePromoteApp` into
  `appHandler.promoteAppEnv` (Kargo Promotion when wired, else store-copy) so the
  per-app handler and the batch share one path; status mapping via
  `statusForPromoteErr`.
- `POST .../stacks/{stack}/previews {name, baseEnv?, imageTag?, apps?}` /
  `DELETE …/previews/{name}` — preview/tear down the whole collection co-located
  in one `{project}-{stack}-preview-{name}` namespace. `createStackPreview`
  clones the base env (via the per-app `PublishAppPreview` path) and overrides
  each member's namespace to co-locate them. It is an **upsert** (re-point every
  member at a new `imageTag`), **developer-callable** (CI once per PR), **skips**
  members with previews disabled, and honors an optional `apps` subset. Delete
  prunes each member's preview gitops so ArgoCD removes the Applications +
  namespace. See [previews.md](previews.md#stack-previews-preview-a-whole-collection-in-one-call).
- `POST .../stacks/{stack}/pin {fromPreview, targetEnv, apps?}` /
  `DELETE .../stacks/{stack}/pin {targetEnv, apps?}` — pin a PR preview
  group to a stable env across the stack, then unpin. Pin state is per-(app,env,
  Kargo-stage), so this is a **fan-out, not a shared field**: each **pipeline**
  member pins its OWN image tag (resolved from its `fromPreview` preview) and
  pauses its own stage; **direct-delivery** members and members lacking that
  preview are **skipped**. Unpin runs each member's freight-restore. Reuses the
  per-app `pinAppEnv`/`unpinAppEnv` cores (extracted like `promoteAppEnv`).
- `POST .../stacks/{stack}/suspend {targetEnv, apps?}` /
  `POST .../stacks/{stack}/resume {targetEnv, apps?}` — suspend (scale down) or
  resume an env across member apps. **Developer-triggerable** and API-first (a CI
  job can suspend idle preview/staging envs off-hours). Fan-out over the
  (optionally subset-narrowed) members via the per-app `suspendAppEnv` core; a
  member not deployed to `targetEnv` is skipped. Each member's chart honors the
  platform's suspend key (default `suspend`; see
  [chart-conventions.md](chart-conventions.md#suspend)). The env stays published
  — no data loss, unlike undeploy.
- `DELETE .../stacks/{stack}?deleteApps=true` — delete every member app (store +
  gitops) and reclaim the stack's shared namespaces
  (`deleteOwnedStackNamespaces`, by the `suparship.io/stack` selector). Without
  the flag the apps are detached and kept (default), and `relocateApp` moves them
  back to their own namespaces.
- UI: a "Batch actions" card on the StackDetail page (sync all, promote all to an
  env, preview the stack, delete stack + all apps), `lib/stacks.ts` clients.

## Phase 4 — Clone stack ✅ (shipped)

Duplicate a collection with a few config diffs — the canonical case being
voiceai → livekit-cloud vs voiceai → self-hosted.

- `POST .../stacks/{stack}/clone { newName, appNames?, displayName?, description?,
  sharedNamespace?, namespacePattern?, rawValues?, envRawValues?, envConfig? }`:
  creates the new stack record (the source spec ⊕ any override fields — a set
  field REPLACES the copied value; omitted carries over; `displayName` resets to
  empty so the clone shows its own name unless named), then recreates each member
  app under the new stack via `appHandler.copyAppAs` — the recreate half of the
  rename path, but a COPY: the source stack and its apps stay intact.
- New app names are derived (default `{newStack}-{oldApp}`, overridable per app
  via `appNames[old]=new`) since app names stay project-unique. Returns
  `cloneStackResponse {stack, results}` with a per-app summary.
- The new stack is saved **before** copying apps so `resolveEnvNamespaces` can
  read its `SharedNamespace` and co-locate the clones. Preview envs aren't copied
  (ephemeral); a failed publish rolls the half-app back.
- Limitation (as with rename): app-tier secret **values** can't be migrated
  (write-only vault) — re-enter under the new apps; the UI warns.
- UI: a "Clone this stack" control in the StackDetail batch-actions card.

## Out of scope (future)

- Reusable **stack blueprint/template** — a versioned, parameterised multi-app
  definition you instantiate many times (a product phase 2, after the instance
  primitive matures).
- **Project/stack-scope gateway routing** and **cross-app canary / traffic
  splitting** — both depend on the Gateway API work (Envoy Gateway + HTTPRoute)
  tracked in ROADMAP.md; intentionally decoupled from stacks.
