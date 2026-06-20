# suparship Roadmap

This document tracks the plan to complete the suparship MVP and add the platform capabilities needed for internal company use.

---

## Current State

### Already Built

- Go HTTP API: auth, org/project/env RBAC, app CRUD, previews, logs, sync, onboarding
- ArgoCD-oriented GitOps publisher (clone → commit → push into Gitea)
- React UI with all MVP route shells (dashboard, apps, templates, previews, settings)
- `web-service` Helm template with a single `web` component
- Dev cluster tooling: kind + ArgoCD + Gitea + seed via Taskfile
- Fake/in-memory dev mode for local iteration without a cluster

### Gaps Before Company Use

- **Kargo**: narrative and comments only — no install, no API integration
- **Promotions**: API handlers exist but do not trigger real stage promotions
- **ExternalSecrets Operator**: not installed or integrated
- **Env/secret hierarchy**: no domain model, no API
- **Worker templates**: only `web-service` exists; no worker or cron built-ins
- **No GitHub Actions CI**: Makefile/Taskfile only
- **`readyz`**: returns a static string, no real health probing

---

## Phase 1: MVP Hardening

> Foundation that must exist before company workloads can run reliably.

### 1.1 Kargo Integration

Wire real GitOps-native promotions using Kargo.

- Add `hack/install-kargo.sh` and `dev:cluster:kargo` Taskfile task
- Create `internal/gitops/kargo.go` — generate `Warehouse` and `Stage` CRs alongside ArgoCD `Application` manifests
- Create `internal/kube/kargo_store.go` — read Kargo `Promotion` and `Stage` objects via dynamic client
- Update `internal/server/promote.go` — replace stub with real Kargo `Promotion` creation

### 1.2 Health Checks and CI

- Make `GET /readyz` verify ArgoCD API reachability and GitOps repo connectivity
- Add `.github/workflows/ci.yaml`:
  - `go test -race ./...`
  - `golangci-lint`
  - `trufflehog` secret scan (required by security policy)

**Deliverable:** a cluster with Kargo installed, promotions that flow through Kargo stages, and CI that gates every PR.

---

## Phase 2: Hierarchical Env Vars and Secrets ✅ (secrets layer complete)

> Highest-priority company requirement. Enables teams to manage config and secrets at any scope.

### Precedence model

Config is merged bottom-up; lower scopes win:

```
org  →  environment-type  →  project  →  app  →  app-environment
```

Each scope stores two kinds of entries:
- **Plain env vars** — key/value pairs, stored in ConfigMaps
- **Secrets** — written to external vault (1Password, etc.) via `VaultWriter`; materialised by ESO

### Secrets layer (done)

The five-level secret hierarchy is fully implemented. See [docs/secrets.md](docs/secrets.md) for the full architecture.

Key capabilities delivered:
- `VaultWriter` interface with K8s demo backend and `MemVaultWriter` for tests
- `VaultBinding` + `IsolationMode` (hard/soft) in `BackendConfig` for SOC2-grade env isolation
- Vendor-neutral `ResourceNaming` patterns (configurable via `OrgSpec`)
- Collapsed `ExternalSecret` model — one per app-env namespace, `dataFrom` merges all scopes
- RBAC-gated API endpoints for five-level secret CRUD
- Optimistic concurrency via `If-Match` / `ETag` headers
- Structured audit logging (keys only, never values)
- Force-sync endpoint to trigger immediate ESO refresh
- CLI: `suparship secrets backend set/binding add/test`, `suparship secrets token import`
- UI: full OrgSettings backend config panel with isolation toggle, bindings table, token import, test
- GitOps publisher for `ClusterSecretStore` and collapsed `ExternalSecret` YAML

### Remaining work

- **Plain env vars**: ConfigMap-based env vars at each scope (API + UI + GitOps output)
- **Merged view**: `/merged` endpoint returning fully resolved set for a target environment
- 1Password Connect `VaultWriter` implementation (interface exists; concrete writer is next)

**Deliverable:** teams can manage secrets at any scope via UI or CLI, with values written directly to the external vault and ESO pulling them into the cluster. Plain env vars are the remaining follow-up.

---

## Phase 3: VoiceAI Worker Templates

