import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";

import { ApiError } from "../lib/api";
import { useAuth } from "../lib/AuthContext";
import { listOrgEnvironments } from "../lib/settings";
import {
  fetchTemplateEffectiveValues,
  updateTemplateMetadata,
} from "../lib/templates";
import { inferType } from "../lib/valuesProjection";
import { getAtPath, stringifyWithLinePaths } from "../lib/valuesTree";
import type {
  TemplateDetail as TemplateDetailType,
  ValueField,
} from "../types";

// DeveloperValuesSection curates the template's developer-facing values
// projection: the small set of Helm values paths developers see (and own) in the
// app editor. No projection => developers are seeded with the full concise
// platform base — fine for platform engineers, noise for developers, which is
// exactly what curating here fixes. Saved via PATCH {developerValues}: in place
// for imported/BYO templates, as a sync-safe override for synced/built-ins.

// Row keeps every input as text so typing never fights coercion; toField()
// converts to the wire shape on save.
interface Row {
  path: string;
  mirrorsText: string;
  title: string;
  type: "" | "string" | "number" | "boolean" | "enum";
  description: string;
  required: boolean;
  defaultText: string;
  optionsText: string;
  minText: string;
  maxText: string;
  pattern: string;
}

function parseMirrors(text: string): string[] {
  return text
    .split(",")
    .map((m) => m.trim())
    .filter((m) => m !== "");
}

function toRow(f: ValueField): Row {
  return {
    path: f.path,
    mirrorsText: (f.mirrors ?? []).join(", "),
    title: f.title ?? "",
    type: f.type ?? "",
    description: f.description ?? "",
    required: f.required ?? false,
    defaultText:
      f.default === undefined || f.default === null ? "" : String(f.default),
    optionsText: (f.options ?? []).join(", "),
    minText: f.min === undefined ? "" : String(f.min),
    maxText: f.max === undefined ? "" : String(f.max),
    pattern: f.pattern ?? "",
  };
}

// scalarFromText coerces free-form default text the way YAML would read it, so
// an untyped field's default of "3" stays a number in the projection.
function scalarFromText(text: string): unknown {
  const t = text.trim();
  if (t === "true") return true;
  if (t === "false") return false;
  if (t !== "" && !Number.isNaN(Number(t))) return Number(t);
  return text;
}

function toField(r: Row): ValueField {
  const f: ValueField = { path: r.path.trim() };
  const mirrors = parseMirrors(r.mirrorsText);
  if (mirrors.length > 0) f.mirrors = mirrors;
  if (r.title.trim()) f.title = r.title.trim();
  if (r.type) f.type = r.type;
  if (r.description.trim()) f.description = r.description.trim();
  if (r.required) f.required = true;
  const d = r.defaultText.trim();
  if (d !== "") {
    f.default =
      r.type === "number"
        ? Number(d)
        : r.type === "boolean"
          ? d === "true"
          : r.type === "string" || r.type === "enum"
            ? d
            : scalarFromText(d);
  }
  if (r.type === "enum") {
    f.options = r.optionsText
      .split(",")
      .map((o) => o.trim())
      .filter((o) => o !== "");
  }
  if (r.type === "number") {
    if (r.minText.trim() !== "") f.min = Number(r.minText);
    if (r.maxText.trim() !== "") f.max = Number(r.maxText);
  }
  if ((r.type === "" || r.type === "string") && r.pattern.trim()) {
    f.pattern = r.pattern.trim();
  }
  return f;
}

// rowError mirrors the server's validateDeveloperValues so the operator gets
// inline feedback instead of a 422 round-trip: non-empty unique paths, enum
// needs options, min ≤ max, pattern must compile.
function rowError(r: Row, all: Row[], i: number): string | null {
  const path = r.path.trim();
  if (path === "") return "path is required";
  // Mirrors share the path namespace: no key may be declared twice, whether as
  // a path or a mirror, within this row or across rows (mirrors the server's
  // validateDeveloperValues).
  const declared = [path, ...parseMirrors(r.mirrorsText)];
  if (new Set(declared).size !== declared.length) {
    return "a mirror duplicates this field's path";
  }
  const others = new Set(
    all.flatMap((o, j) =>
      j === i ? [] : [o.path.trim(), ...parseMirrors(o.mirrorsText)],
    ),
  );
  const clash = declared.find((p) => others.has(p));
  if (clash !== undefined) {
    return `duplicate path ${clash} (also declared on another field)`;
  }
  if (r.type === "enum") {
    const opts = r.optionsText
      .split(",")
      .map((o) => o.trim())
      .filter((o) => o !== "");
    if (opts.length === 0) return "enum needs at least one option";
  }
  if (r.type === "number") {
    if (r.defaultText.trim() !== "" && Number.isNaN(Number(r.defaultText))) {
      return "default must be a number";
    }
    const min = r.minText.trim() === "" ? undefined : Number(r.minText);
    const max = r.maxText.trim() === "" ? undefined : Number(r.maxText);
    if (min !== undefined && Number.isNaN(min)) return "min must be a number";
    if (max !== undefined && Number.isNaN(max)) return "max must be a number";
    if (min !== undefined && max !== undefined && min > max) {
      return "min must be ≤ max";
    }
  }
  if (r.pattern.trim() !== "") {
    try {
      new RegExp(r.pattern);
    } catch {
      return "pattern is not a valid regular expression";
    }
  }
  return null;
}

