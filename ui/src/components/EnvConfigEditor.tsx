/**
 * EnvConfigEditor — a self-contained card for viewing and editing a single
 * hierarchy level of the 5-level env-var + secret-ref system.
 *
 * Usage:
 *   <EnvConfigEditor
 *     title="Org-level variables"
 *     fetchFn={getOrgEnvConfig}
 *     saveFn={updateOrgEnvConfig}
 *   />
 */

import { useCallback, useEffect, useState } from "react";

import type { EnvConfig, SecretRef } from "../lib/envconfig";

// ── Internal types ─────────────────────────────────────────────────────────────

interface KVRow {
  id: number;
  key: string;
  value: string;
}

interface SecretRow extends SecretRef {
  id: number;
}

let _rowId = 0;
function nextId() {
  return ++_rowId;
}

const ENV_KEY_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;

// ── Props ──────────────────────────────────────────────────────────────────────

export interface EnvConfigEditorProps {
  title: string;
  description?: string;
  fetchFn: () => Promise<EnvConfig>;
  saveFn: (cfg: EnvConfig) => Promise<unknown>;
  readOnly?: boolean;
}

// ── Component ──────────────────────────────────────────────────────────────────

export function EnvConfigEditor({
  title,
  description,
  fetchFn,
  saveFn,
  readOnly = false,
}: EnvConfigEditorProps) {
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [config, setConfig] = useState<EnvConfig>({});

  const [editing, setEditing] = useState(false);
  const [draftVars, setDraftVars] = useState<KVRow[]>([]);
  const [draftSecrets, setDraftSecrets] = useState<SecretRow[]>([]);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);

  const load = useCallback(() => {
    setLoading(true);
    setLoadError(null);
    fetchFn()
      .then((cfg) => setConfig(cfg ?? {}))
      .catch((err) =>
        setLoadError(err instanceof Error ? err.message : "Failed to load"),
      )
      .finally(() => setLoading(false));
  }, [fetchFn]);

  useEffect(() => {
    load();
  }, [load]);

  function enterEdit() {
    setDraftVars(
      Object.entries(config.vars ?? {}).map(([key, value]) => ({
        id: nextId(),
        key,
        value,
      })),
    );
    setDraftSecrets(
      (config.secretRefs ?? []).map((r) => ({ ...r, id: nextId() })),
    );
    setSaveError(null);
    setEditing(true);
  }

  function cancelEdit() {
    setEditing(false);
  }

  async function handleSave() {
    // Validate keys
    for (const row of draftVars) {
      if (!row.key) continue;
      if (!ENV_KEY_RE.test(row.key)) {
        setSaveError(
          `Invalid key "${row.key}". Must start with a letter or underscore and contain only letters, digits, or underscores.`,
        );
        return;
      }
    }
    for (const row of draftSecrets) {
      if (!row.envKey) continue;
      if (!ENV_KEY_RE.test(row.envKey)) {
        setSaveError(
          `Invalid secret env key "${row.envKey}". Must match [A-Za-z_][A-Za-z0-9_]*.`,
        );
        return;
      }
    }

    setSaving(true);
    setSaveError(null);
    try {
      const vars: Record<string, string> = {};
      for (const row of draftVars) {
        if (row.key.trim()) vars[row.key.trim()] = row.value;
      }
      const secretRefs: SecretRef[] = draftSecrets
        .filter((r) => r.envKey.trim() && r.secretName.trim())
        .map(({ id: _id, ...r }) => r);

      await saveFn({ vars, secretRefs });
      // Reload to get the authoritative saved state (handles 202-async as well).
      load();
      setEditing(false);
    } catch (err) {
      setSaveError(err instanceof Error ? err.message : "Failed to save");
    } finally {
      setSaving(false);
    }
  }

  const varEntries = Object.entries(config.vars ?? {});
  const secretEntries = config.secretRefs ?? [];
  const totalCount = varEntries.length + secretEntries.length;

  return (
    <div className="rounded-lg border border-gray-200 bg-white">
      {/* Header */}
      <div className="flex items-center justify-between border-b border-gray-100 px-6 py-4">
        <div>
          <div className="flex items-center gap-2">
            <h2 className="text-sm font-medium text-gray-900">{title}</h2>
            {!loading && totalCount > 0 && (
              <span className="rounded-full bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-600">
                {totalCount}
              </span>
            )}
          </div>
          {description && (
            <p className="mt-0.5 text-xs text-gray-500">{description}</p>
          )}
        </div>
        {!readOnly && !loading && !loadError && (
          <div className="flex gap-2">
            {editing ? (
              <>
                <button
                  onClick={cancelEdit}
                  disabled={saving}
                  className="rounded-lg border border-gray-300 px-3 py-1.5 text-xs font-medium text-gray-700 hover:bg-gray-50 disabled:opacity-50"
                >
                  Cancel
                </button>
                <button
                  onClick={handleSave}
                  disabled={saving}
                  className="rounded-lg bg-gray-900 px-3 py-1.5 text-xs font-medium text-white hover:bg-gray-700 disabled:opacity-50"
                >
                  {saving ? "Saving…" : "Save"}
                </button>
              </>
            ) : (
              <button
                onClick={enterEdit}
                className="rounded-lg border border-gray-200 px-3 py-1.5 text-xs font-medium text-gray-700 hover:bg-gray-50"
              >
                Edit
              </button>
            )}
          </div>
        )}
      </div>

      {/* Body */}
      <div className="px-6 py-4">
        {loading ? (
          <div className="space-y-2">
            {[0, 1].map((i) => (
              <div key={i} className="h-8 animate-pulse rounded bg-gray-100" />
            ))}
          </div>
        ) : loadError ? (
          <p className="text-sm text-red-600">{loadError}</p>
        ) : editing ? (
          <EditView
            draftVars={draftVars}
            setDraftVars={setDraftVars}
            draftSecrets={draftSecrets}
            setDraftSecrets={setDraftSecrets}
            saveError={saveError}
          />
        ) : (
          <ReadView varEntries={varEntries} secretEntries={secretEntries} />
        )}
      </div>
    </div>
  );
}

