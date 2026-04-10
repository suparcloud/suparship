import { useEffect, useState } from "react";

import {
  fetchOrg,
  fetchAllRoleBindings,
  listOrgEnvironments,
  createOrgEnvironment,
  updateOrgEnvironment,
  deleteOrgEnvironment,
} from "../lib/settings";
import type { OrgInfo, RoleBinding } from "../types";
import type { OrgEnvironment } from "../lib/settings";

const roleBadgeColor: Record<string, string> = {
  org_admin: "bg-red-50 text-red-700",
  project_admin: "bg-amber-50 text-amber-700",
  developer: "bg-blue-50 text-blue-700",
  viewer: "bg-gray-100 text-gray-600",
};

function RoleBadge({ role }: { role: string }) {
  const color = roleBadgeColor[role] ?? "bg-gray-100 text-gray-600";
  return (
    <span
      className={`inline-block rounded-full px-2.5 py-0.5 text-xs font-medium ${color}`}
    >
      {role}
    </span>
  );
}

// ── Environments section ──────────────────────────────────────────────────────

interface EnvFormState {
  name: string;
  displayName: string;
  order: string;
  clusterRef: string;
  baseDomain: string;
  namespacePattern: string;
}

const emptyEnvForm = (): EnvFormState => ({
  name: "",
  displayName: "",
  order: "",
  clusterRef: "",
  baseDomain: "",
  namespacePattern: "",
});

