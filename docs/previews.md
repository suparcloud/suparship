# Previews

A **preview** is an ephemeral, isolated deployment of an app — typically one per
pull request or branch — that lets you exercise a change end-to-end before it
reaches a stable environment. A preview gets its own namespace
(`{app}-{previewName}`, e.g. `api-pr-42`) and a deterministic URL
(`http://{previewName}.{app}.preview.{baseDomain}`), runs at a single replica by
default, and is **not** part of the staging → prod promotion chain (it has
`Order = 0`). Delete it when the PR closes and it's gone — no lingering state.

**Design principle: clone a base env, don't reconfigure.** A preview is meant to
be cheap and opinionated. Rather than carrying its own full configuration, it
**clones one stable environment** ("the base env") and reuses that env's cluster,
config, and secrets. You layer small, app-wide preview overrides on top — you do
not hand-configure each PR. The bias is toward a sensible default over a
per-preview knob.

## Opting in

An app supports previews when **`AppSpec.PreviewsEnabled`** is true (the default
for new apps). A preview deploys **all of the app's enabled components** — there
is no per-component preview gate, so worker-only apps (e.g. a LiveKit agent)
preview fine. Toggle it on **Overview → Previews enabled**, or via the app
update API (`previewsEnabled: true|false`). When disabled, the **Preview** button
is greyed out and the create API returns `422`.

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
**per-PR** override. Precedence is **base env → preview band → per-PR** (later
wins):

| Layer | Where it's stored |
|-------|-------------------|
| Base env | the stable env's own values / `<app>-config` ConfigMap / `<app>-env-<baseEnv>` vault item |
| Preview band (all previews) | `EnvironmentDefaults["preview"]` (values + env vars) and the `<app>-env-preview` item **inside the base env vault** |
| Per-PR (one preview) | `<app>-env-preview-pr-<name>` item inside the base env vault (read if present) |

**No per-preview vault is ever created.** Preview secrets are extra *items*
inside the base env's existing vault and are read through its existing
ClusterSecretStore — the same pattern cluster overrides use. This scales to N
open PRs with zero new vaults or stores. The non-secret ConfigMap mirrors the
same layering.

Charts can also branch on the platform token **`{platform.envType}`** (which is
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
`pr-42`), pick a **Base env** if you don't want the default → **Create**. The
preview appears under the app's **Previews** tab and on the global
[Previews](/previews) page.

### From the API

```http
POST /api/v1/projects/{project}/apps/{app}/previews
Content-Type: application/json

{ "name": "pr-42" }                 # clones the default base env (staging)
{ "name": "pr-42", "baseEnv": "prod" }  # opt-in: base on prod
```

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
# on PR opened/synchronize
curl -fsS -X POST "$SUPARSHIP_API/projects/$PROJECT/apps/$APP/previews" \
  -H "Authorization: Bearer $SUPARSHIP_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"pr-${PR_NUMBER}\"}"

# on PR closed
curl -fsS -X DELETE "$SUPARSHIP_API/projects/$PROJECT/apps/$APP/previews/pr-${PR_NUMBER}" \
  -H "Authorization: Bearer $SUPARSHIP_TOKEN"
```

To give a specific PR its own secrets, write the `<app>-env-preview-pr-<name>`
item into the base env vault before (or after) creating the preview; the
ExternalSecret references it automatically when present.

## How it's published

Creating a preview persists an `AppEnvironment` (`EnvType=preview`) and publishes
to GitOps: a values file, the `<app>-config` ConfigMap, and an ExternalSecret
that merges `base-env → preview band → per-PR` items, all reading the **base
env's** store. ArgoCD then reconciles the preview namespace. Promotion is
one-directional — a preview can be promoted *to* a stable env, never the reverse.

## See also

- [secrets.md](secrets.md) — the scope/vault model the preview band reuses.
- [app-model.md](app-model.md) — `AppSpec`, `EnvironmentDefaults`, environments.
- [templates-components.md](templates-components.md) — components and `enabled`.
