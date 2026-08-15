import { useEffect, useState } from "react";

import {
  getResolvedEnvConfig,
  type ResolvedEnvVar,
} from "../lib/envconfig";
import {
  listAppGlobalSecretKeys,
  listAppEnvSecretKeys,
} from "../lib/secrets";
import type { ComponentEnvVar } from "../types";

// ComponentEnvPanel — per-component variables, rendered INLINE in the component
// card (an "env vars" disclosure, sibling of "values"). The card already lives
// under the page's selected environment, so there is exactly ONE add/override
// section and it writes THAT env's per-component overrides — no separate
// "all environments" section (field feedback: the env is already selected).
//
//   - Add/override: literal rows for the selected env
//     (EnvironmentOverride.Components[c].Env) — rendered as explicit env[]
//     entries, which beat every envFrom source.
//   - Remove/rename: the collapsed "Inherited from the app" checklist. Any
//     exclusion or rename derives CURATED mode (`inheritAppVars=false` + one
//     mapping per included key → the <app>-<component>-config/-secrets
//     projection), with the freeze tradeoff surfaced. Everything included and
//     unrenamed = live inherit.
//
// Secret VALUES are never entered here: literals land in git values files. New
// secrets are added at app/env scope; inherited secrets can be excluded or
// renamed. Stored app-level literal EnvVars entries (legacy/API-authored) are
// preserved untouched across saves and surfaced as a count.

export interface ComponentEnvValue {
  inheritAppVars: boolean;
  envVars: ComponentEnvVar[];
}

interface ChecklistRow {
  key: string;
  isSecret: boolean;
  // Display value for config vars (secrets never show values).
  value?: string;
  source?: string;
}

