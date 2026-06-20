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

## Phase 3 — Batch lifecycle (planned)

Goal: act on the whole collection in one call — orchestration over the existing
per-app handlers, **no new ArgoCD/Kargo generators**.

- `POST .../stacks/{stack}/sync` — republish every member app.
- `POST .../stacks/{stack}/promote {targetEnv}` — promote every member
  (per-app `handlePromoteApp` / `domainapp.Promote`).
- `POST .../stacks/{stack}/previews {name}` / `DELETE …` — preview/tear down the
  whole collection in a shared preview namespace
  `{project}-{stack}-preview-{name}` so it's co-located + discoverable.
- `DELETE .../stacks/{stack}?deleteApps=true` — delete all members
  (`handleDeleteApp`) + the stack record + reclaim the stack namespace.
- Best-effort with a per-app result summary.

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
