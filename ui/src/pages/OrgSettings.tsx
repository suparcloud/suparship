import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";

import {
  fetchOrg,
  fetchAllRoleBindings,
  listOrgEnvironments,
  createOrgEnvironment,
  updateOrgEnvironment,
  deleteOrgEnvironment,
  getOrgNaming,
  updateOrgNaming,
  listOrgRoutingProfiles,
  upsertOrgRoutingProfile,
  deleteOrgRoutingProfile,
} from "../lib/settings";
import type {
  OrgNaming,
  RoutingProfile,
  ExposeMode,
} from "../lib/settings";
import {
  getOrgEnvConfig,
  updateOrgEnvConfig,
  getEnvTypeEnvConfig,
  updateEnvTypeEnvConfig,
} from "../lib/envconfig";
import {
  getSecretsBackend,
  updateSecretsBackend,
  saveSAToken,
  listVaults,
  setGlobalVault,
  registerEnvVault,
  unregisterEnvVault,
  setClusterConnectToken,
  listSharedGlobalSecretKeys,
  upsertSharedGlobalSecrets,
  deleteSharedGlobalSecretKey,
  listSharedEnvSecretKeys,
  upsertSharedEnvSecrets,
  deleteSharedEnvSecretKey,
} from "../lib/secrets";
import type {
  SecretBackendConfig,
  VaultInfo,
} from "../lib/secrets";
import { listClusters } from "../lib/clusters";
import type { Cluster } from "../lib/clusters";
import { EnvConfigEditor } from "../components/EnvConfigEditor";
import { SecretEditor } from "../components/SecretEditor";
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
  // Every cluster registered with this env. Today only activeClusterRef is
  // deployed to; the rest are reserved for future multi-cluster fan-out.
  clusterRefs: string[];
  // The active deploy target. Must be a member of clusterRefs (or empty,
  // which falls back to clusterRefs[0] at the server).
  activeClusterRef: string;
  // "active" (deploy to activeClusterRef only) or "all" (fan out to every
  // clusterRef — one Application per cluster).
  deployMode: string;
  baseDomain: string;
  namespacePattern: string;
}

const emptyEnvForm = (): EnvFormState => ({
  name: "",
  displayName: "",
  order: "",
  clusterRefs: [],
  activeClusterRef: "",
  deployMode: "active",
  baseDomain: "",
  namespacePattern: "",
});

