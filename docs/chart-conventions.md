# suparShip Chart Conventions

This document is the contract for authors of suparShip *template charts*
(those under `templates/<name>/chart/`). It is enforced through the
shared library chart at `charts/lib/suparship-common/`. New templates
should depend on the library and follow the conventions below; existing
templates have been migrated and serve as worked examples.

## Source of truth

The canonical Helm-values schema is defined by the publisher in Go:
[`internal/helmvalues/values.go`](../internal/helmvalues/values.go).
At publish time, every chart receives an override layer matching that
struct. Charts MUST read the canonical fields under those exact paths;
chart-defined extensions live alongside but never replace them.

```
app:                         # publisher-injected
  name: <app-name>
  env: <env-name>            # promotion stage (staging, prod, pr-42, …)

components:                  # publisher-injected, keyed by component name
  <name>:
    enabled: true            # disabled components must render zero resources
    image:
      repository: ...
      tag: ...
    replicas: 2
    expose: true             # only the routing component renders an Ingress
    port: 8080               # 0 means "use chart default"
    healthCheck: { path: / }
    env: { KEY: value }      # last-write-wins over envFrom
    resources: { size: small }
    ingress:                 # nil unless this is the routing component
      className: nginx
      clusterIssuer: ...     # empty → plain HTTP

routing:                     # publisher-injected
  host: <hostname>            # empty when no expose mode is set
  component: web              # name of the routing component

suparship:                   # publisher-injected
  envFromConfigMaps: [ ... ]
  envFromSecrets:    [ ... ]
```

Chart-specific values (autoscaling, pdb, gateway, lifecycle, podLabels,
…) belong on top-level keys and are merged onto the publisher's output
by Helm's natural value-merge order.

## The library chart

`charts/lib/suparship-common/` is a Helm `type: library` chart. It
exposes the helpers below. Every template chart depends on it:

```yaml
# templates/<name>/chart/Chart.yaml
dependencies:
  - name: suparship-common
    version: "0.1.0"
    repository: "file://../../../charts/lib/suparship-common"
```

Run `helm dep update <chart-dir>` after changing the dependency list;
commit the resulting `Chart.lock` and `charts/suparship-common-*.tgz`
so the publisher can copy a self-contained chart into the GitOps repo.

### Helpers

| Helper | Returns | When to use |
| --- | --- | --- |
| `suparship-common.fullname` | `.Values.app.name` falling back to `.Release.Name`, trimmed to 63 chars | Resource name for single-component charts; base name for multi-component charts |
| `suparship-common.chart` | `<chart-name>-<chart-version>` for the `helm.sh/chart` label | Use inside label sets — most charts get this for free via the label helpers |
| `suparship-common.standardLabels` | Full standard label set (name, instance, version, managed-by, helm.sh/chart, suparship.io/env) | Single-component charts |
| `suparship-common.commonLabels` | Cross-component labels only (instance, managed-by, helm.sh/chart, suparship.io/env). Caller attaches `app.kubernetes.io/name` per component. | Multi-component charts (e.g. voiceai-agent) |
| `suparship-common.standardSelectorLabels` | Just `app.kubernetes.io/name: <fullname>` | Stable Deployment selectors. K8s selectors are immutable post-creation, so this is intentionally minimal |
| `suparship-common.serviceAccountName` | `.Values.serviceAccount.name` falling back to fullname | All charts that create a ServiceAccount |
| `suparship-common.resources` | CPU + memory `requests` / `limits` for a `size` preset | Stateless workloads. Pass `"small"`, `"medium"`, or `"large"` (or a dict `{size: …}`) |
| `suparship-common.componentResources` | Reads `.Values.components.<component>.resources.size` and renders the preset | Single-component charts; thin wrapper. Caller passes `(dict "root" . "component" "web")` |
| `suparship-common.envFrom` | Gated `envFrom:` block from `.Values.suparship.envFromConfigMaps/Secrets`. Suppresses the entire key when both lists are empty | All charts that consume the publisher's hierarchy |
| `suparship-common.reloaderAnnotation` | `reloader.stakater.com/auto: "true"` when `.Values.reloader` is truthy; nothing otherwise | All charts that opt into Stakater Reloader |