function OrgEnvironmentsSection() {
  const [envs, setEnvs] = useState<OrgEnvironment[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [showAddModal, setShowAddModal] = useState(false);
  const [editTarget, setEditTarget] = useState<OrgEnvironment | null>(null);
  const [form, setForm] = useState<EnvFormState>(emptyEnvForm());
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    listOrgEnvironments()
      .then((res) => {
        if (!cancelled) setEnvs(res.environments);
      })
      .catch((err) => {
        if (!cancelled)
          setError(err instanceof Error ? err.message : "Failed to load environments");
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  function openAdd() {
    setForm(emptyEnvForm());
    setSaveError(null);
    setShowAddModal(true);
  }

  function openEdit(env: OrgEnvironment) {
    setForm({
      name: env.name,
      displayName: env.displayName ?? "",
      order: String(env.order),
      clusterRef: env.clusterRef ?? "",
      baseDomain: env.baseDomain ?? "",
      namespacePattern: env.namespacePattern ?? "",
    });
    setSaveError(null);
    setEditTarget(env);
  }

  function closeModal() {
    setShowAddModal(false);
    setEditTarget(null);
  }

  async function handleSave() {
    setSaving(true);
    setSaveError(null);
    try {
      const payload = {
        displayName: form.displayName || undefined,
        order: form.order ? parseInt(form.order, 10) : undefined,
        clusterRef: form.clusterRef || undefined,
        baseDomain: form.baseDomain || undefined,
        namespacePattern: form.namespacePattern || undefined,
      };
      if (editTarget) {
        const updated = await updateOrgEnvironment(editTarget.name, payload);
        setEnvs((prev) =>
          prev.map((e) => (e.name === editTarget.name ? updated : e)),
        );
      } else {
        if (!form.name.trim()) {
          setSaveError("Name is required");
          return;
        }
        const created = await createOrgEnvironment({
          name: form.name.trim(),
          ...payload,
        });
        setEnvs((prev) => [...prev, created].sort((a, b) => a.order - b.order));
      }
      closeModal();
    } catch (err) {
      setSaveError(err instanceof Error ? err.message : "Failed to save");
    } finally {
      setSaving(false);
    }
  }

  async function handleDelete(env: OrgEnvironment) {
    if (
      !confirm(
        `Remove org environment "${env.displayName || env.name}"?\n\nAll projects that inherit this environment will stop receiving it. This cannot be undone.`,
      )
    )
      return;
    try {
      await deleteOrgEnvironment(env.name);
      setEnvs((prev) => prev.filter((e) => e.name !== env.name));
    } catch (err) {
      alert(err instanceof Error ? err.message : "Failed to delete");
    }
  }

  const isEditing = Boolean(editTarget);
  const modalOpen = showAddModal || isEditing;

  return (
    <div className="rounded-lg border border-gray-200 bg-white">
      <div className="flex items-center justify-between border-b border-gray-100 px-6 py-4">
        <div>
          <h2 className="text-sm font-medium text-gray-900">
            Deployment Environments
          </h2>
          <p className="mt-0.5 text-xs text-gray-500">
            Canonical pipeline shared across all projects. Projects may add
            per-environment overrides without changing these defaults.
          </p>
        </div>
        <button
          onClick={openAdd}
          className="rounded-lg bg-gray-900 px-3 py-1.5 text-xs font-medium text-white transition-colors hover:bg-gray-700"
        >
          Add environment
        </button>
      </div>

      {loading ? (
        <div className="space-y-2 p-6">
          {[0, 1].map((i) => (
            <div key={i} className="h-10 animate-pulse rounded bg-gray-100" />
          ))}
        </div>
      ) : error ? (
        <div className="px-6 py-4 text-sm text-red-600">{error}</div>
      ) : envs.length === 0 ? (
        <div className="px-6 py-10 text-center">
          <p className="text-sm text-gray-400">
            No environments defined yet. Add one to start the deployment
            pipeline.
          </p>
        </div>
      ) : (
        <table className="w-full">
          <thead>
            <tr className="border-b border-gray-100 text-left text-xs font-medium uppercase tracking-wider text-gray-500">
              <th className="px-6 py-3">Name</th>
              <th className="px-6 py-3">Cluster</th>
              <th className="px-6 py-3">Base domain</th>
              <th className="px-6 py-3">Namespace pattern</th>
              <th className="px-6 py-3">Order</th>
              <th className="px-6 py-3" />
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-50">
            {envs.map((env) => (
              <tr key={env.name} className="hover:bg-gray-50">
                <td className="px-6 py-3">
                  <span className="text-sm font-medium text-gray-900">
                    {env.displayName || env.name}
                  </span>
                  {env.displayName && (
                    <span className="ml-1.5 font-mono text-xs text-gray-400">
                      {env.name}
                    </span>
                  )}
                </td>
                <td className="px-6 py-3 font-mono text-xs text-gray-600">
                  {env.clusterRef || (
                    <span className="text-gray-300">—</span>
                  )}
                </td>
                <td className="px-6 py-3 font-mono text-xs text-gray-600">
                  {env.baseDomain || <span className="text-gray-300">—</span>}
                </td>
                <td className="px-6 py-3 font-mono text-xs text-gray-600">
                  {env.namespacePattern || (
                    <span className="text-gray-400 italic">
                      {"{app}-{env}"}
                    </span>
                  )}
                </td>
                <td className="px-6 py-3 text-sm text-gray-500">{env.order}</td>
                <td className="px-6 py-3 text-right">
                  <button
                    onClick={() => openEdit(env)}
                    className="mr-3 text-xs text-indigo-600 hover:text-indigo-800"
                  >
                    Edit
                  </button>
                  <button
                    onClick={() => handleDelete(env)}
                    className="text-xs text-red-500 hover:text-red-700"
                  >
                    Remove
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {/* Add / Edit modal */}
      {modalOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
          <div className="w-full max-w-md rounded-xl bg-white p-6 shadow-xl">
            <h3 className="mb-4 text-base font-semibold text-gray-900">
              {isEditing
                ? `Edit "${editTarget!.displayName || editTarget!.name}"`
                : "Add org environment"}
            </h3>

            <div className="space-y-4">
              {!isEditing && (
                <div>
                  <label className="mb-1 block text-xs font-medium text-gray-700">
                    Name <span className="text-red-500">*</span>
                  </label>
                  <input
                    className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
                    placeholder="e.g. staging"
                    value={form.name}
                    onChange={(e) =>
                      setForm((f) => ({ ...f, name: e.target.value }))
                    }
                  />
                </div>
              )}

              <div>
                <label className="mb-1 block text-xs font-medium text-gray-700">
                  Display name
                </label>
                <input
                  className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
                  placeholder="e.g. Staging"
                  value={form.displayName}
                  onChange={(e) =>
                    setForm((f) => ({ ...f, displayName: e.target.value }))
                  }
                />
              </div>

              <div className="grid grid-cols-2 gap-3">
                <div>
                  <label className="mb-1 block text-xs font-medium text-gray-700">
                    Cluster
                  </label>
                  <input
                    className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
                    placeholder="staging-cluster"
                    value={form.clusterRef}
                    onChange={(e) =>
                      setForm((f) => ({ ...f, clusterRef: e.target.value }))
                    }
                  />
                </div>
                <div>
                  <label className="mb-1 block text-xs font-medium text-gray-700">
                    Order
                  </label>
                  <input
                    type="number"
                    className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
                    placeholder="1"
                    value={form.order}
                    onChange={(e) =>
                      setForm((f) => ({ ...f, order: e.target.value }))
                    }
                  />
                </div>
              </div>

              <div>
                <label className="mb-1 block text-xs font-medium text-gray-700">
                  Base domain
                </label>
                <input
                  className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
                  placeholder="staging.example.com"
                  value={form.baseDomain}
                  onChange={(e) =>
                    setForm((f) => ({ ...f, baseDomain: e.target.value }))
                  }
                />
              </div>

              <div>
                <label className="mb-1 block text-xs font-medium text-gray-700">
                  Namespace pattern
                </label>
                <input
                  className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
                  placeholder="{app}  or  {app}-{env}"
                  value={form.namespacePattern}
                  onChange={(e) =>
                    setForm((f) => ({ ...f, namespacePattern: e.target.value }))
                  }
                />
                <p className="mt-1 text-xs text-gray-400">
                  Tokens: {"{app}"}, {"{env}"}, {"{project}"}. Leave blank to
                  use <code className="font-mono">{"{app}-{env}"}</code>.
                </p>
              </div>

              {saveError && (
                <p className="rounded bg-red-50 px-3 py-2 text-xs text-red-700">
                  {saveError}
                </p>
              )}
            </div>

            <div className="mt-6 flex justify-end gap-2">
              <button
                onClick={closeModal}
                disabled={saving}
                className="rounded-lg border border-gray-300 px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50"
              >
                Cancel
              </button>
              <button
                onClick={handleSave}
                disabled={saving}
                className="rounded-lg bg-gray-900 px-4 py-2 text-sm font-medium text-white hover:bg-gray-700 disabled:opacity-50"
              >
                {saving ? "Saving…" : isEditing ? "Save changes" : "Add environment"}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

// ── Main OrgSettings page ─────────────────────────────────────────────────────

export function OrgSettings() {
  const [org, setOrg] = useState<OrgInfo | null>(null);
  const [bindings, setBindings] = useState<RoleBinding[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;

    async function load() {
      try {
        const [orgData, bindingsData] = await Promise.all([
          fetchOrg(),
          fetchAllRoleBindings(),
        ]);
        if (cancelled) return;
        setOrg(orgData);
        setBindings(bindingsData);
      } catch (err) {
        if (cancelled) return;
        setError(err instanceof Error ? err.message : "Failed to load");
      } finally {
        if (!cancelled) setLoading(false);
      }
    }

    load();
    return () => {
      cancelled = true;
    };
  }, []);

  if (loading) {
    return (
      <div className="space-y-4">
        <div className="h-8 w-48 animate-pulse rounded bg-gray-100" />
        <div className="h-32 animate-pulse rounded-lg bg-gray-50" />
        <div className="h-48 animate-pulse rounded-lg bg-gray-50" />
      </div>
    );
  }

  if (error) {
    return (
      <div className="rounded-lg border border-red-200 bg-red-50 p-4">
        <p className="text-sm text-red-700">
          Failed to load organization settings: {error}
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-semibold text-gray-900">Organization</h1>
        <p className="mt-1 text-sm text-gray-500">
          Organization details, deployment pipeline, and project role bindings.
        </p>
      </div>

      {org && (
        <div className="rounded-lg border border-gray-200 bg-white">
          <div className="border-b border-gray-100 px-6 py-4">
            <h2 className="text-sm font-medium text-gray-500">
              Organization Details
            </h2>
          </div>
          <dl className="divide-y divide-gray-100">
            <div className="grid grid-cols-3 px-6 py-3">
              <dt className="text-sm font-medium text-gray-500">Name</dt>
              <dd className="col-span-2 text-sm text-gray-900">{org.name}</dd>
            </div>
            <div className="grid grid-cols-3 px-6 py-3">
              <dt className="text-sm font-medium text-gray-500">
                Display Name
              </dt>
              <dd className="col-span-2 text-sm text-gray-900">
                {org.displayName}
              </dd>
            </div>
            {org.createdAt && (
              <div className="grid grid-cols-3 px-6 py-3">
                <dt className="text-sm font-medium text-gray-500">Created</dt>
                <dd className="col-span-2 text-sm text-gray-900">
                  {new Date(org.createdAt).toLocaleDateString(undefined, {
                    year: "numeric",
                    month: "long",
                    day: "numeric",
                  })}
                </dd>
              </div>
            )}
          </dl>
        </div>
      )}

      {/* Canonical deployment pipeline — org-level environments */}
      <OrgEnvironmentsSection />

      <div className="rounded-lg border border-gray-200 bg-white">
        <div className="border-b border-gray-100 px-6 py-4">
          <h2 className="text-sm font-medium text-gray-500">
            Project Role Bindings
          </h2>
        </div>
        {bindings.length === 0 ? (
          <div className="px-6 py-12 text-center">
            <p className="text-sm text-gray-400">
              No role bindings configured yet.
            </p>
          </div>
        ) : (
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-100 text-left text-xs font-medium uppercase tracking-wider text-gray-500">
                <th className="px-6 py-3">Project</th>
                <th className="px-6 py-3">Team</th>
                <th className="px-6 py-3">Role</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {bindings.map((rb, i) => (
                <tr key={i} className="hover:bg-gray-50">
                  <td className="px-6 py-3 text-sm text-gray-900">
                    {rb.project === "*" ? (
                      <span className="text-gray-400 italic">All projects</span>
                    ) : (
                      rb.project
                    )}
                  </td>
                  <td className="px-6 py-3 text-sm text-gray-900">
                    {rb.team}
                  </td>
                  <td className="px-6 py-3">
                    <RoleBadge role={rb.role} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}
