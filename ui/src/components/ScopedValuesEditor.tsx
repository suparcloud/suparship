import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";

import type { ConfigVariables } from "../lib/configVars";
import {
  diffOverlay,
  mergeOverlay,
  parseYamlOverlay,
  removedBaseKeys,
  stringifyOverlay,
} from "../lib/yamlDoc";
import { ValuesEditor } from "./ValuesEditor";

// A selectable editing scope (all-envs / an env / preview / a cluster). `id` is an
// opaque key the caller's adapters understand; `hasOverride` drives the ● dot.
export interface ValuesScope {
  id: string;
  label: string;
  hasOverride: boolean;
}

// The resolved base for a scope: the values that would deploy with THIS scope's own
// override emptied (chart ⊕ platform ⊕ lower dev layers). The editor seeds
// base ⊕ storedOverride and persists only the diff vs base.
export interface ScopeBase {
  values: Record<string, unknown>;
  chartDefaultsAvailable: boolean;
}

export interface ScopedValuesEditorProps {
  scopes: ValuesScope[];
  initialScopeId?: string;
  /** Token catalog for the "Insert variable" picker (project- or platform-scoped). */
  configVars: ConfigVariables | null;
  /**
   * Resolve a scope's base with its own override emptied. `diffs` carries every
   * scope's current (possibly-unsaved) override so the adapter can layer lower
   * scopes correctly (e.g. an env base includes the all-envs diff).
   */
  getBase: (
    scopeId: string,
    diffs: Record<string, Record<string, unknown>>,
  ) => Promise<ScopeBase>;
  /** The persisted override (diff) currently stored for a scope. */
  getStoredOverride: (scopeId: string) => Record<string, unknown> | undefined;
  /** Persist the computed diff for a scope. */
  saveOverride: (
    scopeId: string,
    diff: Record<string, unknown>,
  ) => Promise<void>;
  /** Optional per-scope help copy shown under the scope tabs. */
  scopeHelp?: (scopeId: string) => ReactNode;
  /** Called after a successful save (e.g. to refetch parent data). */
  onSaved?: () => void | Promise<void>;
}

