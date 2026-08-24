# Example charts

Production-ready, **plain Helm charts** — the way we run suparship in
production. None of them depend on suparship: no library chart, no
`platform.*` keys in their values, no `template.yaml`. They install with
`helm install` on any cluster and could live in your own repo unchanged.

That is the point. suparship adapts to your charts, not the other way
around:

- **Developer values** are mapped from the UI (Template → Developer
  values): pick the chart paths a developer should see (e.g. a "Port"
  field writing both `containerPort` and `service.port`), and app pages
  render a form instead of raw YAML. No chart changes needed.
- **Platform wiring** happens in the app's *values overlay*, using
  `((platform.*))` tokens resolved at publish time — the chart just sees
  ordinary strings:

  ```yaml
  # app values overlay (set once per app or per env in the suparship UI)
  envFrom:
    configMaps: ["((platform.configMapName))"]   # app/env variables
    secrets: ["((platform.secretName))"]         # app/env secrets
  ingress:
    host: ((platform.routingHost))
    className: ((platform.ingressClassName))
    tls:
      clusterIssuer: ((platform.clusterIssuer))
  # or, Gateway API instead of Ingress:
  httpRoute:
    hostnames: ["((platform.routingHost))"]
    parentRefs:
      - name: ((platform.externalGatewayName))
        namespace: ((platform.externalGatewayNamespace))
        sectionName: ((platform.externalGatewaySectionName))
  ```

- **CD image updates** bind to whatever path holds the tag
  (`image.tag` here) via the app's image settings.

## Charts

| Chart | What it deploys | Production defaults |
| --- | --- | --- |
| [`web`](./web) | Deployment + Service, optional Ingress **or** Gateway API HTTPRoute | 2 replicas, zero-downtime rollout (surge/0), PDB, HPA, probes, non-root, preStop drain |
| [`worker`](./worker) | Headless Deployment (queue consumers, background processors) | PDB, optional HPA, long termination grace for in-flight work, non-root |
| [`cronjob`](./cronjob) | CronJob | `Forbid` concurrency, starting deadline, bounded history, non-root |
| [`job`](./job) | One-shot release-gating Job (DB migrations, seed scripts) | ArgoCD `PreSync` hook re-run on every sync, `backoffLimit: 0` so a failed run gates the release, non-root |
| [`gateway`](./gateway) | Standalone edge routing: Gateway API `Gateway`, optional wildcard Certificate, HTTP→HTTPS redirect, extra routes | cross-namespace routes allowed, cert-manager integration |
| [`postgres`](./postgres) | Single-instance PostgreSQL for dev/demo stacks (mark the component **Stateful**) | Recreate + RWO PVC (`resource-policy: keep`), `pg_isready` probes — not HA; bring an operator for production data |

The `web`, `worker`, `cronjob`, and `job` charts default to public demo
images so a fresh install renders something runnable; set
`image.repository`/`tag` to your own build.

Two more conventions the platform relies on:

- **Suspend**: `web`, `worker`, and `cronjob` honor a top-level
  `suspend: true` (the platform writes it when an environment is
  suspended) — Deployments scale to zero and their HPA is omitted;
  CronJobs set `spec.suspend`.
- **CI smoke values**: each chart carries `ci/platform-values.yaml`, the
  platform contract expressed as an app overlay with literal
  `((platform.*))` token strings. `task charts:verify` renders every
  chart with defaults and with these values and asserts the contract
  (tokens land in envFrom/ingress output, suspend scales to zero, the
  job hook annotations render).

## Using them with suparship

Register this directory as a chart source (Templates → Sources → Git
charts directory, path `examples/charts`) — each chart is indexed as a
template automatically — or copy the charts into your own repo and point
at that. Then create apps from them and do all mapping (developer values,
image bindings, platform tokens) from the UI.

The `gateway` chart is the shared edge: deploy it once per cluster (as a
regular suparship app or by hand), configure its Gateway as a routing
profile, and app HTTPRoutes attach to it via the
`((platform.externalGateway*))` tokens shown above.
