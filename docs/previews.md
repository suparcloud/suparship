# Previews

A **preview** is an ephemeral, isolated deployment of an app — typically one per
pull request or branch — that lets you exercise a change end-to-end before it
reaches a stable environment. A preview gets its own namespace (default
`{project}-{app}-preview-{name}`, e.g. `voiceai-api-preview-pr-42`; configurable
per project — see below) and a deterministic URL
(`http://{previewName}.{app}.preview.{baseDomain}`, only when the app exposes an
HTTP route), runs at a single replica by default, and is **not** part of the
staging → prod promotion chain (it has `Order = 0`). Delete it when the PR closes
and it's gone — no lingering state.

**Namespace pattern.** The preview namespace is configurable per project in
**Project → Settings → Namespace patterns → Preview namespace**, or via
`PUT /api/v1/projects/{project}/naming` (`previewNamespacePattern`). Tokens:
`{project}`, `{app}`, `{name}` (the preview name). Blank uses the default
`{project}-{app}-preview-{name}`.

**Shared preview namespace.** Omit `{name}` from the pattern (e.g.
`{project}-previews`) to put **every preview of the project in one namespace**.
suparship then suffixes its own platform resources per preview — the env
ConfigMap becomes `{app}-{name}-config` and the ExternalSecret/Secret
`{app}-{name}-secrets`, and the `((platform.configMapName))` / `((platform.secretName))`
tokens resolve to those suffixed names automatically. **Workload** resources are
the chart's responsibility: include `((platform.previewName))` (the PR id, e.g.
`pr-42`) in the chart's name, typically in `fullnameOverride`:

```yaml
# preview overrides for a shared-namespace project
fullnameOverride: "((platform.project))-((platform.app))-((platform.previewName))"
```

Without this, two previews of the same app would collide in the shared namespace.

**Design principle: clone a base env, don't reconfigure.** A preview is meant to
be cheap and opinionated. Rather than carrying its own full configuration, it
**clones one stable environment** ("the base env") and reuses that env's cluster,
config, and secrets. You layer small, app-wide preview overrides on top — you do
not hand-configure each PR. The bias is toward a sensible default over a
per-preview knob.

## Opting in

An app supports previews when **`AppSpec.PreviewsEnabled`** is true (the default
for new apps). Previews are an **app-level** concept: the preview mirrors exactly
what the base env deploys — there is no per-component preview gate — so worker-only
apps (e.g. a LiveKit agent) and apps that deploy via a chart / raw values without
enumerated components preview fine. Toggle it on **Overview → Previews enabled**,
or via the app update API (`previewsEnabled: true|false`). When disabled, the
**Preview** button is greyed out and the create API returns `422`.

## Base environment

Every preview clones exactly one stable env:

- **Default:** the project's first stable env by promotion order (conventionally
  `staging`).
- **Opt-in:** pass `baseEnv: "prod"` (or any stable env name) to base a preview
  on a different env — for the rare prod-shaped preview. In the UI this is the
  **Base env** dropdown on the create form.

The preview inherits the base env's **active cluster**, its merged **env vars**,
and its **secrets** — exactly what the base env itself resolves.

## Overlays (values, env vars, secrets)

On top of the base env, a preview applies a reserved per-app **preview band** —
one configuration that applies to *every* preview of the app — and, optionally, a
**per-PR** override. Precedence is **template preview values → base env →
preview band → per-PR** (later wins):

| Layer | Where it's stored |
|-------|-------------------|
| Template preview values | the template's `previewDefaultValues` (template.yaml ⊕ the org template override's) — the template's generic preview shape (one replica, smaller resources). Sits **below** the app's own values so a preview still looks like the env it clones |
| Base env | the stable env's own values (app values ⊕ per-env / per-component overrides) / `<app>-config` ConfigMap / `<app>-env-<baseEnv>` vault item |
| Preview band (all previews) | `EnvironmentDefaults["preview"]` (values + env vars) and the `<app>-env-preview` item **inside the base env vault** |
| Per-PR (one preview) | `<app>-env-preview-pr-<name>` item inside the base env vault (read if present) |

**No per-preview vault is ever created.** Preview secrets are extra *items*
inside the base env's existing vault and are read through its existing
ClusterSecretStore — the same pattern cluster overrides use. This scales to N
open PRs with zero new vaults or stores. The non-secret ConfigMap mirrors the
same layering.

Charts can also branch on the platform token **`((platform.envType))`** (which is
`"preview"` in a preview) for anything that must differ structurally.

### Configuring the overlays in the UI

- **Values:** Overview → **VALUES** dropdown → **"preview overrides (all
  previews)"** (shown when previews are enabled).