## Naming

- **Single-component charts** name their workload after the fullname:
  `{{ include "<chart>.fullname" . }}`. Service / PDB / ScaledObject /
  Ingress / HTTPRoute all share the same base name, with optional
  suffixes (`-pdb`, `-scaler`).
- **Multi-component charts** prefix per-component resources so
  collisions are impossible. The pattern: `<chart-prefix>-<component>-<app>`.
  voiceai-agent, for example, ships
  - `voiceai-livekit-agent-server-<app>` (livekit-agent component)
  - `voiceai-livekit-cm-<app>`            (capacity-manager component)
  Define these as chart-local helpers (`<chart>.<component>.name`)
  layered on top of `suparship-common.fullname`.

## Labels

All charts emit the `app.kubernetes.io/*` set plus `helm.sh/chart` and
`suparship.io/env` (when `.Values.app.env` is non-empty). Label keys
are stable; values follow these rules:

| Label | Value |
| --- | --- |
| `app.kubernetes.io/name` | fullname (single-component) or per-component name (multi-component) |
| `app.kubernetes.io/instance` | `.Release.Name` |
| `app.kubernetes.io/version` | `.Values.components.web.image.tag` falling back to chart appVersion |
| `app.kubernetes.io/managed-by` | `.Release.Service` (typically `Helm`) |
| `helm.sh/chart` | `<chart-name>-<chart-version>` |
| `suparship.io/env` | `.Values.app.env`, omitted when empty |

### Selector labels

Selector labels are minimal: only `app.kubernetes.io/name` (single-
component) or `app: <component-name>` (multi-component, kustomize-compat
exception). Selectors are immutable after creation, so anything that
changes with renames (instance), version bumps (version), or chart
upgrades (chart) MUST NOT appear in selectors.

### Per-component label

Every Pod, Deployment / StatefulSet / CronJob (and their owned
resources) MUST carry `suparship.io/component: <component-name>`.
Single-component charts get this for free via `componentLabels`;
multi-component charts include it explicitly via the
`suparship-common.componentNameLabel` helper, on **both** the
Deployment metadata labels and the pod template labels. The pod-side
copy is what `kubectl logs -l suparship.io/component=<name>` and the
addons-health resource walker rely on.

NOT in selectors (it stays in metadata only) so the label can change
in the future without forcing a Deployment recreate.

> **Documented exception — voiceai-agent.** Each component uses
> `app: <component-name>` instead of `app.kubernetes.io/name`. This
> matches the prior kustomize manifests so an in-place migration from
> kustomize → suparShip does not force a Deployment recreate. This
> exception is grandfathered, not a pattern to copy: new charts use
> `app.kubernetes.io/name`.

## Autoscaling

KEDA `ScaledObject` is the standard. New charts MUST NOT use HPA v2 —
the operational story is unified across the platform. Pattern:

```yaml
{{- if .Values.autoscaling.enabled }}
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
spec:
  scaleTargetRef: { kind: Deployment, name: <fullname> }
  minReplicaCount: ...
  maxReplicaCount: ...
  triggers:
    {{- if .Values.autoscaling.triggers }}
    {{- toYaml .Values.autoscaling.triggers | nindent 4 }}
    {{- else }}
    - type: cpu
      metricType: Utilization
      metadata: { value: "{{ .Values.autoscaling.cpuTarget }}" }
    - type: memory
      metricType: Utilization
      metadata: { value: "{{ .Values.autoscaling.memoryTarget }}" }
    {{- end }}
  advanced:
    horizontalPodAutoscalerConfig: { behavior: ... }
{{- end }}
```

Defaults: cpu+memory utilization triggers built from
`autoscaling.cpuTarget` / `memoryTarget`. Templates with workload-
specific scaling (cron + queue-activation, etc.) ship those as the
chart's `autoscaling.triggers` default; users override when needed.

## envFrom (publisher hierarchy)

All charts MUST gate the `envFrom:` block via
`suparship-common.envFrom`. Empty lists must produce no `envFrom:`
key — kubectl rejects the manifest otherwise.

