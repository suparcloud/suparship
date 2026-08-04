import { Suspense, lazy, useEffect, useRef, useState } from "react";

import {
  fetchTemplateEffectiveValues,
  previewTemplateEffectiveValues,
} from "../lib/templates";
import {
  diffOverlay,
  mergeOverlay,
  parseYamlOverlay,
  stringifyOverlay,
} from "../lib/yamlDoc";
import type {
  ComponentCreate,
  ComponentEnvVar,
  ComponentImage,
  ComponentSummary,
  EffectiveValuesResponse,
  TemplateSummary,
  ValueField,
} from "../types";
import { hasProjection, stringifyProjection } from "../lib/valuesProjection";
import type { ConfigVariables } from "../lib/configVars";

// CodeMirror is heavy; load it only when the compose canvas is shown.
const ValuesEditor = lazy(() => import("./ValuesEditor"));

// ComponentDraft is the UI-editable form of one composed-app component:
// structural fields (name / template / type / expose) plus PER-ENV Helm values
// overlays (the value-based config). Component values are per-env only — there is
// no all-envs component base edited here; each env's overlay is kept as editor text
// (seeded with the platform base for that env) + its parsed diff + a parse error.
export interface ComponentDraft {
  name: string;
  /** template name (the component's own chart) */
  template: string;
  /** the chart version this component is PINNED to. Round-tripped unchanged so
   *  saving structure never moves the deployed chart; cleared when the template
   *  picker changes (a retemplate is a new chart, so it pins to latest). */
  templateVersion?: string;
  /** web | worker | job */
  type: string;
  /** disabled | internal | external */
  exposeMode: string;
  /** legacy all-envs base overlay — PRESERVED unchanged (existing components keep
   *  deploying it), not edited here. New components have {}. */
  values: Record<string, unknown>;
  /** per-env values overlay diff, keyed env name (the editable per-env override) */
  envValues: Record<string, Record<string, unknown>>;
  /** per-env editor text, keyed env — seeded with the platform base ⊕ envValues[env] */
  envValuesText: Record<string, string>;
  /** per-env YAML parse error, keyed env */
  envValuesError: Record<string, string | null>;
  /** env policy: inherit all app vars, or curate a subset */
  inheritAppVars: boolean;
  envVars: ComponentEnvVar[];
  /** Kargo image bindings (repo + tag-key path in this component's overlay) */
  images: ComponentImage[];
  /** stateful (database/cache) — renders as its own prune-disabled Application */
  stateful: boolean;
  /** effective preview inclusion — whether this component renders in preview envs */
  previewEnabled: boolean;
}

// envTextsFromValues seeds the per-env editor text map from stored per-env overlays,
// so the "untouched" check in the seed effect (text === stringify(stored)) holds
// before the platform base arrives.
function envTextsFromValues(
  envValues: Record<string, Record<string, unknown>>,
): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [env, ov] of Object.entries(envValues)) {
    out[env] = stringifyOverlay(ov);
  }
  return out;
}

// draftsToEnvComponentValues collects the per-(env, component) overlays a set of
// drafts carry, for the create/update request's `envComponentValues`, over the
// listed envs. By default only non-empty overlays are included (create — nothing to
// clear). Pass includeEmpty=true on the update path so a CLEARED overlay is sent as
// {} and the server deletes that (env, component) pair rather than leaving the stale
// value; every existing overlay is loaded into the drafts, so this never wipes an
// untouched value.
export function draftsToEnvComponentValues(
  drafts: ComponentDraft[],
  environments: string[],
  includeEmpty = false,
): Record<string, Record<string, Record<string, unknown>>> {
  const out: Record<string, Record<string, Record<string, unknown>>> = {};
  for (const env of environments) {
    for (const d of drafts) {
      const ov = d.envValues[env] ?? {};
      const name = d.name.trim();
      if (!name) continue;
      if (Object.keys(ov).length > 0 || includeEmpty) {
        (out[env] ??= {})[name] = ov;
      }
    }
  }
  return out;
}

