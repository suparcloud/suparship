# Installing suparship — SRE Day-1 runbook

This is the platform team's path to a working suparship install. It's a
**dedicated, single-org install**: you (SRE/platform) stand it up and operate
it; your developers consume it to ship apps (see
[apps-and-images.md](apps-and-images.md)).

The end state: the **Platform setup** checklist on the suparship onboarding page
is all green, and a developer can create an app that deploys. Work top to
bottom; each step ends with a **Verify** you can check before moving on.

---

## 0. Prerequisites

suparship is a control plane over ArgoCD + Kargo + External Secrets. These must
exist on the **tooling cluster** (where suparship and ArgoCD run); workload
clusters need sealed-secrets + ESO. sealed-secrets on the tooling cluster is
what makes the config export (step below) able to carry encrypted credentials.

| Component | Where | Required for | Install |
|---|---|---|---|
| ArgoCD | tooling cluster | all delivery | your own, or `dependencies.argocd.enabled=true` |
| Kargo | tooling cluster | promotion pipeline | your own |
| External Secrets Operator | each workload cluster | secrets delivery | your own, or `dependencies.eso.enabled=true` |
| sealed-secrets | tooling + each workload cluster | per-cluster secret-backend read tokens (Vault / 1Password); sealed config export | your own |
| 1Password Connect | reachable from workload clusters | only if using the 1Password secret backend | 1Password Helm chart |

Production guidance: install these **independently** and leave the chart's
`dependencies.*` toggles `false`. The toggles exist for quick evaluation only.

The chart runs a `prereq-check` Job on install that reports ArgoCD / ESO
presence — check its logs if anything downstream misbehaves.

**Verify:** `kubectl get ns argocd` and `kubectl get crd externalsecrets.external-secrets.io` succeed on the relevant clusters.

---

## 1. Provide the admin credential (one of two ways)

The chart does **not** create the admin Secret. Either:

- **A — bootstrap after install** (simplest): install first (step 2), then run
  the `suparship admin bootstrap` command in step 3, or
- **B — provision it up front**: create the Secret
  (`suparship-admin-auth`, keys `username` + `password-hash` (bcrypt)) via
  SealedSecrets / ExternalSecrets / `extraObjects` before installing.

For a first install, A is easiest.

---

## 2. Install the chart

```bash
helm install suparship ./charts/suparship \
  --namespace suparship-system --create-namespace \
  --set org.name=acme \
  --set org.displayName="Acme" \
  --set server.cookieSecure=true        # when serving over HTTPS
```

Useful values (full list in `charts/suparship/values.yaml`):

| Value | Purpose |
|---|---|
| `org.name`, `org.displayName` | seeds the single org |
| `ingress.enabled`, `ingress.host` | expose the UI (otherwise port-forward) |
| `argocd.namespace` | where ArgoCD lives (default `argocd`) |
| `argocd.createSystemProject` | create the `suparship-system` AppProject (leave `true`) |
| `gitops.*`, `clusters[]`, `environments[]`, `secrets.*`, `registry.*` | optional: declare setup in values instead of via the UI |

You can declare the entire setup in values (clusters, environments, gitops,
secrets) and suparship reconciles it into config on first boot — or leave them
empty and drive everything through the UI checklist (steps 4+). Either works;
the UI is friendlier for a first install.

**Verify:** `kubectl rollout status deploy/suparship -n suparship-system` is
Available.

---

## 3. Bootstrap the admin user (if you didn't pre-provision it)

```bash
kubectl exec -n suparship-system deploy/suparship -- \
  suparship admin bootstrap          # add --username ops-admin to change it
```

The generated password prints **once** — store it in your password manager
immediately. (`suparship admin reset-password` rotates it later.)

**Verify:** the command prints a username + password and reports the Secret was
written.

---

## 4. Open the UI and find the checklist

```bash
kubectl port-forward -n suparship-system svc/suparship 8080:80
# open http://localhost:8080  (or your ingress host)
```

Sign in with the admin credential. The onboarding page shows a **Platform
setup** checklist with a live status per step (auth, gitops, clusters,
environments, secret backend). Everything below drives those gates green. Each
gate that isn't ready shows what to do and a link to fix it.

**Verify:** you're signed in; the "Admin authentication" gate is green.

---

## 5. Connect the GitOps repository

Settings → GitOps. Provide the repo URL, provider, branch, and a credential
Secret (or paste credentials). Click **Test connection** — it runs
`git ls-remote` and must succeed.

suparship commits rendered manifests here; ArgoCD syncs them. Without it, app
creation only persists to the cluster store (no delivery).

**Verify:** "Test connection" is green; the **GitOps repository** gate is green.

---

## 6. Register at least one workload cluster

