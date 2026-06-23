import { useEffect, useState } from "react";
import { toast } from "sonner";

import { ApiError } from "../lib/api";
import {
  createProjectToken,
  deleteProjectToken,
  listProjectTokens,
} from "../lib/tokens";
import type {
  ApiToken,
  CreatedToken,
  ProjectTokenRole,
} from "../types";

const inputClass =
  "block w-full rounded-lg border border-gray-300 px-3 py-2 text-sm shadow-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500";

const roleBadge: Record<string, string> = {
  project_admin: "bg-amber-50 text-amber-700",
  developer: "bg-blue-50 text-blue-700",
  viewer: "bg-gray-100 text-gray-600",
};

const ROLE_OPTIONS: { value: ProjectTokenRole; label: string }[] = [
  { value: "viewer", label: "viewer — read-only" },
  { value: "developer", label: "developer — deploy & previews" },
  { value: "project_admin", label: "project_admin — full project control" },
];

function formatDate(ts?: string): string {
  if (!ts) return "—";
  const d = new Date(ts);
  return Number.isNaN(d.getTime()) ? ts : d.toLocaleDateString();
}

function expiryLabel(t: ApiToken): { text: string; expired: boolean } {
  if (!t.expiresAt) return { text: "Never", expired: false };
  const d = new Date(t.expiresAt);
  const expired = d.getTime() < Date.now();
  return { text: formatDate(t.expiresAt), expired };
}

function Modal({
  title,
  onClose,
  children,
}: {
  title: string;
  onClose: () => void;
  children: React.ReactNode;
}) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
      <div className="w-full max-w-md rounded-xl bg-white p-6 shadow-xl">
        <div className="mb-4 flex items-center justify-between">
          <h2 className="text-lg font-semibold text-gray-900">{title}</h2>
          <button
            onClick={onClose}
            className="text-gray-400 hover:text-gray-600"
            aria-label="Close"
          >
            ✕
          </button>
        </div>
        {children}
      </div>
    </div>
  );
}

function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      toast.error("Copy failed — select and copy manually");
    }
  }
  return (
    <button
      onClick={copy}
      className="shrink-0 rounded-lg border border-gray-300 px-3 py-2 text-xs font-medium text-gray-700 hover:bg-gray-50"
    >
      {copied ? "Copied!" : "Copy"}
    </button>
  );
}