> Two purpose-built templates for the company's voice AI workloads.

### Generic `worker` Helm chart base

`templates/worker/chart/` — derived from `web-service` but:
- No `Ingress` by default
- `Service` is optional
- Adds configurable `command`, `args`, and `env` overrides
- Optional `HPA`

### Template: `voiceai-capacity-manager`

Path: `templates/voiceai-capacity-manager/template.yaml`
Category: `voiceai`

Inputs:
- `image`, `replicas`
- `livekit_url` (string)
- `livekit_api_key` (secret ref)
- `livekit_api_secret` (secret ref)
- `max_participants_per_agent` (number)
- `scale_up_threshold`, `scale_down_threshold` (number)
- `log_level` (enum: `debug` / `info` / `warn` / `error`)

### Template: `voiceai-livekit-agent`

Path: `templates/voiceai-livekit-agent/template.yaml`
Category: `voiceai`

Inputs:
- `image`, `replicas`
- `livekit_url`, `livekit_api_key` (secret ref), `livekit_api_secret` (secret ref)
- `agent_name` (string)
- `room_name` (string, optional)
- `model` (enum: `gpt-4o` / `gpt-4o-mini` / `claude-3-5-sonnet`)
- `tts_provider` (enum: `deepgram` / `elevenlabs`)
- `tts_api_key` (secret ref)

**Deliverable:** a voice AI workload can be deployed from the suparship UI by filling in a form — no raw YAML editing.

---

## Phase 4: Release Trains

> Deploy multiple variants of an app into the same environment simultaneously (e.g., stable + canary).

### Concept

A **release train** is a named variant of an app-environment pair with its own image tag, replica count, and traffic weight. Multiple trains coexist in the same namespace.

```
hello / staging
  ├── stable   (image: v1.2.0,  weight: 90)
  └── canary   (image: v1.3.0-rc1, weight: 10)
```

### Domain model addition

```go
// internal/domain/releasetrain.go
type ReleaseTrain struct {
    Name          string
    Image         string
    Tag           string
    Replicas      int
    TrafficWeight int               // 0–100, sum across all trains must equal 100
    EnvOverrides  map[string]string
}
```

`AppEnvironment` gains a `[]ReleaseTrain` field.

### API

```
GET/POST        /api/v1/projects/:project/apps/:app/environments/:env/trains
GET/PUT/DELETE  /api/v1/projects/:project/apps/:app/environments/:env/trains/:train
```

### GitOps output

Each train → one ArgoCD `Application`:
- Name pattern: `{app}-{env}-{train}` (e.g., `hello-staging-stable`, `hello-staging-canary`)
- Path pattern: `gitops-output/envs/{env}/{app}/trains/{train}/`

### Key new files

- `internal/domain/releasetrain.go`
- `internal/gitops/argocd.go` — add `BuildTrainApplication(app, env, train)`
- `internal/server/train_handler.go`
- `internal/kube/train_store.go`

**Deliverable:** teams can ship two versions of an app side by side and control traffic split between them.

---

## Phase 5: Envoy Gateway Traffic Routing

> Weighted traffic splitting across release trains using the Kubernetes Gateway API.

### Install

`hack/install-envoy-gateway.sh` installs Envoy Gateway via Helm and creates a shared `GatewayClass` + `Gateway` in `suparship-system`.

### Generated resources (per app-environment)

One `HTTPRoute` pointing to each train's `Service`, weighted by `TrafficWeight`:

```yaml
# gitops-output/envs/staging/hello/httproute.yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: hello-staging
  namespace: hello-staging
spec:
  parentRefs:
    - name: suparship-gateway
      namespace: suparship-system
  hostnames: ["hello.staging.example.com"]
  rules:
    - backendRefs:
        - name: hello-staging-stable
          weight: 90
        - name: hello-staging-canary
          weight: 10
```

Backend validation enforces that weights sum to 100 before committing to Git.

### Traffic split API

```
PUT /api/v1/projects/:project/apps/:app/environments/:env/traffic
Body: { "trains": [{ "name": "stable", "weight": 90 }, { "name": "canary", "weight": 10 }] }
```

Publisher regenerates the `HTTPRoute` on any weight change.

### Key new files

