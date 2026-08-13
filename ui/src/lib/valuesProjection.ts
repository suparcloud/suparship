import { Document, isMap } from "yaml";

import type { ValueField } from "../types";
import { getAtPath, linePathsOfText, setAtPath } from "./valuesTree";

// The developer-facing values projection: given a template's declared ValueFields
// and the EFFECTIVE values for an env (chart ⊕ platform layers), produce the small,
// self-documenting overlay the editor is seeded with.
//
// Why this is only a presentation concern: diffOverlay (lib/yamlDoc.ts) walks only
// the edited document's keys and never emits deletions, so seeding a subset leaves
// every hidden platform key untouched — it simply isn't in the diff, and keeps
// applying at publish. Nothing about storage or the publish layering changes.

// splitPath turns a declared dotted path into key segments. Dotted form can't
// express a key containing a dot — the same constraint TemplateImage.tagKey and
// the legacy `mappings` keys already carry.
export function splitPath(path: string): string[] {
  return path.split(".").filter((s) => s !== "");
}

// declaredPaths returns every dotted path a field owns: the primary path plus
// its mirrors. Every consumer that asks "which keys belong to this field"
// (seeding, fan-out writes, out-of-projection accounting) walks this list.
export function declaredPaths(f: ValueField): string[] {
  return [f.path, ...(f.mirrors ?? [])];
}

// hasProjection reports whether a template declares a projection at all. False =>
// callers keep seeding from the full concise platform base (pre-projection behaviour).
export function hasProjection(fields: ValueField[] | undefined): boolean {
  return !!fields && fields.length > 0;
}

// inferType guesses a ValueField input type from an observed effective value —
// used to prefill a field created via click-to-expose. Maps and arrays return
// undefined (free-form): the projection can carry them, but they have no typed
// control.
export function inferType(
  v: unknown,
): "string" | "number" | "boolean" | undefined {
  switch (typeof v) {
    case "boolean":
      return "boolean";
    case "number":
      return "number";
    case "string":
      return "string";
    default:
      return undefined;
  }
}

// effectiveValueOf resolves what to SHOW for a field: the value already present in
// the effective values, else the field's declared default.
export function effectiveValueOf(
  base: Record<string, unknown> | null | undefined,
  f: ValueField,
): unknown {
  const v = getAtPath(base, splitPath(f.path));
  return v === undefined ? f.default : v;
}

// A field with no meaningful value is one the developer has to supply.
function isUnset(v: unknown): boolean {
  return v === undefined || v === null || v === "";
}

// projectValues builds the subset object holding just the declared paths, taking
// each value from the effective values (falling back to the declared default).
// Used for the future form renderer and for tests; the 0.1 editor uses
// stringifyProjection, which additionally decides what to comment out.
// Mirror paths are included, each with its own effective value — mirrors only
// converge once the developer actually sets the field.
export function projectValues(
  base: Record<string, unknown> | null | undefined,
  fields: ValueField[],
): Record<string, unknown> {
  let out: Record<string, unknown> = {};
  for (const f of fields) {
    for (const p of declaredPaths(f)) {
      const segs = splitPath(p);
      if (segs.length === 0) continue;
      const v = getAtPath(base, segs);
      out = setAtPath(out, segs, v === undefined ? f.default : v);
    }
  }
  return out;
}

// commentLinesFor renders a field's help text: title, description, and the enum
// vocabulary. Returned WITHOUT leading "#" — the yaml lib adds it for commentBefore,
// and the inherited block adds its own.
function commentLinesFor(f: ValueField): string[] {
  const lines: string[] = [];
  lines.push(f.required ? `${f.title || f.path} (required)` : f.title || f.path);
  if (f.description) {
    for (const l of f.description.trim().split("\n")) lines.push(l.trim());
  }
  if (f.type === "enum" && f.options?.length) {
    lines.push(`one of: ${f.options.join(" | ")}`);
  }
  return lines;
}

// attachComment hangs a field's help text above its KEY in the document, so the
// rendered YAML reads as a documented config file.
function attachComment(doc: Document, segs: string[], lines: string[]) {
  const parent =
    segs.length === 1 ? doc.contents : doc.getIn(segs.slice(0, -1), true);
  if (!isMap(parent)) return;
  const last = segs[segs.length - 1]!;
  for (const pair of parent.items) {
    const key = (pair as { key?: unknown }).key as
      | { value?: unknown; commentBefore?: string }
      | undefined;
    if (!key || key.value !== last) continue;
    // The yaml lib emits "#" + content per line, so carry our own leading space.
    key.commentBefore = lines.map((l) => ` ${l}`).join("\n");
    return;
  }
}

