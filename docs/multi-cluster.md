# Multi-Cluster Support

suparship runs on a **tooling cluster** (alongside ArgoCD) and deploys apps to
one or more separate **workload clusters**. This document explains the cluster
registry, environment-to-cluster mapping, namespace naming rules, and the
GitOps repository layout that makes it all work.

---

## Topology

```
┌──────────────────────────────────────┐
│  Tooling Cluster                     │
│  ┌──────────┐  ┌───────┐  ┌───────┐ │
│  │suparship │  │ArgoCD │  │ Gitea │ │
│  └──────────┘  └───┬───┘  └───────┘ │
└────────────────────┼─────────────────┘
                     │ syncs gitops-output/
          ┌──────────┴───────────┐
          ▼                      ▼
 ┌─────────────────┐   ┌─────────────────┐
 │  Staging Cluster│   │  Prod Cluster   │
 │  ns: hello      │   │  ns: hello      │
 │  (or hello-stg) │   │  (or hello-prod)│
 └─────────────────┘   └─────────────────┘
```

- **suparship** writes GitOps manifests to Gitea and reads runtime status from ArgoCD.
- **ArgoCD** (on the tooling cluster) syncs the GitOps repo and deploys to whichever
  cluster each ApplicationSet targets.
- **Pod logs** are streamed by suparship directly from the workload clusters using
  stored kubeconfigs.
- **Sync / health status** is read from ArgoCD Application CRs on the tooling cluster —
  no direct workload-cluster access is needed for status.

---

## Cluster Registry

Each cluster is registered with suparship once. Registration stores:

| Object | Location | Content |
|--------|----------|---------|
| `ConfigMap suparship-cluster-{name}` | `suparship-system` | name, displayName, apiServer, status |
| `Secret suparship-cluster-kubeconfig-{name}` | `suparship-system` | raw kubeconfig (for log streaming) |
| `Secret cluster-{name}` (ArgoCD format) | `argocd` | server URL + credentials extracted from kubeconfig |

The ArgoCD cluster Secret is created automatically when a cluster is registered
so that ArgoCD can immediately deploy Applications to it.

Kubeconfigs are **never** written to Git. They live only in `suparship-system`
Secrets.

### Registering a cluster

```bash
# CLI
suparship cluster add \
  --name staging-cluster \
  --env staging \
  --kubeconfig ~/.kube/staging-config

# List registered clusters
suparship cluster list

# Remove a cluster
suparship cluster remove staging-cluster
```

Via the API:

```http
POST /api/v1/clusters
Content-Type: application/json

{
  "name": "staging-cluster",
  "displayName": "Staging",
  "env": "staging",
  "kubeconfig": "<base64-encoded kubeconfig>"
}
```

Requires `org_admin` role.

---

## Environment → Cluster Mapping

Each environment in a project carries a `clusterRef` pointing to a registered
cluster. One cluster can serve multiple environments (shared cluster), or each
environment can have its own dedicated cluster.

```yaml
# config/seed/org.yaml — environments are org-level; projects inherit them
environments:
  - name: staging
    displayName: Staging
    order: 1
    clusterRefs: [staging-cluster]  # registered cluster name(s)
    activeClusterRef: staging-cluster  # which one receives deploys
    baseDomain: staging.acme.com   # ingress base domain for this env
    namespacePattern: "{app}"      # see Namespace Patterns below

  - name: prod
    displayName: Production
    order: 2
    clusterRefs: [prod-cluster]
    activeClusterRef: prod-cluster
    baseDomain: prod.acme.com
    namespacePattern: "{app}"
```

`baseDomain` controls ingress hostnames. An app named `hello` in the staging env
produces the URL `http://hello.staging.acme.com`.

---

## Namespace Patterns

When each environment has a **dedicated cluster**, the cluster itself provides
isolation — app namespaces need only the app name (`hello`). When environments
**share a cluster**, namespaces must include an env discriminator to avoid
collisions (`hello-staging`, `hello-prod`).

The `namespacePattern` field on each environment controls this. Supported tokens:

