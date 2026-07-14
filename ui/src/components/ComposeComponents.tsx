import type { ComponentCreate, TemplateSummary } from "../types";

// ComponentDraft is the UI-editable form of one composed-app component. Numeric
// and list fields are kept as strings while editing and coerced to the wire
// shape (ComponentCreate) by toComponentCreate on submit.
export interface ComponentDraft {
  name: string;
  /** template name (the component's own chart) */
  template: string;
  /** web | worker | job */
  type: string;
  /** disabled | internal | external */
  exposeMode: string;
  imageRepository: string;
  imageTag: string;
  /** container port, kept as a string while editing */
  port: string;
  /** entrypoint override, space-separated (e.g. "alembic upgrade head") */
  command: string;
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
    imageRepository: "",
    imageTag: "",
    port: "",
    command: "",
  };
}

// toComponentCreate coerces a draft to the wire shape, dropping empty optional
// fields so the request stays minimal.
export function toComponentCreate(d: ComponentDraft): ComponentCreate {
  const cmd = d.command.trim();
  const repo = d.imageRepository.trim();
  const tag = d.imageTag.trim();
  return {
    name: d.name.trim(),
    type: d.type,
    enabled: true,
    exposeMode: d.type === "web" ? d.exposeMode : undefined,
    template: { name: d.template },
    image: repo || tag ? { repository: repo || undefined, tag: tag || undefined } : undefined,
    port: d.port.trim() ? Number(d.port) : undefined,
    command: cmd ? cmd.split(/\s+/) : undefined,
  };
}

const inputCls =
  "w-full rounded-md border border-gray-300 px-2.5 py-1.5 text-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500";
const labelCls = "mb-1 block text-xs font-medium text-gray-500";

// ComposeComponents is the add-component canvas: a list of component rows, each
// picking its own template and typed config, plus an "Add component" control. It
// is a controlled component — the parent owns the ComponentDraft[] state.
export function ComposeComponents({
  templates,
  components,
  onChange,
}: {
  templates: TemplateSummary[];
  components: ComponentDraft[];
  onChange: (next: ComponentDraft[]) => void;
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

  // When the picked template changes, re-default the type/expose unless the user
  // already diverged from the template's default type.
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
            <button
              type="button"
              onClick={() => remove(i)}
              className="mb-0.5 rounded-md px-2 py-1.5 text-xs font-medium text-gray-400 transition-colors hover:bg-red-50 hover:text-red-600"
              aria-label={`Remove ${c.name || "component"}`}
            >
              Remove
            </button>
          </div>

          {/* Row body: per-component typed config */}
          <div className="mt-3 grid grid-cols-2 gap-2 sm:grid-cols-4">
            <div className="col-span-2">
              <label className={labelCls}>Image repository</label>
              <input
                type="text"
                className={inputCls}
                placeholder="ghcr.io/org/app"
                value={c.imageRepository}
                onChange={(e) => update(i, { imageRepository: e.target.value })}
              />
            </div>
            <div>
              <label className={labelCls}>Tag</label>
              <input
                type="text"
                className={inputCls}
                placeholder="latest"
                value={c.imageTag}
                onChange={(e) => update(i, { imageTag: e.target.value })}
              />
            </div>
            {c.type === "web" ? (
              <div>
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
            ) : (
              <div>
                <label className={labelCls}>Port</label>
                <input
                  type="number"
                  className={inputCls}
                  placeholder="—"
                  value={c.port}
                  onChange={(e) => update(i, { port: e.target.value })}
                />
              </div>
            )}
            {c.type === "web" && (
              <div>
                <label className={labelCls}>Port</label>
                <input
                  type="number"
                  className={inputCls}
                  placeholder="8080"
                  value={c.port}
                  onChange={(e) => update(i, { port: e.target.value })}
                />
              </div>
            )}
            {c.type === "job" && (
              <div className="col-span-2 sm:col-span-4">
                <label className={labelCls}>
                  Command{" "}
                  <span className="font-normal text-gray-400">
                    (runs once before rollout — e.g. a migration)
                  </span>
                </label>
                <input
                  type="text"
                  className={`${inputCls} font-mono`}
                  placeholder="alembic upgrade head"
                  value={c.command}
                  onChange={(e) => update(i, { command: e.target.value })}
                />
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