```yaml
        - name: app
          ...
          {{- include "suparship-common.envFrom" . | nindent 10 }}
          env: ...
```

The publisher writes the full precedence-ordered hierarchy
(`org → env-type → project → app → app-env → cluster`) into
`.Values.suparship.envFromConfigMaps[]` and `envFromSecrets[]`. Charts
do no name construction; the order is preserved.

Direct `env:` entries (from `components.<name>.env` or chart-injected
defaults like OTEL_SERVICE_NAME) override every level above per
Kubernetes envFrom semantics.

## Reloader

Standard Stakater Reloader integration. Annotation is gated on a
top-level `reloader` value; charts MUST NOT hardcode the annotation:

```yaml
metadata:
  annotations:
    {{- include "suparship-common.reloaderAnnotation" . | nindent 4 }}
```

Default `reloader: "true"` in chart `values.yaml`. Charts targeting
clusters without Reloader set `reloader: ""` per-app or per-env.

## Resource size presets

`small`, `medium`, `large` map to:

| Size | requests.cpu | requests.memory | limits.cpu | limits.memory |
| --- | --- | --- | --- | --- |
| small  |  50m | 128Mi |  250m | 256Mi |
| medium | 100m | 256Mi |  500m | 512Mi |
| large  | 250m | 512Mi | 1000m | 1Gi |

Default chart values use `small`. Heavier workloads override per app.

> **Documented exception — voiceai-agent's livekit-agent.** Uses a
> per-chart preset with `xlarge` and request==limit memory to avoid
> node-level OOM on stateful long-running call sessions. The chart
> declares its own resources helper rather than `componentResources`.
> Capacity-manager keeps an explicit `requests/limits` block for the
> same reason. New stateful workloads should follow this pattern.

## Workload conventions

- **Service account**: `serviceAccount.create: true` by default; named
  after the chart's fullname.
- **imagePullPolicy**: `IfNotPresent` for stateless web/worker
  components; `Always` only when image tags are mutable (`latest` or
  similar).
- **Probes**: liveness + readiness with sensible per-chart defaults;
  expose `.Values.probes.*` for tunability when probes vary across
  workloads.
- **Strategy**: `RollingUpdate` with `maxSurge: 1, maxUnavailable: 0`
  for web; `maxUnavailable: 0` for inbound-style "must-not-drop"
  workloads.
- **Lifecycle preStop**: sleep N seconds so the routing fabric stops
  sending traffic before the process exits. Default 15s for web;
  longer for stateful workloads.

## Multi-component charts

Multi-component charts (workloads with two or more Deployments / Jobs)
follow these rules:

- One file per component under `chart/templates/`:
  `<component>-deployment.yaml`, `<component>-pdb.yaml`,
  `<component>-scaledobject.yaml`. Avoid range-over-components: per-
  component knobs scale poorly within a single template.
- Each component's manifests are gated on
  `.Values.components.<component>.enabled` (publisher writes this).
  Domain rules that force a component off (e.g. inbound never has
  capacity-manager) live in chart helpers, not in user-facing inputs:

  ```
  {{- define "voiceai-agent.capacityManagerEnabled" -}}
  {{- $cm := index .Values.components "capacity-manager" -}}
  {{- if and $cm.enabled (ne .Values.agent.type "inbound") -}}true{{- end -}}
  {{- end }}
  ```
- Chart-specific helpers are named `<chart>.<component>.<aspect>`:
  `voiceai-agent.livekitAgent.name`, `voiceai-agent.livekitAgent.selectorLabels`,
  `voiceai-agent.livekitAgent.resources`, `voiceai-agent.livekitAgent.defaultTriggers`.

## Why a library chart

Pre-migration, each of the four template charts re-implemented the
same fullname / labels / selectorLabels / resources / envFrom /
reloader logic — about fifty lines per chart. The drift that
accumulated (different label orderings, different envFrom gating,
HPA-vs-KEDA, default-size differences) was mechanical, not deliberate.
The library chart eliminates duplication and locks the contract in
code. Drift in future templates fails review by being visibly
out-of-pattern rather than blending in.