| Token | Resolves to |
|-------|-------------|
| `{app}` | App name (required — must be present in every pattern) |
| `{env}` | Environment name |
| `{project}` | Project name |

### Examples

```yaml
# Dedicated cluster per env — clean, short namespaces
namespacePattern: "{app}"          # → "hello"

# Shared cluster — env discriminator required
namespacePattern: "{app}-{env}"    # → "hello-staging"

# Project-scoped namespaces
namespacePattern: "{project}-{app}" # → "demo-hello"
```

**Default** (when `namespacePattern` is omitted): `{app}-{env}` — safe for shared
clusters and backward-compatible with older suparship installations.

### What namespace patterns do NOT affect

- **Ingress hostnames** — always derived from `baseDomain`: `{app}.{baseDomain}`
- **Helm release name** — always `{app}` (Helm releases are namespace-scoped;
  no env suffix is needed even on a shared cluster)
- **Preview namespaces** — always `{app}-{previewName}` (the preview name provides
  uniqueness regardless of cluster sharing)

### Validation rules

- Pattern must contain `{app}` — enforced at env create/update time
- Resolved namespace must be a valid DNS label: ≤ 63 characters, lowercase
  alphanumeric and hyphens only

---

## Application Naming

Each app deploys as one ArgoCD `Application` **per cluster** in an env. The
Application name follows the org-wide `argoAppName` pattern (Settings →
Namespace Naming, or `resourceNaming.argoAppName` in org config):

| Token | Resolves to |
|-------|-------------|
| `{project}` | owning project |
| `{app}` | app name |
| `{env}` | environment name |
| `{cluster}` | destination cluster's registered name |

Default: `{project}-{app}-{cluster}`. Cluster names usually carry an env prefix
(e.g. `staging-eastus`), so the default omits `{env}` to avoid redundancy:

```
# Default — env carried by the cluster name
{project}-{app}-{cluster}   → billing-api-staging-eastus

# Explicit env segment (shared clusters across envs)
{project}-{app}-{env}-{cluster}   → billing-api-staging-staging-eastus
```

The pattern **must contain `{app}` and `{cluster}`** — `{app}` for uniqueness
across projects, `{cluster}` so the per-cluster Applications of a fan-out env
don't collide. The platform companion Application (the per-app ConfigMap +
ExternalSecret) is the workload name + `-platform`.

Naming is always per-cluster, even for a single-cluster env, so adding a second
cluster never renames the first cluster's Application (no ArgoCD recreate / no
lost sync history for the existing cluster). Promotion stays **env-level** (one
Kargo Stage per env); a promotion's `argocd-update` step refreshes every
per-cluster Application of that env. Status, deployment history, and diagnostics
resolve Applications by suparship identity labels (`suparship.io/project`,
`/app`, `/env`, `/cluster`), so they are independent of the configured pattern.

> Changing `argoAppName` renames existing Applications; ArgoCD deletes the old
> ones and recreates them on the next publish. See [upgrading.md](upgrading.md).

---

## GitOps Repository Layout

suparship writes to an env/cluster-centric layout. The top-level directory
corresponds to an environment; ArgoCD only needs one ApplicationSet per
environment to deploy all apps to the right cluster.

```
gitops-output/
  staging/
    appset.yaml                    # ApplicationSet for the staging cluster
    {project}/
      appproject.yaml              # ArgoCD AppProject (allowed: staging cluster)
      {app}/
        app.yaml                   # App metadata (name, project, template)
        values.yaml                # Helm values for staging
  prod/
    appset.yaml                    # ApplicationSet for the prod cluster
    {project}/
      appproject.yaml
      {app}/
        app.yaml
        values.yaml
  previews/
    appset.yaml                    # ApplicationSet for preview environments
    {project}/
      {app}-{preview}/
        app.yaml                   # Includes clusterServer + namespace for this preview
        values.yaml
```

### Why env/cluster-centric?

| Concern | Env-centric (this layout) | App-centric (alternative) |
|---------|--------------------------|--------------------------|
| Kargo Stage → Git directory | Natural 1:1 mapping | Cross-directory writes |
| `git log prod/` | Shows all prod changes | Requires grep across dirs |
| Adding a new cluster | New directory only | Regenerate all project AppSets |
| Prod RBAC / CODEOWNERS | Scope to `gitops-output/prod/` | Cannot scope by env |
| AppSet complexity | Git File generator (simple) | Matrix generator (complex) |

