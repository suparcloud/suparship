// SecretValueRows is a buffer-only key/value editor for secret VALUES entered
// before the target (app/env) exists — e.g. the create wizard. Unlike
// SecretEditor (which fetches key names and upserts to a live vault via
// callbacks), this holds rows purely in the parent's state; the parent flushes
// them to the /secrets API AFTER the app is created. Values are masked and never
// leave component state until that post-create flush.

export interface SecretRow {
  key: string;
  value: string;
}

// toEntries coerces rows to the { KEY: value } map the upsert API takes, dropping
// rows with a blank key (a blank value is allowed — an intentionally empty secret).
export function toEntries(rows: SecretRow[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const r of rows) {
    const k = r.key.trim();
    if (k) out[k] = r.value;
  }
  return out;
}

export function SecretValueRows({
  rows,
  onChange,
  addLabel = "Add secret",
}: {
  rows: SecretRow[];
  onChange: (rows: SecretRow[]) => void;
  addLabel?: string;
}) {
  function update(i: number, patch: Partial<SecretRow>) {
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
            placeholder="SECRET_KEY"
            value={r.key}
            onChange={(e) => update(i, { key: e.target.value })}
          />
          <input
            type="password"
            autoComplete="new-password"
            className={`${inputCls} min-w-[10rem] flex-1`}
            placeholder="value"
            value={r.value}
            onChange={(e) => update(i, { value: e.target.value })}
          />
          <button
            type="button"
            onClick={() => remove(i)}
            className="rounded-md px-2 py-1 text-xs text-gray-400 hover:bg-red-50 hover:text-red-600"
            aria-label="Remove secret"
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