// ScopedValuesEditor is the one values editor shared by the app-component panel and
// the template platform-overrides section. The developer edits the FULL resolved
// values for a scope in place; we persist only the diff (minimal override), so
// untouched platform values keep tracking the platform. Additive-only: removing a
// platform-provided key re-inherits it (surfaced as a note) — see yamlDoc.diffOverlay.
export function ScopedValuesEditor({
  scopes,
  initialScopeId,
  configVars,
  getBase,
  getStoredOverride,
  saveOverride,
  scopeHelp,
  onSaved,
}: ScopedValuesEditorProps) {
  const [scopeId, setScopeId] = useState(
    initialScopeId ?? scopes[0]?.id ?? "",
  );
  // Full-resolved editor buffer per scope (undefined = not yet seeded from base).
  const [buffers, setBuffers] = useState<Record<string, string>>({});
  // Fetched base per scope (this scope's own override excluded).
  const [bases, setBases] = useState<Record<string, ScopeBase>>({});
  const [yamlError, setYamlError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  const base = bases[scopeId];
  const buffer = buffers[scopeId];
  const seeded = buffer !== undefined && base !== undefined;

  // The current diff for a scope: from the live buffer when seeded, else the
  // persisted override. Used both for the "Your override" pane and to feed getBase
  // the lower scopes' overrides.
  const diffFor = useCallback(
    (id: string): Record<string, unknown> => {
      const b = bases[id];
      const buf = buffers[id];
      if (b && buf !== undefined) {
        const { value } = parseYamlOverlay(buf);
        if (value) return diffOverlay(b.values, value);
      }
      return getStoredOverride(id) ?? {};
    },
    [bases, buffers, getStoredOverride],
  );

  const allDiffs = useMemo(
    () => Object.fromEntries(scopes.map((s) => [s.id, diffFor(s.id)])),
    [scopes, diffFor],
  );

  // Serialized diffs of the OTHER scopes — the only cross-scope input to this
  // scope's base. Excluding the current scope avoids a fetch loop (this scope's own
  // diff never affects its base).
  const otherDiffsKey = useMemo(
    () =>
      JSON.stringify(
        scopes.filter((s) => s.id !== scopeId).map((s) => [s.id, diffFor(s.id)]),
      ),
    [scopes, scopeId, diffFor],
  );

  // Fetch (or refresh) the selected scope's base; seed its buffer the first time.
  const storedRef = useRef(getStoredOverride);
  storedRef.current = getStoredOverride;
  useEffect(() => {
    if (!scopeId) return;
    let cancelled = false;
    const handle = setTimeout(() => {
      getBase(scopeId, allDiffs)
        .then((b) => {
          if (cancelled) return;
          setBases((prev) => ({ ...prev, [scopeId]: b }));
          setBuffers((prev) =>
            prev[scopeId] !== undefined
              ? prev
              : {
                  ...prev,
                  [scopeId]: stringifyOverlay(
                    mergeOverlay(b.values, storedRef.current(scopeId)),
                  ),
                },
          );
        })
        .catch(() => {
          /* keep last good base */
        });
    }, 300);
    return () => {
      cancelled = true;
      clearTimeout(handle);
    };
    // allDiffs intentionally omitted; otherDiffsKey captures the relevant change.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scopeId, otherDiffsKey, getBase]);

  const currentDiff = seeded ? diffFor(scopeId) : getStoredOverride(scopeId) ?? {};
  const overrideText = stringifyOverlay(currentDiff);
  const removed = useMemo(() => {
    if (!seeded) return [];
    const { value } = parseYamlOverlay(buffer);
    return value ? removedBaseKeys(base.values, value) : [];
  }, [seeded, base, buffer]);

  const save = useCallback(async () => {
    if (!base) return;
    const { value, error } = parseYamlOverlay(buffers[scopeId] ?? "");
    if (error || !value) {
      setYamlError(error ?? "Invalid YAML");
      return;
    }
    setSaving(true);
    try {
      await saveOverride(scopeId, diffOverlay(base.values, value));
      await onSaved?.();
    } finally {
      setSaving(false);
    }
  }, [base, buffers, scopeId, saveOverride, onSaved]);

  const scopeLabel = scopes.find((s) => s.id === scopeId)?.label ?? scopeId;

  return (
    <div>
      {/* Scope tabs with ● override dots */}
      <div className="mb-2 flex flex-wrap items-center gap-1">
        {scopes.map((s) => (
          <button
            key={s.id}
            type="button"
            onClick={() => {
              setScopeId(s.id);
              setYamlError(null);
            }}
            className={`rounded-md px-2.5 py-1 text-xs font-medium ${
              s.id === scopeId
                ? "bg-gray-900 text-white"
                : "bg-gray-100 text-gray-600 hover:bg-gray-200"
            }`}
          >
            {s.label}
            {s.hasOverride && (
              <span
                className={s.id === scopeId ? "text-white" : "text-indigo-500"}
              >
                {" "}
                ●
              </span>
            )}
          </button>
        ))}
      </div>

      {scopeHelp && (
        <p className="mb-3 max-w-3xl text-xs text-gray-500">
          {scopeHelp(scopeId)}
        </p>
      )}

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        {/* LEFT — edit the full resolved values in place */}
        <div>
          {seeded ? (
            <ValuesEditor
              label={`${scopeLabel} — resolved values (edit in place)`}
              value={buffer}
              configVars={configVars}
              height="26rem"
              placeholder={"# no values resolved for this scope"}
              onChange={(t) =>
                setBuffers((prev) => ({ ...prev, [scopeId]: t }))
              }
              onValidChange={(_, err) => setYamlError(err)}
            />
          ) : (
            <div className="flex h-[26rem] items-center justify-center rounded-lg border border-gray-200 text-xs text-gray-400">
              Loading resolved values…
            </div>
          )}
          {base && !base.chartDefaultsAvailable && (
            <p className="mt-1 text-xs text-gray-400">
              Chart defaults aren't readable for this template; showing platform +
              overrides only.
            </p>
          )}
          <p className="mt-1 text-xs text-gray-400">
            Edit anything in place — only your diff is saved. Lists are stored
            whole (editing one pins the list). {"((…))"} tokens resolve at deploy.
          </p>
          {removed.length > 0 && (
            <p className="mt-1 text-xs text-amber-600">
              Removed platform-provided key{removed.length > 1 ? "s" : ""}{" "}
              <span className="font-mono">{removed.slice(0, 3).join(", ")}</span>
              {removed.length > 3 ? "…" : ""} will be re-inherited — deleting
              platform keys isn't supported yet.
            </p>
          )}
        </div>

        {/* RIGHT — the minimal diff we actually store */}
        <div>
          {overrideText.trim() ? (
            <ValuesEditor
              label="Your override — saved"
              value={overrideText}
              height="26rem"
              readOnly
            />
          ) : (
            <div>
              <span className="text-xs font-medium uppercase tracking-wide text-gray-500">
                Your override — saved
              </span>
              <div className="mt-2 flex h-[26rem] items-center justify-center rounded-lg border border-dashed border-gray-200 text-xs text-gray-400">
                Nothing overridden — inherits platform defaults.
              </div>
            </div>
          )}
        </div>
      </div>

      <div className="mt-3 flex items-center gap-3">
        <button
          type="button"
          onClick={save}
          disabled={saving || yamlError !== null || !seeded}
          className="rounded-md bg-gray-900 px-3 py-1.5 text-sm font-medium text-white hover:bg-gray-700 disabled:opacity-50"
        >
          {saving ? "Saving…" : "Save"}
        </button>
        {yamlError && (
          <span className="text-xs text-red-600">Invalid YAML: {yamlError}</span>
        )}
      </div>
    </div>
  );
}

export default ScopedValuesEditor;
