# Releasing suparship

Maintainer guide for cutting a release. The goal: every release ships with an
upgrade path, so OSS operators never hit a silent breaking change.

Releases are automated by [release-please](https://github.com/googleapis/release-please).
You do not tag by hand, edit `CHANGELOG.md`, or bump `Chart.yaml` — merging one
pull request does all of it.

## How it works

Every push to `main` re-runs `release-please`, which reads the conventional
commits since the last release and maintains a standing **`chore: release X.Y.Z`**
pull request. That PR is the release: it accumulates the changelog and the
version bump as you land work, and sits there until you decide to ship.

Merging it:

1. writes `CHANGELOG.md`,
2. bumps `charts/suparship/Chart.yaml` — both `version` and `appVersion`,
3. creates the `vX.Y.Z` tag and the GitHub Release.

That single merge then drives the two existing workflows:

| Trigger | Workflow | Result |
|---|---|---|
| the `vX.Y.Z` tag | `release-image` | `ghcr.io/suparcloud/suparship` at `X.Y.Z`, `X.Y`, `X`, and `latest` |
| the `Chart.yaml` bump | `release-chart` | chart published to `suparcloud/charts` |

Nothing else needs to happen for the artifacts to exist.

### What the version numbers do

Chart `version` and `appVersion` are **locked together** and equal to the release
tag. This chart is suparship's install artifact, not an independent library
chart, so there is no case where they should diverge — and it removes a whole
class of "which one do I bump" mistakes. Both lines carry an
`# x-release-please-version` annotation; leave them alone.

Image tags follow from that: `latest` is the newest **release**, `edge` is the
tip of `main`, and `main-<sha>` pins an individual main build. An operator
pulling `:latest` gets a released build, never unreleased code.

### What decides the number

Conventional commit prefixes, over the commits since the last release:

| Commits since last release | Bump |
|---|---|
| any `feat:` | minor (`0.1.0` → `0.2.0`) |
| only `fix:` / `perf:` / `refactor:` | patch (`0.1.0` → `0.1.1`) |
| `feat!:` or a `BREAKING CHANGE:` footer | minor while pre-1.0, major after |

Pre-1.0 the config sets `bump-minor-pre-major`, so a breaking change bumps the
minor rather than declaring 1.0 by accident.

Because the version is computed from commit messages, **the commit message is
the release note**. A commit landing on `main` with a non-conventional subject
is invisible to the changelog.

## Contract versions — still manual, on purpose

Three versions ship, all surfaced by `suparship version` (defined in
`internal/version`):

- **release** — the tag, chart `version`/`appVersion`, image tag. Automated.
- **config-schema** (`version.Schema`) — the org-config format. **Manual.**
- **generator** (`version.Generator`) — the emitted GitOps manifest/label
  contract. **Manual.**

The two contract versions are judgement calls about data compatibility, not
facts derivable from commit messages, so release-please does not touch them.

| You changed… | Bump |
|---|---|
| anything (normal release) | nothing by hand — release-please handles it |
| the org-config struct so old/new binaries can't both read it | `version.Schema` |
| generated manifest shape, names, or the label contract | `version.Generator` |
| only operational behaviour (no persisted-format change) | neither — note it under "no action needed" |

A `version.Schema` bump is a commitment: a fresh binary reading an older config
must either tolerate it or you must add a migration step. The startup
`rbac.CheckSchema` advisory tells operators a migration is needed — make sure
`docs/upgrading.md` has the matching notes.

## The release checklist

The standing release PR is the checkpoint. Before merging it:

1. **Review the generated changelog.** It is the commit history; if an entry
   reads badly, that commit message was the problem.
2. **Decide the contract-version bumps** using the table above. If either is
   warranted, push the `internal/version/version.go` change to `main` — the
   release PR picks it up automatically.
3. **Roll `docs/upgrading.md`:** move the "Unreleased (next)" notes under the new
   version heading and start a fresh "Unreleased" section. Spell out any
   **required operator action** explicitly — the 1Password token re-paste in the
   first post-`v0.1.0` range is the model: say exactly what to click or run.
4. **Merge the release PR.**
5. **Smoke test the upgrade** from the previous release on a scratch install:
   `suparship backup` first, `helm upgrade`, watch the schema-check log, run a
   test app to Healthy.

`CHANGELOG.md` and `docs/upgrading.md` are not redundant: the changelog says
**what changed**, `upgrading.md` says **what you must do about it**. Only the
first is mechanizable — keep writing the second by hand.

## One-time setup

The first release was pinned with `"release-as": "0.1.0"` in
`release-please-config.json`: with no prior tag, release-please ignores the
manifest baseline and would otherwise start at `1.0.0`. **Remove `release-as`
after the v0.1.0 release PR merges** — left in place, every later release PR
would keep proposing `0.1.0`. From then on the manifest tracks the last tag and
needs no hand edits.


`RELEASE_PLEASE_TOKEN` — a PAT with `contents: write` and `pull-requests: write`,
set as a repository secret. Without it the workflow falls back to `GITHUB_TOKEN`,
which still cuts the release, but tags pushed with `GITHUB_TOKEN` **do not start
new workflow runs** — so `release-image` will not fire and no versioned image is
built. The fallback path is recoverable: dispatch `release-image` manually
against the new tag. The PAT just makes it automatic.

## Principles

- **Never break silently.** If an upgrade needs operator action, it must be in
  `docs/upgrading.md` AND surfaced at runtime (startup log, setup gate, or a
  diagnostic) — not discovered by a broken deploy.
- **Backup is the safety net.** Every upgrade step in the docs starts with
  `suparship backup`; restore + chart rollback is the escape hatch.
- **Prefer tolerant decoding over schema bumps.** The Go config decoder ignores
  unknown/removed fields, so additive and field-removal changes usually need no
  schema bump — reserve bumps for genuinely incompatible format changes.
- **Ship from `main`.** There is no release branch. `main` is always releasable;
  the release PR decides *when*, not *what*.
