# Golden-path acceptance checklist

Two layers verify the golden path:

1. **Automated, in-process** — `test/smoke/golden_path_test.go` drives the full
   HTTP API (login → create app → list/status → edit config → logs → deployment
   history) against the assembled server backed by in-memory fakes + on-disk
   templates. Runs in CI, no cluster needed: `go test ./test/smoke/...`. It locks
   the API contract, RBAC, and handler wiring.

2. **Manual, real cluster** — the steps below. The automated test cannot
   exercise the parts that only a real cluster + ArgoCD reveal (this is where
   every bug we hit in early testing lived): actual ArgoCD sync, multi-cluster
   status/logs routing, the OIDC redirect, and secret materialization. Run this
   once per release against a hub-spoke setup before tagging.

## Prerequisites

- A hub cluster running suparship + ArgoCD (+ Kargo, ESO) per [install.md](install.md).
- At least one **remote** workload cluster registered (Settings → Clusters), so
  status/logs routing is genuinely cross-cluster — not in-cluster.
- A GitOps repo connected and green on the Platform-setup checklist.

## Steps

Tick each; note the build/image tag under test.

- [ ] **Install & checklist** — Platform-setup checklist is all-green
      (gitops, registry, ≥1 cluster, ≥1 environment bound to a cluster, secrets backend).
- [ ] **Create app** — create an app from a template; confirm the gitops repo
      gets `envs/<env>/<project>/<app>/{app.yaml,values.yaml}` and
      `charts/<template>/<version>/`.
- [ ] **ArgoCD syncs** — the `<project>-<app>-<env>` Application appears, goes
      Synced + Healthy, and the workload lands on the **remote** cluster.
- [ ] **Status reflects reality** — app detail + dashboard show the correct
      replica count and "Healthy" (NOT "Not deployed"). This is read from the
      workload cluster via the ClusterClientPool.
- [ ] **Logs stream** — the Logs tab returns pod logs from the workload cluster.
- [ ] **Deployment history** — the Deployments tab lists the ArgoCD sync(s)
      (Application name is `<project>-<app>-<env>`).
- [ ] **Edit config** — change a template value (e.g. image tag) in App → Config;
      confirm values.yaml is re-published and ArgoCD rolls the change.
- [ ] **Addon (if used)** — an app with an addon (e.g. redis/valkey) gets a
      `<app>-addon-<name>` Application with a resolved chart path
      (`charts/<wrapper>/latest/`), and it syncs Healthy.
- [ ] **Promote** — promote to the next environment; Kargo Promotion CR is
      created and the higher env's files land in gitops.
- [ ] **SSO login** — with OIDC configured (Settings → Auth, see
      [sso.md](sso.md)): "Sign in with SSO" redirects to the IdP, returns to a
      logged-in session, and a non-admin user authorized by team/group can act
      per their role (and is denied beyond it).
- [ ] **Break-glass** — the local admin password login still works.
- [ ] **Delete** — deleting the app removes its gitops files and the ArgoCD
      Application terminates cleanly (no stuck-in-Terminating; if any, the
      dashboard surfaces only suparship-managed stuck apps).
- [ ] **Config export** — `GET /api/v1/org/export?format=yaml` reproduces the
      configured org/envs/clusters/gitops/registry/auth/teams/roleBindings with
      no secret values (refs only).

## Multi-cluster fan-out + per-cluster overrides

Only verifiable on a real multi-cluster ArgoCD (unit + smoke tests cover the
manifest shape, not live sync). Requires **two** registered workload clusters.

Setup:

- [ ] Register a **second** workload cluster (Settings → Clusters); both show
      Ready.
- [ ] Bind an environment to **both** clusters (`clusterRefs: [A, B]`) and set
      its **Deploy mode** to **All clusters** (Settings → Organization → the env,
      or `deployMode: all` in values).
- [ ] Deploy an app to that environment.

Fan-out:

- [ ] ArgoCD shows **two** Applications for the app —
      `<project>-<app>-<env>-<clusterA>` and `…-<clusterB>` — each with its
      `spec.destination.server` pointing at the respective cluster, both
      Synced + Healthy on their own cluster.
- [ ] The gitops repo has one `app.yaml` per app under
      `envs/<env>/<project>/<app>/` and a per-cluster
      `envs/<env>/_clusters/<cluster>/<project>/<app>/values.yaml` for each
      cluster.
- [ ] The AppProject authorizes **both** cluster destinations; the per-app
      ConfigMap + ExternalSecret land on **both** clusters (platform AppSet
      fanned out too).
- [ ] App detail shows **aggregated** status (worst-of phase, summed replicas)
      with a per-cluster breakdown in the status diagnostics.

Per-cluster routing (multi-cloud):

- [ ] Give cluster A and B **different base domains** (Settings → Clusters →
      expand → Routing: e.g. A `aws.example.com`, B `azure.example.com`) and, if
      the clouds differ, different ingress class + ClusterIssuer per cluster.
- [ ] After publish, each app's per-cluster
      `_clusters/<cluster>/…/values.yaml` shows a host under that cluster's
      domain (`app.<env>.aws.example.com` vs `…azure.example.com`) and the
      cluster's ingress class/issuer.
- [ ] DNS for each domain points at that cluster's ingress; the app is reachable
      on both clouds at its respective host with a valid cert.

Per-cluster override:

- [ ] In App → Config → **Per-cluster overrides**, set a different **replica
      count** for cluster A than B (e.g. A=3, B=1); Save.
- [ ] Only cluster A's `_clusters/<A>/…/values.yaml` reflects the override; the
      env value still applies to B.
- [ ] After ArgoCD syncs, cluster A runs the overridden replica count and
      cluster B runs the env default — confirm on each cluster.
- [ ] Switching the env's Deploy mode back to **Active cluster only**
      collapses to a single `<project>-<app>-<env>` Application on the active
      cluster (no orphaned per-cluster Applications after the next publish).

## Import from ArgoCD (brownfield)

Only verifiable against a real ArgoCD that already has a cluster registered with
a **token-based** kubeconfig (not exec/cloud-IAM).

- [ ] In an ArgoCD that pre-dates suparship, register a workload cluster with a
      token kubeconfig (`argocd cluster add` against a context that uses a bearer
      token / service-account token).
- [ ] Settings → Clusters → **Import from ArgoCD** lists that cluster as
      importable; a cluster ArgoCD added with exec/cloud-IAM auth (EKS/GKE)
      appears greyed with the "exec / cloud-IAM auth not supported" reason; a
      cluster suparship already manages appears greyed as "already registered".
- [ ] Select the token cluster → Import → it appears in the Clusters list as
      **ready**; live status/logs work (proves the reconstructed kubeconfig
      builds a working client); the Routing editor is available.
- [ ] No **new** ArgoCD cluster Secret was created for that server (import linked
      the existing one); deleting the imported cluster from suparship leaves the
      original ArgoCD cluster Secret intact.
- [ ] On the **k8s** secret backend, deploy an app to an env on the imported
      cluster and confirm its secrets materialize (the ESO ClusterSecretStore was
      published by import). On the **1Password** backend, the cluster shows
      pending-token until you paste its Connect token (Settings → Secrets
      Backend), then secrets materialize.

## On failure

Capture the failing step, the server logs around it (`oidc:` / `gitops:` /
`republish apps:` prefixes), and the relevant ArgoCD Application status. File
against the release before tagging.
