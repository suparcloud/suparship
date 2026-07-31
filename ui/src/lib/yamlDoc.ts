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

// isMap reports whether v is a plain object (a YAML mapping), the only thing the
// merge/diff recurse into — arrays and scalars are treated as leaf values.
function isMap(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

// deepEqual compares two overlay values structurally (maps by key, arrays by
// element, scalars by ===). Used by diffOverlay to decide whether a leaf changed.
function deepEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (Array.isArray(a) && Array.isArray(b)) {
    return a.length === b.length && a.every((x, i) => deepEqual(x, b[i]));
  }
  if (isMap(a) && isMap(b)) {
    const ak = Object.keys(a);
    return (
      ak.length === Object.keys(b).length &&
      ak.every((k) => k in b && deepEqual(a[k], b[k]))
    );
  }
  return false;
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
    if (isMap(ov) && isMap(out[k])) {
      out[k] = mergeOverlay(out[k] as Record<string, unknown>, ov);
    } else {
      out[k] = ov;
    }
  }
  return out;
}

// diffOverlay returns the minimal ADDITIVE overlay D such that
// mergeOverlay(base, D) deep-equals `edited` for every key present in `edited`.
// It is the inverse of mergeOverlay for the diff-based editor: the developer edits
// the full resolved values (base ⊕ their override) and we persist only D.
//
//   - keys added in edited            → included
//   - scalars/arrays that changed     → included wholesale (mirrors merge: a list
//                                        edit pins the whole list)
//   - nested maps                     → recursed; the minimal sub-diff is kept
//   - unchanged keys                  → omitted (that's the point — minimal)
//   - keys in base but NOT in edited  → IGNORED. Additive merges have no delete
//                                        primitive, so a removed base key simply
//                                        re-inherits; see removedBaseKeys to warn.
export function diffOverlay(
  base: Record<string, unknown> | null | undefined,
  edited: Record<string, unknown> | null | undefined,
): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  if (!edited) return out;
  const b = base ?? {};
  for (const [k, ev] of Object.entries(edited)) {
    if (!(k in b)) {
      out[k] = ev;
      continue;
    }
    const bv = b[k];
    if (isMap(ev) && isMap(bv)) {
      const sub = diffOverlay(bv, ev);
      if (Object.keys(sub).length > 0) out[k] = sub;
    } else if (!deepEqual(bv, ev)) {
      out[k] = ev;
    }
  }
  return out;
}

// removedBaseKeys returns the dotted paths present in base but absent in edited —
// the deletions an additive overlay cannot express (they will re-inherit the base
// value). The editor surfaces these as a non-blocking "can't remove a
// platform/chart key here" note. Recurses only where both sides are maps.
export function removedBaseKeys(
  base: Record<string, unknown> | null | undefined,
  edited: Record<string, unknown> | null | undefined,
  prefix = "",
): string[] {
  const b = base ?? {};
  const e = edited ?? {};
  const out: string[] = [];
  for (const [k, bv] of Object.entries(b)) {
    const path = prefix ? `${prefix}.${k}` : k;
    if (!(k in e)) {
      out.push(path);
    } else if (isMap(bv) && isMap(e[k])) {
      out.push(...removedBaseKeys(bv, e[k] as Record<string, unknown>, path));
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