- `internal/gitops/envoygateway.go`
- `internal/server/traffic_handler.go`
- `hack/install-envoy-gateway.sh`

**Deliverable:** changing traffic weights in the UI triggers a deterministic Git commit that Envoy Gateway reconciles within seconds.

---

## Phase 6: Promotion Pipeline (Kargo)

> Safe, auditable promotion from staging to production with auto or manual gates.

### Kargo topology per app

```
Warehouse  (watches image registry for new tags)
    │
    ▼
Stage: staging   (promotionPolicy: auto)
    │
    ▼
Stage: prod      (promotionPolicy: manual — requires explicit approval)
```

### Promotion modes

Configured per environment in `app.yaml`:

```yaml
environments:
  staging:
    promotionPolicy: auto
  prod:
    promotionPolicy: manual
```

Kargo `Stage` CRs are generated with `promotionMechanisms` matching this policy.

### API additions

```
POST /api/v1/projects/:project/apps/:app/promote
     Body: { "from": "staging", "to": "prod", "train": "stable" }

GET  /api/v1/projects/:project/apps/:app/promotions
     → list Kargo Promotion objects with status

POST /api/v1/projects/:project/apps/:app/promotions/:id/approve
     → approve a pending manual promotion gate
```

### GitOps output

Kargo CRs generated under `gitops-output/_infra/kargo/{app}/`:
- `warehouse.yaml`
- `stage-staging.yaml`
- `stage-prod.yaml`

**Deliverable:** a new image tag flows automatically to staging; a developer clicks "Approve" in the UI to promote to production. Full audit trail in Git.

---

## Phase 7: Managed Addons UI

Backend for managed addons (databases, caches, queues) is shipped: the
addon catalog API (`/api/v1/org/addon-profiles`), per-env override,
`AppSpec.Addons` in the app create/update endpoints, the valkey
wrapper template with the Bitnami subchart, and the connection-contract
validator. The system is fully usable end-to-end via curl.

This phase delivers the UI surfaces that turn that into a self-service
experience.

### Org settings — Addons admin panel

Mirror the existing Routing Profiles admin section. Operators
configure which wrapper chart + provider serves each addon type.

```
┌─ Addon Profiles ─────────────────────────────────┐
│  Type      Provider           Chart              │
│  redis     bitnami-valkey     valkey      [⋯]    │
│  postgres  cloudnative-pg     cnpg-cluster [⋯]   │
│                                                  │
│  + Add addon profile                             │
└──────────────────────────────────────────────────┘
```

- Type dropdown sourced from the API's `availableTypes` (closed set
  derived from registered connection contracts in
  `internal/addons/contracts/`).
- Per-environment override entries nested under each environment on
  the existing Environments page.
- Effort: 1–2 days.

### App creation — Addons step

In New App / Edit App, after Components, an Addons section. Devs see
only the type they can claim — types the org hasn't configured don't
appear.

```
┌─ Addons (optional) ──────────────────────────────┐
│  cache    [redis ▼]    [small ▼]      [✕]       │
│  primary  [postgres ▼] [medium ▼] v16 [✕]       │
│                                                  │
│  + Add addon                                     │
└──────────────────────────────────────────────────┘
```

- Type dropdown filters to types with a configured AddonProfile.
- Submitted via the existing `POST /api/v1/projects/{project}/apps`
  body (`addons` field already accepted).
- Effort: 1 day.

### App detail — Addons display + live status

Sibling section to Components on the app detail page. Shows the
claim, the resolved provider per env, the connection-Secret name,
and (eventually) live status from ArgoCD's resource-tree API.

```
┌─ Addons ─────────────────────────────────────────┐
│  cache  · redis · provider=bitnami-valkey        │
│         Secret: hello-addon-cache-conn           │
│         Status: ● Healthy (via ArgoCD)           │
│         Pod logs:  kubectl logs -l               │
│                    suparship.io/addon=cache      │
└──────────────────────────────────────────────────┘
```

- Basic version reads from `AppDetailDTO.addons` (already
  surfaced).
- Live-status piece pulls from ArgoCD's resource-tree API —
  separate integration track that also benefits Components view.
- Per-component / per-addon labels (`suparship.io/component`,
  `suparship.io/addon-type`) already shipped as the selector hooks.