### `app.yaml` — app metadata

Written once per app per environment directory:

```yaml
name: hello
project: demo
template: web-service
```

The ApplicationSet's Git File generator reads these files to discover all apps
deployed in that environment.

### `values.yaml` — per-env Helm values

Serialized `HelmValues` struct (components, routing host, image, replicas). Written
by suparship when an app is created or updated. Routing host is derived using the
env's `baseDomain`.

### `appset.yaml` — ApplicationSet per env

One file per environment. The cluster API server is hardcoded here (not in per-app
files). Uses ArgoCD multi-source Applications (requires ArgoCD ≥ v2.6; pinned at
v2.9.3):

```yaml
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: staging
  namespace: argocd
spec:
  generators:
  - git:
      repoURL: "http://gitea.svc/gitops/gitops.git"
      revision: HEAD
      files:
      - path: "gitops-output/staging/*/*/app.yaml"
  template:
    metadata:
      name: "{{name}}-staging"
    spec:
      project: "{{project}}"
      sources:
      # Source 1: values directory (ref only — no chart here)
      - repoURL: "http://gitea.svc/gitops/gitops.git"
        path: "gitops-output/staging/{{project}}/{{name}}"
        targetRevision: HEAD
        ref: appvalues
      # Source 2: Helm chart, values loaded from source 1 via $appvalues ref
      - repoURL: "http://gitea.svc/gitops/gitops.git"
        path: "charts/{{chartPath}}"   # {{template}}/{{version}} — version-scoped
        targetRevision: HEAD
        helm:
          releaseName: "{{name}}"
          valueFiles:
          - "$appvalues/values.yaml"
      destination:
        server: "https://staging-api:6443"   # hardcoded — this AppSet owns staging
        namespace: "{{name}}"                # resolved from namespacePattern: "{app}"
      syncPolicy:
        automated:
          prune: true
          selfHeal: true
        syncOptions:
        - CreateNamespace=true
```

When `namespacePattern: "{app}-{env}"` is set, the destination namespace becomes
`"{{name}}-staging"` — generated by suparship at AppSet write time.

### `appproject.yaml` — ArgoCD AppProject per project/env

Written alongside the AppSet. Restricts the project's Applications to the correct
cluster API server. Carries `argocd.argoproj.io/sync-wave: "-1"` so ArgoCD creates
it before the ApplicationSet in the same sync.

### Preview layout

Previews are ephemeral. suparship creates a directory under `previews/{project}/{app}-{preview}/`
when a preview is created and deletes it when the preview is torn down. ArgoCD
prunes the child Application automatically when the file disappears — no explicit
ArgoCD API call is needed.

Each preview's `app.yaml` carries its own `clusterServer` and `namespace` (previews
typically go to the staging cluster):

```yaml
# gitops-output/previews/demo/hello-pr-42/app.yaml
appName: hello
project: demo
previewName: pr-42
template: web-service
clusterServer: "https://staging-api:6443"
namespace: hello-pr-42
```

---

## Root ArgoCD Application

The root "App of Apps" in `config/gitops/root-app.yaml` is applied once during
cluster bootstrap. It watches the GitOps repo for the three control-plane files
that suparship writes and applies them to the `argocd` namespace on the tooling
cluster. ArgoCD then generates child Applications from the ApplicationSets
internally — those child Applications are never written to Git.

```yaml
directory:
  recurse: true
  include: "**/{appproject,appset,previews-appset}.yaml"
destination:
  server: https://kubernetes.default.svc   # tooling cluster
  namespace: argocd
```

---

## Runtime Status and Logs

### Sync / health status

suparship reads ArgoCD `Application` CRs from the `argocd` namespace on the
tooling cluster. Application names follow the convention `{app}-{env}` (e.g.
`hello-staging`), which matches the AppSet template's `metadata.name` field.

