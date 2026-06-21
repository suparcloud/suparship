# ADR-0002: The App ↔ Stack Boundary and Deployment Variants

**Status:** Accepted
**Date:** 2026-06-21
**Authors:** suparCloud engineering

---

## Context

[ADR-0001](0001-app-as-primary-deployment-object.md) established the **app** as the
primary deployment object, with **components** (web/worker/cron) as internal
runtime processes. Since then the platform has also grown **stacks** (a grouping +
config-cascade layer over multiple apps — see [stacks.md](../stacks.md)).

This left two overlapping ways to model a real-world "service", with no documented
rule for choosing between them:

- **One multi-component app** — `AppSpec.Components`. There is **no hard cap** on
  component count (`ValidateComponents`, `internal/domain/validate.go`). One app is
  **one Helm release** per env/cluster, **one Kargo pipeline**, promoted atomically.
  The one real structural limit: **at most one exposed (web/ingress) component** per
  app (`ValidateSingleExposedComponent`, `ValidateExposeModes`) — mixed-tier and
  multi-HTTP apps are precluded until per-component `routing.host` chart plumbing exists.
- **A stack of small apps** — `domain.Stack` groups independent apps and adds a
  shared override layer, optional shared namespace + intra-stack DNS, batch
  lifecycle, and clone. Each member keeps its own ArgoCD Application, Kargo
  pipeline, and ingress.

Separately, four mechanisms for **varying** a deployment exist at different
maturity: previews (complete), per-env config / `EnvironmentDefaults` (complete),
clone / `copyAppAs` (exists, reachable via stack-clone), and in-app **release
channels** (canary/stable in one env — not built; depends on the Gateway API
traffic-splitting work).

We need a stated principle so users (and the UI/docs) consistently know which
primitive to reach for, before the 0.1 release.

---

## Decision

### 1. The app boundary

> **One app = one atomic-release unit** (one Kargo pipeline / one image-tag
> promotion) with **at most one HTTP surface**, plus the background process types
> (workers, crons) that ship and roll out *with* it.

An app with one `web` + N `worker` + M `cron` components is normal and fully
supported — there is no component-count limit. The boundary is **shape**, not
size: one HTTP surface, one release.

Reach for **separate apps grouped in a stack** when you need any of:

- **Multiple HTTP surfaces** — separate ingresses/domains (e.g. public API +
  admin UI). The single-exposed-component invariant forces this.
- **Independent release cadences** — processes that promote on their own schedule
  / own image repos and pipelines.
- **Clone the whole collection** with config diffs (e.g. `voiceai → livekit-cloud`
  vs `voiceai → self-hosted`).

The single-exposed-component invariant is **the forcing function** that makes this
boundary concrete, not a count threshold.

### 2. Deployment variants

| Need | Primitive | Status |
|---|---|---|
| Test a branch/PR in isolation | **Preview** (ephemeral env instance of an app) | Shipped |
| Differ config between staging & prod | **Per-env config** (`EnvironmentDefaults`) + promotion | Shipped |
| A second long-lived copy with config diffs (tenant/region, cloud-vs-self-hosted) | **Clone** (stack-clone today; single-app clone if needed later) | Shipped (stack) |
| Canary/stable split *within one env* | **Release channels** | **Post-0.1** (needs Gateway API) |

In-app release channels are **out of scope for 0.1**.

### 3. Stacks are beta for 0.1

Stacks are complete (model/API/UI/clone) and **optional** — apps deploy fully
without them. For 0.1 they ship **labeled beta**, with test hardening, because
their automated coverage is currently thin. Disabling the stack store cleanly
degrades to an app-only product.

---

## Consequences

- **Validation messages** point users toward stacks: rejecting a second exposed
  component now says *"…split additional exposed components into their own apps and
  group them in a stack"* (`internal/domain/validate.go`).
- **No hard cap** on components is added; the boundary is this documented rule plus
  the single-exposed invariant.
- **No lifting** of the single-routing invariant in 0.1. Multiple HTTP surfaces per
  app (per-component `routing.host`) remain future work.
- **Docs**: [app-model.md](../app-model.md) gains an "App sizing & boundary"
  section and the variant matrix; [stacks.md](../stacks.md) reflects beta status and
  cross-links this ADR.
- **Stacks UI** carries a "Beta" badge; `ROADMAP.md` records stacks=beta and
  release-channels=post-0.1.

---

## Alternatives considered

### Allow multiple exposed components per app (lift the single-routing invariant)

Rejected for 0.1. It needs per-component `routing.host` plumbing in the chart and
weakens per-surface isolation. Splitting into separate apps + a stack already
covers the multi-HTTP-surface case with stronger isolation and independent
pipelines.

### Make the stack the deployment/promotion atom (one grouped pipeline)

Rejected (also noted in stacks.md). Too large a rework and weaker per-app
isolation; apps stay the deployment unit and stacks coordinate over them.

### Build in-app release channels for 0.1

Rejected. Zero code today and gated on the Gateway API traffic-splitting work.
Previews + per-env config + clone cover the 0.1 variant needs.

### Add a hard component-count cap

Rejected. The meaningful constraint is one HTTP surface per app, not a number; a
count cap would be arbitrary and block legitimate worker/cron-heavy apps.