// CreateTokenModal collects the token name, role and optional expiry, then on
// submit hands the created token (with its one-time secret) back to the parent.
function CreateTokenModal({
  projectName,
  onClose,
  onCreated,
}: {
  projectName: string;
  onClose: () => void;
  onCreated: (t: CreatedToken) => void;
}) {
  const [name, setName] = useState("");
  const [role, setRole] = useState<ProjectTokenRole>("developer");
  const [expires, setExpires] = useState(false);
  const [expiresInDays, setExpiresInDays] = useState(90);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim()) {
      setError("Name is required");
      return;
    }
    setSaving(true);
    setError(null);
    try {
      const created = await createProjectToken(projectName, {
        name: name.trim(),
        role,
        expiresInDays: expires ? expiresInDays : 0,
      });
      onCreated(created);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to create token");
      setSaving(false);
    }
  }

  return (
    <Modal title="Create API token" onClose={onClose}>
      <form onSubmit={submit} className="space-y-4">
        <label className="block">
          <span className="text-sm font-medium text-gray-700">Name</span>
          <input
            autoFocus
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="github-actions"
            className={`mt-1 ${inputClass}`}
          />
          <p className="mt-1 text-xs text-gray-400">
            A label to recognise this token later. Not used for auth.
          </p>
        </label>

        <label className="block">
          <span className="text-sm font-medium text-gray-700">Role</span>
          <select
            value={role}
            onChange={(e) => setRole(e.target.value as ProjectTokenRole)}
            className={`mt-1 ${inputClass}`}
          >
            {ROLE_OPTIONS.map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </select>
          <p className="mt-1 text-xs text-gray-400">
            The token can act on this project at this role — nothing more.
          </p>
        </label>

        <div>
          <label className="flex items-center gap-2">
            <input
              type="checkbox"
              checked={expires}
              onChange={(e) => setExpires(e.target.checked)}
              className="h-4 w-4 rounded border-gray-300"
            />
            <span className="text-sm font-medium text-gray-700">Set an expiry</span>
          </label>
          {expires && (
            <div className="mt-2 flex items-center gap-2">
              <input
                type="number"
                min={1}
                value={expiresInDays}
                onChange={(e) => setExpiresInDays(Math.max(1, Number(e.target.value)))}
                className={`${inputClass} w-28`}
              />
              <span className="text-sm text-gray-500">days from now</span>
            </div>
          )}
          {!expires && (
            <p className="mt-1 text-xs text-gray-400">
              Leave off for a token that never expires (revoke it manually).
            </p>
          )}
        </div>

        {error && (
          <div className="rounded bg-red-50 px-3 py-2 text-xs text-red-700">
            {error}
          </div>
        )}

        <div className="flex justify-end gap-2 pt-2">
          <button
            type="button"
            onClick={onClose}
            className="rounded-lg border border-gray-300 px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50"
          >
            Cancel
          </button>
          <button
            type="submit"
            disabled={saving}
            className="rounded-lg bg-gray-900 px-4 py-2 text-sm font-medium text-white hover:bg-gray-700 disabled:opacity-50"
          >
            {saving ? "Creating…" : "Create token"}
          </button>
        </div>
      </form>
    </Modal>
  );
}

// RevealTokenModal shows the one-time plaintext secret immediately after
// creation. It can never be retrieved again, so it is surfaced prominently with
// a copy button.
function RevealTokenModal({
  created,
  onClose,
}: {
  created: CreatedToken;
  onClose: () => void;
}) {
  return (
    <Modal title="Copy your API token" onClose={onClose}>
      <div className="space-y-4">
        <div className="rounded bg-amber-50 px-3 py-2 text-xs text-amber-800">
          This is the only time the token is shown. Copy it now and store it
          somewhere safe (e.g. a CI secret). You cannot retrieve it again.
        </div>
        <div className="flex items-center gap-2">
          <code className="flex-1 overflow-x-auto rounded-lg border border-gray-200 bg-gray-50 px-3 py-2 font-mono text-xs text-gray-900">
            {created.token}
          </code>
          <CopyButton text={created.token} />
        </div>
        <dl className="grid grid-cols-3 gap-1 text-xs text-gray-500">
          <dt className="font-medium text-gray-700">Name</dt>
          <dd className="col-span-2">{created.name}</dd>
          <dt className="font-medium text-gray-700">Role</dt>
          <dd className="col-span-2">{created.role}</dd>
          <dt className="font-medium text-gray-700">Expires</dt>
          <dd className="col-span-2">{expiryLabel(created).text}</dd>
        </dl>
        <div className="flex justify-end">
          <button
            onClick={onClose}
            className="rounded-lg bg-gray-900 px-4 py-2 text-sm font-medium text-white hover:bg-gray-700"
          >
            Done
          </button>
        </div>
      </div>
    </Modal>
  );
}