- **Env vars & secrets:** the **Env Vars** tab → the **"Preview band"** box
  (labelled with the base env). *Preview variables* edits the env-var band;
  *Preview secrets* writes the `<app>-env-preview` vault item.

### Configuring the overlays via the API

- Preview secrets band:
  `POST /api/v1/projects/{project}/apps/{app}/secrets/env/{baseEnv}/preview`
- Preview env-var band (writes `EnvironmentDefaults["preview"]`):
  `PUT  /api/v1/projects/{project}/apps/{app}/envs/preview/envconfig`
- Per-PR secret items (`<app>-env-preview-pr-<name>`) are written out-of-band
  (e.g. by CI) into the base env vault and are picked up automatically.

## Launching a preview

### From the UI

Open the app → click **Preview** (top-right) → enter a preview name (e.g.
`pr-42`), optionally pick a **Base env** and an **Image tag** (leave the tag
blank to inherit the base env's image) → **Create**. The preview appears under
the app's **Previews** tab and on the global [Previews](/previews) page.

### From the API

```http
POST /api/v1/projects/{project}/apps/{app}/previews
Content-Type: application/json

{ "name": "pr-42" }                              # clone staging, inherit its image
{ "name": "pr-42", "baseEnv": "prod" }           # opt-in: base on prod
{ "name": "pr-42", "imageTag": "sha-abc1234" }   # deploy a specific image tag
```

**`imageTag`** overrides the tag for every **CD-bound** image — each binding's
`tagKey` in the app's (or, composed, each component's) values (omit it to
inherit the base env's image). Create is an **upsert**: re-POSTing an existing
preview re-publishes it with the new tag — `201` on first create, `200` on
update — so CI can push a fresh image and re-point the preview on every commit.

**Everything else pins via `((platform.imageTag))`.** `imageTag` only reaches
the bound tag keys. Any other image in the values — a sidecar, an init
container, or a chart with no binding at all — would otherwise fall back to
its chart default (often `:latest`) and fail to pull in a preview. Pin it with
the **`((platform.imageTag))`** token in the app's values, its preview
overrides, or the template's `previewDefaultValues` (the dev seeder sets the
`web` template's org override to `image: {tag: ((platform.imageTag))}` for
exactly this). The token is always resolved: the per-PR tag in previews, `""`
in stable envs (where CD owns the tag and the chart default applies):

```yaml
sidecar:
  image:
    repository: acr.io/org/app   # already correct in your values
    tag: ((platform.imageTag))   # tracks the same build as the main container
```

> **Token syntax.** Platform tokens use the `(( ))` delimiter —
> `((platform.*))` and `((vars.*))`. Unlike the older `[[ ]]` form, `(( ))` is
> YAML-safe: `tag: ((platform.imageTag))` parses as a plain string, so it no
> longer needs quoting. The legacy `[[ ]]` delimiter still resolves (quote it in
> YAML), but new values should use `(( ))`.

Tear down with:

```http
DELETE /api/v1/projects/{project}/apps/{app}/previews/pr-42
```

Preview names are sanitised to a DNS label; raw inputs like `feature/my-branch`
or `PR-42` are accepted and normalised.

### From CI (per pull request)

Create a preview on PR open/update and delete it on close. See
[`examples/preview-from-pr.yml`](../examples/preview-from-pr.yml) for a GitHub
Actions workflow. The shape is:

```bash
# on PR opened/synchronize — build & push the image first, then point the
# preview at that tag. The upsert re-publishes on every push (201 then 200).
curl -fsS -X POST "$SUPARSHIP_API/projects/$PROJECT/apps/$APP/previews" \
  -H "Authorization: Bearer $SUPARSHIP_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"pr-${PR_NUMBER}\",\"imageTag\":\"${COMMIT_SHA}\"}"

# on PR closed
curl -fsS -X DELETE "$SUPARSHIP_API/projects/$PROJECT/apps/$APP/previews/pr-${PR_NUMBER}" \
  -H "Authorization: Bearer $SUPARSHIP_TOKEN"
```

To give a specific PR its own secrets, write the `<app>-env-preview-pr-<name>`
item into the base env vault before (or after) creating the preview; the
ExternalSecret references it automatically when present.

## Route: send a stable hostname to a preview

External services — a partner's webhook, a mobile build, a shared QA harness —
often know one URL: `https://myapp.staging.acme.com`. **Route** lets a PR answer
at that URL without touching staging's image or its delivery pipeline.

