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

- A hub cluster running suparShip + ArgoCD (+ Kargo, ESO) per [install.md](install.md).
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
      dashboard surfaces only suparShip-managed stuck apps).
- [ ] **Config export** — `GET /api/v1/org/export?format=yaml` reproduces the
      configured org/envs/clusters/gitops/registry/auth/teams/roleBindings with
      no secret values (refs only).

## On failure

Capture the failing step, the server logs around it (`oidc:` / `gitops:` /
`republish apps:` prefixes), and the relevant ArgoCD Application status. File
against the release before tagging.
