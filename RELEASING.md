# Releasing suparShip

Maintainer guide for cutting a release. The goal: every release ships with an
upgrade path, so OSS operators never hit a silent breaking change.

## Version numbers

Three versions, all surfaced by `suparship version` (defined in
`internal/version`):

- **release** — the chart `appVersion` + container image tag (`charts/suparship/Chart.yaml`).
- **config-schema** (`version.Schema`) — the org-config format. Bump **only**
  when the persisted org ConfigMap format changes incompatibly.
- **generator** (`version.Generator`) — the emitted GitOps manifest/label
  contract. Bump when generated manifests or the `…/generator-version` label change.

See `docs/upgrading.md` for the operator-facing description.

## When to bump the contract versions

| You changed… | Bump |
|---|---|
| anything (normal release) | release tag + chart appVersion |
| the org-config struct in a way old/new binaries can't both read | `version.Schema` |
| generated manifest shape, names, or the label contract | `version.Generator` |
| only operational behaviour (no persisted-format change) | neither contract version — note it under "no action needed" |

A `version.Schema` bump is a commitment: a fresh binary reading an older
config must either tolerate it or you must add a migration step. The startup
`rbac.CheckSchema` advisory tells operators a migration is needed — make sure
`docs/upgrading.md` has the matching notes.

## Release checklist

1. **Land all changes** on `main`; CI green (`go test ./...`, UI build).
2. **Decide the version bumps** using the table above.
3. **Update `docs/upgrading.md`:** move the "Unreleased (next)" notes under the
   new version heading, and start a fresh "Unreleased" section. Spell out any
   **required operator action** explicitly (the 1Password token re-paste in the
   first post-v0.1.0 range is the model: say exactly what to click/run).
4. **Bump versions:**
   - `charts/suparship/Chart.yaml` → `version` + `appVersion`.
   - `internal/version/version.go` → `Schema` / `Generator` if warranted
     (the `Version` var itself is set at build time via ldflags).
5. **Tag + build** the image and chart with the release version.
6. **Smoke test** an upgrade from the previous release on a scratch install:
   `suparship backup` first, `helm upgrade`, watch the schema-check log, run a
   test app to Healthy.

## Principles

- **Never break silently.** If an upgrade needs operator action, it must be in
  `docs/upgrading.md` AND surfaced at runtime (startup log, setup gate, or a
  diagnostic) — not discovered by a broken deploy.
- **Backup is the safety net.** Every upgrade step in the docs starts with
  `suparship backup`; restore + chart rollback is the escape hatch.
- **Prefer tolerant decoding over schema bumps.** The Go config decoder ignores
  unknown/removed fields, so additive and field-removal changes usually need no
  schema bump — reserve bumps for genuinely incompatible format changes.
