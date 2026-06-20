# Stacks

A **stack** is a logical grouping of tightly-coupled apps inside a project (e.g. a
"voiceai" service = `web` + `agent-server` + `capacity-manager`). Apps are meant
to be small (1–2 components); a real service is a *collection* of apps that ship,
scale, and route together. A stack lets you group them, share config/secrets
across the group, co-locate them, act on them as a unit, and clone the whole
collection with variations.

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
- `POST .../stacks/{stack}/previews {name}` / `DELETE …/previews/{name}` —
  preview/tear down the whole collection co-located in one
  `{project}-{stack}-preview-{name}` namespace (`createStackPreview` overrides
  each member's preview namespace + stamps the stack ownership label + publishes).
- `DELETE .../stacks/{stack}?deleteApps=true` — delete every member app (store +
  gitops) and reclaim the stack's shared namespaces
  (`deleteOwnedStackNamespaces`, by the `suparship.io/stack` selector). Without
  the flag the apps are detached and kept (default), and `relocateApp` moves them
  back to their own namespaces.
- UI: a "Batch actions" card on the StackDetail page (sync all, promote all to an
  env, preview the stack, delete stack + all apps), `lib/stacks.ts` clients.

## Phase 4 — Clone stack (planned)

Goal: duplicate a collection with a few config diffs — the canonical case being
voiceai → livekit-cloud vs voiceai → self-hosted.

- `POST .../stacks/{stack}/clone { newName, appNames?: map[old]new, overrides? }`:
  create the new stack record (copy spec ⊕ override diffs), then recreate each
  member app under the new stack via the rename recreate-under-new-name path
  (copy, not move — old stack stays). New app names are derived (default
  `{newStack}-{oldApp}`, user-overridable) since app names stay project-unique.
- Limitation (as with rename): app-tier secret **values** can't be migrated
  (write-only vault) — re-enter under the new stack; the UI warns.

## Out of scope (future)

- Reusable **stack blueprint/template** — a versioned, parameterised multi-app
  definition you instantiate many times (a product phase 2, after the instance
  primitive matures).
- **Project/stack-scope gateway routing** and **cross-app canary / traffic
  splitting** — both depend on the Gateway API work (Envoy Gateway + HTTPRoute)
  tracked in ROADMAP.md; intentionally decoupled from stacks.
