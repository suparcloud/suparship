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

## Phase 2: Hierarchical Env Vars and Secrets

> Highest-priority company requirement. Enables teams to manage config and secrets at any scope.

### Precedence model

Config is merged bottom-up; lower scopes win:

```
org  →  environment  →  project  →  app
```

Each scope stores two kinds of entries:
- **Plain env vars** — key/value pairs, stored in ConfigMaps
- **Secret refs** — `ref:secret-name.key` strings that become `ExternalSecret` CRs at deploy time

### New API surface

```
GET/POST/PUT/DELETE  /api/v1/org/envvars
GET/POST/PUT/DELETE  /api/v1/org/environments/:env/envvars
GET/POST/PUT/DELETE  /api/v1/projects/:project/envvars
GET/POST/PUT/DELETE  /api/v1/projects/:project/apps/:app/envvars
GET                  /api/v1/projects/:project/apps/:app/envvars/merged
```

The `/merged` endpoint returns the fully resolved set for a given target environment — useful for UI diff views and debugging.

### GitOps output (per app-environment)

- One `ConfigMap` containing all merged plain vars
- One `ExternalSecret` CR per secret ref (referencing the company's `ClusterSecretStore`)
- App Helm chart mounts both via `envFrom`

### Key new files

- `internal/domain/envvars.go` — `EnvVar`, `SecretRef`, `EnvVarSet` types and deterministic merge logic
- `internal/kube/envvar_store.go` — CRUD against ConfigMaps in `suparship-system`
- `internal/server/envvar_handler.go` — four-level HTTP handlers
- `internal/gitops/envvars.go` — generates `ConfigMap` + `ExternalSecret` manifests
- `internal/gitops/publisher.go` — updated to call envvar generation on every sync
- `hack/install-external-secrets.sh` — installs External Secrets Operator via Helm

**Deliverable:** teams can set `DATABASE_URL=ref:prod-db.url` at the org level and have it automatically available to every app, with ExternalSecret CRs managing actual secret resolution.

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

## Architecture Decisions

- **ExternalSecrets backend**: MVP generates `ExternalSecret` CRs referencing a `ClusterSecretStore` — the company configures which backend (AWS SSM, Vault, etc.). suparship never holds or transmits secret values.
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