- Effort: 1 day basic, ~3+ days with live status.

### Capability-driven form generation (the unfinished tail of Phase 5/UI work)

Today the UI hardcodes "web has autoscaling sliders, cron has
schedule input" inside React. The capability data we ship at
`GET /api/v1/templates/{name}` (`components[].capabilities`) lets
the form render input groups dynamically:

```ts
template.components.forEach(c => {
  if (c.capabilities.autoscaling !== "none") renderScalingInputs(c);
  if (c.capabilities.pdb)                    renderPDBInputs(c);
  if (c.capabilities.expose)                 renderExposeToggle(c);
  // ...
});
```

Adding a 6th template becomes "free" UI-wise — the form generates
from metadata. Without it, every new template requires a hand-written
form component.

- Effort: 2–3 days.

### Total scope and trade-off

About a week of focused frontend work for parity with the API.
**Without it**: addons are real, backend works, but app developers
and operators use them via curl + YAML editing only — workable for
internal tools, friction-tax once there are multiple users.

**Sequencing note**: this is naturally a parallel track. The
toolchain is React/TypeScript/Vite, not Go — a frontend-leaning
contributor can move on this independently of the backend roadmap.
Backend follow-ups for the addon arc (postgres-via-CloudNativePG
provider as Phase 7's sibling) deliver more raw infrastructure
value per hour for a Go-leaning contributor.

**Deliverable:** an operator configures the org's addon catalog
through the Settings page, an app developer claims `cache: redis`
through the New App form, and the app detail page shows the running
valkey instance with its connection details — no curl required.

---

## Architecture Decisions

- **ExternalSecrets backend**: suparShip writes secret values to the external vault (1Password, Vault, AWS-SM) via the `VaultWriter` interface. ESO pulls them into the cluster via generated `ExternalSecret` CRs. Git never holds secret values — only `ClusterSecretStore` and `ExternalSecret` manifests. See [docs/secrets.md](docs/secrets.md).
- **Isolation modes**: `hard` = one `ClusterSecretStore` + token per environment (SOC2); `soft` = single store (demo/POC). Configurable via `suparship secrets backend set --isolation=hard|soft`.
- **Vendor-neutral naming**: Resources outside `suparship-system` use configurable patterns (default: `{app}`) tracked by `app.kubernetes.io/managed-by: suparship` label rather than name prefix. Users can manage apps via plain GitOps without suparShip.
- **Release train naming**: always `{app}-{env}-{train}` — deterministic, never includes timestamps.
- **Traffic weight validation**: weights must sum to 100; enforced in the API before any GitOps write.
- **Promotion scope**: a promotion moves a coherent `(app, train, image-tag)` tuple — never individual components.
- **Secret values in Git**: prohibited by policy. Only `ref:` strings are committed.

---

## Milestone Summary

| Phase | Theme | Unlocks |
|---|---|---|
| 1 | MVP Hardening | Real promotions, CI gating |
| 2 | Env/Secret Hierarchy | Company-safe config management |
| 3 | VoiceAI Templates | Voice AI workload self-service |
| 4 | Release Trains | Multi-variant deployments |
| 5 | Envoy Gateway Routing | Traffic splitting across trains |
| 6 | Full Kargo Pipeline | Auto + manual promotion gates |
| 7 | Managed Addons UI | Self-service addon claims (backend already shipped) |

---

## Stacks (grouping tightly-coupled apps in a project)

A stack groups apps that ship/scale/route together (e.g. voiceai = web +
agents), with a shared override layer, optional shared namespace, batch
lifecycle, and clone. Full design + phase plan: **[docs/stacks.md](docs/stacks.md)**.

- **Phase 1 — Grouping + override cascade (env/values/secrets) + membership + UI** — ✅ shipped
- **Phase 2 — Shared stack namespace + intra-stack DNS** — ✅ shipped
- **Phase 3 — Batch lifecycle (deploy/promote/preview/delete the whole stack)** — ✅ shipped
- **Phase 4 — Clone stack with overrides (e.g. livekit-cloud vs self-hosted)** — planned

Decoupled / future: reusable stack blueprints; project/stack-scope gateway
routing and cross-app canary (depend on Phase 5 Gateway API above).
