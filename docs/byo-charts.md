# Bring your own Helm charts

Bring-your-own charts is not a mode of suparship — it is **the** model. There
are no built-in templates and no values schema the platform injects: every
chart on the platform is a plain Helm chart you (or your platform team)
registered, and charts never need to know anything about suparship. You deploy
the Helm charts you already have — unmodified — and do all of the platform
wiring from the outside: in the app's values overlay and in the UI. Authoring
a [`template.yaml`](templates.md) is an optional metadata layer on top, not an
entry fee.

Three things make this work:

1. **Chart sources** — point suparship at a directory of plain charts (git or
   OCI); each chart is imported as a template automatically.
2. **`((platform.*))` tokens** — put them in an app's values overlay and the
   publisher resolves them per environment at publish time. The chart just
   sees ordinary strings.
3. **UI-authored developer values** — map the handful of chart paths a
   developer should see onto a form, per template, from the UI. No chart or
   YAML authoring involved.

A set of production-ready starting points lives in
[`examples/charts/`](../examples/charts/): `web` (Ingress or Gateway API),
`worker`, `cronjob`, `job` (release-gating PreSync hook), a standalone
`gateway` edge chart, and a single-instance `postgres` for demo stacks. They
are plain Helm — installable with `helm install` on any cluster — and double
as the reference for the conventions below.

## Registering a chart source

**Templates → Sources → Add source**, type **Git charts repo** (`gitcharts`):

| Field | Meaning | Default |
| --- | --- | --- |
| Repo URL | git clone URL of the repo holding your charts | — |
| Ref | branch / tag / commit | `main` |
| Path | subdirectory scanned for charts | `charts` |
| Credentials | token or username/password for private repos | anonymous |

Every directory under *Path* containing a `Chart.yaml` is imported as a
template. suparship publishes *only your overlay values plus resolved
tokens* — it never injects its own values into your chart; the chart's own
`values.yaml` stays the Helm base. Sources re-sync on an interval
(`SUPARSHIP_TEMPLATE_SYNC_INTERVAL`, default 5m), so pushing a chart change
to the repo rolls it out to new publishes.

