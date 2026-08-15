/**
 * SecretEditor — a self-contained card for managing simple key/value secrets.
 *
 * Generalized to work at any hierarchy level: org, env-type, project, app,
 * or app-env. The caller provides fetchFn, upsertFn, and deleteFn callbacks
 * that target the appropriate API endpoint.
 *
 * Values are write-only: the API only returns key names, never plaintext
 * values. The input field is masked.
 *
 * Usage:
 *   <SecretEditor
 *     title="Org-level secrets"
 *     description="Shared across all apps."
 *     fetchFn={() => listOrgSecretKeys()}
 *     upsertFn={(entries) => upsertOrgSecrets(entries)}
 *     deleteFn={(key) => deleteOrgSecretKey(key)}
 *   />
 */

import { useCallback, useEffect, useState } from "react";

import type { SecretKeysResponse } from "../lib/secrets";

const ENV_KEY_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;

interface DraftRow {
  id: number;
  key: string;
  value: string;
  persisted: boolean;
}

let _rowId = 0;
function nextId() {
  return ++_rowId;
}

export interface SecretEditorProps {
  title?: string;
  description?: string;
  fetchFn: () => Promise<SecretKeysResponse>;
  upsertFn: (entries: Record<string, string>) => Promise<void>;
  deleteFn: (key: string) => Promise<void>;
  readOnly?: boolean;
  // Optional pre-save gate for high-blast-radius scopes (e.g. global secrets on
  // an app with production). Returning false aborts silently, keeping the
  // editor open. Per-key deletion keeps its own confirm.
  confirmSave?: () => boolean;
}