const TYPE_OPTIONS: Array<{ value: Row["type"]; label: string }> = [
  { value: "", label: "free-form" },
  { value: "string", label: "string" },
  { value: "number", label: "number" },
  { value: "boolean", label: "boolean" },
  { value: "enum", label: "enum" },
];

export function DeveloperValuesSection({
  template,
  onUpdated,
}: {
  template: TemplateDetailType;
  onUpdated: (t: TemplateDetailType) => void;
}) {
  const { user } = useAuth();
  const isOrgAdmin = user?.role === "org_admin";
  const inPlace = template.editable === true;

  const [editing, setEditing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [rows, setRows] = useState<Row[]>(
    (template.developerValues ?? []).map(toRow),
  );

  function reset() {
    setRows((template.developerValues ?? []).map(toRow));
    setEditing(false);
  }

  function setRow(i: number, patch: Partial<Row>) {
    setRows((cur) => cur.map((r, idx) => (idx === i ? { ...r, ...patch } : r)));
  }

  // Declaration order drives the seeded editor's field order, so it's editable.
  function move(i: number, delta: -1 | 1) {
    setRows((cur) => {
      const j = i + delta;
      if (j < 0 || j >= cur.length) return cur;
      const next = [...cur];
      [next[i], next[j]] = [next[j]!, next[i]!];
      return next;
    });
  }

  const errors = rows.map((r, i) => rowError(r, rows, i));
  const hasErrors = errors.some((e) => e !== null);

  async function saveList(list: ValueField[], successMsg: string) {
    setSaving(true);
    try {
      const updated = await updateTemplateMetadata(template.name, {
        developerValues: list,
      });
      onUpdated(updated);
      setRows((updated.developerValues ?? []).map(toRow));
      toast.success(successMsg);
      setEditing(false);
    } catch (err) {
      toast.error(
        err instanceof ApiError ? err.message : "Failed to save developer values",
      );
    } finally {
      setSaving(false);
    }
  }

  function save() {
    if (hasErrors) return;
    saveList(
      rows.map(toField),
      inPlace ? "Developer values updated" : "Developer values override saved",
    );
  }

  // On a read-only template an empty list clears the local override, falling
  // back to whatever projection the source itself declares.
  function revertToSource() {
    const confirmed = window.confirm(
      "Revert to the source's own developer values?\n\n" +
        "Your local projection override is removed; the template's " +
        "source-declared list (possibly none) applies again.",
    );
    if (!confirmed) return;
    saveList([], "Reverted to the source projection");
  }

  const fields = template.developerValues ?? [];
  // Mirrors count as exposed too — the browser badges them instead of offering
  // a second (colliding) Expose.
  const exposedPaths = useMemo(
    () =>
      new Set(
        rows
          .flatMap((r) => [r.path.trim(), ...parseMirrors(r.mirrorsText)])
          .filter((p) => p !== ""),
      ),
    [rows],
  );

  const constraintsOf = (f: ValueField): string => {
    const parts: string[] = [];
    if (f.type === "enum" && f.options?.length) parts.push(f.options.join(" | "));
    if (f.min !== undefined || f.max !== undefined) {
      parts.push(`${f.min ?? "…"}–${f.max ?? "…"}`);
    }
    if (f.pattern) parts.push(`/${f.pattern}/`);
    return parts.join(" · ") || "—";
  };

  return (
    <div id="developer-values" className="rounded-xl border border-gray-200 bg-white p-5">
      <div className="mb-3 flex items-center justify-between">
        <div>
          <h2 className="text-sm font-semibold text-gray-900">
            Developer values
          </h2>
          <p className="text-xs text-gray-400">
            The curated subset of values developers see in the app editor.
            Everything else stays platform-managed and inherited.
          </p>
        </div>
        {isOrgAdmin && !editing && (
          <button
            onClick={() => {
              // Reseed from the latest template — another section's save may
              // have refreshed it since mount.
              setRows((template.developerValues ?? []).map(toRow));
              setEditing(true);
            }}
            className="rounded-md px-3 py-1 text-xs font-medium text-gray-500 hover:bg-gray-50"
          >
            Edit
          </button>
        )}
        {editing && (
          <div className="flex items-center gap-2">
            <button
              onClick={reset}
              disabled={saving}
              className="rounded-md px-3 py-1 text-xs font-medium text-gray-500 hover:bg-gray-50"
            >
              Cancel
            </button>
            <button
              onClick={save}
              disabled={saving || hasErrors}
              className="rounded-md bg-gray-900 px-3 py-1 text-xs font-medium text-white hover:bg-gray-700 disabled:opacity-50"
            >
              {saving ? "Saving…" : "Save"}
            </button>
          </div>
        )}
      </div>

      {!inPlace && (
        <div className="mb-3 flex items-center justify-between gap-3 rounded-md bg-blue-50 px-3 py-2">
          <p className="text-xs text-blue-700">
            This template is managed by its source. The list you save here is a
            local, sync-safe override that <strong>replaces</strong> the
            source's own developer values wholesale — source-side changes to
            them won't show up until you revert.
          </p>
          {isOrgAdmin && fields.length > 0 && (
            <button
              onClick={revertToSource}
              disabled={saving}
              className="shrink-0 rounded-md border border-blue-200 px-2 py-1 text-xs font-medium text-blue-700 hover:bg-blue-100 disabled:opacity-50"
            >
              Revert to source
            </button>
          )}
        </div>
      )}

      {!editing ? (
        fields.length === 0 ? (
          <p className="text-xs text-gray-400">
            No developer values curated — developers are seeded with the full
            platform base in the app editor.
            {isOrgAdmin && " Add fields to give them a focused surface."}
          </p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-xs">
              <thead className="text-left text-gray-400">
                <tr>
                  <th className="py-1 pr-3 font-medium">Path</th>
                  <th className="py-1 pr-3 font-medium">Title</th>
                  <th className="py-1 pr-3 font-medium">Type</th>
                  <th className="py-1 pr-3 font-medium">Required</th>
                  <th className="py-1 pr-3 font-medium">Default</th>
                  <th className="py-1 font-medium">Constraints</th>
                </tr>
              </thead>
              <tbody className="text-gray-700">
                {fields.map((f) => (
                  <tr key={f.path} className="border-t border-gray-100">
                    <td className="py-1 pr-3 font-mono">
                      {f.path}
                      {(f.mirrors?.length ?? 0) > 0 && (
                        <span
                          className="text-gray-400"
                          title="Mirror paths — receive the same value."
                        >
                          {" "}
                          → {f.mirrors!.join(", ")}
                        </span>
                      )}
                    </td>
                    <td className="py-1 pr-3">{f.title || "—"}</td>
                    <td className="py-1 pr-3">{f.type || "free-form"}</td>
                    <td className="py-1 pr-3">{f.required ? "yes" : "—"}</td>
                    <td className="py-1 pr-3 font-mono">
                      {f.default === undefined || f.default === null
                        ? "—"
                        : typeof f.default === "object"
                          ? "…"
                          : String(f.default)}
                    </td>
                    <td className="py-1 font-mono">{constraintsOf(f)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )
      ) : (
        <div className="space-y-3">
          {inPlace && (
            <p className="rounded-md bg-gray-50 px-3 py-2 text-xs text-gray-500">
              Saved into the template itself. Removing every field removes the
              projection — developers fall back to the full platform base.
            </p>
          )}
          {rows.map((r, i) => (
            <div
              key={i}
              className={`space-y-2 rounded-lg border p-3 ${
                errors[i] ? "border-red-300" : "border-gray-200"
              }`}
            >
              <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
                <input
                  className="rounded-md border border-gray-300 px-2 py-1 font-mono text-xs"
                  placeholder="values path (e.g. scaling.maxReplicas)"
                  value={r.path}
                  onChange={(e) => setRow(i, { path: e.target.value })}
                />
                <input
                  className="rounded-md border border-gray-300 px-2 py-1 text-xs"
                  placeholder="title (shown to developers)"
                  value={r.title}
                  onChange={(e) => setRow(i, { title: e.target.value })}
                />
              </div>
              <input
                className="w-full rounded-md border border-gray-300 px-2 py-1 font-mono text-xs"
                placeholder="mirror paths, comma-separated — receive the same value (e.g. service.port, healthCheck.port)"
                value={r.mirrorsText}
                onChange={(e) => setRow(i, { mirrorsText: e.target.value })}
              />
              <textarea
                className="w-full rounded-md border border-gray-300 px-2 py-1 text-xs"
                placeholder="description (optional help text)"
                rows={2}
                value={r.description}
                onChange={(e) => setRow(i, { description: e.target.value })}
              />
              <div className="flex flex-wrap items-center gap-2">
                <select
                  className="rounded-md border border-gray-300 px-2 py-1 text-xs"
                  value={r.type}
                  onChange={(e) =>
                    setRow(i, { type: e.target.value as Row["type"] })
                  }
                >
                  {TYPE_OPTIONS.map((t) => (
                    <option key={t.value} value={t.value}>
                      {t.label}
                    </option>
                  ))}
                </select>
                <label className="flex items-center gap-1 text-xs text-gray-600">
                  <input
                    type="checkbox"
                    checked={r.required}
                    onChange={(e) => setRow(i, { required: e.target.checked })}
                  />
                  required
                </label>
                <input
                  className="w-36 rounded-md border border-gray-300 px-2 py-1 font-mono text-xs"
                  placeholder="default"
                  value={r.defaultText}
                  onChange={(e) => setRow(i, { defaultText: e.target.value })}
                />
                {r.type === "enum" && (
                  <input
                    className="min-w-48 flex-1 rounded-md border border-gray-300 px-2 py-1 font-mono text-xs"
                    placeholder="options, comma-separated"
                    value={r.optionsText}
                    onChange={(e) => setRow(i, { optionsText: e.target.value })}
                  />
                )}
                {r.type === "number" && (
                  <>
                    <input
                      className="w-20 rounded-md border border-gray-300 px-2 py-1 font-mono text-xs"
                      placeholder="min"
                      value={r.minText}
                      onChange={(e) => setRow(i, { minText: e.target.value })}
                    />
                    <input
                      className="w-20 rounded-md border border-gray-300 px-2 py-1 font-mono text-xs"
                      placeholder="max"
                      value={r.maxText}
                      onChange={(e) => setRow(i, { maxText: e.target.value })}
                    />
                  </>
                )}
                {(r.type === "" || r.type === "string") && (
                  <input
                    className="min-w-48 flex-1 rounded-md border border-gray-300 px-2 py-1 font-mono text-xs"
                    placeholder="pattern (regex, optional)"
                    value={r.pattern}
                    onChange={(e) => setRow(i, { pattern: e.target.value })}
                  />
                )}
                <div className="ml-auto flex items-center gap-1">
                  <button
                    onClick={() => move(i, -1)}
                    disabled={i === 0}
                    title="Move up"
                    className="rounded-md px-1.5 py-0.5 text-xs text-gray-400 hover:bg-gray-50 disabled:opacity-30"
                  >
                    ▲
                  </button>
                  <button
                    onClick={() => move(i, 1)}
                    disabled={i === rows.length - 1}
                    title="Move down"
                    className="rounded-md px-1.5 py-0.5 text-xs text-gray-400 hover:bg-gray-50 disabled:opacity-30"
                  >
                    ▼
                  </button>
                  <button
                    onClick={() =>
                      setRows((cur) => cur.filter((_, idx) => idx !== i))
                    }
                    className="rounded-md px-2 py-0.5 text-xs font-medium text-red-600 hover:bg-red-50"
                  >
                    Remove
                  </button>
                </div>
              </div>
              {errors[i] && (
                <p className="text-xs text-red-600">{errors[i]}</p>
              )}
            </div>
          ))}
          <button
            onClick={() =>
              setRows((cur) => [
                ...cur,
                {
                  path: "",
                  mirrorsText: "",
                  title: "",
                  type: "",
                  description: "",
                  required: false,
                  defaultText: "",
                  optionsText: "",
                  minText: "",
                  maxText: "",
                  pattern: "",
                },
              ])
            }
            className="rounded-md border border-dashed border-gray-300 px-3 py-1 text-xs font-medium text-gray-500 hover:bg-gray-50"
          >
            + Add field
          </button>

          <ExposeValuesBrowser
            templateName={template.name}
            exposedPaths={exposedPaths}
            onExpose={(f) => setRows((cur) => [...cur, toRow(f)])}
          />
        </div>
      )}
    </div>
  );
}

// ExposeValuesBrowser renders the template's effective values (chart ⊕ template
// ⊕ org override) read-only, with an "Expose" affordance per value — so the
// platform engineer promotes paths by browsing the rendered config instead of
// typing dotted paths from memory. Same browse-then-click idea as
// EffectiveValuesView, but the action (append a projection field) and its
// eligibility rules differ, so this is a sibling, not a variant.
function ExposeValuesBrowser({
  templateName,
  exposedPaths,
  onExpose,
}: {
  templateName: string;
  exposedPaths: Set<string>;
  onExpose: (f: ValueField) => void;
}) {
  const [open, setOpen] = useState(false);
  const [envs, setEnvs] = useState<string[] | null>(null);
  const [env, setEnv] = useState("");
  const [byEnv, setByEnv] = useState<
    Record<string, Record<string, unknown>>
  >({});
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (!open || envs !== null) return;
    listOrgEnvironments()
      .then((res) => {
        const names = (res.environments ?? [])
          .map((e) => e.name)
          .filter((n) => n !== "preview");
        setEnvs(names);
        setEnv((cur) => cur || names[0] || "");
      })
      .catch(() => setEnvs([]));
  }, [open, envs]);

  // Fetch lazily per env and cache — the effective document only changes when
  // the template or its override does, and this browser is short-lived.
  useEffect(() => {
    if (!open || envs === null || byEnv[env] !== undefined) return;
    setLoading(true);
    fetchTemplateEffectiveValues(templateName, env || undefined)
      .then((res) =>
        setByEnv((cur) => ({ ...cur, [env]: res.values ?? {} })),
      )
      .catch(() => setByEnv((cur) => ({ ...cur, [env]: {} })))
      .finally(() => setLoading(false));
  }, [open, envs, env, templateName, byEnv]);

  const effective = byEnv[env];
  const rows = useMemo(
    () => (effective ? stringifyWithLinePaths(effective) : []),
    [effective],
  );

  return (
    <div className="rounded-lg border border-gray-200">
      <button
        onClick={() => setOpen((o) => !o)}
        className="flex w-full items-center justify-between px-3 py-2 text-xs font-medium text-gray-600 hover:bg-gray-50"
      >
        <span>
          {open ? "▾" : "▸"} Browse effective values to expose fields
        </span>
        {open && envs !== null && envs.length > 0 && (
          <select
            value={env}
            onClick={(e) => e.stopPropagation()}
            onChange={(e) => setEnv(e.target.value)}
            className="rounded-md border border-gray-300 px-2 py-0.5 text-xs font-normal"
          >
            {envs.map((e) => (
              <option key={e} value={e}>
                {e}
              </option>
            ))}
          </select>
        )}
      </button>
      {open && (
        <div className="max-h-80 overflow-auto border-t border-gray-100 py-1 font-mono text-xs leading-5">
          {loading || effective === undefined ? (
            <div className="px-3 py-3 text-gray-400">Loading…</div>
          ) : rows.length === 0 ||
            (rows.length === 1 && rows[0]?.text.trim() === "{}") ? (
            <div className="px-3 py-3 text-gray-400">
              No effective values resolved.
            </div>
          ) : (
            rows.map((r, i) => {
              const eligible = r.path !== null && r.kind !== "other";
              const hasDottedKey = r.path?.some((s) => s.includes(".")) ?? false;
              const dotted = r.path?.join(".") ?? "";
              const exposed = eligible && exposedPaths.has(dotted);
              return (
                <div
                  key={i}
                  className="group flex items-center justify-between gap-2 px-3"
                >
                  <span className="whitespace-pre text-gray-800">
                    {r.text || " "}
                  </span>
                  {exposed ? (
                    <span className="shrink-0 rounded bg-indigo-50 px-1.5 py-0.5 text-[10px] font-medium text-indigo-500">
                      exposed
                    </span>
                  ) : eligible && hasDottedKey ? (
                    <span
                      title="A key containing a dot can't be expressed as a dotted path."
                      className="shrink-0 cursor-not-allowed rounded border border-gray-100 px-1.5 py-0.5 text-[10px] text-gray-300 opacity-0 group-hover:opacity-100"
                    >
                      Expose
                    </span>
                  ) : eligible ? (
                    <button
                      type="button"
                      onClick={() => {
                        const v = getAtPath(effective, r.path!);
                        onExpose({
                          path: dotted,
                          type: inferType(v),
                          // Non-scalars have no sensible default text; the field
                          // still projects the whole subtree.
                          default:
                            v !== null && typeof v === "object" ? undefined : v,
                        });
                      }}
                      className="shrink-0 rounded border border-indigo-200 px-1.5 py-0.5 text-[10px] font-medium text-indigo-600 opacity-0 transition hover:bg-indigo-50 group-hover:opacity-100"
                    >
                      Expose
                    </button>
                  ) : null}
                </div>
              );
            })
          )}
        </div>
      )}
    </div>
  );
}

export default DeveloperValuesSection;