> **Template names are namespaced by source.** A source registered with
> *Namespace templates by source* (the default for new sources) imports each
> chart as `<source>.<chart>` — `acme.web`, `beta.web` — so two sources can
> ship charts with the same name. The gallery shows the chart's title with a
> source chip; the qualified name is what apps pin and what the
> `/templates/{name}` routes take.
>
> A source registered **without** namespacing keeps bare chart names, which
> are global: a chart whose name is already provided by a different bare
> source is **refused at sync** ("rename the chart or remove the conflicting
> template") while the rest of the repo imports normally. Switch such a
> source with **Namespace…** on its row in Settings → Templates → Sources:
> suparship re-syncs it under the new names, rewrites every app pinned to
> the old names, republishes those apps (same chart bytes — only the chart
> directory in the GitOps repo moves) and removes the bare-named entries.

For example, to make this repo's example charts available:

- Repo URL: `https://github.com/suparcloud/suparship.git`
- Path: `examples/charts`

Single charts in an OCI registry (`oci://ghcr.io/acme/charts`, chart +
version) and one-off `.tgz` uploads (**Templates → Import**) are also
supported.

## Wiring platform context with `((platform.*))` tokens

Your chart has its own value names (`ingress.host`, `envFrom`, whatever they
are). Instead of teaching the chart about suparship, set those values in the
app's overlay using tokens; the publisher substitutes the per-environment
resolution when it writes each env's `values.yaml`:

```yaml
# app values overlay — chart paths are YOUR chart's; tokens are suparship's
envFrom:
  configMaps: ["((platform.configMapName))"]  # the app's variables, rendered by suparship
  secrets: ["((platform.secretName))"]        # the app's secrets (ExternalSecret target)
ingress:
  enabled: true
  host: ((platform.routingHost))
  className: ((platform.ingressClassName))
  tls:
    clusterIssuer: ((platform.clusterIssuer))
```

`((platform.routingHost))` is a complete host in the platform's default shape
(`{app}.{envType}.{domain}`, previews `{preview}.{app}.preview.{domain}`).
When you want a different shape — `myapp.acme.com`, `api.myapp.acme.com`,
`myapp-api.staging.acme.com` — compose the host yourself from the
platform-owned **name** token and a domain. The platform still owns the part
that has to move: the name folds in the preview id and the
[route](previews.md#route-send-a-stable-hostname-to-a-preview) state.

```yaml
# frontend: the bare app name on the external tier's domain
ingress:
  host: ((platform.appRoutingName)).((platform.externalBaseDomain))     # myapp.acme.com
# api: component-qualified, or a subdomain of the app
ingress:
  host: ((platform.appComponentRoutingName)).((platform.externalBaseDomain))  # myapp-api.acme.com
  # or: api.((platform.appRoutingName)).((platform.externalBaseDomain))       # api.myapp.acme.com
# or let the platform compose it per tier (routing name + tier base domain)
ingress:
  host: ((platform.externalRoutingHost))
```

| | stable env | preview `pr-42` | env routed to preview | preview serving the env |
|---|---|---|---|---|
| `appRoutingName` | `myapp` | `myapp-pr-42` | `myapp-origin` | `myapp` |
| `appComponentRoutingName` | `myapp-api` | `myapp-api-pr-42` | `myapp-api-origin` | `myapp-api` |

The names are always one hyphen-joined DNS label, so a one-level wildcard
certificate covers every variant. A literal host in your values is left alone
by the platform, which also means route cannot move it — route refuses such a
component with a 422 that names it.

Or skip chart ingress entirely and let the platform render the routes: declare
them on the app (Settings → Routes) and set `ingress.enabled: false` /
`httpRoute.enabled: false`. See [routing.md](routing.md) — this is the model for
one hostname shared by several apps, and route-to-preview then switches the
backend instead of the hostname.

Or Gateway API instead of Ingress:

```yaml
httpRoute:
  enabled: true
  hostnames: ["((platform.routingHost))"]
  parentRefs:
    - name: ((platform.externalGatewayName))
      namespace: ((platform.externalGatewayNamespace))
      sectionName: ((platform.externalGatewaySectionName))
```

Commonly used tokens (see `internal/platform/interpolate.go` `PlatformTokens`
for the full catalog):

| Token | Resolves to |
| --- | --- |
| `((platform.configMapName))` / `((platform.secretName))` | the platform-managed env ConfigMap / Secret for this app instance (preview-suffixed in previews) |
| `((platform.routingHost))` | the resolved external host for this env in the default shape, e.g. `myapp.staging.acme.com`. While the env's hostname is [routed to a preview](previews.md#route-send-a-stable-hostname-to-a-preview), the preview gets this host and the env gets `myapp-origin.staging.acme.com` |
| `((platform.appRoutingName))` / `((platform.appComponentRoutingName))` | the platform-owned hostname **label** — `myapp` / `myapp-api` — with the preview id (`myapp-pr-42`) and route state (`myapp-origin`) folded in; compose with a domain to pick your own host shape (see above) |
| `((platform.externalRoutingHost))` / `((platform.internalRoutingHost))` | the routing name under that tier's base domain, e.g. `myapp-api.staging.acme.com` — a complete swappable host with no env-type segment; empty when the tier has no profile |
| `((platform.previewSuffix))` | `""` in stable envs, `-pr-42` in previews — append to a shared literal hostname label so each preview gets its own host |
| `((platform.stack))` | the app's stack (group) name; empty when not in a stack |
| `((platform.ingressClassName))` / `((platform.clusterIssuer))` | the routing profile's IngressClass / cert-manager issuer |
| `((platform.externalGatewayName/Namespace/SectionName))` (+ `internal…`) | the Gateway API parentRef of the resolved routing profile |
| `((platform.env))` / `((platform.envType))` | environment name / classification (`staging`, `prod`, `preview`) |
| `((platform.namespace))` / `((platform.cluster))` | target namespace / cluster |
| `((platform.imageTag))` | the resolved image tag — the per-PR tag in previews, `""` in stable envs (where CD owns the tag) |
| `((platform.previewName))` | the PR id in previews (suffix resource names in shared-namespace previews) |
| `((vars.KEY))` | the resolved value of app/env variable `KEY` |

The environment contract is deliberately tiny: suparship renders the
ConfigMap and Secret *objects*; your chart consumes two *names* however it
likes (typically `envFrom`). Everything else is optional.

## Developer values: a form for your chart, authored in the UI

Raw values YAML is the wrong day-to-day surface for most developers. On the
template's page, **Developer values** lets you curate the fields that matter —
without touching the chart:

- each field maps a title/type/constraints to a chart path
  (e.g. *Port* → `containerPort`);
- a field can **mirror** into several paths at once
  (*Port* → `containerPort` **and** `service.port`);
- app pages then default to a form for those fields (Advanced still exposes
  the full YAML), and only explicitly-set fields are saved as overrides.

## Continuous delivery for chart images

CD does not depend on templates either: in the app's image settings, bind the
container repository to whatever values path holds the tag (`image.tag` in
the example charts). Kargo watches the repository and suparship writes the new
tag to that path on promotion.

## The `gateway` example: a shared edge

The standalone [`gateway` chart](../examples/charts/gateway/) is the pattern
for Gateway API routing at scale: deploy it once per cluster (it renders a
`Gateway` with wildcard listeners, an optional cert-manager wildcard
certificate, and an HTTP→HTTPS redirect), point wildcard DNS at it, and
configure it as a routing profile. App charts then attach HTTPRoutes to it
through the `((platform.externalGateway*))` tokens — no app ever hardcodes
the edge.

## When to add a `template.yaml`

A plain chart covers the whole lifecycle: envs, previews, promotion,
rollback, per-component variables. Author a `template.yaml`
([reference](templates.md)) only when you want to ship curated metadata
*with* the chart: platform-authored default/per-env values overlays,
a declared developer-values projection, or image slots for CD wiring —
things an org can otherwise also layer on from the UI without touching the
chart repo.
