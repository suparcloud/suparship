import { parse, stringify } from "yaml";

// parseYamlOverlay parses a YAML overlay document the user typed into the values
// editor. Empty/whitespace-only text is a valid "no overrides" overlay → {}.
// Returns the parsed object or an error message (never throws).
export function parseYamlOverlay(text: string): {
  value: Record<string, unknown> | null;
  error: string | null;
} {
  const trimmed = text.trim();
  if (trimmed === "") {
    return { value: {}, error: null };
  }
  try {
    const parsed = parse(text);
    if (parsed === null || parsed === undefined) {
      return { value: {}, error: null };
    }
    if (typeof parsed !== "object" || Array.isArray(parsed)) {
      return {
        value: null,
        error: "Values must be a YAML mapping (key: value), not a list or scalar.",
      };
    }
    return { value: parsed as Record<string, unknown>, error: null };
  } catch (e) {
    return { value: null, error: e instanceof Error ? e.message : String(e) };
  }
}

// mergeOverlay deep-merges overlay onto base (returning a new object), mirroring
// the server's helmvalues.DeepMerge: nested maps merge key-by-key; any non-map
// value (scalar, array) replaces wholesale. Used to render a live effective
// preview client-side while the user edits, without a round-trip per keystroke.
export function mergeOverlay(
  base: Record<string, unknown> | null | undefined,
  overlay: Record<string, unknown> | null | undefined,
): Record<string, unknown> {
  const out: Record<string, unknown> = base ? structuredClone(base) : {};
  if (!overlay) return out;
  for (const [k, ov] of Object.entries(overlay)) {
    const isMap = (v: unknown): v is Record<string, unknown> =>
      typeof v === "object" && v !== null && !Array.isArray(v);
    if (isMap(ov) && isMap(out[k])) {
      out[k] = mergeOverlay(out[k] as Record<string, unknown>, ov);
    } else {
      out[k] = ov;
    }
  }
  return out;
}

// deepEqual compares two parsed-YAML values structurally: maps key-by-key, arrays
// element-by-element in order (Helm treats arrays as ordered, replace-wholesale),
// scalars by ===. Used to decide whether an edited leaf still equals its base.
function deepEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (Array.isArray(a) || Array.isArray(b)) {
    if (!Array.isArray(a) || !Array.isArray(b) || a.length !== b.length) {
      return false;
    }
    return a.every((v, i) => deepEqual(v, b[i]));
  }
  const isMap = (v: unknown): v is Record<string, unknown> =>
    typeof v === "object" && v !== null;
  if (isMap(a) && isMap(b)) {
    const ak = Object.keys(a);
    const bk = Object.keys(b);
    if (ak.length !== bk.length) return false;
    return ak.every((k) => k in b && deepEqual(a[k], b[k]));
  }
  return false;
}

// diffOverlay returns the minimal overlay of `full` relative to `base`: the
// developer's override = only the leaves of `full` that differ from `base`
// (Helm-leaf granularity — a differing array or scalar is kept whole; nested maps
// recurse). Keys present in `base` but dropped from `full` are omitted, NOT stored
// as deletions — removing a base line simply re-inherits it. Inverse of mergeOverlay:
// diffOverlay(base, mergeOverlay(base, delta)) === delta for map-shaped deltas.
export function diffOverlay(
  base: Record<string, unknown> | null | undefined,
  full: Record<string, unknown> | null | undefined,
): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  if (!full) return out;
  const b = base ?? {};
  const isMap = (v: unknown): v is Record<string, unknown> =>
    typeof v === "object" && v !== null && !Array.isArray(v);
  for (const [k, fv] of Object.entries(full)) {
    const bv = b[k];
    if (isMap(fv) && isMap(bv)) {
      const sub = diffOverlay(bv, fv);
      if (Object.keys(sub).length > 0) out[k] = sub;
    } else if (!(k in b) || !deepEqual(fv, bv)) {
      out[k] = fv;
    }
  }
  return out;
}

// stringifyOverlay renders an overlay object back to YAML for seeding the editor.
// An empty or absent object becomes "" so the editor starts blank ("inherit all").
export function stringifyOverlay(
  obj: Record<string, unknown> | null | undefined,
): string {
  if (!obj || Object.keys(obj).length === 0) {
    return "";
  }
  return stringify(obj);
}
