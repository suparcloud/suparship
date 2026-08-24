# suparship Chart Conventions

suparship deploys **plain Helm charts**. There is no library chart to depend
on, no values schema the platform injects, and no required labels or helpers:
if a chart installs with `helm install`, it works on suparship. The charts in
[`examples/charts/`](../examples/charts/) (`web`, `worker`, `cronjob`, `job`,
`gateway`, `postgres`) are the worked examples for everything below.

This page is the whole platform↔chart contract, plus the optional conventions
our example charts follow. For the operational side (registering charts,
wiring apps) see [byo-charts.md](byo-charts.md).

## The contract

Two things — both consumed from the app's **values overlay**, never from the
chart itself:

1. **`((platform.*))` / `((vars.KEY))` tokens.** Any string leaf in a values
   overlay may use them; the publisher resolves them per environment when it
   writes each env's `values.yaml`. The chart just sees ordinary strings.
   The full token catalog lives in
   [`internal/platform/interpolate.go`](../internal/platform/interpolate.go)
   (`PlatformTokens`).
2. **The platform-rendered env objects.** suparship renders the app's
   variables into a ConfigMap and its secrets into an ExternalSecret-backed
   Secret; the chart reaches them by name through
   `((platform.configMapName))` / `((platform.secretName))`. The names resolve
   per instance: the app-wide objects by default, preview-suffixed names in
   shared-namespace previews, or a component's own object when it extends/
   overrides variables (a merged ConfigMap) or curates a subset. For a
   component that opts out of app secrets, `((platform.secretName))` resolves
   to the empty string — no app secrets; drop the token from that component's
   overlay (or curate a per-component subset, which the token then points at).

The typical wiring, in an app overlay:

```yaml
# app values overlay — the paths are YOUR chart's; the tokens are suparship's
envFrom:
  configMaps: ["((platform.configMapName))"]  # app/env variables
  secrets: ["((platform.secretName))"]        # app/env secrets
ingress:
  enabled: true
  host: ((platform.routingHost))
  className: ((platform.ingressClassName))
  tls:
    clusterIssuer: ((platform.clusterIssuer))
```

Everything else — replicas, resources, autoscaling, probes, commands — is
ordinary chart values, set through the same overlay.

## Suspend

The platform's suspend/resume ops (per env, and stack-wide via fan-out) work
by writing a single boolean into the env's values. By convention that key is
top-level **`suspend`**:

```yaml
suspend: true   # written by the platform when an env is suspended; absent otherwise
```

A chart honors it by scaling its workloads to zero (or gating them) when
`.Values.suspend` is true — keeping the Deployment/StatefulSet and its
Service/PVCs in place so resume is instant and no data is lost (unlike
undeploy/prune). In the example charts, `web` and `worker` scale to 0 and
omit their HPA; `cronjob` maps it onto `CronJob.spec.suspend`.

A template can point suspend at a **different** values key (e.g. a
per-component toggle) by declaring it:

```yaml
# template.yaml
spec:
  features:
    suspend:
      valuesKey: components.web.suspend   # default when omitted: "suspend"
```

The platform only ever writes the key when suspended; on resume it writes
nothing and the chart default (running) applies. A chart that ignores the key
simply won't react — suspend is then a no-op for it.

## Optional conventions the example charts follow

None of these are required by the platform; they are production defaults
worth copying:

- **Non-root**: `runAsNonRoot`, a fixed non-zero UID, and a restricted
  securityContext, so charts pass restricted PodSecurity out of the box.
- **Probes**: liveness + readiness with sensible defaults, tunable via
  `.Values.probes.*`.
- **Zero-downtime rollout**: `RollingUpdate` with `maxSurge: 1,
  maxUnavailable: 0` for web workloads, plus a `preStop` sleep so the routing
  fabric drains before the process exits.
- **PodDisruptionBudget**: default-on with `maxUnavailable: 1`, so
  single-replica apps still drain during node maintenance.
- **Autoscaling**: a plain HPA gated on `.Values.autoscaling.enabled` —
  standard Kubernetes, no controller dependency. (Charts are free to use KEDA
  or anything else; the platform doesn't care.)
- **Release-gating jobs**: the `job` chart runs as an ArgoCD `PreSync` hook
  with `backoffLimit: 0`, so a failed migration gates the release.
- **CI smoke values**: each chart carries `ci/platform-values.yaml` — the
  platform contract expressed as an app overlay with literal `((platform.*))`
  token strings. `task charts:verify` (`hack/charts-verify.sh`) lints every
  chart, renders defaults plus these values, and asserts the contract: tokens
  land in envFrom/ingress output, `suspend: true` scales to zero, the job
  hook annotations render.

## See also

- [`examples/charts/README.md`](../examples/charts/README.md) — the example
  catalog and per-chart defaults.
- [byo-charts.md](byo-charts.md) — registering chart sources and wiring apps.
- [templates.md](templates.md) — the optional `template.yaml` metadata layer
  (default overlays, developer values, image slots).
