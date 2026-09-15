# ADR-0003: Source-qualified template identity

**Status:** Accepted

## Context

A template's identity was its name, minted from the chart's `Chart.yaml`
`name` at sync. That name keys everything: the template ConfigMaps
(`suparship-template-<name>`, label `suparship.io/template-name`), every
`/api/v1/templates/{name}` route, the org-level override, app and component
pins (`template: {name, version}`), and the GitOps chart directory
`charts/<name>/<version>/`. Two sources shipping a chart with the same name
therefore collided, and the sync refused the second one ("the template name is
already provided by source …"). Platform teams routinely keep several chart
repos — a company catalog and a team's fork of it — that both contain `web`.

Two ways to fix this were considered:

1. Add a separate source dimension (a composite key `{source, name}` or a
   generated id) and thread it through storage, routes, pins and paths.
2. Make the name itself carry the source (`<source>.<chart>`), keep it the
   single identity, and lean on the existing `spec.title` for display.

## Decision

Option 2. At sync time a source that opted in (`namespaced: true`, the
default for new sources) has each imported template renamed to
`<source>.<chart>`; `metadata.source` records the source for display. The
separator is `.` because it is legal in a DNS-1123 ConfigMap name, in a label
value and in a single URL path segment, and because the chart-name sanitizer
never emits it — a qualified name can never collide with a bare one. Source
names of namespaced sources must be DNS labels so the prefix is always legal.

Namespacing is **opt-in per source** rather than applied to every source at
once, because turning it on renames templates that running apps are pinned
to. An explicit action (`POST /templates/registry/sources/{name}/namespace`)
performs that migration: re-sync under the new names, rewrite the pins of
every app on the old names (versions unchanged), republish those apps, carry
org overrides over, delete the bare entries. Sources that stay un-namespaced
keep the global-uniqueness guard among themselves.

## Consequences

- `internal/kube`, `internal/gitops`, `internal/domain`, every by-name
  resolver, the six template routes and the UI API functions are untouched:
  a qualified name is just a name. The change lives in the sync engine, the
  registry model, the list DTO (`source`) and the gallery/pickers (title +
  source chip).
- The rendered manifests of a migrated app do not change; only the chart
  directory in the GitOps repo moves, so ArgoCD re-resolves the source path
  once without a diff.
- Template names are now validated (`[a-z0-9.-]`, no leading/trailing
  separator). Existing names produced by the sanitizer already comply.
- A `.` in a name means "namespaced"; nothing else may introduce one.

## Migration strategy

Nothing happens on upgrade. Operators namespace legacy sources one at a time
from Settings → Templates → Sources when they are ready to absorb the
republish. Exports carry `namespaced` so a restored install recreates the
source the same way.