> **Route sends staging's hostname to the preview; pin sends the preview's image
> to staging.** Route when you need the stable URL. Pin when you need the image
> deployed there (a rollback hold, or a deliberate unmerged deploy).

Two mechanisms, same command. An app with [platform-owned routes](routing.md)
is routed by a **backend switch**: staging's HTTPRoute keeps its hostname and
its backend now names the preview's Service — one publish per side, seconds,
no ArgoCD wait. An app whose chart owns its ingress is routed by the **host
swap** described below.

```http
POST   /api/v1/projects/{project}/apps/{app}/environments/{env}/route   {"fromPreview": "pr-42"}
DELETE /api/v1/projects/{project}/apps/{app}/environments/{env}/route
```

While routed:

| who | serves | notes |
|---|---|---|
| preview `pr-42` | `myapp.staging.acme.com` | staging's normal host; the preview's own `pr-42.myapp.preview.…` host is not served |
| env `staging` | `myapp-origin.staging.acme.com` | the alternate host, one label deep so a wildcard cert still covers it |

Composed apps route per component: `myapp-api.staging.…` → the preview,
`myapp-api-origin.staging.…` → the env.

Nothing changes in the image pipeline. Staging keeps receiving mainline freight,
Kargo auto-promotion keeps flowing to prod, and manual promotion works as usual.
Mechanically it is a host swap: only the routing tokens change on both sides —
`((platform.routingHost))`, the per-tier `((platform.externalRoutingHost))` /
`((platform.internalRoutingHost))`, or a host you composed from
`((platform.appRoutingName))` / `((platform.appComponentRoutingName))` (see
[byo-charts.md](byo-charts.md#wiring-platform-context-with-platform-tokens)) —
so it works for any chart and any app that exposes an HTTP route,
direct-delivery apps included. The host shapes above assume the default
`routingHost`; with a composed host the same rule applies to whatever you
built: the preview gets the env's name, the env gets `-origin`.

Guardrails:

- **Prod is never routed.** `env` must be a non-prod stable env, and the preview
  must be based on it (`baseEnv`), since it reuses that env's cluster and vault.
- **Only platform-managed hosts move.** Route refuses (422) when an exposed
  component's effective values carry no routing token — a literal host would
  stay put on both sides. The error names the component and the tokens.
- **Names must fit a DNS label.** Launching a preview is refused when
  `{app}-{component}-{preview}` for an exposed component exceeds 63 characters,
  since a host composed from the name tokens could not be created.
- **One preview per env.** Routing a second preview replaces the first, which
  goes back to its own host in the same operation.
- **Deleting the preview restores the env** before the preview's files are
  pruned, so the hostname never goes dark. Undeploying a routed env is refused
  until it is restored.
- **A PR push keeps the route.** Re-POSTing the preview with a new image tag
  re-renders it on the routed host; nothing in the workflow changes.
- **Developer-callable**, the same role as launching the preview. Stacks fan it
  out across members: see [stacks.md](stacks.md).

Two things to know about the edge:

- **The switch takes a few seconds and is not seamless.** ingress-nginx's
  admission webhook refuses an Ingress that claims a host and path another
  Ingress still holds, so the side giving the hostname up is published first
  (staging moves to `-origin`), suparship waits for ArgoCD to apply it, and only
  then is the preview published to claim it. Restore runs in the opposite order:
  the preview releases, then staging takes its hostname back. Between the two
  syncs the hostname answers with the controller's default 404. If the wait
  times out, the claim is published anyway and the Applications' sync retry
  policy finishes the handover.
- **Certificates.** If your chart issues a per-release certificate (the example
  `web` chart uses one `<release>-tls` secret covering its hosts), each route or
  restore triggers a re-issue for the new host set and a brief TLS gap. A
  wildcard certificate on the env's domain — what the example `gateway` chart
  sets up — makes the switch instant and avoids ACME rate limits.

The UI shows a 🔀 badge on both envs. The env's page shows where its traffic
goes and its alternate host; both pages offer **Restore**.

### From CI (route on a label)

CI only has to say *when* to route: the route survives PR pushes, and closing
the PR restores staging through the preview delete. A PR label models that state
well — visible in the PR, gated by repo write access, handled by the
`labeled`/`unlabeled` events. The preview token's developer role is enough.

```bash
# on label "route-staging" added
curl -fsS -X POST "$SUPARSHIP_API/projects/$PROJECT/apps/$APP/environments/staging/route" \
  -H "Authorization: Bearer $SUPARSHIP_TOKEN" -H 'Content-Type: application/json' \
  -d "{\"fromPreview\":\"pr-${PR_NUMBER}\"}"

# on label removed — restore while the PR stays open
curl -fsS -X DELETE "$SUPARSHIP_API/projects/$PROJECT/apps/$APP/environments/staging/route" \
  -H "Authorization: Bearer $SUPARSHIP_TOKEN"
```

**Route and restore are asynchronous by default.** They wait for ArgoCD
between their two publishes, which takes longer than a typical ingress or
gateway timeout, so the calls above return `202 Accepted` with a task:

```jsonc
{ "taskId": "pintask_…", "state": "pending",
  "statusUrl": "/api/v1/projects/{project}/tasks/pintask_…" }
```

Poll `statusUrl` (any project viewer can read it) until `state` is
`succeeded` or `failed`. While it runs the task reports what it is doing:

```jsonc
{ "id": "pintask_…", "kind": "route-app", "state": "running",
  "phase": "wait",                       // release → wait → claim
  "message": "waiting for ArgoCD to apply the hostname release on staging",
  "createdAt": "…", "updatedAt": "…" }

{ "id": "pintask_…", "state": "succeeded", "status": 200,
  "result": { "host": "myapp.staging.acme.com", "from": "pr-42", "message": "…" } }
```

`result` is exactly what the synchronous call would have returned (the
per-member rows for a stack route); a failed task carries `status` and `error`.
Tasks stay queryable for 30 minutes. To get the inline result instead — only
sensible when nothing in front of suparship will time out — pass `?async=0` or
send `Prefer: wait`.

Both example workflows carry a working version:
[`examples/preview-from-pr.yml`](../examples/preview-from-pr.yml) (per app) and
[`examples/stack-preview-from-pr.yml`](../examples/stack-preview-from-pr.yml)
(`route-<env>` labels beside `pin-<env>`, one call for the whole stack). Two
details they handle: the label may arrive before the preview exists (the route
returns 404 — the job re-applies the label's route once the preview is created),
and a second PR taking the same label replaces the first (the first PR's preview
quietly returns to its own URL; check the env's `routedToPreview` first if you
want to refuse that).

## Stack previews (preview a whole collection in one call)

When your service is a [stack](stacks.md) of apps, you can preview **every member
at once** instead of looping over each app. The whole collection comes up in one
shared namespace — `{project}-{stack}-preview-{name}` — so members reach each
other by in-cluster DNS (e.g. `web` → `agent-server`), exactly as a stack's
shared namespace does for stable envs.

```http
POST   /api/v1/projects/{project}/stacks/{stack}/previews
DELETE /api/v1/projects/{project}/stacks/{stack}/previews/{name}
```

```jsonc
{ "name": "pr-42" }                             // all previewable members, base env's image
{ "name": "pr-42", "imageTag": "sha-abc1234" }  // re-point every member at this tag
{ "name": "pr-42", "baseEnv": "prod" }          // clone prod instead of staging
{ "name": "pr-42", "apps": ["web", "agent"] }   // only these members
```

- **Developer-callable** (same role as the per-app preview route), so CI can hit
  it with a project developer token.
- **Upsert**, per member — re-POSTing re-points every member at a new `imageTag`,
  so CI can call it once per PR **push**. The response is a per-app summary
  (`{app, ok, skipped, message, error}` rows).
- **Skips** members with previews disabled (`PreviewsEnabled=false`) — a skipped
  row, not a failure — so a mixed stack previews cleanly.
- **`imageTag` applies to every member.** This fits the monorepo case where all
  apps are built from one commit SHA. If members use different tags, omit it (each
  inherits its base env image) or drive them via the per-app endpoint.

The big win over the per-app loop: **membership is resolved server-side**, so
adding or removing a member app needs no change to your CI workflow. See
[`examples/stack-preview-from-pr.yml`](../examples/stack-preview-from-pr.yml) for
a once-per-PR GitHub Actions workflow.

## How it's published

Creating a preview persists an `AppEnvironment` (`EnvType=preview`) and publishes
to GitOps: a values file, the `<app>-config` ConfigMap, and an ExternalSecret
that merges `base-env → preview band → per-PR` items, all reading the **base
env's** store. ArgoCD then reconciles the preview namespace. Promotion is
one-directional — a preview can be promoted *to* a stable env, never the reverse.
A preview that is routed a stable env's hostname is rendered on that host (and
the env on its `-origin` host) by the same values mapper, so every republish of
either side preserves the swap until it is restored.

## See also

- [secrets.md](secrets.md) — the scope/vault model the preview band reuses.
- [app-model.md](app-model.md) — `AppSpec`, `EnvironmentDefaults`, environments.
- [templates-components.md](templates-components.md) — composed-app components and `previewEnabled`.