export function SecretEditor({
  title = "Secrets",
  description = "Key/value secrets. Values are stored securely and never shown after saving.",
  fetchFn,
  upsertFn,
  deleteFn,
  readOnly = false,
  confirmSave,
}: SecretEditorProps) {
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [keys, setKeys] = useState<string[]>([]);
  const [secretName, setSecretName] = useState<string>("");

  const [editing, setEditing] = useState(false);
  const [draftRows, setDraftRows] = useState<DraftRow[]>([]);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);

  const [deletingKey, setDeletingKey] = useState<string | null>(null);

  const load = useCallback(() => {
    setLoading(true);
    setLoadError(null);
    fetchFn()
      .then((res) => {
        setKeys(res.keys.map((k) => k.key));
        setSecretName(res.secretName);
      })
      .catch((err) =>
        setLoadError(err instanceof Error ? err.message : "Failed to load"),
      )
      .finally(() => setLoading(false));
  }, [fetchFn]);

  useEffect(() => {
    load();
  }, [load]);

  function enterEdit() {
    setDraftRows([{ id: nextId(), key: "", value: "", persisted: false }]);
    setSaveError(null);
    setEditing(true);
  }

  function cancelEdit() {
    setEditing(false);
  }

  function addRow() {
    setDraftRows((prev) => [
      ...prev,
      { id: nextId(), key: "", value: "", persisted: false },
    ]);
  }

  function removeRow(id: number) {
    setDraftRows((prev) => prev.filter((r) => r.id !== id));
  }

  function updateRow(id: number, field: "key" | "value", val: string) {
    setDraftRows((prev) =>
      prev.map((r) => (r.id === id ? { ...r, [field]: val } : r)),
    );
  }

  async function handleSave() {
    if (confirmSave && !confirmSave()) return;
    const entries: Record<string, string> = {};
    for (const row of draftRows) {
      const k = row.key.trim();
      if (!k) continue;
      if (!ENV_KEY_RE.test(k)) {
        setSaveError(
          `Invalid key "${k}". Must start with a letter or underscore and contain only letters, digits, or underscores.`,
        );
        return;
      }
      if (!row.value) {
        setSaveError(`Value for "${k}" must not be empty.`);
        return;
      }
      entries[k] = row.value;
    }

    if (Object.keys(entries).length === 0) {
      setSaveError("Add at least one key/value pair.");
      return;
    }

    setSaving(true);
    setSaveError(null);
    try {
      await upsertFn(entries);
      load();
      setEditing(false);
    } catch (err) {
      setSaveError(err instanceof Error ? err.message : "Failed to save");
    } finally {
      setSaving(false);
    }
  }

  async function handleDelete(key: string) {
    if (!confirm(`Delete secret "${key}"? This cannot be undone.`)) return;
    setDeletingKey(key);
    try {
      await deleteFn(key);
      load();
    } catch (err) {
      alert(err instanceof Error ? err.message : "Failed to delete");
    } finally {
      setDeletingKey(null);
    }
  }

  const inputCls =
    "rounded border border-gray-300 px-2 py-1 font-mono text-xs focus:border-indigo-400 focus:outline-none focus:ring-1 focus:ring-indigo-400";

  return (
    <div className="rounded-lg border border-gray-200 bg-white">
      {/* Header */}
      <div className="flex items-center justify-between border-b border-gray-100 px-6 py-4">
        <div>
          <div className="flex items-center gap-2">
            <h2 className="text-sm font-medium text-gray-900">{title}</h2>
            {!loading && keys.length > 0 && (
              <span className="rounded-full bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-600">
                {keys.length}
              </span>
            )}
          </div>
          <p className="mt-0.5 text-xs text-gray-500">{description}</p>
          {secretName && (
            <p className="mt-0.5 text-xs text-gray-400">
              K8s Secret:{" "}
              <code className="rounded bg-gray-100 px-1 py-0.5 font-mono text-xs">
                {secretName}
              </code>
            </p>
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
                  {saving ? "Saving\u2026" : "Save"}
                </button>
              </>
            ) : (
              <button
                onClick={enterEdit}
                className="rounded-lg border border-gray-200 px-3 py-1.5 text-xs font-medium text-gray-700 hover:bg-gray-50"
              >
                Add / Update
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
          <div className="space-y-4">
            <div className="space-y-1.5">
              {draftRows.map((row) => (
                <div key={row.id} className="flex items-center gap-2">
                  <input
                    className={`${inputCls} w-48 flex-shrink-0`}
                    placeholder="SECRET_KEY"
                    value={row.key}
                    onChange={(e) => updateRow(row.id, "key", e.target.value)}
                  />
                  <span className="text-gray-300">=</span>
                  <input
                    type="password"
                    className={`${inputCls} min-w-0 flex-1`}
                    placeholder="secret value"
                    value={row.value}
                    onChange={(e) => updateRow(row.id, "value", e.target.value)}
                  />
                  <button
                    type="button"
                    onClick={() => removeRow(row.id)}
                    className="flex-shrink-0 text-gray-400 hover:text-red-500"
                    title="Remove"
                  >
                    &times;
                  </button>
                </div>
              ))}
            </div>
            <button
              type="button"
              onClick={addRow}
              className="text-xs font-medium text-indigo-600 hover:text-indigo-800"
            >
              + Add another
            </button>
            {saveError && (
              <p className="rounded bg-red-50 px-3 py-2 text-xs text-red-700">
                {saveError}
              </p>
            )}
          </div>
        ) : keys.length === 0 ? (
          <p className="text-sm text-gray-400 italic">
            No secrets configured. Click "Add / Update" to create one.
          </p>
        ) : (
          <table className="w-full text-sm">
            <tbody className="divide-y divide-gray-50">
              {keys.map((key) => (
                <tr key={key} className="group">
                  <td className="py-1.5 pr-4 font-mono text-xs font-medium text-gray-900">
                    {key}
                  </td>
                  <td className="py-1.5 font-mono text-xs text-gray-400">
                    ••••••••
                  </td>
                  {!readOnly && (
                    <td className="py-1.5 text-right">
                      <button
                        onClick={() => handleDelete(key)}
                        disabled={deletingKey === key}
                        className="invisible text-xs text-red-500 hover:text-red-700 group-hover:visible disabled:opacity-50"
                      >
                        {deletingKey === key ? "Deleting\u2026" : "Delete"}
                      </button>
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}
