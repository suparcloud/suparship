# Secrets Management

suparship organises secrets along two axes — **scope** (where a value varies) and
**tier** (who owns it) — and materialises them at runtime with the
[External Secrets Operator](https://external-secrets.io) (ESO). The same model
works with two backends: plain Kubernetes Secrets (`k8s`) and **1Password**.

The setup is **opinionated by design** — one supported way to wire each backend.
Fewer knobs, less cognitive load, easier audits.

## The model: three scopes, two tiers

**Scopes** are the variability axis — they answer *"does this value change per
environment or per cluster?"*

| Scope | Stored in | Read by |
|---|---|---|
| `global` | the **global vault** (one) | every cluster's ESO |
| `env` | the **env vault** for that env (one per environment) | the clusters that run that env |
| `cluster` | **cluster-override items inside the env vault** (no vault of its own) | the clusters that run that env |

There are only **two kinds of vaults** — global and per-env. Cluster overrides
are per-`(env, cluster)` items inside the env vault (e.g. `web-cluster-eu-1`
inside `suparship-secrets-env-prod`), so registering or removing a cluster never
touches the vault layer. An override set for `staging`/`eu-1` does not affect
`prod`/`eu-1`.

**Tiers** are the ownership axis within each scope:

- **shared** — org-admin-owned defaults that apply to every app in the scope.
- **app** — one app's own values in that scope.

**Precedence on key collision:** `cluster > env > global` and, within a scope,
`app > shared`. ESO merges everything into one Secret per app and later entries
win, so a `cluster`/`app` value beats a `global`/`shared` one.

> **Cluster scope is a platform-engineering escape hatch** — incident
> break-glass, regional tuning, per-cluster feature kill-switches. It overrides
> every other scope and applies per `(env, cluster)` pair. Use sparingly.

**Previews** reuse the same mechanism: a preview reads a per-app
`<app>-env-preview` band (and an optional per-PR `<app>-env-preview-pr-<name>`)
item **inside the base env's vault** — no per-preview vault is created. See
[previews.md](previews.md).

### Vault, item, and store names

For the **1Password** backend a "vault" is a real 1Password vault; for the
**k8s** backend a "vault" is a namespace ESO reads Secrets from. Each vault holds
one `shared-*` item plus one `<app>-*` item per app; an **env vault additionally
holds the cluster-override items** for each cluster bound to that env.

| Thing | Pattern | Example (`app=web, env=prod, cluster=eu-1`) |
|---|---|---|
| Global vault | `suparship-secrets-global` | `suparship-secrets-global` |
| Env vault | `suparship-secrets-env-{env}` | `suparship-secrets-env-prod` |
| Shared item | `shared-{suffix}` | `shared-global`, `shared-env-prod`, `shared-cluster-eu-1` |
| App item | `{app}-{suffix}` | `web-global`, `web-env-prod`, `web-cluster-eu-1` |
| ClusterSecretStore (1Password, one per cluster) | `suparship-store` | `suparship-store` |
| ClusterSecretStore (k8s, one per vault) | `suparship-store-{global\|env-{env}}` | `suparship-store-global`, `suparship-store-env-prod` |
| Connect token Secret (1Password, one per cluster) | `op-connect-token` | `op-connect-token` |
| App K8s Secret | `{app}-secrets` | `web-secrets` |
| App ConfigMap | `{app}-config` | `web-config` |

…where item `{suffix}` is `global`, `env-{env}`, or `cluster-{cluster}`.

**One store, one token per cluster (1Password).** An `ExternalSecret` works best
against a single store, and ESO's 1Password provider can list **multiple vaults
in one `ClusterSecretStore`** — so every workload cluster runs exactly one store,
named `suparship-store` on every cluster, whose `vaults:` map lists the global
vault plus the env vault(s) of the environments bound to that cluster. Cluster
items keep their `cluster-{cluster}` suffix and resolve through that same store
(item names are scope-unique, so multi-vault lookup is unambiguous). Because the
store name is identical everywhere, app `ExternalSecret`s are cluster-agnostic —
rebinding an env to a different cluster needs no regeneration.

The **k8s** backend keeps one store per vault/namespace
(`suparship-store-{global|env-{env}}`) — ESO's `kubernetes` provider reads
exactly one `remoteNamespace` per store.

## Two credential types (1Password)

1Password Service Accounts **cannot create vaults or issue Connect tokens** — the
operator creates both manually in the 1Password console. suparship handles the
cluster-side automation: sealing each cluster's Connect token, generating its
unified `ClusterSecretStore`, and publishing the manifests to GitOps.

- **Service Account (SA) token** (stored in suparship): used to write/read/delete
  secret items in 1Password vaults — the data plane for developer secrets.
- **Connect token, one per cluster** (sealed, deployed to that cluster): ESO uses
  it to read secret values at runtime. Each cluster's token is granted access to
  **every vault that cluster reads** — the global vault plus the env vaults of
  the environments bound to it.

> **Isolation note:** Connect tokens are vault-scoped (not item-scoped), so every
> cluster bound to an env can technically read all items in that env's vault —
> including sibling clusters' override items. This is an accepted trade-off for
> not having to create/register a vault per cluster.

> **Binding changes need a token re-issue.** A cluster's token must cover its
> vault set. When you bind a *new* env to a cluster, issue a new Connect token in
> the 1Password console that also covers the new env vault, and re-paste it for
> that cluster — suparship re-seals and republishes the store. (Registering a new
> env vault or changing bindings already republishes the store automatically; only
> the token's vault access needs the manual step.)

> **Why two tokens?** 1Password Service Account tokens [cannot issue Connect
> server tokens or grant Connect servers vault access](https://1password.community/discussion/167592).
> The Connect tokens must be created manually in the 1Password web console.

## Architecture

```mermaid
sequenceDiagram
    participant Admin as Admin (manual)
    participant OP as 1Password Console
    participant UI as suparship UI
    participant API as suparship Backend
    participant Git as GitOps Repo
    participant Argo as ArgoCD
    participant Cluster as Workload Cluster

    Admin->>OP: 1. Create global + per-env vaults
    Admin->>OP: 2. Create SA token with R/W to all vaults
    Admin->>OP: 3. Create Connect Server, grant access to all vaults
    Admin->>UI: 4. Paste SA token
    UI->>API: POST /sa-token
    API-->>UI: valid, N vaults visible
    Admin->>UI: 5. Pick global vault + register env vaults (IDs only)
    UI->>API: PUT /global-vault, POST /vaults/env/{name}
    Admin->>OP: 6. Issue ONE Connect token per cluster (access: global + its env vaults)
    Admin->>UI: 7. Paste each cluster's Connect token
    UI->>API: POST /org/secret-backend/clusters/{cluster}/connect-token
    API->>API: Fetch sealed-secrets cert, seal the token
    API->>Git: Commit sealed-token.yaml + store.yaml (one pair per cluster)
    Git->>Argo: Sync
    Argo->>Cluster: Deploy SealedSecret + unified ClusterSecretStore
    Cluster->>Cluster: sealed-secrets decrypts token, ESO reads all vaults via one store
```

## Admin walkthrough (1Password)

### Prerequisites

1. A **1Password account** with admin access to create vaults, Service Accounts,
   and Connect Servers.
2. **sealed-secrets** installed on each workload cluster (suparship can install
   it — see Step 4).
3. **External Secrets Operator** installed on each workload cluster (suparship
   can install it — see Step 4).
4. **1Password Connect** deployed and reachable from the workload clusters.

### Step 1: Create vaults in 1Password

Service Accounts can't create vaults, so the operator creates them by hand:

1. One **global vault** (e.g. `company-global`) — org-wide secrets, read-only
   from every cluster.
2. One **env vault** per environment (e.g. `staging-env`, `prod-env`). This
   vault also carries the per-cluster override items for that env — no
   per-cluster vault is needed.
3. A **Service Account** with Read & Write access to all of them. Copy its token.

> No vault naming convention is enforced — suparship stores the UUID of whichever
> vault you pick/register, not its name.

### Step 2: Save the SA token

**UI:** Settings → Secrets Backend → Provider: 1Password → paste SA token → Save.
suparship validates it and reports how many vaults are visible. The token is
stored as a K8s Secret `suparship-system/suparship-op-sa-token`.

### Step 3: Register vaults (IDs only)

**UI:** Settings → Secrets Backend → **Global vault** → "List vaults" → pick the
global vault → Save. Then **+ Add Vault** → pick each environment and its vault.

```bash
curl -X PUT $SUPARSHIP_URL/api/v1/org/secret-backend/global-vault \
  -H "Cookie: session=…" -H "Content-Type: application/json" \
  -d '{"vaultId": "abc-123"}'
curl -X POST $SUPARSHIP_URL/api/v1/org/secret-backend/vaults/env/staging \
  -d '{"vaultId": "def-456"}'
```

Registration only records which vault backs each scope — no tokens here. Cluster
overrides need no registration of their own; they ride along in the env vault.

### Step 4: Check cluster prerequisites

**UI:** Settings → Secrets Backend → Prerequisites shows green/red per component.
If sealed-secrets or ESO are missing, suparship offers a one-click install
(pinned chart versions):

- sealed-secrets: `2.16.2` from `bitnami-labs.github.io/sealed-secrets`
- external-secrets: `0.10.7` from `charts.external-secrets.io`

### Step 5: Set up 1Password Connect

In the 1Password web console:

1. Create a **Connect Server** and grant it access to **all** suparship vaults.
2. Deploy 1Password Connect to a cluster reachable from your workloads.
3. Issue **one Connect token per cluster**, granting it access to the global
   vault plus the env vaults of the environments bound to that cluster.

### Step 6: Paste each cluster's Connect token

**UI:** Settings → Secrets Backend → **Cluster Connect Tokens** → paste the
cluster's token → Seal.

```bash
curl -X POST $SUPARSHIP_URL/api/v1/org/secret-backend/clusters/prod-eu/connect-token \
  -d '{"connectToken": "<that cluster's connect token>"}'
```

suparship stashes the token, seals it against the cluster's sealed-secrets cert,
and publishes exactly two files under `_secret-stores/{cluster}/` —
`sealed-token.yaml` (Secret `op-connect-token`) and `store.yaml` (the unified
`suparship-store` listing every vault the cluster reads) — synced by that
cluster's ArgoCD Application. Registering vaults or changing env bindings later
republishes the store automatically using the stashed token.

### Rotating a Connect token

Re-paste a new token for the cluster — suparship re-stashes, re-seals, and
re-publishes. Revoke the old token manually in 1Password afterward. The same
flow covers widening access when a new env is bound to the cluster.

### Removing a vault registration

Remove it from the Secrets Backend table. The vault and its contents are
preserved in 1Password; clean up the Connect token/vault manually if no longer
needed.

## Platform-managed Secret & ConfigMap (per app)

When suparship publishes an app it generates exactly **one** `ExternalSecret`
(→ K8s `Secret` `{app}-secrets`) and **one** `ConfigMap` (`{app}-config`) per
bound environment. These are **owned by the platform, not the app chart** — they
are written into a platform-owned GitOps tree and shipped by a platform
ApplicationSet, so application charts stay decoupled from suparship and only need
to `envFrom` the two well-known names.

### GitOps layout

```
_app-resources/                          # platform-owned, sibling of _secret-stores/
  {env}/{project}/{app}/
    meta.yaml             # generator params: name, project, namespace
    env-configmap.yaml    # {app}-config
    external-secret.yaml  # {app}-secrets   (omitted when no scope has keys)
  previews/{project}/{preview}/
    meta.yaml             # + clusterServer for the preview's cluster
    env-configmap.yaml
    external-secret.yaml
_infra/
  {env}-platform-appset.yaml       # 1 App per app → workload cluster + {{namespace}}
  previews-platform-appset.yaml    # 1 App per preview → {{clusterServer}} + {{namespace}}
_secret-stores/{cluster}/          # per-cluster sealed-token.yaml + store.yaml (one token, one unified store)
envs/{env}/{project}/{app}/        # app Application = chart + values ONLY
  app.yaml  values.yaml
```

The root app syncs the platform ApplicationSets to the control plane; ArgoCD
reconciles them into per-app Applications that sync `_app-resources/...` onto the
workload clusters in the app's namespace.

### The chart contract

App charts never define these objects — they just `envFrom` the names suparship
passes via values (`suparship.envFromConfigMaps` = `[{app}-config]`,
`suparship.envFromSecrets` = `[{app}-secrets]`):

```yaml
# in deployment.yaml (simplified)
envFrom:
  {{- range .Values.suparship.envFromConfigMaps }}
  - configMapRef: { name: {{ . }}, optional: true }
  {{- end }}
  {{- range .Values.suparship.envFromSecrets }}
  - secretRef: { name: {{ . }}, optional: true }
  {{- end }}
```

Entries are `optional: true`, so an app pod can start before the platform
Application has populated the Secret/ConfigMap — ESO + reloader fill them in.
No sync-wave dependency is required.

### Preview environments

On preview create suparship writes `meta.yaml`, `env-configmap.yaml`, and
`external-secret.yaml` to `_app-resources/previews/{project}/{preview}/`
(carrying the preview's `clusterServer`). The `previews-platform-appset.yaml`
ApplicationSet fans these out per preview. On preview delete the directory is
removed.

## The merged ExternalSecret

Each app namespace gets **one** `ExternalSecret` named `{app}-secrets`. Its
`dataFrom` lists every present scope/tier item in precedence order. On the
**1Password** backend every item extracts from the cluster's single unified
store, so no per-entry `sourceRef` is needed:

```yaml
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: web-secrets
  namespace: acme-web-prod
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: suparship-store          # the cluster's one unified store
    kind: ClusterSecretStore
  target:
    name: web-secrets
    creationPolicy: Owner
  dataFrom:
    - extract: { key: "shared-global" }       # global vault
    - extract: { key: "web-global" }          # global vault
    - extract: { key: "shared-env-prod" }     # env vault
    - extract: { key: "web-env-prod" }        # env vault
    - extract: { key: "web-cluster-eu-1" }    # env vault (cluster override); wins last
```

Only scopes/tiers that actually have keys get a `dataFrom` entry — computed at
publish time. Cluster items are included only when the env is bound to a cluster.
Item names are scope-unique, so the multi-vault lookup is unambiguous.

The cluster's unified `ClusterSecretStore` (1Password) lists every vault the
cluster reads and references its one sealed Connect token:

```yaml
apiVersion: external-secrets.io/v1
kind: ClusterSecretStore
metadata:
  name: suparship-store
spec:
  provider:
    onepassword:
      connectHost: http://onepassword-connect.onepassword-connect.svc.cluster.local:8080
      vaults:
        <global vault UUID>: 1
        <env-prod vault UUID>: 2
      auth:
        secretRef:
          connectTokenSecretRef:
            name: op-connect-token
            key: token
            namespace: external-secrets
```

For the **k8s** backend a store reads exactly one namespace via the
`suparship-eso-reader` ServiceAccount in `suparship-system`, so there is one
store per vault/namespace and the ExternalSecret carries per-entry
`sourceRef.storeRef`s instead:

```yaml
spec:
  provider:
    kubernetes:
      remoteNamespace: suparship-secrets-env-prod
      auth:
        serviceAccount:
          name: suparship-eso-reader
          namespace: suparship-system
```

## Populating secrets

After app creation:

1. Open the vault that backs the scope you want — the global vault, or the env
   vault (which also holds that env's cluster-override items).
2. Find the item suparship created (`shared-*` for org-admin defaults,
   `{app}-*` for an app).
3. Replace the `_placeholder` field with real `KEY = value` pairs.

ESO syncs to the K8s Secret within the `refreshInterval` (default `1h`). Force an
immediate sync:

```bash
kubectl annotate externalsecret <app>-secrets \
  suparship.io/forced-sync-at="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -n <namespace> --overwrite
```

The UI Save button does this for you via
`POST /api/v1/projects/:project/apps/:app/secrets/sync`.

## Where to write each scope/tier (UI + API)

| Scope | Tier | UI | API |
|---|---|---|---|
| global | shared | Org Settings → Secrets | `…/org/secrets/global` |
| global | app | App → Secrets → Global | `…/projects/{p}/apps/{a}/secrets/global` |
| env | shared | Org Settings → Secrets (env) | `…/org/secrets/env/{env}` |
| env | app | App → Secrets → Env | `…/projects/{p}/apps/{a}/secrets/env/{env}` |
| cluster | shared | Cluster Settings → Overrides (per env) | `…/org/secrets/env/{env}/cluster/{cluster}` |
| cluster | app | App → Secrets → Env → Cluster | `…/projects/{p}/apps/{a}/secrets/env/{env}/cluster/{cluster}` |

## RBAC matrix

| Operation | Required role |
|---|---|
| global / env / cluster **shared** writes | `org_admin` |
| app-tier writes (any scope) | `developer` (or higher) for the project |
| Backend setup, SA token, vault registration | `org_admin` |

All scopes are readable by any authenticated user (`viewer` and above).

## Security guarantees

| Risk | Mitigation |
|---|---|
| SA token blast radius | suparship validates vault count on paste; scope the SA to suparship vaults only. |
| SA token leak from cluster | K8s Secret restricted via RBAC to the suparship controller ServiceAccount. |
| Per-cluster credential blast radius | One Connect token per cluster, granted access only to the vaults that cluster reads (global + its envs). Clusters sharing an env vault can read each other's override items — accepted trade-off. |
| Connect token in Git | Always sealed before commit; plaintext never crosses the suparship process boundary except during in-memory seal (a stash copy lives in `suparship-system` for re-seals). |
| Token rotation | Re-paste the cluster's token; revoke the old one manually in 1Password. |
| Audit trail | Every write/delete and vault registration is logged with actor, scope, vault, and result. |

Key names and outcomes are logged; **secret values, SA tokens, and Connect
tokens are never logged.**

## Install-time conventions

These are intentionally not configurable — they keep every install identical and
easy to audit.

| Symbol | Value |
|---|---|
| SA token K8s Secret | `suparship-op-sa-token` in `suparship-system` |
| Connect token secret (per cluster) | `op-connect-token` |
| Connect token stash (per cluster) | `suparship-onepassword-connect-token-cluster-{cluster}` in `suparship-system` |
| ClusterSecretStore (1Password, per cluster) | `suparship-store` |
| ClusterSecretStore (k8s, per vault) | `suparship-store-{global\|env-{env}}` |
| Remote namespace (ESO resources) | `external-secrets` |
| Connect endpoint | `http://onepassword-connect.onepassword-connect.svc.cluster.local:8080` |
| k8s ESO reader | ServiceAccount `suparship-eso-reader` (cluster-wide Secret read) |

## Troubleshooting

### App pods don't see global keys

The cluster's Connect token is missing or can't read the global vault. Confirm
the global vault is registered (Step 3), the cluster's token is sealed
(`_secret-stores/{cluster}/sealed-token.yaml` exists and the Cluster Connect
Tokens card shows "sealed"), the token's vault access includes the global vault,
and force a re-sync.

### `gitops: skipping publish for unbound env`

The env has no `ClusterRef`, or the referenced cluster isn't registered. The
publisher writes nothing for unbound envs. Fix in Settings → Environments →
assign a cluster, then "Sync to Git" on the app.

### "Resolved Secrets" shows keys but they don't reach the workload

Most likely the `ExternalSecret` was generated before a scope's vault/token was
registered. Re-publish:
`POST /api/v1/projects/{project}/apps/{app}/sync` — the publisher rebuilds the
merged ExternalSecret from the current vault registrations.

### Env- or cluster-scope keys don't reach the workload

Cluster-override items live in the **env vault**. Confirm the env vault is
registered (Step 3), the cluster's `store.yaml` lists that vault's UUID (it is
republished automatically on registration/binding changes when a token is
stashed), and the cluster's Connect token has access to the env vault — binding
a new env to a cluster requires re-issuing + re-pasting the token. Also confirm
the override was written for the right `(env, cluster)` pair — an override for
`staging`/`eu-1` does not apply to `prod`/`eu-1`.

## Keep going without suparship

The GitOps repository is fully self-contained:

- `_secret-stores/{cluster}/sealed-token.yaml` — the cluster's sealed Connect token
- `_secret-stores/{cluster}/store.yaml` — the cluster's unified `ClusterSecretStore`
- `_app-resources/{env}/{project}/{app}/external-secret.yaml` — `ExternalSecret`
- `_app-resources/{env}/{project}/{app}/env-configmap.yaml` — `ConfigMap`

Anyone can `kubectl apply` these (or let ArgoCD do it) without ever running
suparship. suparship is the control plane for the **inputs** (vault items,
generated CRs in Git); the runtime is plain ESO + sealed-secrets + ArgoCD.

## Out of scope (this iteration)

- Sealing-cert rotation / re-sealing on cert change.
- HashiCorp Vault and AWS-SM `VaultStore` implementations.
- Binary / file secrets (TLS certs, kubeconfigs) — UI accepts only UTF-8
  `KEY=value`, ≤64 KiB.
- Per-PR preview vaults — previews resolve secrets from the env/global scopes.
- Vault-side cleanup on registration removal — items remain in 1Password for the
  admin to clean up.
- Automated Connect token issuance (blocked by 1Password SA token limitations).