const COMPONENT_TYPES = ["web", "worker", "job"] as const;
const EXPOSE_MODES = ["disabled", "internal", "external"] as const;

// defaultPreview is the type default for preview inclusion, mirroring the backend
// ComponentSpec.EnabledInPreview: web/worker preview by default; stateful
// (database/cache) and one-shot (job/cron) components do not.
function defaultPreview(type: string, stateful: boolean): boolean {
  if (stateful) return false;
  return type !== "job" && type !== "cron";
}

// categoryToType maps a template's category to a sensible default component
// type. Addon (database/cache) templates default to "worker" (headless, non-
// exposed). Falls back to "web" for anything unrecognised.
export function categoryToType(category: string): string {
  switch (category) {
    case "worker":
      return "worker";
    case "job":
    case "cron":
      return "job";
    case "addon":
      return "worker";
    default:
      return "web";
  }
}

// newComponentDraft seeds a component row from a template, defaulting its type
// from the template category and exposing web components externally by default.
// An addon-category template (a database/cache) seeds as stateful — its own
// prune-disabled Application, no app-var inheritance, no CD image, unexposed.
export function newComponentDraft(
  tmpl: Pick<TemplateSummary, "name" | "category">,
  name: string,
): ComponentDraft {
  const type = categoryToType(tmpl.category);
  const isAddon = tmpl.category === "addon";
  return {
    name,
    template: tmpl.name,
    type,
    exposeMode: type === "web" ? "external" : "disabled",
    values: {},
    envValues: {},
    envValuesText: {},
    envValuesError: {},
    inheritAppVars: !isAddon,
    envVars: [],
    images: [],
    stateful: isAddon,
    previewEnabled: defaultPreview(type, isAddon),
  };
}

// draftFromSummary seeds an editable draft from an existing app component (the
// edit-composed flow on the app detail page), preserving its template, expose
// mode, per-env value overlays, and env policy. The legacy all-envs base `values`
// is preserved as-is (not edited) so saving structure never wipes it.
export function draftFromSummary(c: ComponentSummary): ComponentDraft {
  const envValues = c.envValues ?? {};
  return {
    name: c.name,
    template: c.template ?? "",
    templateVersion: c.templateVersion,
    type: c.type,
    exposeMode: c.exposeMode || (c.type === "web" ? "external" : "disabled"),
    values: c.values ?? {},
    envValues,
    envValuesText: envTextsFromValues(envValues),
    envValuesError: {},
    inheritAppVars: c.inheritAppVars ?? true,
    envVars: c.envVars ?? [],
    images: c.images ?? [],
    stateful: c.stateful ?? false,
    // Effective preview inclusion from the backend (explicit override or type default).
    previewEnabled: c.enabledInPreview,
  };
}

// toComponentCreate coerces a draft to the wire shape, dropping empty optional
// fields so the request stays minimal.
export function toComponentCreate(d: ComponentDraft): ComponentCreate {
  const envVars = d.inheritAppVars
    ? []
    : d.envVars
        .filter((e) => e.name.trim())
        .map((e) => ({
          name: e.name.trim(),
          ...(e.fromConfig
            ? { fromConfig: e.fromConfig.trim() }
            : e.fromSecret
              ? { fromSecret: e.fromSecret.trim() }
              : { value: e.value ?? "" }),
        }));
  const images = d.images
    .filter((im) => im.tagKey.trim())
    .map((im) => ({
      tagKey: im.tagKey.trim(),
      ...(im.name ? { name: im.name } : {}),
      // Repository is derived from discovery; carried only for legacy fallback.
      ...(im.repository ? { repository: im.repository.trim() } : {}),
      ...(im.tagPattern ? { tagPattern: im.tagPattern.trim() } : {}),
      ...(im.selectionStrategy
        ? { selectionStrategy: im.selectionStrategy.trim() }
        : {}),
    }));
  return {
    name: d.name.trim(),
    type: d.type,
    enabled: true,
    exposeMode: d.type === "web" ? d.exposeMode : undefined,
    template: {
      name: d.template,
      // Echo the pin back so an edit can't be read as "upgrade me to latest".
      ...(d.templateVersion ? { version: d.templateVersion } : {}),
    },
    values: Object.keys(d.values).length > 0 ? d.values : undefined,
    // Only send when opting out — inherit (default) needs no field.
    inheritAppVars: d.inheritAppVars ? undefined : false,
    envVars: envVars.length > 0 ? envVars : undefined,
    images: images.length > 0 ? images : undefined,
    stateful: d.stateful ? true : undefined,
    // Send an explicit preview override only when it differs from the type default,
    // so a component left at its default keeps nil (default) semantics server-side.
    previewEnabled:
      d.previewEnabled === defaultPreview(d.type, d.stateful)
        ? undefined
        : d.previewEnabled,
  };
}