// ── Read view ──────────────────────────────────────────────────────────────────

function ReadView({
  varEntries,
  secretEntries,
}: {
  varEntries: [string, string][];
  secretEntries: SecretRef[];
}) {
  if (varEntries.length === 0 && secretEntries.length === 0) {
    return (
      <p className="text-sm text-gray-400 italic">No variables configured.</p>
    );
  }

  return (
    <div className="space-y-4">
      {varEntries.length > 0 && (
        <div>
          <p className="mb-2 text-xs font-medium uppercase tracking-wider text-gray-400">
            Variables
          </p>
          <table className="w-full text-sm">
            <tbody className="divide-y divide-gray-50">
              {varEntries.map(([key, value]) => (
                <tr key={key}>
                  <td className="py-1.5 pr-4 font-mono text-xs font-medium text-gray-900 w-48 align-top">
                    {key}
                  </td>
                  <td className="py-1.5 font-mono text-xs text-gray-600 break-all">
                    {value}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {secretEntries.length > 0 && (
        <div>
          <p className="mb-2 text-xs font-medium uppercase tracking-wider text-gray-400">
            Secret references
          </p>
          <table className="w-full text-xs">
            <thead>
              <tr className="text-left text-gray-400 uppercase tracking-wider">
                <th className="pb-1 pr-4 font-medium">Env key</th>
                <th className="pb-1 pr-4 font-medium">Provider</th>
                <th className="pb-1 pr-4 font-medium">Secret name</th>
                <th className="pb-1 font-medium">Secret key</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {secretEntries.map((r) => (
                <tr key={r.envKey}>
                  <td className="py-1.5 pr-4 font-mono font-medium text-gray-900">
                    {r.envKey}
                  </td>
                  <td className="py-1.5 pr-4 text-gray-500">{r.provider}</td>
                  <td className="py-1.5 pr-4 font-mono text-gray-600">
                    {r.secretName}
                  </td>
                  <td className="py-1.5 font-mono text-gray-600">
                    {r.secretKey}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

// ── Edit view ──────────────────────────────────────────────────────────────────

function EditView({
  draftVars,
  setDraftVars,
  draftSecrets,
  setDraftSecrets,
  saveError,
}: {
  draftVars: KVRow[];
  setDraftVars: React.Dispatch<React.SetStateAction<KVRow[]>>;
  draftSecrets: SecretRow[];
  setDraftSecrets: React.Dispatch<React.SetStateAction<SecretRow[]>>;
  saveError: string | null;
}) {
  function addVar() {
    setDraftVars((prev) => [...prev, { id: nextId(), key: "", value: "" }]);
  }

  function removeVar(id: number) {
    setDraftVars((prev) => prev.filter((r) => r.id !== id));
  }

  function updateVar(id: number, field: "key" | "value", val: string) {
    setDraftVars((prev) =>
      prev.map((r) => (r.id === id ? { ...r, [field]: val } : r)),
    );
  }

  function addSecret() {
    setDraftSecrets((prev) => [
      ...prev,
      {
        id: nextId(),
        envKey: "",
        provider: "kubernetes",
        secretName: "",
        secretKey: "",
      },
    ]);
  }

  function removeSecret(id: number) {
    setDraftSecrets((prev) => prev.filter((r) => r.id !== id));
  }

  function updateSecret(
    id: number,
    field: keyof Omit<SecretRow, "id">,
    val: string,
  ) {
    setDraftSecrets((prev) =>
      prev.map((r) => (r.id === id ? { ...r, [field]: val } : r)),
    );
  }

  const inputCls =
    "w-full rounded border border-gray-300 px-2 py-1 font-mono text-xs focus:border-indigo-400 focus:outline-none focus:ring-1 focus:ring-indigo-400";

  // Inline variant without w-full — used in the flex k/v row where explicit
  // widths (w-48 / flex-1) must not be overridden by Tailwind's w-full.
  const inlineCls =
    "rounded border border-gray-300 px-2 py-1 font-mono text-xs focus:border-indigo-400 focus:outline-none focus:ring-1 focus:ring-indigo-400";

  return (
    <div className="space-y-6">
      {/* Variables section */}
      <div>
        <div className="mb-2 flex items-center justify-between">
          <p className="text-xs font-medium uppercase tracking-wider text-gray-400">
            Variables
          </p>
          <button
            type="button"
            onClick={addVar}
            className="text-xs font-medium text-indigo-600 hover:text-indigo-800"
          >
            + Add variable
          </button>
        </div>

        {draftVars.length === 0 ? (
          <p className="text-xs italic text-gray-400">
            No variables. Click "+ Add variable" to add one.
          </p>
        ) : (
          <div className="space-y-1.5">
            {draftVars.map((row) => (
              <div key={row.id} className="flex items-center gap-2">
                <input
                  className={`${inlineCls} w-48 flex-shrink-0`}
                  placeholder="KEY"
                  value={row.key}
                  onChange={(e) => updateVar(row.id, "key", e.target.value)}
                />
                <span className="text-gray-300">=</span>
                <input
                  className={`${inlineCls} min-w-0 flex-1`}
                  placeholder="value"
                  value={row.value}
                  onChange={(e) => updateVar(row.id, "value", e.target.value)}
                />
                <button
                  type="button"
                  onClick={() => removeVar(row.id)}
                  className="flex-shrink-0 text-gray-400 hover:text-red-500"
                  title="Remove"
                >
                  ✕
                </button>
              </div>
            ))}
          </div>
        )}
      </div>

      {/* Secret references section */}
      <div>
        <div className="mb-2 flex items-center justify-between">
          <p className="text-xs font-medium uppercase tracking-wider text-gray-400">
            Secret references
          </p>
          <button
            type="button"
            onClick={addSecret}
            className="text-xs font-medium text-indigo-600 hover:text-indigo-800"
          >
            + Add secret ref
          </button>
        </div>

        {draftSecrets.length === 0 ? (
          <p className="text-xs italic text-gray-400">
            No secret references. Click "+ Add secret ref" to reference an
            external secret.
          </p>
        ) : (
          <div className="space-y-2">
            {draftSecrets.map((row) => (
              <div
                key={row.id}
                className="rounded-lg border border-gray-200 bg-gray-50 p-3"
              >
                <div className="grid grid-cols-2 gap-2">
                  <div>
                    <label className="mb-0.5 block text-xs text-gray-500">
                      Env key
                    </label>
                    <input
                      className={inputCls}
                      placeholder="MY_SECRET"
                      value={row.envKey}
                      onChange={(e) =>
                        updateSecret(row.id, "envKey", e.target.value)
                      }
                    />
                  </div>
                  <div>
                    <label className="mb-0.5 block text-xs text-gray-500">
                      Provider
                    </label>
                    <select
                      className="w-full rounded border border-gray-300 px-2 py-1 text-xs focus:border-indigo-400 focus:outline-none focus:ring-1 focus:ring-indigo-400"
                      value={row.provider}
                      onChange={(e) =>
                        updateSecret(row.id, "provider", e.target.value)
                      }
                    >
                      <option value="kubernetes">kubernetes</option>
                      <option value="vault">vault</option>
                      <option value="aws-sm">aws-sm</option>
                    </select>
                  </div>
                  <div>
                    <label className="mb-0.5 block text-xs text-gray-500">
                      Secret name
                    </label>
                    <input
                      className={inputCls}
                      placeholder="my-secret"
                      value={row.secretName}
                      onChange={(e) =>
                        updateSecret(row.id, "secretName", e.target.value)
                      }
                    />
                  </div>
                  <div>
                    <label className="mb-0.5 block text-xs text-gray-500">
                      Secret key
                    </label>
                    <input
                      className={inputCls}
                      placeholder="api-key"
                      value={row.secretKey}
                      onChange={(e) =>
                        updateSecret(row.id, "secretKey", e.target.value)
                      }
                    />
                  </div>
                </div>
                <div className="mt-2 flex justify-end">
                  <button
                    type="button"
                    onClick={() => removeSecret(row.id)}
                    className="text-xs text-red-500 hover:text-red-700"
                  >
                    Remove
                  </button>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>

      {saveError && (
        <p className="rounded bg-red-50 px-3 py-2 text-xs text-red-700">
          {saveError}
        </p>
      )}
    </div>
  );
}
