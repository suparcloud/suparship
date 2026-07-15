import { Suspense, lazy, useEffect, useRef, useState } from "react";

import { fetchTemplateEffectiveValues } from "../lib/templates";
import { mergeOverlay, stringifyOverlay } from "../lib/yamlDoc";
import type {
  ComponentCreate,
  ComponentEnvVar,
  ComponentSummary,
  EffectiveValuesResponse,
  TemplateSummary,
} from "../types";
import type { ConfigVariables } from "../lib/configVars";

// CodeMirror is heavy; load it only when the compose canvas is shown.
const ValuesEditor = lazy(() => import("./ValuesEditor"));

// ComponentDraft is the UI-editable form of one composed-app component:
// structural fields (name / template / type / expose) plus a per-component Helm
// values overlay (the value-based config), kept as editor text + its parsed form.
export interface ComponentDraft {
  name: string;
  /** template name (the component's own chart) */
  template: string;
  /** web | worker | job */
  type: string;
  /** disabled | internal | external */
  exposeMode: string;
  /** per-component values overlay — editor text, its parsed object, and a parse error */
  valuesText: string;
  values: Record<string, unknown>;
  valuesError: string | null;
  /** env policy: inherit all app vars, or curate a subset */
  inheritAppVars: boolean;
  envVars: ComponentEnvVar[];
}

const COMPONENT_TYPES = ["web", "worker", "job"] as const;
const EXPOSE_MODES = ["disabled", "internal", "external"] as const;

// categoryToType maps a template's category to a sensible default component
// type. Falls back to "web" for anything unrecognised.
export function categoryToType(category: string): string {
  switch (category) {
    case "worker":
      return "worker";
    case "job":
    case "cron":
      return "job";
    default:
      return "web";
  }
}

// newComponentDraft seeds a component row from a template, defaulting its type
// from the template category and exposing web components externally by default.
export function newComponentDraft(
  tmpl: Pick<TemplateSummary, "name" | "category">,
  name: string,
): ComponentDraft {
  const type = categoryToType(tmpl.category);
  return {
    name,
    template: tmpl.name,
    type,
    exposeMode: type === "web" ? "external" : "disabled",
    valuesText: "",
    values: {},
    valuesError: null,
    inheritAppVars: true,
    envVars: [],
  };
}

// draftFromSummary seeds an editable draft from an existing app component (the
// edit-composed flow on the app detail page), preserving its template, expose
// mode, values overlay, and env policy.
export function draftFromSummary(c: ComponentSummary): ComponentDraft {
  return {
    name: c.name,
    template: c.template ?? "",
    type: c.type,
    exposeMode: c.exposeMode || (c.type === "web" ? "external" : "disabled"),
    valuesText: c.values ? stringifyOverlay(c.values) : "",
    values: c.values ?? {},
    valuesError: null,
    inheritAppVars: c.inheritAppVars ?? true,
    envVars: c.envVars ?? [],
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
  return {
    name: d.name.trim(),
    type: d.type,
    enabled: true,
    exposeMode: d.type === "web" ? d.exposeMode : undefined,
    template: { name: d.template },
    values: Object.keys(d.values).length > 0 ? d.values : undefined,
    // Only send when opting out — inherit (default) needs no field.
    inheritAppVars: d.inheritAppVars ? undefined : false,
    envVars: envVars.length > 0 ? envVars : undefined,
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
}: {
  templates: TemplateSummary[];
  components: ComponentDraft[];
  onChange: (next: ComponentDraft[]) => void;
  configVars?: ConfigVariables | null;
}) {
  // Effective-values base (chart + platform defaults) per template, for the
  // read-only preview pane. Fetched once per distinct template and cached; two
  // components on the same template share one fetch.
  const [bases, setBases] = useState<Record<string, EffectiveValuesResponse | null>>({});
  const fetched = useRef<Set<string>>(new Set());
  const templateKey = components.map((c) => c.template).join(",");
  useEffect(() => {
    for (const name of new Set(components.map((c) => c.template).filter(Boolean))) {
      if (fetched.current.has(name)) continue;
      fetched.current.add(name);
      fetchTemplateEffectiveValues(name)
        .then((res) => setBases((prev) => ({ ...prev, [name]: res })))
        .catch(() => setBases((prev) => ({ ...prev, [name]: null })));
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [templateKey]);

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

  // When the picked template changes, re-default the type/expose.
  function onTemplateChange(i: number, name: string) {
    const tmpl = templates.find((t) => t.name === name);
    if (!tmpl) {
      update(i, { template: name });
      return;
    }
    const type = categoryToType(tmpl.category);
    update(i, {
      template: name,
      type,
      exposeMode: type === "web" ? components[i]?.exposeMode || "external" : "disabled",
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
              <label className={labelCls}>Template</label>
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

          {/* Per-component values overlay — deep-merged onto this component's
              chart values, in the chart's own schema (canonical or BYO) — plus a
              read-only effective preview (chart + platform defaults ⊕ overrides). */}
          <div className="mt-3">
            <label className={labelCls}>
              Values{" "}
              <span className="font-normal text-gray-400">
                (overrides for this component's chart — e.g.{" "}
                <code className="font-mono">components.web.image.tag</code>)
              </span>
            </label>
            <Suspense
              fallback={
                <div className="rounded-lg border border-gray-200 p-3 text-xs text-gray-400">
                  Loading editor…
                </div>
              }
            >
              <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
                <ValuesEditor
                  label="Your overrides"
                  value={c.valuesText}
                  configVars={configVars ?? undefined}
                  height="14rem"
                  placeholder={
                    "# e.g.\ncomponents:\n  web:\n    image:\n      repository: ghcr.io/org/app\n      tag: v1"
                  }
                  onChange={(text) => update(i, { valuesText: text })}
                  onValidChange={(parsed, err) =>
                    update(i, {
                      valuesError: err,
                      ...(parsed ? { values: parsed } : {}),
                    })
                  }
                />
                <ValuesEditor
                  label={
                    bases[c.template]?.chartDefaultsAvailable
                      ? "Effective (chart + platform ⊕ overrides)"
                      : "Effective (platform ⊕ overrides)"
                  }
                  value={stringifyOverlay(
                    mergeOverlay(bases[c.template]?.values ?? {}, c.values),
                  )}
                  height="14rem"
                  readOnly
                />
              </div>
            </Suspense>
          </div>

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