const inputCls =
  "w-full rounded-md border border-gray-300 px-2.5 py-1.5 text-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500";
const labelCls = "mb-1 block text-xs font-medium text-gray-500";

// ComposeComponents is the add-component canvas: a list of component rows, each
// picking its own template + a per-component values overlay, plus an "Add
// component" control. Controlled — the parent owns the ComponentDraft[] state.
export function ComposeComponents({
  templates,
  components,
  onChange,
  configVars,
  environments,
}: {
  templates: TemplateSummary[];
  components: ComponentDraft[];
  onChange: (next: ComponentDraft[]) => void;
  configVars?: ConfigVariables | null;
  // The env scopes to edit values for. Create passes just the base deploy env
  // (e.g. ["staging"]); manage passes the app's stable envs. Values are per-env.
  environments: string[];
}) {
  const envs = environments.length > 0 ? environments : ["staging"];
  // Per-(template, env) bases, fetched once each and cached (components sharing a
  // template+env share the fetch):
  //   - bases: chart + platform defaults, env-agnostic, for the expandable
  //     "effective" reference.
  //   - conciseBases: the CONCISE platform base for that env (template ⊕ platform
  //     overrides, NO chart defaults) used to SEED the editor and highlight the diff
  //     — identical to the app-detail per-component editor.
  const [bases, setBases] = useState<Record<string, EffectiveValuesResponse | null>>({});
  const [conciseBases, setConciseBases] = useState<
    Record<string, Record<string, unknown>>
  >({});
  //   - projections: the template's declared developer-facing values projection,
  //     from the same concise-base response (no extra round trip). When a template
  //     declares one, the editor is seeded with just those keys instead of the whole
  //     base — see the seed effect below.
  //   - fullBases: per-(template, env) effective values WITH chart defaults, used
  //     ONLY to prefill the projected keys. It is not the diff base: the interesting
  //     defaults (image, port, size) live in the chart layer that the concise base
  //     drops, so the projection has to read them from here. Fetched only for
  //     templates that declare a projection, so nothing else pays for it.
  const [projections, setProjections] = useState<Record<string, ValueField[]>>({});
  const [fullBases, setFullBases] = useState<
    Record<string, Record<string, unknown>>
  >({});
  // Rows that asked to see the whole platform base (one-way reveal; see showAllFor).
  const [showAll, setShowAll] = useState<Record<number, boolean>>({});
  const fetchedChart = useRef<Set<string>>(new Set());
  const fetchedBase = useRef<Set<string>>(new Set());
  const fetchedFull = useRef<Set<string>>(new Set());
  // Which component rows have their (on-demand) values editor expanded, by index.
  const [valuesOpen, setValuesOpen] = useState<Record<number, boolean>>({});
  // Which rows have their expandable "effective (as deployed)" reference open.
  const [effectiveOpen, setEffectiveOpen] = useState<Record<number, boolean>>({});
  // The env tab selected per component row (defaults to the first env).
  const [activeEnv, setActiveEnv] = useState<Record<number, string>>({});
  const baseKey = (template: string, env: string) => `${template} ${env}`;

  // The seed base for a (component, env): the concise PLATFORM base for that env
  // (shared per template+env) with the component's legacy all-envs base overlay
  // folded in — matching the per-component card. So an existing base shows as
  // un-highlighted context and the per-env diff is minimal relative to platform ⊕
  // base. undefined until the platform base has loaded.
  const seedBaseFor = (
    c: ComponentDraft,
    env: string,
  ): Record<string, unknown> | undefined => {
    const platform = conciseBases[baseKey(c.template, env)];
    if (platform === undefined) return undefined;
    return mergeOverlay(platform, c.values);
  };

  const templateKey = components.map((c) => c.template).join(",");
  const envKey = envs.join(",");
  useEffect(() => {
    for (const name of new Set(components.map((c) => c.template).filter(Boolean))) {
      // Env-agnostic chart base — one per template, for the effective reference.
      if (!fetchedChart.current.has(name)) {
        fetchedChart.current.add(name);
        fetchTemplateEffectiveValues(name)
          .then((res) => setBases((prev) => ({ ...prev, [name]: res })))
          .catch(() => setBases((prev) => ({ ...prev, [name]: null })));
      }
      // Concise seed base — one per (template, env): empty dev overlay +
      // asComponentOverlay (merge the stored platform override) + skipChart.
      for (const env of envs) {
        const k = baseKey(name, env);
        if (fetchedBase.current.has(k)) continue;
        fetchedBase.current.add(k);
        previewTemplateEffectiveValues(name, env, "", {}, false, true, true)
          .then((res) => {
            setConciseBases((prev) => ({
              ...prev,
              [k]: (res.values as Record<string, unknown>) ?? {},
            }));
            // The projection rides this response. When the template declares one,
            // pull the chart-inclusive base too — that's where the projected keys'
            // real defaults live.
            const fields = res.developerValues ?? [];
            setProjections((prev) => ({ ...prev, [name]: fields }));
            if (hasProjection(fields) && !fetchedFull.current.has(k)) {
              fetchedFull.current.add(k);
              previewTemplateEffectiveValues(name, env, "", {}, false, true, false)
                .then((full) =>
                  setFullBases((prev) => ({
                    ...prev,
                    [k]: (full.values as Record<string, unknown>) ?? {},
                  })),
                )
                .catch(() => setFullBases((prev) => ({ ...prev, [k]: {} })));
            }
          })
          .catch(() => setConciseBases((prev) => ({ ...prev, [k]: {} })));
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [templateKey, envKey]);

  // Once a (template, env) base arrives, seed each row's editor text for that env
  // with base ⊕ its stored per-env overlay — but only where the developer hasn't
  // edited (text still the bare overlay). Converges after one pass.
  //
  // When the template declares a developer-values projection AND the component has
  // no stored overlay yet, the seed is the PROJECTION instead: the handful of keys
  // that are the developer's, prefilled from the chart-inclusive base. A component
  // that already carries values keeps the plain base ⊕ overlay seed — we never
  // rewrite work someone has already done.
  useEffect(() => {
    let changed = false;
    const next = components.map((c, i) => {
      let text = c.envValuesText;
      let mutated = false;
      const fields = projections[c.template] ?? [];
      const projected = hasProjection(fields) && !showAll[i];
      for (const env of envs) {
        const base = seedBaseFor(c, env);
        if (!base) continue;
        const stored = c.envValues[env] ?? {};
        const cur = text[env] ?? "";
        if (cur !== stringifyOverlay(stored)) continue; // edited or seeded
        const full = fullBases[baseKey(c.template, env)];
        const useProjection = projected && Object.keys(stored).length === 0;
        if (useProjection && full === undefined) continue; // wait for the full base
        const seeded = useProjection
          ? stringifyProjection(mergeOverlay(full, c.values), fields)
          : stringifyOverlay(mergeOverlay(base, stored));
        if (seeded === cur) continue;
        if (!mutated) {
          text = { ...text };
          mutated = true;
        }
        text[env] = seeded;
      }
      if (!mutated) return c;
      changed = true;
      return { ...c, envValuesText: text };
    });
    if (changed) onChange(next);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [conciseBases, fullBases, projections, showAll, components, envKey]);

  // showAllFor reveals the whole platform base for one row, for BYO charts and keys
  // the projection doesn't cover. One-way and non-destructive: the developer's
  // current overlay is merged on top, so nothing they typed is lost. Re-seeding is
  // driven by clearing the row's editor text back to the bare overlay, which makes
  // the seed effect above run again with showAll set.
  function showAllFor(i: number) {
    const c = components[i];
    if (!c) return;
    const text: Record<string, string> = { ...c.envValuesText };
    for (const env of envs) {
      const base = seedBaseFor(c, env);
      const parsed = parseYamlOverlay(text[env] ?? "").value ?? {};
      text[env] = stringifyOverlay(mergeOverlay(base ?? {}, parsed));
    }
    setShowAll((prev) => ({ ...prev, [i]: true }));
    update(i, { envValuesText: text });
  }

  function update(i: number, patch: Partial<ComponentDraft>) {
    onChange(components.map((c, idx) => (idx === i ? { ...c, ...patch } : c)));
  }
  function remove(i: number) {
    onChange(components.filter((_, idx) => idx !== i));
  }
  function add() {
    const first = templates[0];
    if (!first) return;
    onChange([
      ...components,
      newComponentDraft(first, `component-${components.length + 1}`),
    ]);
  }

  // Curated env-var editing (only when a component opts out of inheriting all app vars).
  function updateEnv(i: number, j: number, patch: Partial<ComponentEnvVar>) {
    const c = components[i];
    if (!c) return;
    update(i, {
      envVars: c.envVars.map((e, idx) => (idx === j ? { ...e, ...patch } : e)),
    });
  }
  function removeEnv(i: number, j: number) {
    const c = components[i];
    if (!c) return;
    update(i, { envVars: c.envVars.filter((_, idx) => idx !== j) });
  }
  function addEnv(i: number) {
    const c = components[i];
    if (!c) return;
    update(i, { envVars: [...c.envVars, { name: "", value: "" }] });
  }
  // When the picked template changes, re-default the type/expose AND reset the
  // component's values/image state: the previous chart's per-env overrides, base
  // overlay, and image bindings targeted a different schema, so clearing them lets
  // the editor re-seed from the NEW template's platform base (otherwise the seed
  // effect's "untouched" guard keeps the buffer stuck on the first template's base).
  function onTemplateChange(i: number, name: string) {
    const reset: Partial<ComponentDraft> = {
      values: {},
      envValues: {},
      envValuesText: {},
      envValuesError: {},
      images: [],
      // Drop the pin too — it belonged to the old chart. Sending no version lets
      // the server pin the retemplated component to the new template's latest.
      templateVersion: undefined,
    };
    const tmpl = templates.find((t) => t.name === name);
    if (!tmpl) {
      update(i, { template: name, ...reset });
      return;
    }
    const type = categoryToType(tmpl.category);
    update(i, {
      template: name,
      type,
      exposeMode: type === "web" ? components[i]?.exposeMode || "external" : "disabled",
      ...reset,
    });
  }

  return (
    <div className="space-y-3">
      {components.map((c, i) => (
        <div
          key={i}
          className="rounded-lg border border-gray-200 bg-white p-3 shadow-sm"
        >
          {/* Row header: name · template · type · remove */}
          <div className="flex flex-wrap items-end gap-2">
            <div className="min-w-[9rem] flex-1">
              <label className={labelCls}>Name</label>
              <input
                type="text"
                className={inputCls}
                placeholder="api"
                value={c.name}
                onChange={(e) => update(i, { name: e.target.value })}
              />
            </div>
            <div className="min-w-[10rem] flex-1">
              <label className={labelCls}>
                Template
                {/* The pin is shown, not editable: saving structure here keeps
                    the chart where it is. Versions are changed via Upgrade. */}
                {c.templateVersion && (
                  <span className="ml-1 font-normal text-gray-400">
                    · pinned v{c.templateVersion}
                  </span>
                )}
              </label>
              <select
                className={inputCls}
                value={c.template}
                onChange={(e) => onTemplateChange(i, e.target.value)}
              >
                {templates.map((t) => (
                  <option key={t.name} value={t.name}>
                    {t.title}
                  </option>
                ))}
              </select>
            </div>
            <div className="w-28">
              <label className={labelCls}>Type</label>
              <select
                className={inputCls}
                value={c.type}
                onChange={(e) => update(i, { type: e.target.value })}
              >
                {COMPONENT_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {t}
                  </option>
                ))}
              </select>
            </div>
            {c.type === "web" && (
              <div className="w-32">
                <label className={labelCls}>Expose</label>
                <select
                  className={inputCls}
                  value={c.exposeMode}
                  onChange={(e) => update(i, { exposeMode: e.target.value })}
                >
                  {EXPOSE_MODES.map((m) => (
                    <option key={m} value={m}>
                      {m}
                    </option>
                  ))}
                </select>
              </div>
            )}
            <button
              type="button"
              onClick={() => remove(i)}
              className="mb-0.5 rounded-md px-2 py-1.5 text-xs font-medium text-gray-400 transition-colors hover:bg-red-50 hover:text-red-600"
              aria-label={`Remove ${c.name || "component"}`}
            >
              Remove
            </button>
          </div>

          {/* Per-component PER-ENV values — one editor per env tab, pre-filled with
              the concise platform base for that env; the developer's changes are
              highlighted and only the diff is saved as that env's component overlay.
              Same surface as the app-detail per-component card. On-demand
              (collapsed) so adding several components isn't cluttered. */}
          {(() => {
            const env = activeEnv[i] ?? envs[0] ?? "staging";
            const base = seedBaseFor(c, env);
            const hasOverride = Object.values(c.envValues).some(
              (v) => v && Object.keys(v).length > 0,
            );
            return (
              <div className="mt-3">
                <button
                  type="button"
                  onClick={() => setValuesOpen((o) => ({ ...o, [i]: !o[i] }))}
                  aria-expanded={!!valuesOpen[i]}
                  className="inline-flex items-center gap-1 text-xs font-medium text-gray-500 hover:text-gray-700"
                >
                  <span
                    className={`transition-transform ${valuesOpen[i] ? "rotate-90" : ""}`}
                  >
                    ▸
                  </span>
                  values
                  {hasOverride && !valuesOpen[i] && (
                    <span className="ml-1 rounded-full bg-indigo-50 px-1.5 py-0.5 text-[10px] text-indigo-600">
                      overridden
                    </span>
                  )}
                </button>
                {valuesOpen[i] && (
                  <Suspense
                    fallback={
                      <div className="mt-2 rounded-lg border border-gray-200 p-3 text-xs text-gray-400">
                        Loading editor…
                      </div>
                    }
                  >
                    <div className="mt-2">
                      {/* Env tabs (only when more than one env is offered). */}
                      {envs.length > 1 && (
                        <div className="mb-2 flex flex-wrap gap-1">
                          {envs.map((e) => {
                            const dirty =
                              (c.envValues[e] &&
                                Object.keys(c.envValues[e]!).length > 0) ||
                              false;
                            return (
                              <button
                                key={e}
                                type="button"
                                onClick={() =>
                                  setActiveEnv((m) => ({ ...m, [i]: e }))
                                }
                                className={`rounded px-2 py-0.5 text-[11px] font-medium transition-colors ${
                                  env === e
                                    ? "bg-gray-900 text-white"
                                    : "bg-gray-100 text-gray-600 hover:bg-gray-200"
                                }`}
                              >
                                {e}
                                {dirty ? " ●" : ""}
                              </button>
                            );
                          })}
                        </div>
                      )}
                      {hasProjection(projections[c.template]) && !showAll[i] ? (
                        <p className="mb-2 text-xs text-gray-400">
                          The settings this template exposes for {env}, pre-filled
                          with their current values — uncomment a line to override
                          it.{" "}
                          <span className="rounded-sm bg-indigo-50 px-1 text-indigo-600">
                            Your changes
                          </span>{" "}
                          are highlighted; only the difference is saved.{" "}
                          <button
                            type="button"
                            onClick={() => showAllFor(i)}
                            className="underline hover:text-gray-600"
                          >
                            Show all platform values
                          </button>{" "}
                          for anything not listed.
                        </p>
                      ) : (
                        <p className="mb-2 text-xs text-gray-400">
                          Pre-filled with the platform base for {env} (template ⊕
                          platform defaults).{" "}
                          <span className="rounded-sm bg-indigo-50 px-1 text-indigo-600">
                            Your changes
                          </span>{" "}
                          are highlighted; only the difference is saved for {env}.
                          Reference{" "}
                          <code className="font-mono">{"((platform.*))"}</code> /{" "}
                          <code className="font-mono">{"((vars.*))"}</code> tokens.
                        </p>
                      )}
                      <ValuesEditor
                        label={`Your override — ${env}`}
                        value={c.envValuesText[env] ?? ""}
                        configVars={configVars ?? undefined}
                        highlightBase={base}
                        height="16rem"
                        placeholder={
                          "# e.g.\nresources:\n  requests:\n    cpu: 200m"
                        }
                        onChange={(text) =>
                          update(i, {
                            envValuesText: { ...c.envValuesText, [env]: text },
                          })
                        }
                        onValidChange={(parsed, err) =>
                          update(i, {
                            envValuesError: { ...c.envValuesError, [env]: err },
                            ...(parsed
                              ? {
                                  envValues: {
                                    ...c.envValues,
                                    [env]: diffOverlay(base ?? {}, parsed),
                                  },
                                }
                              : {}),
                          })
                        }
                      />
                      {c.envValuesError[env] && (
                        <p className="mt-2 text-xs text-red-600">
                          Invalid YAML: {c.envValuesError[env]}
                        </p>
                      )}
                      {/* Effective (chart + platform ⊕ overrides) — advanced
                          reference, hidden by default, mirroring the card. */}
                      <button
                        type="button"
                        onClick={() =>
                          setEffectiveOpen((o) => ({ ...o, [i]: !o[i] }))
                        }
                        aria-expanded={!!effectiveOpen[i]}
                        className="mt-3 inline-flex items-center gap-1 text-xs font-medium text-gray-500 hover:text-gray-700"
                      >
                        <span
                          className={`transition-transform ${effectiveOpen[i] ? "rotate-90" : ""}`}
                        >
                          ▸
                        </span>
                        {effectiveOpen[i] ? "Hide" : "Show"} effective — {env}
                      </button>
                      {effectiveOpen[i] && (
                        <div className="mt-2">
                          <ValuesEditor
                            label={
                              bases[c.template]?.chartDefaultsAvailable
                                ? "Effective (chart + platform ⊕ overrides)"
                                : "Effective (platform ⊕ overrides)"
                            }
                            value={stringifyOverlay(
                              mergeOverlay(
                                mergeOverlay(
                                  bases[c.template]?.values ?? {},
                                  c.values,
                                ),
                                c.envValues[env] ?? {},
                              ),
                            )}
                            height="16rem"
                            readOnly
                          />
                        </div>
                      )}
                    </div>
                  </Suspense>
                )}
              </div>
            );
          })()}

          {/* Environment: inherit all app vars, or curate a subset. */}
          <div className="mt-3 border-t border-gray-100 pt-3">
            <label className="flex items-center gap-2 text-xs font-medium text-gray-600">
              <input
                type="checkbox"
                checked={c.inheritAppVars}
                onChange={(e) => update(i, { inheritAppVars: e.target.checked })}
                className="h-3.5 w-3.5 rounded border-gray-300"
              />
              Inherit all app vars (config + secrets)
            </label>
            {!c.inheritAppVars && (
              <div className="mt-2 space-y-2">
                <p className="text-xs text-gray-400">
                  This component sees only the vars below — pick a literal, an app
                  config key, or an app secret key (renamed into a per-component
                  Secret). No blanket app secrets.
                </p>
                {c.envVars.map((e, j) => {
                  const fromConfig = e.fromConfig !== undefined;
                  const fromSecret = e.fromSecret !== undefined;
                  const source = fromConfig
                    ? "config"
                    : fromSecret
                      ? "secret"
                      : "value";
                  const keyed = fromConfig || fromSecret;
                  return (
                    <div key={j} className="flex flex-wrap items-center gap-2">
                      <input
                        type="text"
                        className={`${inputCls} w-40 font-mono`}
                        placeholder="ENV_NAME"
                        value={e.name}
                        onChange={(ev) => updateEnv(i, j, { name: ev.target.value })}
                      />
                      <select
                        className={`${inputCls} w-36`}
                        value={source}
                        onChange={(ev) =>
                          updateEnv(
                            i,
                            j,
                            ev.target.value === "config"
                              ? { fromConfig: "", fromSecret: undefined, value: undefined }
                              : ev.target.value === "secret"
                                ? { fromSecret: "", fromConfig: undefined, value: undefined }
                                : { value: "", fromConfig: undefined, fromSecret: undefined },
                          )
                        }
                      >
                        <option value="value">literal</option>
                        <option value="config">app config key</option>
                        <option value="secret">app secret key</option>
                      </select>
                      <input
                        type="text"
                        className={`${inputCls} min-w-[8rem] flex-1 ${keyed ? "font-mono" : ""}`}
                        placeholder={
                          fromConfig
                            ? "APP_CONFIG_KEY"
                            : fromSecret
                              ? "APP_SECRET_KEY"
                              : "value"
                        }
                        value={
                          (fromConfig
                            ? e.fromConfig
                            : fromSecret
                              ? e.fromSecret
                              : e.value) ?? ""
                        }
                        onChange={(ev) =>
                          updateEnv(
                            i,
                            j,
                            fromConfig
                              ? { fromConfig: ev.target.value }
                              : fromSecret
                                ? { fromSecret: ev.target.value }
                                : { value: ev.target.value },
                          )
                        }
                      />
                      <button
                        type="button"
                        onClick={() => removeEnv(i, j)}
                        className="rounded-md px-2 py-1 text-xs text-gray-400 hover:bg-red-50 hover:text-red-600"
                        aria-label="Remove variable"
                      >
                        ✕
                      </button>
                    </div>
                  );
                })}
                <button
                  type="button"
                  onClick={() => addEnv(i)}
                  className="text-xs font-medium text-indigo-600 hover:text-indigo-700"
                >
                  + Add variable
                </button>
              </div>
            )}

            {/* Stateful: render this component as its OWN prune-disabled ArgoCD
                Application (a database/cache), so a shared auto-sync can't prune
                its data. Auto-set for addon-category templates. */}
            <div className="mt-3">
              <label className="flex items-center gap-2 text-sm text-gray-700">
                <input
                  type="checkbox"
                  className="h-4 w-4 rounded border-gray-300"
                  checked={c.stateful}
                  onChange={(ev) => update(i, { stateful: ev.target.checked })}
                />
                Stateful (database/cache)
              </label>
              <p className="mt-1 text-xs text-gray-400">
                Its own prune-disabled Application, decoupled from the app's
                auto-sync. Data survival on removal needs the chart's PVC marked{" "}
                <code>helm.sh/resource-policy: keep</code>.
              </p>
            </div>

            {/* Preview inclusion: when the app creates a preview (PR) env, only
                components with this on are rendered into it. Defaults by type
                (web/worker on; stateful + job/cron off). */}
            <div className="mt-3">
              <label className="flex items-center gap-2 text-sm text-gray-700">
                <input
                  type="checkbox"
                  className="h-4 w-4 rounded border-gray-300"
                  checked={c.previewEnabled}
                  onChange={(ev) =>
                    update(i, { previewEnabled: ev.target.checked })
                  }
                />
                Include in previews
              </label>
              <p className="mt-1 text-xs text-gray-400">
                Rendered into ephemeral PR preview envs when on. Defaults:
                web/worker on; databases and one-shot job/cron off.
              </p>
            </div>

          </div>
        </div>
      ))}

      <button
        type="button"
        onClick={add}
        className="w-full rounded-lg border border-dashed border-gray-300 py-2 text-sm font-medium text-gray-500 transition-colors hover:border-gray-400 hover:bg-gray-50 hover:text-gray-700"
      >
        + Add component
      </button>
    </div>
  );
}
