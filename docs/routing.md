# Platform-owned routing

suparship can render the HTTP surface of an app itself — Gateway API `HTTPRoute`
objects (and the `ReferenceGrant`s cross-namespace backends need) — from a
declared **route spec**, the same way it renders the env ConfigMap and Secret.
The chart stays routing-agnostic (its own ingress disabled). Routing then works
the same for every chart, and **route-to-preview becomes a backend switch**:
the hostname never changes; the route's backend points at the preview's Service.

When you want it:

- one hostname fanning out to several apps by path (`/` → `web`, `/api` → `api`)
- a "routes app" today (a chart whose only job is HTTPRoutes) — retire it
- route-to-preview without any hostname handover or ArgoCD wait

When you don't: a single app whose chart already renders its own Ingress or
HTTPRoute from `((platform.routingHost))` works as before, including the
[host swap](previews.md#route-send-a-stable-hostname-to-a-preview).

Either edge works. A tier whose [routing profile](multi-cluster.md) carries a
`gateway` renders **HTTPRoutes** attached to it; a tier with only an
`ingressClassName` renders **Ingresses** (with a cert-manager issuer annotation
and TLS block when the profile sets `clusterIssuer`). The dev cluster's
ingress-nginx works out of the box.

## Declaring routes

Routes are **app-owned**. The New App form seeds them: each web component with
an Expose tier gets a rule (`/` for the first, `/<component>` for the rest) on
`((platform.appRoutingName)).((platform.<tier>BaseDomain))`, unless you untick
**Platform-managed routing** to let the chart's own ingress handle it. Edit them
later under **Settings → Routes** (a form, with YAML behind Advanced), and see
what is rendered per environment — hostnames, each path's Service, and any
preview switched in — on the **Traffic** tab. In API terms, `routes` on the
create request and on PATCH, and `GET …/apps/{app}/routes` for the live view.
A routes change republishes every stable env **and the app's open previews**
(each preview carries its own copy), so nothing keeps serving the old rules.

A rule's port is the backend **Service** port, not the container port. Picking
a component in the editor fills it from the component's `service.port` value;
a rule left on a port nothing listens on shows up as 503/504 at the edge.

Each app declares the surfaces it serves:

```yaml
# app "web" — Settings → Routes
routes:
  - name: site
    hostnames: ["shop((platform.previewSuffix)).((platform.externalBaseDomain))"]
    rules:
      - pathPrefix: /
        backend: { component: web, port: 80 }
# app "api"
routes:
  - name: site
    hostnames: ["shop((platform.previewSuffix)).((platform.externalBaseDomain))"]
    rules:
      - pathPrefix: /api
        backend: { component: web, port: 8080 }
```

**One hostname per environment.** When two environments share a base domain
(the dev loop: staging and prod on `localhost`), a hostname without an env
segment renders identically for both — DNS can only point it at one of them,
and on a shared cluster the ingress admission webhook refuses the second
object. Saving such a route is rejected with a message naming the two envs;
use `((platform.envType))` in the host (the New App form seeds that form
automatically when the org's envs share a base domain), or give the envs
different base domains.

Two apps declaring rules on the same hostname is the normal way to share it:
Gateway API merges HTTPRoutes on one host (longest prefix wins), and suparship
checks that no two rules claim the same `(hostname, pathPrefix)` when you save.

| field | meaning |
|---|---|
| `name` | identifies the route within its owner; default `route-<n>` |
| `hostnames` | one or more hosts; `((platform.*))` tokens allowed. Default `((platform.appRoutingName)).((platform.externalBaseDomain))` |
| `tier` | `external` (default) or `internal` — picks the Gateway from the routing profile |
| `rules[].pathPrefix` | Gateway API `PathPrefix` match |
| `rules[].backend` | `app` (default: the declaring app; any app in the project), `component`, `port` (required), `service` (override; default `{app}-{component}`, the Service the example charts render) |

A **stack** may declare routes too, as centralized sugar: a rule's backend names
a member app, and at publish time the stack's routes are expanded into each
backend member's own routes. Nothing else changes — if you move the rules to the
apps later, the rendered objects are identical. The stack default hostname is
`((platform.stack))((platform.previewSuffix)).((platform.externalBaseDomain))`.

Tokens that matter here: `((platform.appRoutingName))` (the app label, preview
and route variants folded in), `((platform.previewSuffix))` (`""` in stable
envs, `-pr-42` in a preview — append it to a shared literal host so each preview
gets its own), `((platform.stack))`, and the per-tier base domains. See the
[token table](byo-charts.md#wiring-platform-context-with-platform-tokens).

## What gets rendered

A route is split **per backend app**: each app's platform resources
(`_app-resources/{env}/{project}/{app}/route-<name>.yaml`) carry only the rules
that forward to that app. In a stable env an HTTPRoute and its Service therefore
always share a namespace — no cross-namespace references, shared stack namespace
or not — and the platform ApplicationSet applies them into the app's namespace.
An app not deployed in an env renders no rules there (its paths 404 at the
gateway).

Objects carry `suparship.io/app`, `suparship.io/env`, `suparship.io/route` (and
`suparship.io/stack`) labels; the runtime attributes them to the app by
`suparship.io/app`, since ArgoCD rewrites `app.kubernetes.io/instance` on
everything it syncs.

On an **Ingress** edge a backend in another namespace (a composite preview's
sibling paths, or a backend switch) is reached through an `ExternalName`
Service shim rendered next to the Ingress — `service-route-<app>-<svc>.yaml`
pointing at `<svc>.<ns>.svc.cluster.local` — because an Ingress backend must be
local. ingress-nginx proxies ExternalName backends natively; check your
controller if it is a different one. No `ReferenceGrant`s are written on an
Ingress edge (a cluster without Gateway API has no such CRD).

## Previews

A preview renders the same routes on the preview form of the hostname (the
tokens above make `shop-pr-42.acme.com` from `shop((platform.previewSuffix))…`).
When several apps share the hostname, a single-app preview is **composite**: its
own paths go to the preview's Services, every other app's paths are forwarded to
that app's base-env Services cross-namespace, with a `ReferenceGrant` rendered
into each sibling's base-env resources. So a PR of `api` is testable end to end
at `shop-pr-42.acme.com` without previewing `web`. A stack preview (all members
co-located) needs no cross-namespace references.

## Route-to-preview: the backend switch

`POST …/environments/{env}/route {fromPreview}` on a platform-routed app sets the
same marker the host swap uses (`routedToPreview`) but renders differently: the
stable env's HTTPRoute keeps its hostname and its `backendRefs` now name the
preview namespace's Service; the preview's resources gain a `ReferenceGrant`
allowing the env's HTTPRoute to cross into it. One publish of each side, no
hostname handover, no admission-webhook ordering and no ArgoCD wait — it
completes in seconds. Restore and preview-delete reverse it. The env badge,
CI commands and the task API are unchanged.

## Limitations

- Ingress edges rely on `ExternalName` backends for cross-namespace forwarding
  (composite previews, backend switch); ingress-nginx supports it, other
  controllers may not.
- Backends assume the Service is named `{app}-{component}` (or `{app}`) unless
  `service` overrides it. Projects whose preview namespace pattern omits `{name}`
  suffix Service names per preview; the platform does not follow that yet.
- Sibling base envs are republished whenever a sharing app's preview is created
  or deleted (one batched publish), to keep their `ReferenceGrant`s in step.
- No weights, header matches or redirects in v1: rules are `pathPrefix → backend`.