function OrgEnvironmentsSection() {
  const [envs, setEnvs] = useState<OrgEnvironment[]>([]);
  const [clusters, setClusters] = useState<Cluster[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [showAddModal, setShowAddModal] = useState(false);
  const [editTarget, setEditTarget] = useState<OrgEnvironment | null>(null);
  const [form, setForm] = useState<EnvFormState>(emptyEnvForm());
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    Promise.all([listOrgEnvironments(), listClusters()])
      .then(([envRes, clusters]) => {
        if (!cancelled) {
          setEnvs(envRes.environments);
          setClusters(clusters);
        }
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
      clusterRefs: env.clusterRefs ?? [],
      activeClusterRef: env.activeClusterRef ?? "",
      deployMode: env.deployMode ?? "active",
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
      // Normalise: empty active when no clusters registered, and active
      // must be a member of the registered set.
      let activeRef = form.activeClusterRef;
      if (form.clusterRefs.length === 0) {
        activeRef = "";
      } else if (activeRef && !form.clusterRefs.includes(activeRef)) {
        // User unchecked the active one without re-picking. Drop it; the
        // server falls back to clusterRefs[0].
        activeRef = "";
      }
      const payload = {
        displayName: form.displayName || undefined,
        order: form.order ? parseInt(form.order, 10) : undefined,
        clusterRefs: form.clusterRefs,
        activeClusterRef: activeRef || undefined,
        deployMode: form.deployMode || undefined,
        baseDomain: form.baseDomain || undefined,
        namespacePattern: form.namespacePattern || undefined,
      };
      if (editTarget) {
        const updated = await updateOrgEnvironment(editTarget.name, payload);
        setEnvs((prev) =>
          prev.map((e) => (e.name === editTarget.name ? updated : e)),
        );
        // A relocating change (active cluster / namespace) is saved but not
        // auto-applied — surface the migration notice so it isn't silent.
        if (updated.warning) {
          toast.warning(updated.warning, { duration: 12000 });
        }
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

  function toggleClusterRef(name: string) {
    setForm((f) => {
      const has = f.clusterRefs.includes(name);
      const nextRefs = has
        ? f.clusterRefs.filter((c) => c !== name)
        : [...f.clusterRefs, name];
      // If we removed the active one, clear it so the server falls back.
      const nextActive =
        has && f.activeClusterRef === name ? "" : f.activeClusterRef;
      return { ...f, clusterRefs: nextRefs, activeClusterRef: nextActive };
    });
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
                  {env.clusterRefs && env.clusterRefs.length > 0 ? (
                    <div className="flex flex-wrap gap-1">
                      {env.clusterRefs.map((c) => {
                        const active =
                          c === (env.activeClusterRef || env.clusterRefs?.[0]);
                        return (
                          <span
                            key={c}
                            className={
                              active
                                ? "rounded bg-indigo-50 px-1.5 py-0.5 text-indigo-700"
                                : "rounded bg-gray-50 px-1.5 py-0.5 text-gray-600"
                            }
                            title={active ? "active deploy target" : "registered, not active"}
                          >
                            {c}
                            {active && " ●"}
                          </span>
                        );
                      })}
                    </div>
                  ) : (
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

              <div>
                <label className="mb-1 block text-xs font-medium text-gray-700">
                  Clusters
                </label>
                <p className="mb-2 text-xs text-gray-400">
                  Register one or more clusters with this environment. Today
                  only the cluster marked <span className="font-medium">Active</span> receives deploys; the
                  others are reserved for future multi-cluster fan-out.
                </p>
                {clusters.length === 0 ? (
                  <p className="rounded bg-amber-50 px-3 py-2 text-xs text-amber-700">
                    No clusters registered yet. Register a cluster under
                    <span className="font-mono"> Settings → Clusters</span> first.
                  </p>
                ) : (
                  <div className="space-y-1.5 rounded-lg border border-gray-200 p-2">
                    {clusters.map((c) => {
                      const checked = form.clusterRefs.includes(c.name);
                      const isActive =
                        checked &&
                        (form.activeClusterRef === c.name ||
                          (!form.activeClusterRef &&
                            form.clusterRefs[0] === c.name));
                      return (
                        <label
                          key={c.name}
                          className="flex items-center gap-3 rounded px-1.5 py-1 hover:bg-gray-50"
                        >
                          <input
                            type="checkbox"
                            checked={checked}
                            onChange={() => toggleClusterRef(c.name)}
                            className="rounded border-gray-300 text-indigo-600 focus:ring-indigo-500"
                          />
                          <span className="flex-1 font-mono text-xs">
                            {c.displayName ? (
                              <>
                                {c.displayName}
                                <span className="ml-1.5 text-gray-400">{c.name}</span>
                              </>
                            ) : (
                              c.name
                            )}
                          </span>
                          {checked && (
                            <label className="flex items-center gap-1.5 text-xs text-gray-600">
                              <input
                                type="radio"
                                name="activeClusterRef"
                                checked={isActive}
                                onChange={() =>
                                  setForm((f) => ({
                                    ...f,
                                    activeClusterRef: c.name,
                                  }))
                                }
                                className="text-indigo-600 focus:ring-indigo-500"
                              />
                              <span>Active</span>
                            </label>
                          )}
                        </label>
                      );
                    })}
                  </div>
                )}
              </div>

              <div>
                <label className="mb-1 block text-xs font-medium text-gray-700">
                  Deploy mode
                </label>
                <select
                  className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
                  value={form.deployMode}
                  onChange={(e) =>
                    setForm((f) => ({ ...f, deployMode: e.target.value }))
                  }
                >
                  <option value="active">Active cluster only</option>
                  <option value="all">All clusters (fan out)</option>
                </select>
                <p className="mt-1 text-xs text-gray-400">
                  {form.deployMode === "all"
                    ? "Apps deploy to every registered cluster — one Application per cluster."
                    : "Apps deploy only to the cluster marked Active."}
                </p>
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

// ── Environment-type env config section ───────────────────────────────────────

function EnvironmentTypeEnvConfigSection({
  environments,
}: {
  environments: OrgEnvironment[];
}) {
  const [selectedEnvType, setSelectedEnvType] = useState<string>("");

  // Initialise selector once environments load.
  useEffect(() => {
    const first = environments[0];
    if (first && !selectedEnvType) {
      setSelectedEnvType(first.name);
    }
  }, [environments, selectedEnvType]);

  const fetchFn = useCallback(
    () => getEnvTypeEnvConfig(selectedEnvType),
    [selectedEnvType],
  );
  const saveFn = useCallback(
    (cfg: Parameters<typeof updateEnvTypeEnvConfig>[1]) =>
      updateEnvTypeEnvConfig(selectedEnvType, cfg),
    [selectedEnvType],
  );

  if (environments.length === 0) return null;

  return (
    <div className="space-y-3">
      {/* Env-type selector */}
      <div className="flex items-center gap-3">
        <label className="text-sm font-medium text-gray-700">
          Environment type
        </label>
        <div className="flex gap-1.5">
          {environments.map((env) => (
            <button
              key={env.name}
              onClick={() => setSelectedEnvType(env.name)}
              className={`rounded-full px-3 py-0.5 text-xs font-medium transition-colors ${
                selectedEnvType === env.name
                  ? "bg-gray-900 text-white"
                  : "border border-gray-200 text-gray-600 hover:bg-gray-50"
              }`}
            >
              {env.displayName || env.name}
            </button>
          ))}
        </div>
      </div>

      {selectedEnvType && (
        <>
          <EnvConfigEditor
            key={selectedEnvType}
            title={`Variables for "${selectedEnvType}" environments`}
            description="Applied to every app running in this environment type across all projects."
            fetchFn={fetchFn}
            saveFn={saveFn}
          />
          <SecretEditor
            key={`secrets-${selectedEnvType}`}
            title={`Shared secrets for "${selectedEnvType}"`}
            description="Shared secrets applied to every app in this environment (env scope). App-level env secrets override these."
            fetchFn={() => listSharedEnvSecretKeys(selectedEnvType)}
            upsertFn={(entries) => upsertSharedEnvSecrets(selectedEnvType, entries)}
            deleteFn={(key) => deleteSharedEnvSecretKey(selectedEnvType, key)}
          />
        </>
      )}
    </div>
  );
}

// ── Namespace Naming section ──────────────────────────────────────────────────

const SHARED_APP_PRESETS = [
  { label: "{project}-{app}-{env}", value: "{project}-{app}-{env}" },
  { label: "{project}-{app}", value: "{project}-{app}" },
];
const DEDICATED_APP_PRESETS = [
  { label: "{project}-{app}", value: "{project}-{app}" },
  { label: "{app}", value: "{app}" },
];
const SHARED_PROJECT_PRESETS = [
  { label: "{project}-{env}", value: "{project}-{env}" },
  { label: "{project}", value: "{project}" },
];
const DEDICATED_PROJECT_PRESETS = [
  { label: "{project}", value: "{project}" },
];

function NamespaceNamingSection() {
  const [naming, setNaming] = useState<OrgNaming>({});
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    let cancelled = false;
    getOrgNaming()
      .then((n) => { if (!cancelled) setNaming(n); })
      .catch((err) => { if (!cancelled) setError(err instanceof Error ? err.message : "Failed to load"); })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, []);

  async function handleSave() {
    setSaving(true);
    setSaveError(null);
    setSaved(false);
    try {
      const updated = await updateOrgNaming(naming);
      setNaming(updated);
      setSaved(true);
    } catch (err) {
      setSaveError(err instanceof Error ? err.message : "Failed to save");
    } finally {
      setSaving(false);
    }
  }

  // Live preview using sample values
  function renderPreview(pattern: string): string {
    if (!pattern) return "";
    return pattern
      .replace("{org}", "myorg")
      .replace("{project}", "billing")
      .replace("{app}", "api")
      .replace("{env}", "staging")
      .replace("{cluster}", "staging-eastus");
  }

  return (
    <div className="rounded-lg border border-gray-200 bg-white">
      <div className="border-b border-gray-100 px-6 py-4">
        <h2 className="text-sm font-medium text-gray-900">Namespace Naming</h2>
        <p className="mt-0.5 text-xs text-gray-500">
          Org-wide default patterns for Kubernetes namespaces. Projects and apps
          can override these. Leave blank for the topology-aware default.
        </p>
      </div>

      <div className="px-6 py-4">
        {loading ? (
          <div className="h-24 animate-pulse rounded bg-gray-100" />
        ) : error ? (
          <p className="text-sm text-red-600">{error}</p>
        ) : (
          <div className="space-y-6">
            {/* Project namespace pattern */}
            <div className="space-y-2">
              <label className="block text-xs font-medium text-gray-700">
                Project namespace pattern
              </label>
              <p className="text-xs text-gray-400">
                Tokens: <code className="font-mono">{"{org}"}</code>,{" "}
                <code className="font-mono">{"{project}"}</code>,{" "}
                <code className="font-mono">{"{env}"}</code>. Used when an app
                has{" "}
                <code className="font-mono">namespaceScope: project</code>.
              </p>
              <div className="flex flex-wrap gap-1.5 mb-1">
                {[...SHARED_PROJECT_PRESETS, ...DEDICATED_PROJECT_PRESETS.filter(
                  (p) => !SHARED_PROJECT_PRESETS.some((s) => s.value === p.value)
                )].map((p) => (
                  <button
                    key={p.value}
                    type="button"
                    onClick={() => setNaming((n) => ({ ...n, projectNamespace: p.value }))}
                    className={`rounded px-2 py-0.5 text-xs font-mono transition-colors ${
                      naming.projectNamespace === p.value
                        ? "bg-indigo-100 text-indigo-800"
                        : "border border-gray-200 text-gray-600 hover:bg-gray-50"
                    }`}
                  >
                    {p.label}
                  </button>
                ))}
              </div>
              <input
                className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
                placeholder="e.g. {project}-{env}  or  {project}"
                value={naming.projectNamespace ?? ""}
                onChange={(e) => setNaming((n) => ({ ...n, projectNamespace: e.target.value || undefined }))}
              />
              {naming.projectNamespace && (
                <p className="text-xs text-gray-400">
                  Preview:{" "}
                  <code className="font-mono text-gray-700">
                    {renderPreview(naming.projectNamespace)}
                  </code>
                </p>
              )}
              {!naming.projectNamespace && (
                <p className="text-xs text-gray-400 italic">
                  Empty → topology-aware default: <code className="font-mono">billing-staging</code> (shared) or <code className="font-mono">billing</code> (dedicated)
                </p>
              )}
            </div>

            {/* App namespace pattern */}
            <div className="space-y-2">
              <label className="block text-xs font-medium text-gray-700">
                App namespace pattern
              </label>
              <p className="text-xs text-gray-400">
                Tokens: <code className="font-mono">{"{org}"}</code>,{" "}
                <code className="font-mono">{"{project}"}</code>,{" "}
                <code className="font-mono">{"{app}"}</code>,{" "}
                <code className="font-mono">{"{env}"}</code>. Used for apps with
                dedicated namespaces (default).
              </p>
              <div className="flex flex-wrap gap-1.5 mb-1">
                {[...SHARED_APP_PRESETS, ...DEDICATED_APP_PRESETS.filter(
                  (p) => !SHARED_APP_PRESETS.some((s) => s.value === p.value)
                )].map((p) => (
                  <button
                    key={p.value}
                    type="button"
                    onClick={() => setNaming((n) => ({ ...n, appNamespace: p.value }))}
                    className={`rounded px-2 py-0.5 text-xs font-mono transition-colors ${
                      naming.appNamespace === p.value
                        ? "bg-indigo-100 text-indigo-800"
                        : "border border-gray-200 text-gray-600 hover:bg-gray-50"
                    }`}
                  >
                    {p.label}
                  </button>
                ))}
              </div>
              <input
                className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
                placeholder="e.g. {project}-{app}-{env}  or  {project}-{app}"
                value={naming.appNamespace ?? ""}
                onChange={(e) => setNaming((n) => ({ ...n, appNamespace: e.target.value || undefined }))}
              />
              {naming.appNamespace && (
                <p className="text-xs text-gray-400">
                  Preview:{" "}
                  <code className="font-mono text-gray-700">
                    {renderPreview(naming.appNamespace)}
                  </code>
                </p>
              )}
              {!naming.appNamespace && (
                <p className="text-xs text-gray-400 italic">
                  Empty → topology-aware default: <code className="font-mono">billing-api-staging</code> (shared) or <code className="font-mono">billing-api</code> (dedicated)
                </p>
              )}
            </div>

            {/* ArgoCD Application name pattern */}
            <div className="space-y-2">
              <label className="block text-xs font-medium text-gray-700">
                ArgoCD Application name pattern
              </label>
              <p className="text-xs text-gray-400">
                Tokens: <code className="font-mono">{"{project}"}</code>,{" "}
                <code className="font-mono">{"{app}"}</code>,{" "}
                <code className="font-mono">{"{env}"}</code>,{" "}
                <code className="font-mono">{"{cluster}"}</code>. Must include{" "}
                <code className="font-mono">{"{app}"}</code> and{" "}
                <code className="font-mono">{"{cluster}"}</code> so each app's
                per-cluster Application stays uniquely named.
              </p>
              <div className="flex flex-wrap gap-1.5 mb-1">
                {[
                  { label: "{project}-{app}-{cluster}", value: "{project}-{app}-{cluster}" },
                  { label: "{project}-{app}-{env}-{cluster}", value: "{project}-{app}-{env}-{cluster}" },
                ].map((p) => (
                  <button
                    key={p.value}
                    type="button"
                    onClick={() => setNaming((n) => ({ ...n, argoAppName: p.value }))}
                    className={`rounded px-2 py-0.5 text-xs font-mono transition-colors ${
                      naming.argoAppName === p.value
                        ? "bg-indigo-100 text-indigo-800"
                        : "border border-gray-200 text-gray-600 hover:bg-gray-50"
                    }`}
                  >
                    {p.label}
                  </button>
                ))}
              </div>
              <input
                className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
                placeholder="e.g. {project}-{app}-{cluster}"
                value={naming.argoAppName ?? ""}
                onChange={(e) => setNaming((n) => ({ ...n, argoAppName: e.target.value || undefined }))}
              />
              {naming.argoAppName ? (
                <p className="text-xs text-gray-400">
                  Preview:{" "}
                  <code className="font-mono text-gray-700">
                    {renderPreview(naming.argoAppName)}
                  </code>
                </p>
              ) : (
                <p className="text-xs text-gray-400 italic">
                  Empty → default <code className="font-mono">{"{project}-{app}-{cluster}"}</code>:{" "}
                  <code className="font-mono">billing-api-staging-eastus</code>. Changing this renames existing Applications (ArgoCD recreates them).
                </p>
              )}
            </div>

            {saveError && (
              <p className="rounded bg-red-50 px-3 py-2 text-xs text-red-700">
                {saveError}
              </p>
            )}
            {saved && (
              <p className="rounded bg-green-50 px-3 py-2 text-xs text-green-700">
                Namespace naming patterns saved.
              </p>
            )}

            <div className="flex justify-end">
              <button
                onClick={handleSave}
                disabled={saving}
                className="rounded-lg bg-gray-900 px-4 py-2 text-sm font-medium text-white hover:bg-gray-700 disabled:opacity-50"
              >
                {saving ? "Saving…" : "Save naming patterns"}
              </button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

// ── Routing profiles section ──────────────────────────────────────────────────
//
// One profile per ExposeMode tier (internal, external) maps to an
// IngressClass + cert-manager ClusterIssuer the chart uses for components
// that target that tier. ClusterIssuer is optional — empty means plain
// HTTP, no cert-manager annotation, no tls block.

const ROUTING_TIERS: Array<{
  name: ExposeMode;
  title: string;
  description: string;
}> = [
  {
    name: "internal",
    title: "Internal tier",
    description:
      "For components reachable only from inside the cluster network (e.g. internal nginx, private CA). Components opt in by setting exposeMode: internal.",
  },
  {
    name: "external",
    title: "External tier",
    description:
      "For components reachable from the public internet. Typically points at the public ingress class and a public-facing cert-manager ClusterIssuer like letsencrypt-prod.",
  },
];

function RoutingProfilesSection() {
  const [profiles, setProfiles] = useState<Record<ExposeMode, RoutingProfile | undefined>>({
    internal: undefined,
    external: undefined,
  });
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    listOrgRoutingProfiles()
      .then((resp) => {
        if (cancelled) return;
        const next: Record<ExposeMode, RoutingProfile | undefined> = {
          internal: undefined,
          external: undefined,
        };
        for (const p of resp.routingProfiles) {
          next[p.name] = p;
        }
        setProfiles(next);
      })
      .catch((err) => {
        if (!cancelled) setError(err instanceof Error ? err.message : "Failed to load");
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  function applyProfile(p: RoutingProfile) {
    setProfiles((prev) => ({ ...prev, [p.name]: p }));
  }

  function clearProfile(name: ExposeMode) {
    setProfiles((prev) => ({ ...prev, [name]: undefined }));
  }

  return (
    <div className="rounded-lg border border-gray-200 bg-white">
      <div className="border-b border-gray-100 px-6 py-4">
        <h2 className="text-sm font-medium text-gray-900">Ingress &amp; TLS routing profiles</h2>
        <p className="mt-0.5 text-xs text-gray-500">
          Two named tiers (<code className="font-mono">internal</code>,{" "}
          <code className="font-mono">external</code>) map components to an
          IngressClass and an optional cert-manager ClusterIssuer. Per-environment
          overrides live on the env record.
        </p>
      </div>

      <div className="px-6 py-4">
        {loading ? (
          <div className="h-24 animate-pulse rounded bg-gray-100" />
        ) : error ? (
          <p className="text-sm text-red-600">{error}</p>
        ) : (
          <div className="space-y-6">
            {ROUTING_TIERS.map((tier) => (
              <RoutingProfileEditor
                key={tier.name}
                tier={tier}
                profile={profiles[tier.name]}
                onSaved={applyProfile}
                onCleared={() => clearProfile(tier.name)}
              />
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

interface RoutingProfileEditorProps {
  tier: { name: ExposeMode; title: string; description: string };
  profile: RoutingProfile | undefined;
  onSaved: (p: RoutingProfile) => void;
  onCleared: () => void;
}

function RoutingProfileEditor({ tier, profile, onSaved, onCleared }: RoutingProfileEditorProps) {
  const [ingressClassName, setIngressClassName] = useState(profile?.ingressClassName ?? "");
  const [clusterIssuer, setClusterIssuer] = useState(profile?.clusterIssuer ?? "");
  const [baseDomain, setBaseDomain] = useState(profile?.baseDomain ?? "");
  const [gatewayName, setGatewayName] = useState(profile?.gateway?.name ?? "");
  const [gatewayNamespace, setGatewayNamespace] = useState(profile?.gateway?.namespace ?? "");
  const [gatewaySectionName, setGatewaySectionName] = useState(profile?.gateway?.sectionName ?? "");
  const [saving, setSaving] = useState(false);
  const [removing, setRemoving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  // Re-sync local form state when the parent profile prop changes (e.g. after
  // an initial load completes or another tier saves).
  useEffect(() => {
    setIngressClassName(profile?.ingressClassName ?? "");
    setClusterIssuer(profile?.clusterIssuer ?? "");
    setBaseDomain(profile?.baseDomain ?? "");
    setGatewayName(profile?.gateway?.name ?? "");
    setGatewayNamespace(profile?.gateway?.namespace ?? "");
    setGatewaySectionName(profile?.gateway?.sectionName ?? "");
  }, [profile]);

  async function handleSave() {
    if (!ingressClassName.trim()) {
      setSaveError("ingressClassName is required");
      return;
    }
    setSaving(true);
    setSaveError(null);
    setSaved(false);
    try {
      const gwName = gatewayName.trim();
      const updated = await upsertOrgRoutingProfile(tier.name, {
        ingressClassName: ingressClassName.trim(),
        clusterIssuer: clusterIssuer.trim() || undefined,
        baseDomain: baseDomain.trim() || undefined,
        gateway: gwName
          ? {
              name: gwName,
              namespace: gatewayNamespace.trim() || undefined,
              sectionName: gatewaySectionName.trim() || undefined,
            }
          : undefined,
      });
      onSaved(updated);
      setSaved(true);
    } catch (err) {
      setSaveError(err instanceof Error ? err.message : "Failed to save");
    } finally {
      setSaving(false);
    }
  }

  async function handleRemove() {
    if (!profile) return;
    setRemoving(true);
    setSaveError(null);
    setSaved(false);
    try {
      await deleteOrgRoutingProfile(tier.name);
      onCleared();
    } catch (err) {
      setSaveError(err instanceof Error ? err.message : "Failed to remove");
    } finally {
      setRemoving(false);
    }
  }

  return (
    <div className="rounded border border-gray-200 px-4 py-3">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h3 className="text-sm font-medium text-gray-900">{tier.title}</h3>
          <p className="mt-0.5 text-xs text-gray-500">{tier.description}</p>
        </div>
        {profile && (
          <span className="rounded-full bg-green-50 px-2 py-0.5 text-[10px] font-medium text-green-700">
            configured
          </span>
        )}
      </div>

      <div className="mt-3 grid gap-3 sm:grid-cols-2">
        <div>
          <label className="block text-xs font-medium text-gray-700">
            Ingress class name <span className="text-red-500">*</span>
          </label>
          <input
            className="mt-1 w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
            placeholder={tier.name === "internal" ? "nginx-internal" : "nginx"}
            value={ingressClassName}
            onChange={(e) => setIngressClassName(e.target.value)}
          />
        </div>
        <div>
          <label className="block text-xs font-medium text-gray-700">
            Cert-manager ClusterIssuer
          </label>
          <input
            className="mt-1 w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
            placeholder={tier.name === "internal" ? "internal-ca (optional)" : "letsencrypt-prod (optional)"}
            value={clusterIssuer}
            onChange={(e) => setClusterIssuer(e.target.value)}
          />
          <p className="mt-1 text-xs text-gray-400">
            Empty → plain HTTP (no TLS, no cert-manager annotation).
          </p>
        </div>
        <div className="sm:col-span-2">
          <label className="block text-xs font-medium text-gray-700">
            Base domain override
          </label>
          <input
            className="mt-1 w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
            placeholder="leave blank to inherit env baseDomain"
            value={baseDomain}
            onChange={(e) => setBaseDomain(e.target.value)}
          />
        </div>
        <div className="sm:col-span-2">
          <label className="block text-xs font-medium text-gray-700">
            Gateway API (optional)
          </label>
          <p className="mt-0.5 text-xs text-gray-400">
            For charts routing via Gateway API / Envoy. Exposed as{" "}
            <span className="font-mono">
              ((platform.{tier.name}Gateway*))
            </span>{" "}
            for HTTPRoute parentRefs.
          </p>
          <div className="mt-1 grid gap-2 sm:grid-cols-3">
            <input
              className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
              placeholder={tier.name === "internal" ? "envoy-internal" : "envoy-external"}
              value={gatewayName}
              onChange={(e) => setGatewayName(e.target.value)}
            />
            <input
              className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
              placeholder="envoy-gateway-system"
              value={gatewayNamespace}
              onChange={(e) => setGatewayNamespace(e.target.value)}
            />
            <input
              className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
              placeholder="https (section)"
              value={gatewaySectionName}
              onChange={(e) => setGatewaySectionName(e.target.value)}
            />
          </div>
        </div>
      </div>

      {saveError && (
        <p className="mt-3 rounded bg-red-50 px-3 py-2 text-xs text-red-700">{saveError}</p>
      )}
      {saved && (
        <p className="mt-3 rounded bg-green-50 px-3 py-2 text-xs text-green-700">
          {tier.title} saved.
        </p>
      )}

      <div className="mt-3 flex justify-end gap-2">
        {profile && (
          <button
            onClick={handleRemove}
            disabled={removing || saving}
            className="rounded-lg border border-gray-300 px-3 py-1.5 text-xs font-medium text-gray-700 hover:bg-gray-50 disabled:opacity-50"
          >
            {removing ? "Removing…" : "Remove"}
          </button>
        )}
        <button
          onClick={handleSave}
          disabled={saving || removing}
          className="rounded-lg bg-gray-900 px-3 py-1.5 text-xs font-medium text-white hover:bg-gray-700 disabled:opacity-50"
        >
          {saving ? "Saving…" : profile ? "Update" : "Save"}
        </button>
      </div>
    </div>
  );
}

// ── Secrets backend section ────────────────────────────────────────────────────

const BACKEND_OPTIONS = [
  {
    value: "k8s",
    label: "Kubernetes Secrets",
    description: "Native K8s Secrets in app namespaces (demo/default)",
  },
  {
    value: "onepassword",
    label: "1Password",
    description: "1Password via External Secrets Operator (production)",
  },
];

function SecretsBackendSection() {
  const [config, setConfig] = useState<SecretBackendConfig | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  // SA Token paste state
  const [saToken, setSaToken] = useState("");
  const [tokenSaving, setTokenSaving] = useState(false);
  const [tokenMsg, setTokenMsg] = useState<string | null>(null);

  // Vault list from SA token
  const [vaults, setVaults] = useState<VaultInfo[]>([]);
  const [vaultsLoading, setVaultsLoading] = useState(false);

  // Add binding form state
  const [showAddBinding, setShowAddBinding] = useState(false);
  const [bindEnv, setBindEnv] = useState("");
  const [bindVaultId, setBindVaultId] = useState("");
  const [bindBusy, setBindBusy] = useState(false);
  // When set, the binding form edits this already-bound env (vs adding a new one).
  const [editingEnv, setEditingEnv] = useState("");
  const [removingEnv, setRemovingEnv] = useState("");

  // Setup guide toggle
  const [showGuide, setShowGuide] = useState(false);

  // Org environments (for the vault registration dropdown) + clusters (for
  // the per-cluster Connect token card)
  const [orgEnvs, setOrgEnvs] = useState<OrgEnvironment[]>([]);
  const [orgClusters, setOrgClusters] = useState<Cluster[]>([]);

  useEffect(() => {
    let cancelled = false;
    Promise.all([getSecretsBackend(), listOrgEnvironments(), listClusters()])
      .then(([cfg, envsResp, clustersResp]) => {
        if (!cancelled) {
          setConfig(cfg);
          setOrgEnvs(envsResp.environments || []);
          setOrgClusters(clustersResp || []);
        }
      })
      .catch((err) => {
        if (!cancelled)
          setError(err instanceof Error ? err.message : "Failed to load");
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const loadVaults = useCallback(async () => {
    setVaultsLoading(true);
    try {
      const v = await listVaults();
      setVaults(v);
    } catch {
      setVaults([]);
    } finally {
      setVaultsLoading(false);
    }
  }, []);

  async function handleTypeChange(value: string) {
    if (!config) return;
    setSaving(true);
    setError(null);
    try {
      // Only change the active backend type. The server preserves the previously
      // configured backend's settings (e.g. 1Password) so re-selecting it reloads
      // its config — don't clear them here.
      const result = await updateSecretsBackend({ type: value });
      setConfig(result);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to save");
    } finally {
      setSaving(false);
    }
  }

  async function handleSaveToken() {
    if (!saToken.trim()) return;
    setTokenSaving(true);
    setTokenMsg(null);
    try {
      const res = await saveSAToken(saToken.trim());
      if (res.valid) {
        setTokenMsg(`Token saved. ${res.vaultCount ?? 0} vault(s) accessible.`);
        setSaToken("");
        const updated = await getSecretsBackend();
        setConfig(updated);
        await loadVaults();
      } else {
        setTokenMsg(res.error || "Token validation failed.");
      }
    } catch (err) {
      setTokenMsg(err instanceof Error ? err.message : "Failed to save token");
    } finally {
      setTokenSaving(false);
    }
  }

  async function handleAddBinding() {
    if (!bindEnv || !bindVaultId) return;
    setBindBusy(true);
    setError(null);
    try {
      const vault = vaults.find((v) => v.id === bindVaultId);
      await registerEnvVault(bindEnv, {
        vaultId: bindVaultId,
        vaultName: vault?.title,
      });
      const updated = await getSecretsBackend();
      setConfig(updated);
      setShowAddBinding(false);
      setBindEnv("");
      setBindVaultId("");
      setEditingEnv("");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Vault registration failed");
    } finally {
      setBindBusy(false);
    }
  }

  // startEditBinding opens the form pre-filled to change an env's vault.
  function startEditBinding(env: string, vaultId: string) {
    setEditingEnv(env);
    setBindEnv(env);
    setBindVaultId(vaultId);
    setShowAddBinding(true);
    if (vaults.length === 0) loadVaults();
  }

  async function removeBinding(env: string) {
    if (
      !window.confirm(
        `Remove the vault binding for "${env}"? Secrets for this environment will no longer resolve until you bind a vault again.`,
      )
    ) {
      return;
    }
    setRemovingEnv(env);
    setError(null);
    try {
      await unregisterEnvVault(env);
      const updated = await getSecretsBackend();
      setConfig(updated);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to remove binding");
    } finally {
      setRemovingEnv("");
    }
  }


  return (
    <div className="rounded-lg border border-gray-200 bg-white">
      <div className="border-b border-gray-100 px-6 py-4">
        <h2 className="text-sm font-medium text-gray-900">Secrets Backend</h2>
        <p className="mt-0.5 text-xs text-gray-500">
          Configure how app secrets are stored and delivered to clusters.
        </p>
      </div>

      <div className="px-6 py-4">
        {loading ? (
          <div className="h-10 animate-pulse rounded bg-gray-100" />
        ) : error ? (
          <p className="text-sm text-red-600">{error}</p>
        ) : config ? (
          <div className="space-y-6">
            {/* Provider selector */}
            <div className="space-y-2">
              <label className="text-xs font-medium text-gray-700">Provider</label>
              {BACKEND_OPTIONS.map((opt) => (
                <label
                  key={opt.value}
                  className={`flex cursor-pointer items-start gap-3 rounded-lg border p-3 transition-colors ${
                    config.type === opt.value
                      ? "border-indigo-200 bg-indigo-50"
                      : "border-gray-200 hover:bg-gray-50"
                  }`}
                >
                  <input
                    type="radio"
                    name="secrets-backend"
                    value={opt.value}
                    checked={config.type === opt.value}
                    disabled={saving}
                    onChange={() => handleTypeChange(opt.value)}
                    className="mt-0.5"
                  />
                  <div>
                    <span className="text-sm font-medium text-gray-900">
                      {opt.label}
                    </span>
                    <p className="text-xs text-gray-500">{opt.description}</p>
                  </div>
                </label>
              ))}
            </div>

            {/* ExternalSecret settings (apply to every backend) */}
            <div className="space-y-2">
              <label className="text-xs font-medium text-gray-700">
                ExternalSecret refresh interval
              </label>
              <p className="text-xs text-gray-500">
                How often External Secrets Operator re-pulls secret values into
                app namespaces (<code className="font-mono">spec.refreshInterval</code>
                on every generated ExternalSecret). Go duration, e.g.{" "}
                <code className="font-mono">1m</code>,{" "}
                <code className="font-mono">30s</code>,{" "}
                <code className="font-mono">1h</code>. Defaults to{" "}
                <code className="font-mono">1m</code>.
              </p>
              <div className="flex items-end gap-3">
                <input
                  type="text"
                  className="w-40 rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono"
                  placeholder="1m"
                  defaultValue={config.externalSecrets?.refreshInterval ?? ""}
                  id="es-refresh-interval-input"
                />
                <button
                  onClick={async () => {
                    const val = (
                      document.getElementById(
                        "es-refresh-interval-input",
                      ) as HTMLInputElement
                    )?.value?.trim();
                    setSaving(true);
                    try {
                      const updated = await updateSecretsBackend({
                        ...config,
                        externalSecrets: { refreshInterval: val },
                      });
                      setConfig(updated);
                    } finally {
                      setSaving(false);
                    }
                  }}
                  disabled={saving}
                  className="rounded-lg bg-gray-900 px-4 py-2 text-sm font-medium text-white hover:bg-gray-700 disabled:opacity-50"
                >
                  {saving ? "Saving…" : "Save"}
                </button>
              </div>
              <p className="text-xs text-gray-400">
                Current:{" "}
                <span className="font-mono">
                  {config.externalSecrets?.refreshInterval || "1m (default)"}
                </span>
              </p>
            </div>

            {/* 1Password section */}
            {config.type === "onepassword" && (
              <div className="space-y-5">
                {/* Collapsible setup guide */}
                <div className="rounded-lg border border-blue-100 bg-blue-50/50">
                  <button
                    onClick={() => setShowGuide(!showGuide)}
                    className="flex w-full items-center justify-between px-4 py-3 text-left"
                  >
                    <span className="text-sm font-medium text-blue-900">
                      Setup Guide
                    </span>
                    <span className="text-xs text-blue-600">
                      {showGuide ? "Hide" : "Show steps"}
                    </span>
                  </button>
                  {showGuide && (
                    <div className="space-y-3 border-t border-blue-100 px-4 py-3">
                      <p className="text-xs text-blue-900">
                        <strong>Two kinds of vaults:</strong> one{" "}
                        <strong>global vault</strong> (org-wide, read-only from
                        every cluster) and one <strong>env vault</strong> per
                        environment. Per-cluster overrides are{" "}
                        <strong>items inside the env vault</strong> (e.g.{" "}
                        <code>myapp-cluster-eu-1</code>), so clusters need no
                        vault of their own. Each scope holds both org-admin (
                        <strong>shared</strong>) and per-app secrets; precedence
                        is <code>cluster &gt; env &gt; global</code> and{" "}
                        <code>app &gt; shared</code>. Each cluster runs{" "}
                        <strong>one ClusterSecretStore</strong> (
                        <code>suparship-store</code>) listing every vault it
                        reads, authenticated by{" "}
                        <strong>one Connect token per cluster</strong>.
                        1Password Service Accounts cannot create vaults or
                        Connect tokens — you create both manually in the
                        1Password console; suparShip handles the cluster-side
                        automation (sealing the token, generating the store,
                        publishing to GitOps).
                      </p>
                      <ol className="space-y-2 text-xs text-blue-900">
                        <li>
                          <strong>1. Create the global and per-env vaults</strong>{" "}
                          in the 1Password web console (e.g.{" "}
                          <code>company-global</code>,{" "}
                          <code>staging-env</code>).
                        </li>
                        <li>
                          <strong>2. Create a Service Account</strong> with
                          Read &amp; Write access to all those vaults. (No
                          vault-creation permission is needed — SAs can't
                          create vaults regardless.)
                        </li>
                        <li>
                          <strong>3. Paste the SA token</strong> below —
                          suparShip validates it and shows how many vaults
                          are visible.
                        </li>
                        <li>
                          <strong>4. Pick the global vault</strong> from the
                          dropdown that appears after the SA token is saved.
                        </li>
                        <li>
                          <strong>5. Register env vaults</strong> below — pick
                          the environment and its vault.
                        </li>
                        <li>
                          <strong>6. Set up a Connect Server</strong> in
                          1Password, grant it access to <strong>all</strong>{" "}
                          suparShip vaults, and deploy Connect to your workload
                          clusters (Helm chart or Docker).
                        </li>
                        <li>
                          <strong>7. Issue one Connect token per cluster</strong>{" "}
                          in the 1Password console, with access to the global
                          vault plus the env vaults of the environments that
                          land on that cluster.
                        </li>
                        <li>
                          <strong>8. Paste each cluster&apos;s token</strong> in
                          the Cluster Connect Tokens section below — suparShip
                          seals it and publishes the cluster&apos;s single
                          SealedSecret + ClusterSecretStore to GitOps. Binding a
                          new env to a cluster later requires a token that also
                          covers the new env vault (re-issue + re-paste).
                        </li>
                      </ol>
                      <p className="text-xs text-blue-900">
                        Need more detail? See{" "}
                        <code>docs/secrets.md</code> for the architecture
                        diagram, troubleshooting, and the full RBAC matrix.
                      </p>
                    </div>
                  )}
                </div>

                {/* SA Token paste field */}
                <div className="space-y-3">
                  <label className="block text-xs font-medium text-gray-700">
                    Service Account Token
                  </label>
                  <p className="text-xs text-gray-500">
                    Paste the 1Password Service Account token. It needs Read
                    &amp; Write access to every vault you want suparShip to
                    manage — the global vault and each env vault. 1Password
                    Service Accounts cannot create vaults, so make sure these
                    vaults already exist before pasting.
                  </p>
                  <div className="flex items-end gap-3">
                    <div className="flex-1">
                      <input
                        type="password"
                        className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono"
                        placeholder="Paste SA token here…"
                        value={saToken}
                        onChange={(e) => setSaToken(e.target.value)}
                      />
                    </div>
                    <button
                      onClick={handleSaveToken}
                      disabled={tokenSaving || !saToken.trim()}
                      className="rounded-lg bg-gray-900 px-4 py-2 text-sm font-medium text-white hover:bg-gray-700 disabled:opacity-50"
                    >
                      {tokenSaving ? "Saving…" : "Save"}
                    </button>
                  </div>
                  {tokenMsg && (
                    <p className="text-xs text-gray-600">{tokenMsg}</p>
                  )}
                </div>

                {/* Global vault picker — operator selects the vault they
                    created manually in the 1Password console. 1Password
                    Service Accounts cannot create vaults, so suparShip cannot
                    auto-provision this. */}
                <PlatformVaultPicker
                  config={config}
                  onChanged={async () => {
                    const updated = await getSecretsBackend();
                    setConfig(updated);
                  }}
                />

                {/* Connect Server endpoint */}
                <div className="space-y-1">
                  <label className="block text-xs font-medium text-gray-700">
                    Connect Server URL{" "}
                    <span className="font-normal text-gray-400">(org default)</span>
                  </label>
                  <p className="text-xs text-gray-500">
                    In-cluster URL where the 1Password Connect server is
                    reachable. Used in every <code className="font-mono">ClusterSecretStore</code> unless
                    overridden per-binding.
                  </p>
                  <div className="flex items-end gap-3">
                    <input
                      type="text"
                      className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono"
                      placeholder="http://onepassword-connect.<namespace>.svc.cluster.local:8080"
                      defaultValue={
                        config.onePassword?.connect?.endpoint ?? ""
                      }
                      id="connect-endpoint-input"
                    />
                    <button
                      onClick={async () => {
                        const val = (
                          document.getElementById(
                            "connect-endpoint-input",
                          ) as HTMLInputElement
                        )?.value?.trim();
                        if (!config?.onePassword) return;
                        setSaving(true);
                        try {
                          const updated = await updateSecretsBackend({
                            type: config.type,
                            onePassword: {
                              ...config.onePassword,
                              connect: {
                                ...config.onePassword.connect,
                                endpoint: val,
                              },
                            },
                          });
                          setConfig(updated);
                        } finally {
                          setSaving(false);
                        }
                      }}
                      disabled={saving}
                      className="rounded-lg bg-gray-900 px-4 py-2 text-sm font-medium text-white hover:bg-gray-700 disabled:opacity-50"
                    >
                      {saving ? "Saving…" : "Save"}
                    </button>
                  </div>
                  {config.onePassword?.connect?.endpoint && (
                    <p className="text-xs text-green-600">
                      Stored: {config.onePassword.connect.endpoint}
                    </p>
                  )}
                </div>

                {/* Environment bindings table */}
                <div>
                  <div className="mb-2 flex items-center justify-between">
                    <label className="text-xs font-medium text-gray-700">
                      Environment Bindings
                    </label>
                    <button
                      onClick={() => {
                        const opening = !showAddBinding;
                        setShowAddBinding(opening);
                        if (opening) {
                          if (vaults.length === 0) loadVaults();
                        } else {
                          // Cancel: clear any in-progress edit/add.
                          setEditingEnv("");
                          setBindEnv("");
                          setBindVaultId("");
                        }
                      }}
                      className="text-xs font-medium text-indigo-600 hover:text-indigo-800"
                    >
                      {showAddBinding ? "Cancel" : "+ Add Vault"}
                    </button>
                  </div>

                  {/* Add vault form (env scope; cluster overrides live inside
                      the env vault, so there is nothing to register per cluster) */}
                  {showAddBinding && (
                    <div className="mb-4 space-y-3 rounded-lg border border-gray-200 bg-gray-50/50 p-4">
                      <div>
                        <label className="mb-1 block text-xs font-medium text-gray-700">
                          Environment
                        </label>
                        {(() => {
                          if (editingEnv) {
                            return (
                              <div className="rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm font-mono">
                                {editingEnv}
                              </div>
                            );
                          }
                          const boundEnvs = new Set(
                            (config?.onePassword?.envVaults || []).map((v) => v.key),
                          );
                          const available = orgEnvs.filter(
                            (e) => !boundEnvs.has(e.name),
                          );
                          return available.length > 0 ? (
                            <select
                              className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm"
                              value={bindEnv}
                              onChange={(e) => setBindEnv(e.target.value)}
                            >
                              <option value="">Select an environment…</option>
                              {available.map((e) => (
                                <option key={e.name} value={e.name}>
                                  {e.name}
                                </option>
                              ))}
                            </select>
                          ) : (
                            <p className="text-xs text-gray-400">
                              All environments are already bound. Create a new
                              environment in Settings &gt; Environments first.
                            </p>
                          );
                        })()}
                        <p className="mt-1 text-xs text-gray-400">
                          Registration records which vault backs the env.
                          Per-cluster overrides are items inside this vault —
                          no per-cluster vault needed. Connect tokens are
                          pasted per cluster below.
                        </p>
                      </div>
                      <div>
                        <label className="mb-1 block text-xs font-medium text-gray-700">
                          Vault
                        </label>
                        {vaultsLoading ? (
                          <div className="h-9 animate-pulse rounded bg-gray-100" />
                        ) : vaults.length > 0 ? (
                          <select
                            className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm"
                            value={bindVaultId}
                            onChange={(e) => setBindVaultId(e.target.value)}
                          >
                            <option value="">Select a vault…</option>
                            {vaults.map((v) => (
                              <option key={v.id} value={v.id}>
                                {v.title} ({v.id.slice(0, 8)}…)
                              </option>
                            ))}
                          </select>
                        ) : (
                          <input
                            type="text"
                            className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono"
                            placeholder="Vault UUID (save SA token first to see a dropdown)"
                            value={bindVaultId}
                            onChange={(e) => setBindVaultId(e.target.value)}
                          />
                        )}
                      </div>
                      <button
                        onClick={handleAddBinding}
                        disabled={bindBusy || !bindEnv || !bindVaultId}
                        className="rounded-lg bg-gray-900 px-4 py-2 text-sm font-medium text-white hover:bg-gray-700 disabled:opacity-50"
                      >
                        {bindBusy
                          ? "Saving…"
                          : editingEnv
                            ? "Update Vault"
                            : "Register Vault"}
                      </button>
                    </div>
                  )}

                  {/* Provisioned vaults across all scopes (read-only) */}
                  {(() => {
                    const op = config.onePassword;
                    const rows: Array<{
                      scope: string;
                      key: string;
                      vaultName?: string;
                      vaultId: string;
                      provisioned?: boolean;
                      lastError?: string;
                    }> = [];
                    if (op?.globalVault?.vaultId) {
                      rows.push({ scope: "global", key: "—", ...op.globalVault });
                    }
                    for (const v of op?.envVaults || []) {
                      rows.push({ scope: "env", ...v, key: v.key || "" });
                    }
                    if (rows.length === 0) {
                      return (
                        <p className="text-xs text-gray-400">
                          No vaults registered yet. Click &ldquo;+ Add
                          Vault&rdquo; to register an environment vault; set
                          the global vault above.
                        </p>
                      );
                    }
                    return (
                      <table className="w-full text-sm">
                        <thead>
                          <tr className="border-b border-gray-100 text-left text-xs font-medium uppercase tracking-wider text-gray-500">
                            <th className="py-2">Scope</th>
                            <th className="py-2">Key</th>
                            <th className="py-2">Vault</th>
                            <th className="py-2">Status</th>
                            <th className="py-2 text-right">Actions</th>
                          </tr>
                        </thead>
                        <tbody className="divide-y divide-gray-50">
                          {rows.map((b) => (
                            <tr key={`${b.scope}-${b.key}`}>
                              <td className="py-2 font-mono text-xs">{b.scope}</td>
                              <td className="py-2 font-mono text-xs">{b.key}</td>
                              <td className="py-2 font-mono text-xs">
                                {b.vaultName || b.vaultId}
                              </td>
                              <td className="py-2 text-xs">
                                {b.provisioned ? (
                                  <span className="inline-flex items-center gap-1 rounded bg-green-50 px-2 py-0.5 text-green-700">
                                    registered
                                  </span>
                                ) : (
                                  <span className="inline-flex items-center gap-1 rounded bg-amber-50 px-2 py-0.5 text-amber-700">
                                    pending
                                  </span>
                                )}
                                {b.lastError && (
                                  <span className="ml-2 text-xs text-red-600">
                                    {b.lastError}
                                  </span>
                                )}
                              </td>
                              <td className="py-2 text-right text-xs">
                                {b.scope === "env" ? (
                                  <span className="inline-flex gap-3">
                                    <button
                                      onClick={() => startEditBinding(b.key, b.vaultId)}
                                      className="font-medium text-indigo-600 hover:text-indigo-800"
                                    >
                                      Edit
                                    </button>
                                    <button
                                      onClick={() => removeBinding(b.key)}
                                      disabled={removingEnv === b.key}
                                      className="font-medium text-red-600 hover:text-red-800 disabled:opacity-50"
                                    >
                                      {removingEnv === b.key ? "Removing…" : "Remove"}
                                    </button>
                                  </span>
                                ) : (
                                  <span className="text-gray-300">—</span>
                                )}
                              </td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    );
                  })()}
                </div>

                {/* Per-cluster Connect tokens — one token per cluster,
                    covering every vault the cluster reads. */}
                <ClusterTokensCard
                  config={config}
                  clusters={orgClusters}
                  orgEnvs={orgEnvs}
                  onChanged={async () => {
                    const updated = await getSecretsBackend();
                    setConfig(updated);
                  }}
                />

                <MigrationPanel config={config} />
              </div>
            )}
          </div>
        ) : null}
      </div>
    </div>
  );
}

// ── Global vault picker ──────────────────────────────────────────────────────
//
// 1Password Service Accounts cannot create new vaults. The operator creates
// the global vault by hand in the 1Password console; suparShip just needs to
// know which vault it is. This component lists every vault the SA token can
// see, lets the operator pick one, and PUTs the choice to
// /org/secret-backend/global-vault. Clusters read the vault through their own
// per-cluster Connect token (ClusterTokensCard).

function PlatformVaultPicker({
  config,
  onChanged,
}: {
  config: SecretBackendConfig;
  onChanged: () => Promise<void>;
}) {
  const currentID = config.onePassword?.globalVault?.vaultId ?? "";
  const currentName = config.onePassword?.globalVault?.vaultName ?? "";
  const [vaults, setVaults] = useState<VaultInfo[] | null>(null);
  const [selectedID, setSelectedID] = useState(currentID);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [savedMsg, setSavedMsg] = useState<string | null>(null);

  // Keep the dropdown in sync with the persisted ID across re-fetches.
  useEffect(() => {
    setSelectedID(currentID);
  }, [currentID]);

  async function handleLoadVaults() {
    setLoading(true);
    setError(null);
    try {
      const v = await listVaults();
      setVaults(v);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load vaults");
    } finally {
      setLoading(false);
    }
  }

  async function handleSave() {
    if (!selectedID) return;
    setSaving(true);
    setError(null);
    setSavedMsg(null);
    try {
      const picked = vaults?.find((v) => v.id === selectedID);
      const res = await setGlobalVault(selectedID, picked?.title);
      setSavedMsg(
        `Saved. Global vault "${res.vaultName}" set. Each cluster reads it through its own Connect token (pasted per cluster below).`,
      );
      await onChanged();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to save");
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="space-y-2">
      <label className="block text-xs font-medium text-gray-700">
        Global vault
      </label>
      <p className="text-xs text-gray-500">
        Pick the 1Password vault you created (manually) for global-scope
        secrets — read-only from every cluster's ESO. Env vaults are
        registered separately in the table below.
      </p>
      <div className="flex items-center gap-2">
        {currentID ? (
          <span className="flex items-center gap-1.5 text-xs text-green-700">
            <span className="inline-block h-1.5 w-1.5 rounded-full bg-green-500" />
            Currently:{" "}
            <code className="font-mono">{currentName || currentID}</code>
          </span>
        ) : (
          <span className="flex items-center gap-1.5 text-xs text-amber-700">
            <span className="inline-block h-1.5 w-1.5 rounded-full bg-amber-500" />
            Not set — global-scope secret writes will fail until a vault is
            picked.
          </span>
        )}
      </div>

      <div className="flex items-end gap-2">
        {vaults === null ? (
          <button
            type="button"
            onClick={handleLoadVaults}
            disabled={loading}
            className="rounded-lg border border-gray-300 px-3 py-2 text-xs font-medium text-gray-700 hover:bg-gray-50 disabled:opacity-50"
          >
            {loading ? "Loading…" : "List vaults"}
          </button>
        ) : (
          <div className="flex flex-1 flex-col gap-2">
            <div className="flex items-center gap-2">
              <select
                value={selectedID}
                onChange={(e) => setSelectedID(e.target.value)}
                className="flex-1 rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono"
              >
                <option value="">— select a vault —</option>
                {vaults.map((v) => (
                  <option key={v.id} value={v.id}>
                    {v.title}
                  </option>
                ))}
              </select>
              <button
                type="button"
                onClick={handleLoadVaults}
                disabled={loading}
                title="Refresh the vault list"
                className="rounded-lg border border-gray-300 px-3 py-2 text-xs text-gray-600 hover:bg-gray-50 disabled:opacity-50"
              >
                ⟳
              </button>
            </div>
            <button
              type="button"
              onClick={handleSave}
              disabled={saving || !selectedID || selectedID === currentID}
              className="self-start rounded-lg bg-gray-900 px-4 py-2 text-sm font-medium text-white hover:bg-gray-700 disabled:opacity-50"
            >
              {saving ? "Saving…" : "Save"}
            </button>
          </div>
        )}
      </div>
      {error && <p className="text-xs text-red-600">{error}</p>}
      {savedMsg && <p className="text-xs text-green-700">{savedMsg}</p>}
    </div>
  );
}

// ── Per-cluster Connect tokens ───────────────────────────────────────────────
//
// One Connect token per cluster, with access to every vault the cluster reads
// (the global vault + the env vaults of the environments bound to it). Pasting
// a token seals it and publishes the cluster's single unified
// ClusterSecretStore (suparship-store). Binding a new env to a cluster later
// needs a re-issued token covering the new vault — re-paste it here.

function ClusterTokensCard({
  config,
  clusters,
  orgEnvs,
  onChanged,
}: {
  config: SecretBackendConfig;
  clusters: Cluster[];
  orgEnvs: OrgEnvironment[];
  onChanged: () => Promise<void>;
}) {
  const [tokenInputs, setTokenInputs] = useState<Record<string, string>>({});
  const [busyCluster, setBusyCluster] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const tokenRefs = config.onePassword?.clusterTokens || [];
  const refFor = (name: string) => tokenRefs.find((t) => t.cluster === name);
  const boundEnvsFor = (name: string) =>
    orgEnvs
      .filter(
        (e) =>
          e.activeClusterRef === name || (e.clusterRefs || []).includes(name),
      )
      .map((e) => e.name);

  async function handleSaveToken(cluster: string) {
    const token = (tokenInputs[cluster] || "").trim();
    if (!token) return;
    setBusyCluster(cluster);
    setError(null);
    try {
      await setClusterConnectToken(cluster, { connectToken: token });
      setTokenInputs((cur) => ({ ...cur, [cluster]: "" }));
      await onChanged();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to save token");
      await onChanged(); // pick up LastError recorded by the server
    } finally {
      setBusyCluster(null);
    }
  }

  return (
    <div>
      <label className="text-xs font-medium text-gray-700">
        Cluster Connect Tokens
      </label>
      <p className="mb-2 mt-0.5 text-xs text-gray-500">
        One Connect token per cluster, covering the global vault plus the env
        vaults bound to that cluster. Pasting a token publishes the
        cluster&apos;s single{" "}
        <code className="font-mono">suparship-store</code> ClusterSecretStore.
      </p>
      {clusters.length === 0 ? (
        <p className="text-xs text-gray-400">
          No clusters registered yet. Register one under Settings &gt;
          Clusters first.
        </p>
      ) : (
        <div className="space-y-3">
          {clusters.map((c) => {
            const ref = refFor(c.name);
            const envs = boundEnvsFor(c.name);
            return (
              <div
                key={c.name}
                className="rounded-lg border border-gray-200 bg-gray-50/50 p-3"
              >
                <div className="flex items-center justify-between">
                  <div>
                    <span className="font-mono text-sm text-gray-900">
                      {c.name}
                    </span>
                    <span className="ml-2 text-xs text-gray-400">
                      reads: global
                      {envs.length > 0 ? `, ${envs.join(", ")}` : ""}
                    </span>
                  </div>
                  {ref?.sealed ? (
                    <span className="inline-flex items-center gap-1 rounded bg-green-50 px-2 py-0.5 text-xs text-green-700">
                      sealed
                    </span>
                  ) : (
                    <span className="inline-flex items-center gap-1 rounded bg-amber-50 px-2 py-0.5 text-xs text-amber-700">
                      pending token
                    </span>
                  )}
                </div>
                {ref?.lastError && (
                  <p className="mt-1 text-xs text-red-600">{ref.lastError}</p>
                )}
                <div className="mt-2 flex items-end gap-2">
                  <input
                    type="password"
                    className="flex-1 rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono"
                    placeholder={
                      ref?.sealed
                        ? "Paste a new token to rotate / widen vault access…"
                        : "Paste this cluster's Connect token…"
                    }
                    value={tokenInputs[c.name] || ""}
                    onChange={(e) =>
                      setTokenInputs((cur) => ({
                        ...cur,
                        [c.name]: e.target.value,
                      }))
                    }
                  />
                  <button
                    onClick={() => handleSaveToken(c.name)}
                    disabled={
                      busyCluster === c.name ||
                      !(tokenInputs[c.name] || "").trim()
                    }
                    className="rounded-lg bg-gray-900 px-4 py-2 text-sm font-medium text-white hover:bg-gray-700 disabled:opacity-50"
                  >
                    {busyCluster === c.name ? "Sealing…" : "Seal"}
                  </button>
                </div>
              </div>
            );
          })}
        </div>
      )}
      {error && <p className="mt-2 text-xs text-red-600">{error}</p>}
    </div>
  );
}

function MigrationPanel(_props: { config: SecretBackendConfig }) {
  // Migration to 1Password was removed in the 3-scope model (clean
  // replacement, no legacy data migration).
  return null;
}

// ── Main OrgSettings page ─────────────────────────────────────────────────────

export function OrgSettings() {
  const [org, setOrg] = useState<OrgInfo | null>(null);
  const [bindings, setBindings] = useState<RoleBinding[]>([]);
  const [environments, setEnvironments] = useState<OrgEnvironment[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;

    async function load() {
      try {
        const [orgData, bindingsData, envsData] = await Promise.all([
          fetchOrg(),
          fetchAllRoleBindings(),
          listOrgEnvironments(),
        ]);
        if (cancelled) return;
        setOrg(orgData);
        setBindings(bindingsData);
        setEnvironments(envsData.environments);
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

      {/* Secrets backend configuration — org_admin only */}
      <SecretsBackendSection />

      {/* Org-wide namespace naming patterns — org_admin only */}
      <NamespaceNamingSection />

      {/* Ingress class + cert-manager ClusterIssuer per routing tier */}
      <RoutingProfilesSection />

      {/* Org-wide environment variables */}
      <EnvConfigEditor
        title="Org-wide variables"
        description="Applied to every app in the org. Lower hierarchy levels override these."
        fetchFn={getOrgEnvConfig}
        saveFn={updateOrgEnvConfig}
      />

      {/* Shared global secrets */}
      <SecretEditor
        title="Shared global secrets"
        description="Shared secrets applied to every app in every environment (global scope). App-level global secrets override these."
        fetchFn={listSharedGlobalSecretKeys}
        upsertFn={upsertSharedGlobalSecrets}
        deleteFn={deleteSharedGlobalSecretKey}
      />

      {/* Per-environment-type variables and secrets */}
      <EnvironmentTypeEnvConfigSection environments={environments} />

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
