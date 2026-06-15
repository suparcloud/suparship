import { useEffect, useMemo, useRef, useState } from "react";

import type { ConfigVariables } from "../lib/configVars";

interface VariablePickerProps {
  variables: ConfigVariables | null;
  onInsert: (token: string) => void;
  /** Optional compact button label. Defaults to "Insert variable". */
  label?: string;
}

interface Entry {
  token: string;
  label: string;
  group: string;
  hint?: string;
}

// VariablePicker renders an "Insert variable" dropdown: a grouped, searchable
// list of platform metadata ({platform.*}) and non-secret env vars ({vars.*}).
// Clicking an entry calls onInsert with its token. Discoverable — users never
// type the token syntax by hand.
export function VariablePicker({
  variables,
  onInsert,
  label = "Insert variable",
}: VariablePickerProps) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    function onClick(e: MouseEvent) {
      if (ref.current && !ref.current.contains(e.target as Node)) {
        setOpen(false);
      }
    }
    document.addEventListener("mousedown", onClick);
    return () => document.removeEventListener("mousedown", onClick);
  }, [open]);

  const entries = useMemo<Entry[]>(() => {
    if (!variables) return [];
    const platform: Entry[] = variables.platform.map((p) => ({
      token: p.token,
      label: p.label,
      group: p.group,
      hint: p.description,
    }));
    const vars: Entry[] = variables.vars.map((v) => ({
      token: v.token,
      label: v.name,
      group: "Env vars",
      hint: v.scope,
    }));
    return [...platform, ...vars];
  }, [variables]);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    const matches = q
      ? entries.filter(
          (e) =>
            e.token.toLowerCase().includes(q) ||
            e.label.toLowerCase().includes(q),
        )
      : entries;
    const groups: Record<string, Entry[]> = {};
    for (const e of matches) (groups[e.group] ??= []).push(e);
    return groups;
  }, [entries, query]);

  return (
    <div className="relative inline-block" ref={ref}>
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="rounded border border-gray-300 px-2 py-1 text-xs font-medium text-gray-600 hover:bg-gray-50"
        title="Insert a platform or env-var reference"
      >
        {"{ } "}
        {label} ▾
      </button>
      {open && (
        <div className="absolute right-0 z-20 mt-1 w-72 rounded-md border border-gray-200 bg-white shadow-lg">
          <div className="border-b border-gray-100 p-2">
            <input
              autoFocus
              type="text"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search variables…"
              className="block w-full rounded border border-gray-300 px-2 py-1 text-xs focus:border-indigo-500 focus:outline-none"
            />
          </div>
          <div className="max-h-64 overflow-y-auto py-1">
            {!variables && (
              <p className="px-3 py-2 text-xs text-gray-400">Loading…</p>
            )}
            {variables && Object.keys(filtered).length === 0 && (
              <p className="px-3 py-2 text-xs text-gray-400">No matches.</p>
            )}
            {Object.entries(filtered).map(([group, items]) => (
              <div key={group}>
                <div className="px-3 py-1 text-[10px] font-semibold uppercase tracking-wider text-gray-400">
                  {group}
                </div>
                {items.map((e) => (
                  <button
                    key={e.token}
                    type="button"
                    onClick={() => {
                      onInsert(e.token);
                      setOpen(false);
                      setQuery("");
                    }}
                    className="flex w-full items-center justify-between gap-2 px-3 py-1.5 text-left hover:bg-indigo-50"
                  >
                    <span className="font-mono text-xs text-gray-800">
                      {e.token}
                    </span>
                    {e.hint && (
                      <span className="shrink-0 text-[10px] text-gray-400">
                        {e.hint}
                      </span>
                    )}
                  </button>
                ))}
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

// insertAtCursor inserts text at the current selection of an input/textarea (or
// appends when no ref/selection), calling onChange with the new value. Returns
// the new value so callers can also update React state.
export function insertAtCursor(
  el: HTMLInputElement | HTMLTextAreaElement | null,
  current: string,
  text: string,
): string {
  if (!el || el.selectionStart == null) {
    return current + text;
  }
  const start = el.selectionStart;
  const end = el.selectionEnd ?? start;
  const next = current.slice(0, start) + text + current.slice(end);
  // Restore caret just after the inserted token on the next tick.
  requestAnimationFrame(() => {
    const pos = start + text.length;
    el.focus();
    el.setSelectionRange(pos, pos);
  });
  return next;
}