// ProjectTokensSection lists, mints, and revokes a project's API tokens. All
// actions require project_admin server-side; non-admins see a 403 hint instead
// of the table.
export function ProjectTokensSection({ projectName }: { projectName: string }) {
  const [tokens, setTokens] = useState<ApiToken[]>([]);
  const [loading, setLoading] = useState(true);
  const [forbidden, setForbidden] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [showCreate, setShowCreate] = useState(false);
  const [revealed, setRevealed] = useState<CreatedToken | null>(null);

  async function reload() {
    const res = await listProjectTokens(projectName);
    setTokens(res.tokens);
  }

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setForbidden(false);
    setError(null);
    listProjectTokens(projectName)
      .then((res) => {
        if (!cancelled) setTokens(res.tokens);
      })
      .catch((err) => {
        if (cancelled) return;
        if (err instanceof ApiError && err.status === 403) {
          setForbidden(true);
        } else {
          setError(err instanceof Error ? err.message : "Failed to load tokens");
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [projectName]);

  async function handleDelete(t: ApiToken) {
    if (!confirm(`Revoke token "${t.name}"? Any caller using it will stop working.`)) {
      return;
    }
    try {
      await deleteProjectToken(projectName, t.id);
      toast.success(`Revoked token "${t.name}"`);
      await reload();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to revoke token");
    }
  }

  function onCreated(created: CreatedToken) {
    setShowCreate(false);
    setRevealed(created);
    void reload();
  }

  return (
    <div className="rounded-xl border border-gray-200 bg-white">
      <div className="flex items-center justify-between border-b border-gray-100 px-6 py-4">
        <div>
          <h2 className="text-base font-medium text-gray-900">API tokens</h2>
          <p className="mt-1 text-sm text-gray-500">
            Bearer tokens for CI and scripts. Each is scoped to this project at a
            fixed role. See{" "}
            <a
              href="https://github.com/suparcloud/suparship/blob/main/docs/api-tokens.md"
              target="_blank"
              rel="noreferrer"
              className="text-indigo-600 hover:text-indigo-800"
            >
              docs
            </a>
            .
          </p>
        </div>
        {!forbidden && (
          <button
            onClick={() => setShowCreate(true)}
            className="shrink-0 rounded-lg bg-gray-900 px-3 py-1.5 text-xs font-medium text-white hover:bg-gray-700"
          >
            Create token
          </button>
        )}
      </div>

      <div className="px-6 py-4">
        {forbidden ? (
          <p className="text-sm text-gray-500">
            Only project admins can manage API tokens.
          </p>
        ) : loading ? (
          <p className="text-sm text-gray-400">Loading…</p>
        ) : error ? (
          <p className="text-sm text-red-600">{error}</p>
        ) : tokens.length === 0 ? (
          <p className="text-sm text-gray-500">
            No tokens yet. Create one to authenticate CI or scripts against this
            project.
          </p>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="text-left text-xs font-medium uppercase tracking-wide text-gray-400">
                <th className="pb-2 pr-4">Name</th>
                <th className="pb-2 pr-4">Role</th>
                <th className="pb-2 pr-4">Created</th>
                <th className="pb-2 pr-4">Expires</th>
                <th className="pb-2" />
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {tokens.map((t) => {
                const exp = expiryLabel(t);
                return (
                  <tr key={t.id} className="group">
                    <td className="py-2 pr-4 font-medium text-gray-900">{t.name}</td>
                    <td className="py-2 pr-4">
                      <span
                        className={`rounded-full px-2 py-0.5 text-xs font-medium ${
                          roleBadge[t.role] ?? "bg-gray-100 text-gray-600"
                        }`}
                      >
                        {t.role}
                      </span>
                    </td>
                    <td className="py-2 pr-4 text-gray-500">{formatDate(t.createdAt)}</td>
                    <td className="py-2 pr-4">
                      <span className={exp.expired ? "text-red-600" : "text-gray-500"}>
                        {exp.text}
                        {exp.expired && " (expired)"}
                      </span>
                    </td>
                    <td className="py-2 text-right">
                      <button
                        onClick={() => handleDelete(t)}
                        className="text-xs font-medium text-red-500 opacity-0 hover:text-red-700 group-hover:opacity-100"
                      >
                        Revoke
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </div>

      {showCreate && (
        <CreateTokenModal
          projectName={projectName}
          onClose={() => setShowCreate(false)}
          onCreated={onCreated}
        />
      )}
      {revealed && (
        <RevealTokenModal created={revealed} onClose={() => setRevealed(null)} />
      )}
    </div>
  );
}
