# Apps & Images — the developer path

This is the day-to-day developer flow on suparship: create an app from a
template, point it at your container image, push from CI, and promote it
through environments. The SRE/platform team does the one-time setup (clusters,
GitOps repo, registry, secrets — see the platform setup checklist on the
onboarding page); developers only deal with apps.

## The mental model

```
your CI builds + pushes an image  ─►  registry (e.g. ghcr.io/acme/web:1.4.2)
                                          │
                          Kargo Warehouse watches the repo for new tags
                                          │  new freight
                                          ▼
                 first env (e.g. staging) auto-promotes  ─►  ArgoCD syncs  ─►  pods run
                                          │  you click Promote
                                          ▼
                              next env (e.g. prod)
```

suparship never builds images — that's your CI's job. suparship watches the
registry, renders the Kubernetes manifests into the GitOps repo, and ArgoCD
delivers them. Promotion between environments is gated and explicit (except the
first environment, which auto-promotes new freight).

## 1. Create the app

App → New, pick a template, and set the **image repository** — the bare image
reference, no scheme and no tag:

| Field | Example | Notes |
|---|---|---|
| `image_repository` | `ghcr.io/acme/web` | host + path only. **No** `https://`, **no** `:tag`, **no** `@sha256:…`. |
| `image_tag` | `1.4.2` | optional; the running tag. CI updates this via new images, not by editing here. |

suparship rejects a malformed `image_repository` at creation time (a scheme, a
tag, a digest, or whitespace) so you find out immediately rather than seeing
pods stuck in `InvalidImageName` later.

> If you leave `image_repository` empty, the app uses a non-resolving
> placeholder (`ghcr.io/{project}/{app}`) and its pods will sit in
> `InvalidImageName` / `ErrImagePull`. The app's status page shows this as a
> diagnostic with a suggested fix, and the server logs a warning on publish.
> Some templates ship their own image (e.g. a stock nginx) — those don't need
> one.

## 2. Make sure the registry is reachable

If your image is in a **private** registry, the platform team configures it once
under Settings → Registry (host, username, credential Secret). suparship then
creates the `imagePullSecret` in each app namespace. Public images (e.g.
`ghcr.io` public, Docker Hub library images) need no registry config.

The registry URL there is also a bare host (`ghcr.io`,
`registry.example.com:5000`) — no scheme.

## 3. Push from CI

Your pipeline builds and pushes a tagged image to that repository:

```bash
docker build -t ghcr.io/acme/web:1.4.2 .
docker push ghcr.io/acme/web:1.4.2
```

By default the Kargo Warehouse only treats **SemVer** tags (`1.4.2`,
`2.0.0`) as new freight. When you set a concrete `image_repository` on the app,
suparship relaxes the Warehouse to accept any tag — tighten the tag pattern on
the Warehouse directly if you want stricter matching.

No webhook is required: Kargo polls the registry. A newly pushed tag appears as
freight within the Warehouse's polling interval.

## 4. Deploy & promote

- The **first** environment in the pipeline (lowest `Order`, e.g. `staging`)
  **auto-promotes** new freight — push a tag and it rolls out there on its own.
- Every later environment is **gated**: open the app, review the freight, and
  click **Promote** to advance it (e.g. staging → prod). suparship creates a
  Kargo Promotion; ArgoCD then syncs the new revision to that env's cluster.

The promotion order is the org environment `Order` set by the platform team
under Settings → Environments.

## 5. Watch it land

The app detail page shows, per environment:

- **status** (Healthy / Progressing / Degraded / Not deployed) and replica counts
- **Diagnostics** — if delivery is stuck, the ArgoCD/ESO reason is shown here
  with a suggested fix (bad image, secret store not ready, destination
  mismatch, …) so you rarely need to drop into ArgoCD directly
- **endpoints** (ingress URLs) once healthy

## Secrets & config (brief)

App config and secrets are layered global → env → cluster (cluster wins), and
secrets resolve through External Secrets from the platform's secret backend.
Set them per scope on the app's Secrets/Variables tabs. See
[secrets.md](secrets.md) for the full model.

## Troubleshooting quick hits

| Symptom | Likely cause | Fix |
|---|---|---|
| Pods `InvalidImageName` / `ErrImagePull` | no/placeholder `image_repository`, or missing pull secret | set a real image repository; have the platform team configure the registry |
| App "Not deployed", diagnostic shows a destination/appproject error | env not bound to a registered cluster | platform team binds the env to a cluster (Settings → Environments) |
| Secret keys don't reach pods; diagnostic "store not ready" | cluster's secret backend not finished | platform team completes the secret-backend gate on the setup checklist |
| New image tag doesn't show as freight | tag isn't SemVer (default pattern) | push a SemVer tag, or loosen the Warehouse tag pattern |