The following fields are mapped to `domain.AppRuntimeStatus`:

| ArgoCD field | Maps to |
|-------------|---------|
| `status.sync.status` | `Synced` / `OutOfSync` |
| `status.health.status` | `Healthy` / `Degraded` / `Progressing` |
| `status.operationState.finishedAt` | `LastDeployed` |

No direct workload-cluster access is needed for status queries.

### Pod logs

Logs require direct access to the workload cluster. suparship maintains a
`ClusterClientPool` that loads kubeconfigs from `suparship-cluster-kubeconfig-*`
Secrets at startup. Log requests are routed to the correct client based on
`EnvironmentInstance.ClusterName`. The namespace for the log query is resolved
using the environment's `NamespacePattern`.

---

## Scaling to Multiple Clusters per Environment

The current model is 1 cluster per environment. When you need multiple clusters
per environment (e.g. multi-region staging), the path is:

1. Register additional clusters with suparship: `suparship cluster add --name staging-eu ...`
2. Create a new top-level environment directory in the GitOps repo: `gitops-output/staging-eu/`
3. suparship writes a new `appset.yaml` pointing at the `staging-eu` cluster API server
4. Apps published with `PublishApp` write their `values.yaml` into the new directory

No schema changes to the `Environment` type, `NamespacePattern`, or `app.yaml` format
are required. Each cluster-environment pair is just another directory with its own AppSet.

---

## Security

- **Kubeconfigs** are stored as Kubernetes Secrets in `suparship-system` — never in Git
- **Cluster API server URLs** appear in `appset.yaml` — this is intentional and not sensitive
- **ArgoCD cluster Secrets** are created in the `argocd` namespace using credentials
  extracted from the provided kubeconfig via `k8s.io/client-go/tools/clientcmd`
- **Prod cluster RBAC** can be enforced via Git CODEOWNERS on `gitops-output/prod/` —
  only approved PRs can change production manifests
- Cluster registration requires `org_admin` role in suparship

---

## API Reference

| Method | Path | Description | Role required |
|--------|------|-------------|---------------|
| `GET` | `/api/v1/clusters` | List registered clusters | authenticated |
| `POST` | `/api/v1/clusters` | Register a cluster | org_admin |
| `GET` | `/api/v1/clusters/:name` | Cluster detail + reachability | authenticated |
| `DELETE` | `/api/v1/clusters/:name` | Deregister a cluster | org_admin |

### POST `/api/v1/clusters` request body

```json
{
  "name": "staging-cluster",
  "displayName": "Staging",
  "env": "staging",
  "kubeconfig": "<base64-encoded kubeconfig>"
}
```

### GET `/api/v1/clusters` response

```json
[
  {
    "name": "staging-cluster",
    "displayName": "Staging",
    "apiServer": "https://staging-api:6443",
    "status": "ready"
  },
  {
    "name": "prod-cluster",
    "displayName": "Production",
    "apiServer": "https://prod-api:6443",
    "status": "ready"
  }
]
```

---

## Migration from Single-Cluster Layout

If you have an existing `gitops-output/` directory using the old per-app Application
CR layout (`{project}/{app}/{env}/argocd-app.yaml`), run the migration script:

```bash
./hack/migrate-gitops-paths.sh
```

The script:
1. Extracts inline Helm values from each `argocd-app.yaml` → `{env}/{project}/{app}/values.yaml`
2. Writes `{env}/{project}/{app}/app.yaml` from the Application's labels
3. Generates `{env}/appset.yaml` for each discovered environment
4. Generates `{env}/{project}/appproject.yaml` for each project/env pair
5. Deletes the old `argocd-app.yaml` files
6. Updates the root app include pattern

The migration is committed as a single atomic commit. ArgoCD will pick up the new
structure on the next sync; existing workloads are not affected because the ArgoCD
Applications generated by the new AppSets are functionally identical to the old
manually written Application CRs.

---

## Related docs

- [app-model.md](app-model.md) — the org/project/app/environment hierarchy
- [templates.md](templates.md) — Helm chart templates and the values schema
- [local-dns.md](local-dns.md) — setting up local ingress DNS for development
