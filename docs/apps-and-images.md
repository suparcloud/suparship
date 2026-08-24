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

## 1. Create the app and point it at your image

App → New, pick a template, and set the image **in the chart's own values** —
typically `image.repository` (bare reference: host + path, **no** `https://`,
**no** `:tag`, **no** `@sha256:…`) via the values editor or the template's
developer-values form. There is no separate image field on the app: the chart
defines where its image lives, and suparship reads it from the values.

Then bind it for CD: on the app's Overview, image settings list every image
**discovered** in the effective values (chart defaults ⊕ template/org
overlays ⊕ your own overlay — every image block with a repository). Pick the
ones CD should manage; each binding is keyed by the chart's own **tag path**
(`image.tag` in the example charts), which is where suparship writes the
promoted tag. Unbound images (sidecars, init containers) are simply left
alone. For a composed app, bindings live per component, against that
component's chart. No bindings = no CD: suparship writes no Kargo Warehouse
and promotions stay paused until you bind an image.

> The example charts default to public demo images, so an app with no image
> configured still deploys something runnable — set `image.repository` to
> your build when you're ready.

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
docker build -t ghcr.io/acme/web:$(git rev-parse --short=7 HEAD) .
docker push ghcr.io/acme/web:$(git rev-parse --short=7 HEAD)
```

By default a binding's Warehouse subscription accepts **7-character git SHA**
tags (`^[0-9a-f]{7}$`) and promotes the most recently pushed one
(`NewestBuild`). Both are per-binding settings in the image editor — set a
`tagPattern` and/or a `selectionStrategy` (`SemVer`, `Digest`, `Lexical`) to
match your CI's tag scheme.

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
| Pods `InvalidImageName` / `ErrImagePull` | bad `image.repository` in the values, or missing pull secret | fix the image repository in the app's values; have the platform team configure the registry |
| App "Not deployed", diagnostic shows a destination/appproject error | env not bound to a registered cluster | platform team binds the env to a cluster (Settings → Environments) |
| Secret keys don't reach pods; diagnostic "store not ready" | cluster's secret backend not finished | platform team completes the secret-backend gate on the setup checklist |
| New image tag doesn't show as freight | tag doesn't match the binding's pattern (default: 7-char git SHA) | push a matching tag, or change the binding's `tagPattern`/`selectionStrategy` |
| Promote does nothing, no Warehouse exists | no image binding on the app/component | bind an image in the app's image settings |