// isPrefixOrEqual reports whether path `a` is `b` or an ancestor of it.
function isPrefixOrEqual(a: string[], b: string[]): boolean {
  return a.length <= b.length && a.every((s, i) => b[i] === s);
}

function indentOf(line: string): number {
  return line.length - line.trimStart().length;
}

// stringifyProjection renders the seed text for the values editor.
//
// A field is emitted LIVE only when the developer must actually supply it —
// `required`, or the effective value is unset. Everything else is emitted
// COMMENTED OUT with its inherited value shown.
//
// That split is the point, not decoration. The interesting defaults live in the
// CHART layer, so a projection prefilled live across the board would make
// diffOverlay persist values the developer never chose — PINNING them, so a later
// chart or template default change silently stops reaching the app. Commented lines
// produce no diff, so inheritance keeps working until the developer opts in.
//
// All fields are rendered into ONE document in declaration order, then the
// inherited lines are commented out IN PLACE. Emitting the inherited keys as a
// separate trailing block would look tidier but breaks the moment a developer uses
// it: uncommenting `components.web.port` under a live `components:` block yields a
// duplicate top-level key and a YAML error. Commenting per line keeps every key in
// its correct position in the tree, so uncommenting just works.
export function stringifyProjection(
  base: Record<string, unknown> | null | undefined,
  fields: ValueField[],
): string {
  const present = fields.filter((f) => splitPath(f.path).length > 0);
  if (present.length === 0) return "";

  // The live/commented decision is per FIELD (from the primary path's value),
  // but every declared path — primary and mirrors — is emitted, each showing its
  // OWN inherited value: mirrors may legitimately diverge (containerPort 8080 vs
  // service.port 80) until the developer sets the field and the form syncs them.
  const livePaths: string[][] = [];
  let obj: Record<string, unknown> = {};
  for (const f of present) {
    const live = f.required || isUnset(effectiveValueOf(base, f));
    for (const p of declaredPaths(f)) {
      const segs = splitPath(p);
      if (segs.length === 0) continue;
      if (live) livePaths.push(segs);
      const raw = getAtPath(base, segs);
      const v = raw === undefined ? f.default : raw;
      obj = setAtPath(obj, segs, live && isUnset(v) ? "" : v);
    }
  }

  const doc = new Document(obj);
  for (const f of present) {
    attachComment(doc, splitPath(f.path), commentLinesFor(f));
    for (const m of f.mirrors ?? []) {
      attachComment(doc, splitPath(m), [`same value as ${f.path}`]);
    }
  }

  // Comment out every line that no live field needs. A parent map is kept only when
  // a live field lives under it — otherwise `healthCheck:` would survive with its
  // sole child commented and parse as an explicit null override.
  const rows = linePathsOfText(String(doc));
  const out: string[] = [];
  let blockIndent: number | null = null;
  // A field's help text is rendered as comment lines ABOVE its key. They carry no
  // path, so they're buffered until we know whether that key stays live — a
  // commented key needs its help text re-hung at column 0 too, or the block reads
  // ragged ("    # Title" above "#     key: v").
  let pending: string[] = [];
  const flush = (commented: boolean) => {
    for (const l of pending) {
      out.push(
        commented
          ? `#${" ".repeat(indentOf(l) + 1)}${l.replace(/^\s*#\s?/, "")}`
          : l,
      );
    }
    pending = [];
  };

  for (const row of rows) {
    const indent = indentOf(row.text);
    if (blockIndent !== null && (row.text.trim() === "" || indent > blockIndent)) {
      out.push(row.text.trim() === "" ? "#" : `# ${row.text}`);
      continue;
    }
    blockIndent = null;
    if (!row.path) {
      if (row.text.trim().startsWith("#")) pending.push(row.text);
      else out.push(row.text);
      continue;
    }
    const commented = !livePaths.some((lp) => isPrefixOrEqual(row.path!, lp));
    flush(commented);
    if (commented) {
      out.push(`# ${row.text}`);
      blockIndent = indent; // also comment this key's nested value / array items
      continue;
    }
    out.push(row.text);
  }
  flush(false);

  const header =
    livePaths.length === present.length
      ? []
      : ["# Commented lines are inherited — uncomment one to override it.", ""];
  return [...header, ...out].join("\n").replace(/\n*$/, "\n");
}
