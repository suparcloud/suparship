import { Suspense, lazy } from "react";

import type { ComponentCreate, TemplateSummary } from "../types";
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
  };
}

// toComponentCreate coerces a draft to the wire shape, dropping empty optional
// fields so the request stays minimal.
export function toComponentCreate(d: ComponentDraft): ComponentCreate {
  return {
    name: d.name.trim(),
    type: d.type,
    enabled: true,
    exposeMode: d.type === "web" ? d.exposeMode : undefined,
    template: { name: d.template },
    values: Object.keys(d.values).length > 0 ? d.values : undefined,
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
              chart values, in the chart's own schema (canonical or BYO). */}
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
              <ValuesEditor
                value={c.valuesText}
                configVars={configVars ?? undefined}
                height="12rem"
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
            </Suspense>
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