export function ComponentEnvPanel({
  componentName,
  value,
  appCtx,
  envOverride,
  envName,
  onSave,
  saveLabel = "Save",
  saving = false,
}: {
  componentName: string;
  // Current stored/draft state.
  value: ComponentEnvValue;
  // When the app exists: enables the inherited-keys checklist.
  appCtx?: { project: string; appName: string; env: string | null };
  // Live mode: current per-(env, component) literal overrides for envName —
  // the panel's single add/override section. Absent in draft mode (create/
  // manage canvas), where only inheritance curation is editable.
  envOverride?: Record<string, string>;
  envName?: string | null;
  onSave: (next: {
    inheritAppVars: boolean;
    envVars: ComponentEnvVar[];
    envOverride?: Record<string, string>;
  }) => void | Promise<void>;
  saveLabel?: string;
  saving?: boolean;
}) {
  const [rows, setRows] = useState<ChecklistRow[]>([]);
  const [loadingKeys, setLoadingKeys] = useState(false);
  const [inheritedOpen, setInheritedOpen] = useState(false);

  // Editing state, seeded from `value` on mount (the panel mounts when its
  // disclosure opens) and again when the checklist keys arrive.
  const [included, setIncluded] = useState<Record<string, boolean>>({});
  const [renames, setRenames] = useState<Record<string, string>>({});
  // Stored entries preserved verbatim across saves: source-mappings whose key
  // no longer exists in the app config, and app-level literal entries
  // (authored via API / legacy UI — this panel adds per-env literals instead).
  const [staleMappings, setStaleMappings] = useState<ComponentEnvVar[]>([]);
  const [keptLiterals, setKeptLiterals] = useState<ComponentEnvVar[]>([]);
  const [overrides, setOverrides] = useState<{ name: string; value: string }[]>([]);

  // Fetch the app's inheritable keys: resolved env vars (config + secret-ref
  // keys) plus vault secret key names (global + env scope).
  useEffect(() => {
    if (!appCtx) return;
    let cancelled = false;
    setLoadingKeys(true);
    const env = appCtx.env;
    Promise.allSettled([
      env
        ? getResolvedEnvConfig(appCtx.project, appCtx.appName, env)
        : Promise.resolve({ vars: [] as ResolvedEnvVar[] }),
      listAppGlobalSecretKeys(appCtx.project, appCtx.appName),
      env
        ? listAppEnvSecretKeys(appCtx.project, appCtx.appName, env)
        : Promise.resolve({ keys: [] as { key: string }[] }),
    ]).then(([resolved, globalSecrets, envSecrets]) => {
      if (cancelled) return;
      const out: ChecklistRow[] = [];
      const seen = new Set<string>();
      if (resolved.status === "fulfilled") {
        for (const v of resolved.value.vars ?? []) {
          if (seen.has(v.key)) continue;
          seen.add(v.key);
          out.push({
            key: v.key,
            isSecret: !!v.isSecret,
            value: v.isSecret ? undefined : v.value,
            source: v.source,
          });
        }
      }
      const addSecretKeys = (res: PromiseSettledResult<{ keys?: { key: string }[] }>) => {
        if (res.status !== "fulfilled") return;
        for (const k of res.value.keys ?? []) {
          if (seen.has(k.key)) continue;
          seen.add(k.key);
          out.push({ key: k.key, isSecret: true });
        }
      };
      addSecretKeys(globalSecrets as PromiseSettledResult<{ keys?: { key: string }[] }>);
      addSecretKeys(envSecrets as PromiseSettledResult<{ keys?: { key: string }[] }>);
      out.sort((a, b) => a.key.localeCompare(b.key));
      setRows(out);
      setLoadingKeys(false);
    });
    return () => {
      cancelled = true;
    };
  }, [appCtx?.project, appCtx?.appName, appCtx?.env]); // eslint-disable-line react-hooks/exhaustive-deps

  // Seed the edit state from the stored value on mount and when the key list
  // arrives (curated seeding needs the keys to mark exclusions).
  useEffect(() => {
    const inc: Record<string, boolean> = {};
    const ren: Record<string, string> = {};
    const stale: ComponentEnvVar[] = [];
    const literals: ComponentEnvVar[] = [];
    const keySet = new Set(rows.map((r) => r.key));
    if (value.inheritAppVars) {
      for (const r of rows) inc[r.key] = true;
      for (const e of value.envVars) {
        if (e.value !== undefined && e.value !== "") literals.push(e);
      }
    } else {
      for (const r of rows) inc[r.key] = false;
      for (const e of value.envVars) {
        const src = e.fromConfig || e.fromSecret;
        if (src) {
          if (keySet.has(src)) {
            inc[src] = true;
            if (e.name !== src) ren[src] = e.name;
          } else {
            stale.push(e);
          }
        } else {
          literals.push(e);
        }
      }
      // A stored curated list is worth surfacing immediately.
      setInheritedOpen(true);
    }
    setIncluded(inc);
    setRenames(ren);
    setStaleMappings(stale);
    setKeptLiterals(literals);
    setOverrides(
      Object.entries(envOverride ?? {}).map(([name, v]) => ({ name, value: v })),
    );
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [rows, componentName]);

  const anyExcluded = rows.some((r) => included[r.key] === false);
  const anyRenamed = Object.values(renames).some((v) => v.trim() !== "");
  // Derived mode: curated the moment any key is excluded/renamed (or the
  // stored spec was curated and still carries stale mappings).
  const curated = anyExcluded || anyRenamed || staleMappings.length > 0;
  const nVars = rows.filter((r) => !r.isSecret).length;
  const nSecrets = rows.filter((r) => r.isSecret).length;
  const nExcluded = rows.filter((r) => included[r.key] === false).length;

  function buildEnvVars(): ComponentEnvVar[] {
    const out: ComponentEnvVar[] = [];
    if (curated) {
      for (const r of rows) {
        if (!included[r.key]) continue;
        const name = (renames[r.key] ?? "").trim() || r.key;
        out.push(
          r.isSecret ? { name, fromSecret: r.key } : { name, fromConfig: r.key },
        );
      }
      out.push(...staleMappings);
    }
    // App-level literal entries pass through untouched either way.
    out.push(...keptLiterals);
    return out;
  }

  async function handleSave() {
    const next: Parameters<typeof onSave>[0] = {
      inheritAppVars: !curated,
      envVars: buildEnvVars(),
    };
    if (envOverride !== undefined) {
      const ov: Record<string, string> = {};
      for (const o of overrides) {
        if (o.name.trim()) ov[o.name.trim()] = o.value;
      }
      next.envOverride = ov;
    }
    await onSave(next);
  }

  const inputCls =
    "rounded border border-gray-300 px-2 py-1 font-mono text-xs focus:border-indigo-400 focus:outline-none";

  return (
    <div className="mt-2 space-y-4 rounded-lg border border-gray-200 bg-white p-3">
      {/* THE add/override section — one, scoped to the env the card already
          shows. No separate all-environments section. */}
      {envOverride !== undefined && envName ? (
        <section>
          <h3 className="text-xs font-medium uppercase tracking-wider text-gray-400">
            Component variables — {envName}
          </h3>
          <p className="mt-0.5 text-xs text-gray-500">
            Variables for <span className="font-mono">{componentName}</span> in{" "}
            <span className="font-mono">{envName}</span>. A name matching an
            inherited key <strong>overrides</strong> it. Secret values are added
            at app/env scope — map inherited secrets below.
          </p>
          <div className="mt-2 space-y-1.5">
            {overrides.map((o, i) => (
              <div key={i} className="flex items-center gap-2">
                <input
                  className={`${inputCls} w-44`}
                  placeholder="NAME"
                  value={o.name}
                  onChange={(e) =>
                    setOverrides((cur) =>
                      cur.map((x, j) => (j === i ? { ...x, name: e.target.value } : x)),
                    )
                  }
                />
                <input
                  className={`${inputCls} flex-1`}
                  placeholder="value"
                  value={o.value}
                  onChange={(e) =>
                    setOverrides((cur) =>
                      cur.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)),
                    )
                  }
                />
                <button
                  onClick={() => setOverrides((cur) => cur.filter((_, j) => j !== i))}
                  className="rounded px-1.5 py-0.5 text-xs text-red-600 hover:bg-red-50"
                >
                  ✕
                </button>
              </div>
            ))}
            <button
              onClick={() => setOverrides((cur) => [...cur, { name: "", value: "" }])}
              className="rounded-md border border-dashed border-gray-300 px-2.5 py-1 text-xs font-medium text-gray-500 hover:bg-gray-50"
            >
              + Add variable
            </button>
          </div>
        </section>
      ) : (
        <p className="text-xs text-gray-500">
          Curate what this component inherits below; add its variables from the
          component card once the app exists.
        </p>
      )}

      {appCtx && (
        <div className="rounded-md border border-gray-100">
          <button
            type="button"
            onClick={() => setInheritedOpen((v) => !v)}
            className="flex w-full items-center gap-1.5 px-3 py-2 text-left text-xs font-medium text-gray-600 hover:bg-gray-50"
          >
            <span className={`transition-transform ${inheritedOpen ? "rotate-90" : ""}`}>▸</span>
            Inherited from the app
            <span className="font-normal text-gray-400">
              ({nVars} variable{nVars === 1 ? "" : "s"}, {nSecrets} secret
              {nSecrets === 1 ? "" : "s"}
              {nExcluded > 0 ? `, ${nExcluded} excluded` : ""}
              {keptLiterals.length > 0 ? `, +${keptLiterals.length} literal` : ""})
            </span>
          </button>
          {inheritedOpen && (
            <div className="border-t border-gray-100 px-3 py-2">
              {curated && (
                <div className="mb-2 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-800">
                  Excluding or renaming keys switches this component to an{" "}
                  <strong>explicit list</strong> — new app variables stop
                  flowing in automatically until everything is included again.
                </div>
              )}
              <p className="mb-2 text-xs text-gray-500">
                Uncheck a key to hide it from this component; rename to expose
                it under a different variable name.
              </p>
              {loadingKeys ? (
                <p className="text-xs text-gray-400">Loading keys…</p>
              ) : rows.length === 0 ? (
                <p className="text-xs italic text-gray-400">
                  The app defines no variables or secrets yet.
                </p>
              ) : (
                <ul className="divide-y divide-gray-50 rounded-lg border border-gray-100">
                  {/* Master checkbox: indeterminate when only some keys are
                      included, so the mixed state is visible at a glance.
                      Deselect-all = the component inherits nothing. */}
                  <li className="flex items-center gap-2 bg-gray-50/60 px-3 py-1.5">
                    <input
                      type="checkbox"
                      checked={rows.every((r) => included[r.key] !== false)}
                      ref={(el) => {
                        if (el) {
                          const some = rows.some((r) => included[r.key] !== false);
                          const all = rows.every((r) => included[r.key] !== false);
                          el.indeterminate = some && !all;
                        }
                      }}
                      onChange={(e) =>
                        setIncluded(
                          Object.fromEntries(rows.map((r) => [r.key, e.target.checked])),
                        )
                      }
                    />
                    <span className="text-xs font-medium text-gray-600">
                      {rows.every((r) => included[r.key] !== false)
                        ? "Deselect all"
                        : "Select all"}
                    </span>
                  </li>
                  {rows.map((r) => (
                    <li key={r.key} className="flex items-center gap-2 px-3 py-1.5">
                      <input
                        type="checkbox"
                        checked={included[r.key] !== false}
                        onChange={(e) =>
                          setIncluded((cur) => ({ ...cur, [r.key]: e.target.checked }))
                        }
                      />
                      <span className="min-w-0 flex-1 truncate font-mono text-xs text-gray-800">
                        {r.key}
                        {r.isSecret && (
                          <span className="ml-1.5 rounded bg-amber-50 px-1 py-0.5 text-[10px] font-medium text-amber-600">
                            secret
                          </span>
                        )}
                        {!r.isSecret && r.value !== undefined && (
                          <span className="ml-2 text-gray-400">= {r.value}</span>
                        )}
                      </span>
                      {included[r.key] !== false && (
                        <input
                          className={`${inputCls} w-36`}
                          placeholder="expose as (optional)"
                          value={renames[r.key] ?? ""}
                          onChange={(e) =>
                            setRenames((cur) => ({ ...cur, [r.key]: e.target.value }))
                          }
                        />
                      )}
                    </li>
                  ))}
                </ul>
              )}
              {staleMappings.length > 0 && (
                <p className="mt-2 text-xs text-amber-700">
                  {staleMappings.length} stored mapping(s) reference keys the app
                  no longer defines (
                  {staleMappings.map((m) => m.fromConfig || m.fromSecret).join(", ")}
                  ) — kept as-is; remove them by re-including everything.
                </p>
              )}
              {keptLiterals.length > 0 && (
                <p className="mt-2 text-xs text-gray-400">
                  {keptLiterals.length} app-level literal entr
                  {keptLiterals.length === 1 ? "y" : "ies"} (
                  {keptLiterals.map((l) => l.name).join(", ")}) kept — managed
                  via the API.
                </p>
              )}
            </div>
          )}
        </div>
      )}

      <div className="flex justify-end">
        <button
          onClick={handleSave}
          disabled={saving}
          className="rounded-md bg-gray-900 px-3 py-1.5 text-xs font-medium text-white hover:bg-gray-700 disabled:opacity-50"
        >
          {saving ? "Saving…" : saveLabel}
        </button>
      </div>
    </div>
  );
}

export default ComponentEnvPanel;
