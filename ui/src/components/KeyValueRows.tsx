// KeyValueRows is a buffer-only key/value editor for values entered before the
// target (app/env) exists — e.g. the create wizard. It holds rows purely in the
// parent's state; the parent decides where they go (a config-var field in the
// create request, or a post-create secret upsert). Set `masked` for secret
// values (password inputs); leave it off for visible config vars.

export interface KVRow {
  key: string;
  value: string;
}

// toEntries coerces rows to the { KEY: value } map the API takes, dropping rows
// with a blank key (a blank value is allowed — an intentionally empty entry).
export function toEntries(rows: KVRow[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const r of rows) {
    const k = r.key.trim();
    if (k) out[k] = r.value;
  }
  return out;
}

export function KeyValueRows({
  rows,
  onChange,
  addLabel = "Add",
  masked = false,
  keyPlaceholder = "KEY",
}: {
  rows: KVRow[];
  onChange: (rows: KVRow[]) => void;
  addLabel?: string;
  masked?: boolean;
  keyPlaceholder?: string;
}) {
  function update(i: number, patch: Partial<KVRow>) {
    onChange(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  }
  function remove(i: number) {
    onChange(rows.filter((_, j) => j !== i));
  }
  function add() {
    onChange([...rows, { key: "", value: "" }]);
  }

  const inputCls =
    "block rounded-md border border-gray-300 px-3 py-2 text-sm shadow-sm transition-colors focus:border-gray-900 focus:outline-none focus:ring-1 focus:ring-gray-900";

  return (
    <div className="space-y-2">
      {rows.map((r, i) => (
        <div key={i} className="flex flex-wrap items-center gap-2">
          <input
            type="text"
            className={`${inputCls} w-56 font-mono`}
            placeholder={keyPlaceholder}
            value={r.key}
            onChange={(e) => update(i, { key: e.target.value })}
          />
          <input
            type={masked ? "password" : "text"}
            autoComplete={masked ? "new-password" : "off"}
            className={`${inputCls} min-w-[10rem] flex-1`}
            placeholder="value"
            value={r.value}
            onChange={(e) => update(i, { value: e.target.value })}
          />
          <button
            type="button"
            onClick={() => remove(i)}
            className="rounded-md px-2 py-1 text-xs text-gray-400 hover:bg-red-50 hover:text-red-600"
            aria-label="Remove entry"
          >
            ✕
          </button>
        </div>
      ))}
      <button
        type="button"
        onClick={add}
        className="rounded-md border border-dashed border-gray-300 px-3 py-1.5 text-xs font-medium text-gray-600 hover:border-gray-400 hover:text-gray-900"
      >
        + {addLabel}
      </button>
    </div>
  );
}
