# Bring your own Helm charts

suparship does not require charts to know anything about suparship. You can
deploy the Helm charts you already have — unmodified — and do all of the
platform wiring from the outside: in the app's values overlay and in the UI.
This is how we run suparship in production, and it is the recommended starting
point; authoring [templates](templates.md) is an optional layer you can add
later, not an entry fee.

Three things make this work:

1. **Chart sources** — point suparship at a directory of plain charts (git or
   OCI); each chart is imported as a *passthrough* template automatically.
2. **`((platform.*))` tokens** — put them in an app's values overlay and the
   publisher resolves them per environment at publish time. The chart just
   sees ordinary strings.
3. **UI-authored developer values** — map the handful of chart paths a
   developer should see onto a form, per template, from the UI. No chart or
   YAML authoring involved.

A set of production-ready starting points lives in
[`examples/charts/`](../examples/charts/): `web` (Ingress or Gateway API),
`worker`, `cronjob`, and a standalone `gateway` edge chart. They are plain
Helm — installable with `helm install` on any cluster — and double as the
reference for the conventions below.

## Registering a chart source

**Templates → Sources → Add source**, type **Git charts repo** (`gitcharts`):

| Field | Meaning | Default |
| --- | --- | --- |
| Repo URL | git clone URL of the repo holding your charts | — |
| Ref | branch / tag / commit | `main` |
| Path | subdirectory scanned for charts | `charts` |
| Credentials | token or username/password for private repos | anonymous |

Every directory under *Path* containing a `Chart.yaml` is imported as a
template. Charts without a bundled `template.yaml` (the normal case) come in
as **passthrough**: suparship publishes *only your overlay values plus
resolved tokens* — it never injects its own values schema into your chart.
Sources re-sync on an interval (`SUPARSHIP_TEMPLATE_SYNC_INTERVAL`, default
5m), so pushing a chart change to the repo rolls it out to new publishes.

> **Template names are global.** The imported template is named after the
> chart (`Chart.yaml` `name`), across *all* sources and built-ins. A chart
> whose name is already provided by a different source or by a built-in
> template is **refused at sync** — the source's sync result names the owner
> ("rename the chart or remove the conflicting template") while the rest of
> the repo imports normally. Give your charts names that are unique across
> everything you register (e.g. the built-in templates already claim
> `worker` and `cronjob`).

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

Commonly used tokens (see `internal/helmvalues/values.go` `PlatformContext`
for the full list):

| Token | Resolves to |
| --- | --- |
| `((platform.configMapName))` / `((platform.secretName))` | the platform-managed env ConfigMap / Secret for this app instance (preview-suffixed in previews) |
| `((platform.routingHost))` | the resolved external host for this env, e.g. `myapp.staging.acme.com` |
| `((platform.ingressClassName))` / `((platform.clusterIssuer))` | the routing profile's IngressClass / cert-manager issuer |
| `((platform.externalGatewayName/Namespace/SectionName))` (+ `internal…`) | the Gateway API parentRef of the resolved routing profile |
| `((platform.env))` / `((platform.envType))` | environment name / classification (`staging`, `prod`, `preview`) |
| `((platform.namespace))` / `((platform.cluster))` | target namespace / cluster |
| `((platform.imageTag))` | the app's resolved image tag (pin sidecars to the same build) |
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

## When to graduate to a template

Passthrough charts cover the whole lifecycle: envs, previews, promotion,
rollback, per-component variables. Author a `template.yaml`
([reference](templates.md)) only when you want the extra layer it buys:
org-curated presets and inputs, canonical values injection for
`suparship-common` charts, or per-env template defaults maintained by a
platform team.
