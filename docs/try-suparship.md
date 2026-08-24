# Try suparship

The fastest honest way to evaluate suparship: a real cluster on your laptop
running the full golden path — Git push → CI → preview environments →
staging → promotion to prod — on a working demo app. Works on **macOS** and
**Linux** (Ubuntu/Debian and RHEL/CentOS/Fedora).

Two speeds:

- **60 seconds, no cluster** — the UI with seeded data, backend in-process:

  ```bash
  git clone https://github.com/suparcloud/suparship && cd suparship
  cp .env.example .env
  task dev        # → http://localhost:5173  (admin@local / admin123)
  ```

  Good for a feel of the surfaces; nothing actually deploys.

- **The real thing (~15 minutes)** — everything below.

## Prerequisites

| Tool | macOS | Ubuntu / Debian | RHEL / CentOS / Fedora |
|---|---|---|---|
| Docker (rootful¹) | [OrbStack](https://orbstack.dev) or Docker Desktop | `docker.io` / [docker-ce](https://docs.docker.com/engine/install/) | [docker-ce](https://docs.docker.com/engine/install/) |
| kind, ctlptl, tilt | `brew install kind tilt-dev/tap/ctlptl tilt-dev/tap/tilt` | release binaries / curl installers (links in the check below) | same |
| kubectl, helm | `brew install kubectl helm` | vendor repos or release binaries | same |
| Node.js (npm) | `brew install node` | `nodesource` / distro package | same |
| htpasswd | preinstalled | `apache2-utils` | `httpd-tools` |
| go-task | `brew install go-task` | [taskfile.dev/install](https://taskfile.dev/installation/) | same |

¹ the dev ingress publishes host port **80**; rootless docker can't bind it.

`task up` runs a preflight first and prints exactly what's missing with
install links — you don't need to pre-check this table.

## Start it

```bash
git clone https://github.com/suparcloud/suparship && cd suparship
task dev:dns          # once per machine: *.localhost → 127.0.0.1
                      # (no-op on distros where it already resolves)
task up               # kind cluster + ArgoCD, Kargo, Gitea+CI, Vault, ingress,
                      # registries + suparship itself (first run: a few minutes)
task demo:shipnotes   # second terminal (or ▶ demo-shipnotes in the Tilt UI):
                      # deploys the shipnotes demo end-to-end
```

`task up` prints an endpoints table with every URL and credential (one dev
password everywhere: **`admin123`**; suparship login **`admin@local`**). The
same table lives on the `endpoints` resource in the Tilt UI
(<http://localhost:10350>) with clickable links.

`task demo:shipnotes` mirrors the
[shipnotes demo app](https://github.com/suparcloud/suparship-demo) (React +
FastAPI + Postgres) into the local Gitea, wires its CI, waits for the first
image build, and creates it as a composed app — three components: `frontend`
(routed), `api` (internal), `db` (stateful Postgres). It prints the tour when
done.

## The tour

1. **Browse it** — <http://shipnotes-frontend.staging.localhost>. The footer
   shows the exact CI-built tag that's running; notes you add round-trip
   through the api into Postgres, with `DATABASE_URL` delivered from Vault
   through the platform's secret contract — the app itself has zero
   platform-specific code.
2. **The app in suparship** — <http://localhost:5173> → `demo` → `shipnotes`:
   the environment pipeline, per-component status, deployments, logs.
3. **Variables & secrets** — Settings → Variables & secrets: `POSTGRES_DB`
   is an app *variable*; `DATABASE_URL`, `POSTGRES_USER`, and
   `POSTGRES_PASSWORD` are Vault-backed *secrets* (values never shown, never
   in git). The db component maps the credentials into its own curated
   `shipnotes-db-config` / `shipnotes-db-secrets` objects — see the db card's
   **Variables** panel — and its chart just `envFrom`s two names. Nothing
   sensitive lives in chart values.
4. **Preview environments** — open a PR in the Gitea mirror
   (<http://localhost:3000/gitops/suparship-demo>, `gitops` /
   `gitops-dev-only`): edit any file on a branch → open the PR → CI builds
   both images at `pr-<n>-<7sha>` and a preview appears at
   `http://pr-<n>.shipnotes-frontend.preview.localhost`. Every push re-points
   it; closing the PR tears it down.
5. **Staging follows main** — merge the PR: CI builds `main-<7sha>`, the
   app's Warehouse discovers the tag, and staging rolls to it (CD managed).
6. **Promote to prod** — in the app UI hit **Promote** (staging → prod).
   <http://shipnotes-frontend.prod.localhost> then serves the *same immutable
   tag* — promotion moves the artifact, it never rebuilds.
7. **Keep going** — per-component values and variables (the component cards),
   rollback (re-promote a previous freight from the deployment history), the
   Kargo UI's freight timeline (<http://localhost:8083>), ArgoCD's view of
   the composed app (<http://localhost:8081>), and
   [bring-your-own-charts](byo-charts.md) — that IS the model: every one of
   the demo's components (the `web` chart behind frontend/api, the Postgres
   behind db) is a plain Helm chart imported from `examples/charts/` via the
   template registry, configured purely through its own values with
   `((platform.*))` tokens.

## When something doesn't look right

- **A `.localhost` URL doesn't resolve** → run `task dev:dns` once (the
  endpoints table warns about this).
- **CI runs sit queued** → re-trigger the `act-runner` resource in the Tilt
  UI.
- **First runs are slow on purpose**: `task up` pulls images and installs
  charts; the first CI build pulls the job image and builds cold. Both are
  cached afterwards.
- Everything else: the [contributor guide](contributor-guide/hacking-on-suparship.md)
  documents the plumbing and a full troubleshooting list.

## Tear it down

```bash
task down            # stop the dev loop (keeps the cluster)
task cluster:delete  # delete the cluster + local registry entirely
```

When you're ready for a real install on your own cluster:
[docs/install.md](install.md).