Settings → Clusters → Register. Provide a name (DNS label), the API server URL
(`https://…`, no trailing space — it's validated), and the kubeconfig. suparship
fetches the sealed-secrets cert in the background.

> The first cluster can be the tooling cluster itself (`inCluster: true` /
> `https://kubernetes.default.svc`).

> **Need a kubeconfig?** The most portable credential is a ServiceAccount token.
> See [Create a token-based kubeconfig](cluster-kubeconfig.md) for a copy-paste
> recipe (also the recommended path for EKS/GKE clusters that can't be imported
> because they use exec / cloud-IAM auth).

**Brownfield — already running ArgoCD?** Settings → Clusters → **Import from
ArgoCD** lists the clusters ArgoCD already has registered. Importing reconstructs
a kubeconfig from each ArgoCD registration and wires it exactly like a fresh
registration (stored kubeconfig + sealing cert + ESO ClusterSecretStore) — so the
imported cluster can deliver secrets, not just receive deploys. Clusters that
ArgoCD authenticates to with exec / cloud-IAM auth (EKS `aws-iam-authenticator`,
GKE `gcloud`) are listed but not importable — register those with a token-based
kubeconfig instead. On the 1Password backend, an imported cluster still needs its
Connect token pasted in step 8.

**Verify:** the cluster shows **ready**; the **Workload clusters** gate is green.

---

## 7. Define environments and bind them to clusters

Settings → Environments. Create your pipeline (e.g. `staging` order 1, `prod`
order 2) and **bind each to a registered cluster**. The lowest-order env is the
auto-promote entry point; later envs are gated promotions.

**Verify:** each environment shows a bound cluster; the **Environments** gate is
green.

---

## 8. Configure the secret backend

- **k8s backend** (default): nothing to do — the gate is already green.
- **1Password backend**: this is multi-step; the **Secret backend** gate names
  exactly what's missing as you go. Full detail in [secrets.md](secrets.md):
  1. paste the Service Account token (Settings → Secrets Backend),
  2. pick the global vault,
  3. register an env vault per environment,
  4. set the org-level Connect Server URL (validated — host + port, Connect's
     API is usually `:8080`),
  5. paste **one Connect token per cluster** (covering the global + that
     cluster's env vaults) and seal it.

**Verify:** the **Secret backend** gate is green (k8s: automatic; 1Password: all
five sub-steps done).

---

## 9. (Optional) Configure a private registry

Settings → Registry, if your developers' images are private. URL is a bare host
(`ghcr.io`, no scheme). suparship then creates `imagePullSecret`s in app
namespaces. Public images need nothing here.

---

## 10. Smoke-test with a real app

Hand off to a developer (or do it yourself) per
[apps-and-images.md](apps-and-images.md): create an app from a template, set its
`image_repository`, and confirm it goes **Healthy** in the first environment.
The app detail page's **Diagnostics** panel explains any failure with a
suggested fix.

**Verify:** the test app is Healthy and its endpoint serves; **PlatformReady**
on the onboarding page is true.

---

## Config as code: export with sealed credentials

Once the setup checklist is green, persist the whole configuration in git:

**Platform → Export Configuration → Download values.yaml (sealed
credentials)** (`GET /api/v1/org/export?includeSecrets=1&format=yaml`,
org_admin). The file contains the full Helm values for your install — org,
environments, clusters, gitops, registry, secrets backend, teams, role
bindings, OIDC — **plus every platform credential as a `SealedSecret` under
`extraObjects`**: the gitops repo credentials, the secret-backend write token
and per-cluster stash tokens, registry auth, the OIDC client secret, the
admin credential, and template-source credentials. The blobs are encrypted
for this cluster's sealed-secrets controller, so the file is safe to commit.

- `helm upgrade suparship ./charts/suparship -f exported-values.yaml`
  reproduces the entire setup; the sealed-secrets controller unseals the
  credentials back into the Secrets suparship reads.
- **Back up the sealed-secrets controller key.** The blobs decrypt only with
  it — disaster recovery onto a fresh cluster means restoring the key first.
  Re-export after any key rotation.
- Re-export after settings changes you want persisted (the export is always
  a live snapshot; nothing is stored server-side).

---

## Recovery & operations

- **Wiped the GitOps `_secret-stores/` tree?** Restart the suparship pod —
  startup self-heal re-seals each cluster's Connect token + store from the local
  stash (1Password backend). Clusters with no stashed token are logged; re-paste
  those.
- **App stuck / "Not deployed"?** Check the app's Diagnostics panel first — it
  surfaces the ArgoCD/ESO reason. Most causes (bad image, unbound env, secret
  store not ready) point at a setup gate.
- **Rotate the admin password:** `suparship admin reset-password`.
- **Upgrade:** `helm upgrade` with the same values. Generated GitOps resources
  carry a `suparship.io/generator-version` label; review release notes for
  migrations before upgrading across breaking changes.

## Where things live

| Thing | Location |
|---|---|
| suparship server + config | `suparship-system` namespace (ConfigMaps + Secrets) |
| Admin credential | Secret `suparship-admin-auth` (configurable) |
| Rendered manifests | your GitOps repo (`_app-resources/`, `_infra/`, `_secret-stores/`, `envs/`) |
| Per-cluster sealed token + store | `_secret-stores/{cluster}/` |
| Connect-token stash (recovery) | Secrets in `suparship-system` |
