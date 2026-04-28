import { useCallback, useEffect, useState } from "react";

import {
  fetchOrg,
  fetchAllRoleBindings,
  listOrgEnvironments,
  createOrgEnvironment,
  updateOrgEnvironment,
  deleteOrgEnvironment,
  getOrgNaming,
  updateOrgNaming,
} from "../lib/settings";
import type { OrgNaming } from "../lib/settings";
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
  addBinding,
  removeBinding,
  setPlatformVault,
  listOrgSecretKeys,
  upsertOrgSecrets,
  deleteOrgSecretKey,
  listEnvTypeSecretKeys,
  upsertEnvTypeSecrets,
  deleteEnvTypeSecretKey,
  migrateToOnePassword,
} from "../lib/secrets";
import type {
  MigrateToOnePasswordResponse,
  SecretBackendConfig,
  VaultInfo,
} from "../lib/secrets";
import { fetchProjects } from "../lib/settings";
import { listClusters } from "../lib/clusters";
import type { Cluster } from "../lib/clusters";
import type { Project } from "../types";
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

// ── Environment-type env config section ───────────────────────────────────────

function EnvironmentTypeEnvConfigSection({
  environments,
}: {
  environments: OrgEnvironment[];
}) {
  const [selectedEnvType, setSelectedEnvType] = useState<string>("");

  // Initialise selector once environments load.
  useEffect(() => {
    if (environments.length > 0 && !selectedEnvType) {
      setSelectedEnvType(environments[0].name);
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
            title={`Secrets for "${selectedEnvType}" environments`}
            description="Secrets applied to every app in this environment type."
            fetchFn={() => listEnvTypeSecretKeys(selectedEnvType)}
            upsertFn={(entries) => upsertEnvTypeSecrets(selectedEnvType, entries)}
            deleteFn={(key) => deleteEnvTypeSecretKey(selectedEnvType, key)}
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
      .replace("{env}", "staging");
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
  const [bindToken, setBindToken] = useState("");
  const [bindConnectEndpoint, setBindConnectEndpoint] = useState("");
  const [bindBusy, setBindBusy] = useState(false);

  // Remove binding state
  const [removingEnv, setRemovingEnv] = useState<string | null>(null);

  // Setup guide toggle
  const [showGuide, setShowGuide] = useState(false);

  // Org environments (for binding dropdown)
  const [orgEnvs, setOrgEnvs] = useState<OrgEnvironment[]>([]);

  useEffect(() => {
    let cancelled = false;
    Promise.all([getSecretsBackend(), listOrgEnvironments()])
      .then(([cfg, envsResp]) => {
        if (!cancelled) {
          setConfig(cfg);
          setOrgEnvs(envsResp.environments || []);
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
      const updated: Partial<SecretBackendConfig> = { type: value };
      if (value === "k8s") {
        updated.onePassword = undefined;
      }
      const result = await updateSecretsBackend(updated);
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
    if (!bindEnv || !bindVaultId || !bindToken.trim()) return;
    setBindBusy(true);
    setError(null);
    try {
      const vault = vaults.find((v) => v.id === bindVaultId);
      const res = await addBinding(
        bindEnv,
        bindVaultId,
        bindToken.trim(),
        vault?.title,
        bindConnectEndpoint.trim() || undefined,
      );
      if (res.error) {
        setError(res.error);
      } else {
        const updated = await getSecretsBackend();
        setConfig(updated);
        setShowAddBinding(false);
        setBindEnv("");
        setBindVaultId("");
        setBindToken("");
        setBindConnectEndpoint("");
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : "Add binding failed");
    } finally {
      setBindBusy(false);
    }
  }

  async function handleRemoveBinding(env: string) {
    if (!confirm(`Remove binding for ${env}? The vault itself will be kept in 1Password.`)) return;
    setRemovingEnv(env);
    setError(null);
    try {
      await removeBinding(env);
      const updated = await getSecretsBackend();
      setConfig(updated);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Remove failed");
    } finally {
      setRemovingEnv(null);
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
                        <strong>Two vault tiers:</strong> a{" "}
                        <strong>platform-shared vault</strong> for org and
                        project secrets (read-only from every cluster), plus
                        one <strong>env vault</strong> per environment for
                        env-type, app, app-env, and cluster secrets. 1Password
                        Service Accounts cannot create vaults or Connect
                        tokens — you create both manually in the 1Password
                        console; suparShip handles the cluster-side automation
                        (sealing tokens, generating ClusterSecretStores,
                        publishing to GitOps).
                      </p>
                      <ol className="space-y-2 text-xs text-blue-900">
                        <li>
                          <strong>
                            1. Create the platform-shared vault and per-env
                            vaults
                          </strong>{" "}
                          in the 1Password web console (e.g.{" "}
                          <code>company-shared</code>,{" "}
                          <code>staging-apps</code>,{" "}
                          <code>prod-apps</code>).
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
                          <strong>4. Pick the platform-shared vault</strong>{" "}
                          from the dropdown that appears after the SA token
                          is saved.
                        </li>
                        <li>
                          <strong>5. Set up a Connect Server</strong> in
                          1Password and grant it access to <strong>all</strong>{" "}
                          suparShip vaults — every env vault <em>and</em> the
                          platform vault.
                        </li>
                        <li>
                          <strong>6. Deploy Connect</strong> to your tooling
                          cluster (Helm chart or Docker).
                        </li>
                        <li>
                          <strong>7. Issue per-env Connect tokens</strong> in
                          the 1Password console. Each token must read{" "}
                          <strong>both vaults</strong>: its env vault{" "}
                          <em>and</em> the platform vault. Without platform
                          access, ESO can't resolve org/project items at sync
                          time.
                        </li>
                        <li>
                          <strong>8. Add bindings</strong> below — select env
                          vault, paste the env's Connect token, for each
                          environment. suparShip seals the token and publishes
                          the SealedSecret + ClusterSecretStore to GitOps.
                        </li>
                        <li>
                          <strong>9. (Optional) Migrate existing K8s Secrets</strong>{" "}
                          using the &ldquo;Migrate K8s Secrets to
                          1Password&rdquo; panel below. Idempotent; safe to
                          re-run.
                        </li>
                      </ol>
                      <p className="text-xs text-blue-900">
                        Need more detail? See{" "}
                        <code>docs/secrets.md</code> for architecture diagrams,
                        troubleshooting, and the full RBAC matrix.
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
                    manage — the platform-shared vault and each env vault.
                    1Password Service Accounts cannot create vaults, so make
                    sure these vaults already exist before pasting.
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

                {/* Platform-shared vault picker — operator selects the vault
                    they created manually in the 1Password console. 1Password
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
                        setShowAddBinding(!showAddBinding);
                        if (!showAddBinding && vaults.length === 0) {
                          loadVaults();
                        }
                      }}
                      className="text-xs font-medium text-indigo-600 hover:text-indigo-800"
                    >
                      {showAddBinding ? "Cancel" : "+ Add Binding"}
                    </button>
                  </div>

                  {/* Add binding form */}
                  {showAddBinding && (
                    <div className="mb-4 space-y-3 rounded-lg border border-gray-200 bg-gray-50/50 p-4">
                      <div>
                        <label className="mb-1 block text-xs font-medium text-gray-700">
                          Environment
                        </label>
                        {(() => {
                          const boundEnvs = new Set(
                            (config?.onePassword?.bindings || []).map((b) => b.env),
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
                      <div>
                        <label className="mb-1 block text-xs font-medium text-gray-700">
                          Connect Token
                        </label>
                        <input
                          type="password"
                          className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono"
                          placeholder="Paste the per-env Connect token…"
                          value={bindToken}
                          onChange={(e) => setBindToken(e.target.value)}
                        />
                      </div>
                      <div>
                        <label className="mb-1 block text-xs font-medium text-gray-700">
                          Connect Server URL{" "}
                          <span className="font-normal text-gray-400">(optional)</span>
                        </label>
                        <input
                          type="text"
                          className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono"
                          placeholder="http://onepassword-connect.1password.svc.cluster.local:8080"
                          value={bindConnectEndpoint}
                          onChange={(e) => setBindConnectEndpoint(e.target.value)}
                        />
                        <p className="mt-1 text-xs text-gray-400">
                          Override the 1Password Connect server URL used in the{" "}
                          <code className="font-mono">ClusterSecretStore</code>. Leave
                          blank to use the org default or the built-in default.
                        </p>
                      </div>
                      <button
                        onClick={handleAddBinding}
                        disabled={
                          bindBusy ||
                          !bindEnv ||
                          !bindVaultId ||
                          !bindToken.trim()
                        }
                        className="rounded-lg bg-gray-900 px-4 py-2 text-sm font-medium text-white hover:bg-gray-700 disabled:opacity-50"
                      >
                        {bindBusy ? "Saving…" : "Add Binding"}
                      </button>
                    </div>
                  )}

                  {/* Existing bindings */}
                  {(config.onePassword?.bindings || []).length === 0 ? (
                    <p className="text-xs text-gray-400">
                      No environment bindings yet. Click &ldquo;+ Add
                      Binding&rdquo; to connect an environment to a 1Password
                      vault.
                    </p>
                  ) : (
                    <table className="w-full text-sm">
                      <thead>
                        <tr className="border-b border-gray-100 text-left text-xs font-medium uppercase tracking-wider text-gray-500">
                          <th className="py-2">Env</th>
                          <th className="py-2">Vault</th>
                          <th className="py-2">ClusterSecretStore</th>
                          <th className="py-2">Status</th>
                          <th className="py-2"></th>
                        </tr>
                      </thead>
                      <tbody className="divide-y divide-gray-50">
                        {(config.onePassword?.bindings || []).map((b) => (
                          <tr key={b.env}>
                            <td className="py-2 font-mono text-xs">{b.env}</td>
                            <td className="py-2 font-mono text-xs">
                              {b.vaultName || b.vaultId}
                            </td>
                            <td className="py-2 font-mono text-xs text-gray-500">
                              {b.clusterSecretStoreName || "—"}
                            </td>
                            <td className="py-2 text-xs">
                              {b.provisioned ? (
                                <span className="inline-flex items-center gap-1 rounded bg-green-50 px-2 py-0.5 text-green-700">
                                  bound
                                </span>
                              ) : (
                                <span className="inline-flex items-center gap-1 rounded bg-amber-50 px-2 py-0.5 text-amber-700">
                                  pending
                                </span>
                              )}
                              {b.connectEndpoint && (
                                <span
                                  className="ml-2 font-mono text-xs text-gray-400"
                                  title="Per-binding Connect endpoint override"
                                >
                                  {b.connectEndpoint}
                                </span>
                              )}
                              {b.lastError && (
                                <span className="ml-2 text-xs text-red-600">
                                  {b.lastError}
                                </span>
                              )}
                            </td>
                            <td className="py-2 text-right space-x-2">
                              <button
                                onClick={() => handleRemoveBinding(b.env)}
                                disabled={removingEnv === b.env}
                                className="text-xs text-red-600 hover:text-red-800 disabled:opacity-50"
                              >
                                {removingEnv === b.env ? "Removing…" : "Remove"}
                              </button>
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  )}
                </div>

                {/* Migration panel — visible once the platform vault is ready. */}
                <MigrationPanel config={config} />
              </div>
            )}
          </div>
        ) : null}
      </div>
    </div>
  );
}

// ── Migration panel ──────────────────────────────────────────────────────────

// ── Platform vault picker ────────────────────────────────────────────────────
//
// 1Password Service Accounts cannot create new vaults. The operator creates
// the platform-shared vault by hand in the 1Password console; suparShip just
// needs to know which vault it is. This component lists every vault the SA
// token can see, lets the operator pick one, and POSTs the choice to
// /org/secret-backend/platform-vault.
//
// Note: the upper-level writer in the suparShip server is built once at
// startup using the persisted PlatformVaultID. After picking, a server
// restart is required before org / project secret writes start landing in
// the chosen vault — surfaced inline.

function PlatformVaultPicker({
  config,
  onChanged,
}: {
  config: SecretBackendConfig;
  onChanged: () => Promise<void>;
}) {
  const currentID = config.onePassword?.platformVaultId ?? "";
  const currentName = config.onePassword?.platformVaultName ?? "";
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
      const res = await setPlatformVault(selectedID, picked?.title);
      setSavedMsg(
        `Saved. Restart the suparShip server so the new platform vault (${res.vaultName}) becomes the source of truth for org / project secrets.`,
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
        Platform-shared vault
      </label>
      <p className="text-xs text-gray-500">
        Pick the 1Password vault you created (manually) for org and project
        secrets — read-only from every cluster's ESO. Per-env vaults are
        configured separately in the bindings table below.
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
            Not set — org / project secret writes will fail until a vault is
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
          <>
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
              onClick={handleSave}
              disabled={saving || !selectedID || selectedID === currentID}
              className="rounded-lg bg-gray-900 px-4 py-2 text-sm font-medium text-white hover:bg-gray-700 disabled:opacity-50"
            >
              {saving ? "Saving…" : "Save"}
            </button>
            <button
              type="button"
              onClick={handleLoadVaults}
              disabled={loading}
              title="Refresh the vault list"
              className="rounded-lg border border-gray-300 px-3 py-2 text-xs text-gray-600 hover:bg-gray-50 disabled:opacity-50"
            >
              ⟳
            </button>
          </>
        )}
      </div>
      {error && <p className="text-xs text-red-600">{error}</p>}
      {savedMsg && <p className="text-xs text-green-700">{savedMsg}</p>}
    </div>
  );
}

function MigrationPanel({ config }: { config: SecretBackendConfig }) {
  const platformVaultID = config.onePassword?.platformVaultId ?? "";
  const bindings = config.onePassword?.bindings ?? [];

  const [projects, setProjects] = useState<Project[]>([]);
  const [clusters, setClusters] = useState<Cluster[]>([]);
  const [selectedEnvs, setSelectedEnvs] = useState<Record<string, boolean>>({});
  const [selectedProjects, setSelectedProjects] = useState<Record<string, boolean>>({});
  const [selectedClusters, setSelectedClusters] = useState<Record<string, boolean>>({});
  const [submitting, setSubmitting] = useState(false);
  const [result, setResult] = useState<MigrateToOnePasswordResponse | null>(null);
  const [error, setError] = useState<string | null>(null);

  // Load inventory once.
  useEffect(() => {
    let cancelled = false;
    async function load() {
      try {
        const [{ projects: ps }, cs] = await Promise.all([
          fetchProjects(),
          listClusters(),
        ]);
        if (cancelled) return;
        setProjects(ps);
        setClusters(cs);
        // Default-select every entry — operators usually want to migrate the
        // full inventory once. Individual rows can still be opted out.
        const envInit: Record<string, boolean> = {};
        for (const b of bindings) envInit[b.env] = true;
        setSelectedEnvs(envInit);
        const projInit: Record<string, boolean> = {};
        for (const p of ps) projInit[p.name] = true;
        setSelectedProjects(projInit);
        const clusterInit: Record<string, boolean> = {};
        for (const c of cs) clusterInit[c.name] = true;
        setSelectedClusters(clusterInit);
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : "Failed to load inventory");
      }
    }
    load();
    return () => {
      cancelled = true;
    };
    // bindings come from `config` which is stable for the panel's lifetime;
    // no need to re-run on every keystroke elsewhere on the page.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  if (!platformVaultID) {
    return (
      <div className="mt-6 rounded-lg border border-amber-200 bg-amber-50 p-4 text-sm text-amber-800">
        Migration is unavailable until the platform vault is provisioned —
        re-paste the SA token in the section above to create it.
      </div>
    );
  }

  const pickedKeys = (m: Record<string, boolean>) =>
    Object.entries(m).filter(([, v]) => v).map(([k]) => k);

  async function handleMigrate() {
    setSubmitting(true);
    setError(null);
    setResult(null);
    try {
      const res = await migrateToOnePassword({
        envTypes: pickedKeys(selectedEnvs),
        projects: pickedKeys(selectedProjects),
        clusters: pickedKeys(selectedClusters),
      });
      setResult(res);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Migration failed");
    } finally {
      setSubmitting(false);
    }
  }

  function toggle(setter: React.Dispatch<React.SetStateAction<Record<string, boolean>>>, key: string) {
    setter((prev) => ({ ...prev, [key]: !prev[key] }));
  }

  return (
    <div className="mt-6 space-y-4 rounded-lg border border-gray-200 bg-white p-5">
      <div>
        <h3 className="text-sm font-semibold text-gray-900">
          Migrate K8s Secrets to 1Password
        </h3>
        <p className="mt-1 text-xs text-gray-500">
          One-shot copy of org / env-type / project / cluster Secrets currently
          stored in <code className="font-mono">suparship-system</code> into
          your 1Password vaults. App and app-env secrets are not migrated —
          rotate those manually after the switch. Idempotent — re-running picks
          up new keys without clobbering values already entered directly into
          the vault.
        </p>
      </div>

      <div className="grid grid-cols-1 gap-4 md:grid-cols-3">
        <CheckboxList
          title="Environments"
          empty="No env bindings configured."
          items={bindings.map((b) => ({ key: b.env, label: b.env }))}
          selected={selectedEnvs}
          onToggle={(k) => toggle(setSelectedEnvs, k)}
        />
        <CheckboxList
          title="Projects"
          empty="No projects yet."
          items={projects.map((p) => ({ key: p.name, label: p.displayName || p.name }))}
          selected={selectedProjects}
          onToggle={(k) => toggle(setSelectedProjects, k)}
        />
        <CheckboxList
          title="Clusters"
          empty="No clusters registered."
          items={clusters.map((c) => ({ key: c.name, label: c.displayName || c.name }))}
          selected={selectedClusters}
          onToggle={(k) => toggle(setSelectedClusters, k)}
        />
      </div>

      <div className="flex items-center gap-3">
        <button
          onClick={handleMigrate}
          disabled={submitting}
          className="rounded-md bg-indigo-600 px-4 py-2 text-sm font-medium text-white hover:bg-indigo-700 disabled:opacity-50"
        >
          {submitting ? "Migrating…" : "Migrate to 1Password"}
        </button>
        <p className="text-xs text-gray-400">
          Org-scope keys are always migrated.
        </p>
      </div>

      {error && (
        <div className="rounded border border-red-200 bg-red-50 p-3 text-xs text-red-700">
          {error}
        </div>
      )}

      {result && (
        <div className="rounded border border-green-200 bg-green-50 p-3 text-xs text-green-800">
          <p className="font-medium">Migration complete.</p>
          <ul className="mt-1 list-disc space-y-0.5 pl-5">
            <li>Org keys: {result.orgKeys}</li>
            <li>
              Env-types:{" "}
              {summariseCounts(result.envTypeKeys) || "none"}
            </li>
            <li>
              Projects:{" "}
              {summariseCounts(result.projectKeys) || "none"}
            </li>
            <li>
              Clusters:{" "}
              {summariseCounts(result.clusterKeys) || "none"}
            </li>
          </ul>
        </div>
      )}
    </div>
  );
}

interface CheckboxListProps {
  title: string;
  empty: string;
  items: Array<{ key: string; label: string }>;
  selected: Record<string, boolean>;
  onToggle: (key: string) => void;
}

function CheckboxList({ title, empty, items, selected, onToggle }: CheckboxListProps) {
  return (
    <div>
      <p className="mb-2 text-xs font-medium uppercase tracking-wider text-gray-500">
        {title}
      </p>
      {items.length === 0 ? (
        <p className="text-xs text-gray-400">{empty}</p>
      ) : (
        <ul className="space-y-1">
          {items.map((it) => (
            <li key={it.key} className="flex items-center gap-2">
              <input
                id={`migrate-${title}-${it.key}`}
                type="checkbox"
                checked={!!selected[it.key]}
                onChange={() => onToggle(it.key)}
                className="h-4 w-4 rounded border-gray-300 text-indigo-600 focus:ring-indigo-500"
              />
              <label
                htmlFor={`migrate-${title}-${it.key}`}
                className="text-sm text-gray-700"
              >
                {it.label}
              </label>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function summariseCounts(counts: Record<string, number>): string {
  const entries = Object.entries(counts).filter(([, n]) => n > 0);
  if (entries.length === 0) return "";
  return entries.map(([k, n]) => `${k} (${n})`).join(", ");
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

      {/* Org-wide environment variables */}
      <EnvConfigEditor
        title="Org-wide variables"
        description="Applied to every app in the org. Lower hierarchy levels override these."
        fetchFn={getOrgEnvConfig}
        saveFn={updateOrgEnvConfig}
      />

      {/* Org-wide secrets */}
      <SecretEditor
        title="Org-wide secrets"
        description="Secrets shared across every app in the org. Lower hierarchy levels override these."
        fetchFn={listOrgSecretKeys}
        upsertFn={upsertOrgSecrets}
        deleteFn={deleteOrgSecretKey}
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
