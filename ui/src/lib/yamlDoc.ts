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
