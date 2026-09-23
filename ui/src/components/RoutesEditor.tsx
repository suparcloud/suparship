import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";

import { ValuesEditor } from "./ValuesEditor";
import { parseYamlOverlay, stringifyOverlay } from "../lib/yamlDoc";
import type { RouteComponent } from "../lib/routes";
import type { RouteRule, RouteSpec } from "../types";

/**
 * RoutesEditor edits an owner's platform-owned routes. The form is the
 * default view (one card per route, rules as prefix → backend); the YAML
 * document `routes: [...]` is the Advanced view. Saving PATCHes the owner.
 */
export function RoutesEditor({
  routes,
  defaultHost,
  ownerKind,
  backendApps,
  components,
  onSave,
}: {
  routes: RouteSpec[];
  /** The hostname used when a route declares none. */
  defaultHost: string;
  ownerKind: "app" | "stack";
  /** Apps a rule may forward to (stack: the members; app: siblings). */
  backendApps?: string[];
  /** The owner app's components (app only), for the component picker. A
   *  component's known Service port fills the rule's port when it is picked. */
  components?: RouteComponent[];
  onSave: (routes: RouteSpec[]) => Promise<void>;
}) {
  const [draft, setDraft] = useState<RouteSpec[]>(() => structuredClone(routes));
  const [advanced, setAdvanced] = useState(false);
  const [yamlText, setYamlText] = useState(() => stringifyOverlay({ routes }));
  const [yamlError, setYamlError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const initialJSON = useMemo(() => JSON.stringify(routes), [routes]);
  const dirty = advanced ? yamlText !== stringifyOverlay({ routes }) : JSON.stringify(draft) !== initialJSON;

  function switchMode(toAdvanced: boolean) {
    if (toAdvanced) {
      setYamlText(stringifyOverlay({ routes: draft }));
    } else {
      const { value, error } = parseYamlOverlay(yamlText);
      if (error || !value) {
        toast.error(error || "Fix the YAML before switching to the form");
        return;
      }
      const list = (value as { routes?: unknown }).routes;
      setDraft(Array.isArray(list) ? (list as RouteSpec[]) : []);
    }
    setAdvanced(toAdvanced);
  }

  function current(): RouteSpec[] | null {
    if (!advanced) return draft;
    const { value, error } = parseYamlOverlay(yamlText);
    if (error || !value) {
      toast.error(error || "Invalid YAML");
      return null;
    }
    const list = (value as { routes?: unknown }).routes;
    if (list !== undefined && !Array.isArray(list)) {
      toast.error("`routes` must be a list");
      return null;
    }
    return (list as RouteSpec[]) ?? [];
  }

  async function save() {
    const list = current();
    if (!list) return;
    setBusy(true);
    try {
      await onSave(list);
      toast.success("Routes saved");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to save routes");
    } finally {
      setBusy(false);
    }
  }

  const updateRoute = (i: number, patch: Partial<RouteSpec>) =>
    setDraft((d) => d.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  const updateRule = (i: number, k: number, patch: Partial<RouteRule>) =>
    setDraft((d) =>
      d.map((r, j) =>
        j === i ? { ...r, rules: r.rules.map((rule, l) => (l === k ? { ...rule, ...patch } : rule)) } : r,
      ),
    );

  // Port for a fresh rule: unset when components are known (picking one fills
  // it from the chart's service port), 80 only when nothing better is known.
  const newRule = (): RouteRule => ({
    pathPrefix: "/",
    backend: components && components.length > 0 ? { port: 0 } : { port: 80 },
  });
  const pickComponent = (i: number, k: number, rule: RouteRule, name: string) => {
    const known = components?.find((c) => c.name === name)?.port;
    updateRule(i, k, { backend: { ...rule.backend, component: name || undefined, ...(known ? { port: known } : {}) } });
  };

  const input = "w-full rounded-md border border-gray-300 px-2 py-1 text-xs font-mono";
  const label = "block text-[11px] font-medium uppercase tracking-wide text-gray-400";

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <p className="max-w-2xl text-sm text-gray-600">
          HTTP surfaces suparship renders for this {ownerKind} (an Ingress or HTTPRoute per
          env). Hostnames may use tokens — default{" "}
          <code className="font-mono text-xs">{defaultHost}</code>; append{" "}
          <code className="font-mono text-xs">((platform.previewSuffix))</code> to a shared
          literal host. Keep the chart's own ingress off. Route-to-preview switches the backend;
          the hostname never changes.
        </p>
        <button
          type="button"
          onClick={() => switchMode(!advanced)}
          className="text-xs text-gray-500 underline hover:text-gray-700"
        >
          {advanced ? "Form" : "Advanced (YAML)"}
        </button>
      </div>

      {advanced ? (
        <>
          <ValuesEditor
            value={yamlText}
            onChange={setYamlText}
            onValidChange={(_p, err) => setYamlError(err)}
            height="18rem"
          />
          {yamlError && <p className="text-xs text-red-600">{yamlError}</p>}
        </>
      ) : (
        <div className="space-y-3">
          {draft.length === 0 && (
            <p className="rounded-md border border-dashed border-gray-200 px-4 py-6 text-center text-xs text-gray-400">
              No routes. Add one to expose this {ownerKind} through the platform.
            </p>
          )}
          {draft.map((r, i) => (
            <div key={i} className="rounded-lg border border-gray-200 p-3">
              <div className="grid gap-3 sm:grid-cols-[1fr_2fr_auto]">
                <div>
                  <label className={label}>Name</label>
                  <input
                    className={input}
                    value={r.name ?? ""}
                    placeholder={`route-${i + 1}`}
                    onChange={(e) => updateRoute(i, { name: e.target.value || undefined })}
                  />
                </div>
                <div>
                  <label className={label}>Hostnames (comma-separated)</label>
                  <HostnamesInput
                    className={input}
                    value={r.hostnames ?? []}
                    placeholder={defaultHost}
                    onChange={(hostnames) => updateRoute(i, { hostnames })}
                  />
                </div>
                <div>
                  <label className={label}>Tier</label>
                  <select
                    className={input}
                    value={r.tier ?? "external"}
                    onChange={(e) => updateRoute(i, { tier: e.target.value as RouteSpec["tier"] })}
                  >
                    <option value="external">external</option>
                    <option value="internal">internal</option>
                  </select>
                </div>
              </div>
              <div className="mt-3 space-y-2">
                <span className={label}>Rules</span>
                {r.rules.map((rule, k) => (
                  <div key={k} className="grid items-end gap-2 sm:grid-cols-[1fr_1fr_1fr_5rem_1fr_auto]">
                    <div>
                      <label className={label}>Path prefix</label>
                      <input className={input} value={rule.pathPrefix} onChange={(e) => updateRule(i, k, { pathPrefix: e.target.value })} />
                    </div>
                    <div>
                      <label className={label}>App</label>
                      {backendApps && backendApps.length > 0 ? (
                        <select
                          className={input}
                          value={rule.backend.app ?? ""}
                          onChange={(e) => updateRule(i, k, { backend: { ...rule.backend, app: e.target.value || undefined } })}
                        >
                          {ownerKind === "app" && <option value="">(this app)</option>}
                          {backendApps.map((a) => (
                            <option key={a} value={a}>{a}</option>
                          ))}
                        </select>
                      ) : (
                        <input className={input} value={rule.backend.app ?? ""} placeholder="(this app)" onChange={(e) => updateRule(i, k, { backend: { ...rule.backend, app: e.target.value || undefined } })} />
                      )}
                    </div>
                    <div>
                      <label className={label}>Component</label>
                      {components && components.length > 0 && !rule.backend.app ? (
                        <select
                          className={input}
                          value={rule.backend.component ?? ""}
                          onChange={(e) => pickComponent(i, k, rule, e.target.value)}
                        >
                          <option value="">(none)</option>
                          {components.map((c) => (
                            <option key={c.name} value={c.name}>{c.port ? `${c.name} (:${c.port})` : c.name}</option>
                          ))}
                        </select>
                      ) : (
                        <input className={input} value={rule.backend.component ?? ""} onChange={(e) => updateRule(i, k, { backend: { ...rule.backend, component: e.target.value || undefined } })} />
                      )}
                    </div>
                    <div>
                      <label className={label}>Port</label>
                      <input className={input} type="number" value={rule.backend.port || ""} placeholder="service port" onChange={(e) => updateRule(i, k, { backend: { ...rule.backend, port: Number(e.target.value) } })} />
                    </div>
                    <div>
                      <label className={label}>Service (override)</label>
                      <input className={input} value={rule.backend.service ?? ""} placeholder="{app}-{component}" onChange={(e) => updateRule(i, k, { backend: { ...rule.backend, service: e.target.value || undefined } })} />
                    </div>
                    <button
                      type="button"
                      onClick={() => updateRoute(i, { rules: r.rules.filter((_, l) => l !== k) })}
                      className="mb-0.5 text-xs text-gray-400 hover:text-red-600"
                      title="Remove rule"
                    >
                      ✕
                    </button>
                  </div>
                ))}
                <div className="flex gap-3">
                  <button
                    type="button"
                    onClick={() => updateRoute(i, { rules: [...r.rules, newRule()] })}
                    className="text-xs text-gray-600 underline hover:text-gray-800"
                  >
                    + rule
                  </button>
                  <button
                    type="button"
                    onClick={() => setDraft((d) => d.filter((_, j) => j !== i))}
                    className="text-xs text-gray-400 underline hover:text-red-600"
                  >
                    remove route
                  </button>
                </div>
              </div>
            </div>
          ))}
          <button
            type="button"
            onClick={() => setDraft((d) => [...d, { hostnames: [], rules: [newRule()] }])}
            className="text-xs text-gray-600 underline hover:text-gray-800"
          >
            + route
          </button>
        </div>
      )}

      <div className="flex items-center gap-2">
        <button
          onClick={save}
          disabled={busy || !dirty || (advanced && !!yamlError)}
          className="rounded-md bg-gray-900 px-3 py-1.5 text-xs font-medium text-white hover:bg-gray-700 disabled:opacity-50"
        >
          {busy ? "Saving…" : "Save routes"}
        </button>
        {dirty && (
          <button
            onClick={() => {
              setDraft(structuredClone(routes));
              setYamlText(stringifyOverlay({ routes }));
            }}
            disabled={busy}
            className="text-xs text-gray-500 underline hover:text-gray-700"
          >
            Discard
          </button>
        )}
      </div>
    </div>
  );
}

function parseHostnames(text: string): string[] {
  return text
    .split(",")
    .map((h) => h.trim())
    .filter(Boolean);
}

/**
 * HostnamesInput edits a comma-separated list while keeping the raw text the
 * user typed: a controlled input re-rendered from the parsed list would drop
 * the comma (and the space after it) on every keystroke, so a second hostname
 * could never be typed. The text is re-seeded only when the list changes from
 * outside (Discard, YAML → form) and tidied to "a, b" on blur.
 */
function HostnamesInput({
  value,
  placeholder,
  className,
  onChange,
}: {
  value: string[];
  placeholder: string;
  className: string;
  onChange: (hostnames: string[]) => void;
}) {
  const joined = value.join(", ");
  const [text, setText] = useState(joined);
  useEffect(() => {
    if (parseHostnames(text).join(", ") !== joined) setText(joined);
    // Only an outside change of the list re-seeds the text.
  }, [joined]);
  return (
    <input
      className={className}
      value={text}
      placeholder={placeholder}
      onChange={(e) => {
        setText(e.target.value);
        onChange(parseHostnames(e.target.value));
      }}
      onBlur={() => setText(parseHostnames(text).join(", "))}
    />
  );
}
