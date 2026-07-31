import { useMemo } from "react";

import {
  getAtPath,
  pathKey,
  stringifyWithLinePaths,
  type ValueLine,
} from "../lib/valuesTree";

type Origin = "scope" | "base" | "inherited";

interface EffectiveValuesViewProps {
  // The backend's Helm-merged "as deployed" values for the selected env.
  effective: Record<string, unknown> | null;
  // pathKey-encoded leaf paths the CURRENT scope's override sets ("your {scope}").
  scopePaths: Set<string>;
  // pathKey-encoded leaf paths the all-envs override sets ("your all-envs"); pass an
  // empty set when the current scope IS all-envs. Checked after scopePaths, so a
  // path set by both is attributed to the (higher-precedence) current scope.
  basePaths: Set<string>;
  // Label for the current scope in the legend / tag, e.g. "staging" or "preview".
  scopeLabel: string;
  // Add the value at `path` to the current scope's override (value = its effective
  // value). For an inherited or all-envs-only value.
  onOverride: (path: string[], value: unknown) => void;
  // Remove `path` from the current scope's override (revert to inherited).
  onReset: (path: string[]) => void;
}

// EffectiveValuesView renders the Helm-merged effective values read-only, tints
// each value by where it comes from (your scope / your all-envs / inherited), and
// lets you click-to-override an inherited value or reset one of your own — turning
// "blank box, guess the YAML path" into "browse the rendered config, click what to
// change." Origin is pure Helm precedence (highest override overlay that owns the
// leaf; arrays are whole), computed from the overlays the caller already holds.
export function EffectiveValuesView({
  effective,
  scopePaths,
  basePaths,
  scopeLabel,
  onOverride,
  onReset,
}: EffectiveValuesViewProps) {
  const rows = useMemo(
    () => stringifyWithLinePaths(effective),
    [effective],
  );

  const originOf = (r: ValueLine): Origin | null => {
    // Only concrete values (a scalar leaf or a whole array) carry an origin; map
    // keys and array-item lines are structural.
    if (!r.path || (r.kind !== "leaf" && r.kind !== "arrayKey")) return null;
    const k = pathKey(r.path);
    if (scopePaths.has(k)) return "scope";
    if (basePaths.has(k)) return "base";
    return "inherited";
  };

  const isEmpty =
    rows.length === 0 ||
    (rows.length === 1 &&
      (rows[0]?.text.trim() === "{}" || rows[0]?.text.trim() === ""));

  return (
    <div>
      <div className="mb-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-gray-500">
        <span className="flex items-center gap-1">
          <span className="inline-block h-3 w-3 rounded-sm bg-indigo-100 ring-1 ring-indigo-200" />
          your {scopeLabel}
        </span>
        <span className="flex items-center gap-1">
          <span className="inline-block h-3 w-3 rounded-sm bg-amber-100 ring-1 ring-amber-200" />
          your all-envs
        </span>
        <span className="flex items-center gap-1">
          <span className="inline-block h-3 w-3 rounded-sm bg-white ring-1 ring-gray-200" />
          inherited (chart ⊕ template ⊕ platform)
        </span>
      </div>
      <div className="max-h-[26rem] overflow-auto rounded-lg border border-gray-200 bg-white py-1 font-mono text-xs leading-5">
        {isEmpty ? (
          <div className="px-3 py-4 text-gray-400">
            No values resolved for this scope.
          </div>
        ) : (
          rows.map((r, i) => {
            const origin = originOf(r);
            return (
              <div
                key={i}
                className={`group flex items-center justify-between gap-2 px-3 ${
                  origin === "scope"
                    ? "bg-indigo-50"
                    : origin === "base"
                      ? "bg-amber-50"
                      : ""
                }`}
              >
                <span className="whitespace-pre text-gray-800">
                  {r.text || " "}
                </span>
                {origin === "scope" ? (
                  <button
                    type="button"
                    onClick={() => onReset(r.path!)}
                    className="shrink-0 rounded border border-gray-200 px-1.5 py-0.5 text-[10px] font-medium text-gray-500 opacity-0 transition hover:bg-gray-100 group-hover:opacity-100"
                  >
                    Reset
                  </button>
                ) : origin ? (
                  <button
                    type="button"
                    onClick={() => onOverride(r.path!, getAtPath(effective, r.path!))}
                    className="shrink-0 rounded border border-indigo-200 px-1.5 py-0.5 text-[10px] font-medium text-indigo-600 opacity-0 transition hover:bg-indigo-50 group-hover:opacity-100"
                  >
                    Override
                  </button>
                ) : null}
              </div>
            );
          })
        )}
      </div>
    </div>
  );
}

export default EffectiveValuesView;
