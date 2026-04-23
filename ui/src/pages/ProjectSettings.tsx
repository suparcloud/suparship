import { useCallback, useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";

import { listClusters } from "../lib/clusters";
import type { Cluster } from "../lib/clusters";
import {
  createProjectEnvironment,
  deleteProjectEnvironment,
  listProjectEnvironments,
  updateProjectEnvironment,
  getProjectNaming,
  updateProjectNaming,
} from "../lib/projects";
import type { ProjectEnvironment, ProjectNaming } from "../lib/projects";
import { getProjectEnvConfig, updateProjectEnvConfig } from "../lib/envconfig";
import { EnvConfigEditor } from "../components/EnvConfigEditor";
import { SecretEditor } from "../components/SecretEditor";
import {
  listProjectSecretKeys,
  upsertProjectSecrets,
  deleteProjectSecretKey,
} from "../lib/secrets";

// ── Origin badge ──────────────────────────────────────────────────────────────

function OriginBadge({ origin }: { origin?: string }) {
  if (origin === "org") {
    return (
      <span className="rounded-full bg-blue-50 px-2 py-0.5 text-xs font-medium text-blue-700">
        inherited
      </span>
    );
  }
  if (origin === "override") {
    return (
      <span className="rounded-full bg-amber-50 px-2 py-0.5 text-xs font-medium text-amber-700">
        override
      </span>
    );
  }
  if (origin === "project") {
    return (
      <span className="rounded-full bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-600">
        project-only
      </span>
    );
  }
  return null;
}

// ── Environment form modal ────────────────────────────────────────────────────

interface EnvFormProps {
  projectName: string;
  clusters: Cluster[];
  /** When provided we are editing/overriding an existing env. */
  initial?: ProjectEnvironment;
  /** True when initial is an org-inherited env with no current override. */
  isFirstOverride?: boolean;
  onClose: () => void;
  onSaved: () => void;
}

function EnvForm({
  projectName,
  clusters,
  initial,
  isFirstOverride = false,
  onClose,
  onSaved,
}: EnvFormProps) {
  const isEdit = !!initial;

  const [name, setName] = useState(initial?.name ?? "");
  const [displayName, setDisplayName] = useState(initial?.displayName ?? "");
  const [clusterRef, setClusterRef] = useState(initial?.clusterRef ?? "");
  const [baseDomain, setBaseDomain] = useState(initial?.baseDomain ?? "");
  const [namespacePattern, setNamespacePattern] = useState(
    initial?.namespacePattern ?? "",
  );
  const [order, setOrder] = useState(initial?.order ?? 0);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      if (isEdit) {
        await updateProjectEnvironment(projectName, initial!.name, {
          displayName: displayName || undefined,
          clusterRef: clusterRef || undefined,
          baseDomain: baseDomain || undefined,
          namespacePattern: namespacePattern || undefined,
          order: order || undefined,
        });
      } else {
        await createProjectEnvironment(projectName, {
          name,
          displayName: displayName || undefined,
          clusterRef: clusterRef || undefined,
          baseDomain: baseDomain || undefined,
          namespacePattern: namespacePattern || undefined,
          order: order || undefined,
        });
      }
      onSaved();
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to save");
    } finally {
      setSubmitting(false);
    }
  }

  const title = isFirstOverride
    ? `Override "${initial!.displayName || initial!.name}" for this project`
    : isEdit
      ? `Edit "${initial!.displayName || initial!.name}"`
      : "Add project environment";

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
      <div className="w-full max-w-lg rounded-xl bg-white p-6 shadow-xl">
        <h3 className="mb-1 text-base font-semibold text-gray-900">{title}</h3>
        {isFirstOverride && (
          <p className="mb-4 text-xs text-gray-500">
            Set project-specific values below. Leave a field blank to keep the
            org default.
          </p>
        )}

        <form onSubmit={handleSubmit} className="space-y-4 mt-4">
          {!isEdit && (
            <div>
              <label className="mb-1 block text-xs font-medium text-gray-700">
                Name <span className="text-red-500">*</span>
              </label>
              <input
                required
                className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
                placeholder="e.g. canary"
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </div>
          )}

          <div>
            <label className="mb-1 block text-xs font-medium text-gray-700">
              Display name
            </label>
            <input
              className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
              placeholder={isFirstOverride ? "(leave blank to keep org default)" : "e.g. Canary"}
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
            />
          </div>

          <div>
            <label className="mb-1 block text-xs font-medium text-gray-700">
              Cluster
            </label>
            {clusters.length > 0 ? (
              <select
                className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
                value={clusterRef}
                onChange={(e) => setClusterRef(e.target.value)}
              >
                <option value="">
                  {isFirstOverride ? "(keep org default)" : "— none —"}
                </option>
                {clusters.map((c) => (
                  <option key={c.name} value={c.name}>
                    {c.displayName || c.name}
                  </option>
                ))}
              </select>
            ) : (
              <input
                className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
                placeholder={isFirstOverride ? "(leave blank to keep org default)" : "cluster-name"}
                value={clusterRef}
                onChange={(e) => setClusterRef(e.target.value)}
              />
            )}
          </div>

          <div>
            <label className="mb-1 block text-xs font-medium text-gray-700">
              Base domain
            </label>
            <input
              className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
              placeholder={isFirstOverride ? "(leave blank to keep org default)" : "staging.example.com"}
              value={baseDomain}
              onChange={(e) => setBaseDomain(e.target.value)}
            />
          </div>

          <div>
            <label className="mb-1 block text-xs font-medium text-gray-700">
              Namespace pattern
            </label>
            <input
              className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
              placeholder={isFirstOverride ? "(leave blank to keep org default)" : "{app}  or  {app}-{env}"}
              value={namespacePattern}
              onChange={(e) => setNamespacePattern(e.target.value)}
            />
            <p className="mt-1 text-xs text-gray-400">
              Tokens: {"{app}"}, {"{env}"}, {"{project}"}
            </p>
          </div>

          <div>
            <label className="mb-1 block text-xs font-medium text-gray-700">
              Order
            </label>
            <input
              type="number"
              className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
              value={order}
              onChange={(e) => setOrder(Number(e.target.value))}
            />
          </div>

          {error && (
            <p className="rounded bg-red-50 px-3 py-2 text-xs text-red-700">
              {error}
            </p>
          )}

          <div className="flex justify-end gap-2 pt-2">
            <button
              type="button"
              onClick={onClose}
              disabled={submitting}
              className="rounded-lg border border-gray-300 px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50"
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={submitting}
              className="rounded-lg bg-gray-900 px-4 py-2 text-sm font-medium text-white hover:bg-gray-700 disabled:opacity-50"
            >
              {submitting
                ? "Saving…"
                : isFirstOverride
                  ? "Save override"
                  : isEdit
                    ? "Save changes"
                    : "Add environment"}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

// ── Project namespace naming section ─────────────────────────────────────────

const PROJECT_NS_PRESETS = [
  { label: "{project}-{env}", value: "{project}-{env}" },
  { label: "{project}", value: "{project}" },
  { label: "{org}-{project}-{env}", value: "{org}-{project}-{env}" },
];

function ProjectNamespaceSection({ projectName }: { projectName: string }) {
  const [naming, setNaming] = useState<ProjectNaming>({});
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    let cancelled = false;
    getProjectNaming(projectName)
      .then((n) => { if (!cancelled) setNaming(n); })
      .catch(() => { if (!cancelled) setNaming({}); })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [projectName]);

  async function handleSave() {
    setSaving(true);
    setSaveError(null);
    setSaved(false);
    try {
      const updated = await updateProjectNaming(projectName, naming);
      setNaming(updated);
      setSaved(true);
    } catch (err) {
      setSaveError(err instanceof Error ? err.message : "Failed to save");
    } finally {
      setSaving(false);
    }
  }

  function renderPreview(pattern: string): string {
    return pattern
      .replace("{org}", "myorg")
      .replace("{project}", projectName || "myproject")
      .replace("{env}", "staging");
  }

  return (
    <div className="rounded-lg border border-gray-200 bg-white">
      <div className="border-b border-gray-100 px-6 py-4">
        <h2 className="text-sm font-medium text-gray-900">
          Project Namespace Pattern
        </h2>
        <p className="mt-0.5 text-xs text-gray-500">
          Override the org-level default for this project's Kubernetes
          namespace. Apps with{" "}
          <code className="font-mono text-xs">namespaceScope: project</code>{" "}
          will use this namespace. Leave blank to inherit from org settings.
        </p>
      </div>
      <div className="px-6 py-4">
        {loading ? (
          <div className="h-16 animate-pulse rounded bg-gray-100" />
        ) : error ? (
          <p className="text-sm text-red-600">{error}</p>
        ) : (
          <div className="space-y-3">
            <div className="flex flex-wrap gap-1.5">
              {PROJECT_NS_PRESETS.map((p) => (
                <button
                  key={p.value}
                  type="button"
                  onClick={() => setNaming({ namespacePattern: p.value })}
                  className={`rounded px-2 py-0.5 text-xs font-mono transition-colors ${
                    naming.namespacePattern === p.value
                      ? "bg-indigo-100 text-indigo-800"
                      : "border border-gray-200 text-gray-600 hover:bg-gray-50"
                  }`}
                >
                  {p.label}
                </button>
              ))}
              <button
                type="button"
                onClick={() => setNaming({ namespacePattern: "" })}
                className="rounded px-2 py-0.5 text-xs text-gray-400 hover:text-gray-600 border border-dashed border-gray-200"
              >
                Clear (inherit from org)
              </button>
            </div>
            <input
              className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
              placeholder="e.g. {project}-{env}  (blank = inherit from org)"
              value={naming.namespacePattern ?? ""}
              onChange={(e) =>
                setNaming({ namespacePattern: e.target.value || undefined })
              }
            />
            <p className="text-xs text-gray-400">
              Tokens: <code className="font-mono">{"{org}"}</code>,{" "}
              <code className="font-mono">{"{project}"}</code>,{" "}
              <code className="font-mono">{"{env}"}</code>
            </p>
            {naming.namespacePattern && (
              <p className="text-xs text-gray-400">
                Preview:{" "}
                <code className="font-mono text-gray-700">
                  {renderPreview(naming.namespacePattern)}
                </code>
              </p>
            )}
            {saveError && (
              <p className="rounded bg-red-50 px-3 py-2 text-xs text-red-700">
                {saveError}
              </p>
            )}
            {saved && (
              <p className="rounded bg-green-50 px-3 py-2 text-xs text-green-700">
                Project namespace pattern saved.
              </p>
            )}
            <div className="flex justify-end">
              <button
                onClick={handleSave}
                disabled={saving}
                className="rounded-lg bg-gray-900 px-4 py-2 text-sm font-medium text-white hover:bg-gray-700 disabled:opacity-50"
              >
                {saving ? "Saving…" : "Save namespace pattern"}
              </button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

// ── ProjectSettings page ──────────────────────────────────────────────────────

export function ProjectSettings() {
  const { project = "" } = useParams();
  const [envs, setEnvs] = useState<ProjectEnvironment[]>([]);
  const [clusters, setClusters] = useState<Cluster[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [showAdd, setShowAdd] = useState(false);
  const [editEnv, setEditEnv] = useState<ProjectEnvironment | null>(null);
  const [isFirstOverride, setIsFirstOverride] = useState(false);

  const fetchProjectEnvConfig = useCallback(
    () => getProjectEnvConfig(project),
    [project],
  );
  const saveProjectEnvConfig = useCallback(
    (cfg: Parameters<typeof updateProjectEnvConfig>[1]) =>
      updateProjectEnvConfig(project, cfg),
    [project],
  );

  const reload = useCallback(async () => {
    try {
      const [envsData, clustersData] = await Promise.all([
        listProjectEnvironments(project),
        listClusters().catch(() => [] as Cluster[]),
      ]);
      setEnvs(envsData);
      setClusters(clustersData);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load");
    } finally {
      setLoading(false);
    }
  }, [project]);

  useEffect(() => {
    reload();
  }, [reload]);

  async function handleDelete(env: ProjectEnvironment) {
    const isInherited = env.origin === "org";
    const isOverride = env.origin === "override";
    const isProjectOnly = env.origin === "project";

    let confirmMsg: string;
    if (isInherited) {
      // Shouldn't normally reach here since inherited has no delete action,
      // but guard defensively.
      return;
    } else if (isOverride) {
      confirmMsg = `Reset "${env.displayName || env.name}" to org defaults?\n\nYour project-level overrides will be removed and the org settings will apply again.`;
    } else if (isProjectOnly) {
      confirmMsg = `Remove environment "${env.displayName || env.name}"?\n\nThis environment is project-specific and will be fully deleted.`;
    } else {
      confirmMsg = `Remove environment "${env.displayName || env.name}"?`;
    }

    if (!confirm(confirmMsg)) return;

    try {
      await deleteProjectEnvironment(project, env.name);
      await reload();
    } catch (err) {
      alert(err instanceof Error ? err.message : "Failed to delete");
    }
  }

  function openEdit(env: ProjectEnvironment) {
    setIsFirstOverride(env.origin === "org");
    setEditEnv(env);
  }

  if (loading) {
    return (
      <div className="mx-auto max-w-3xl space-y-4 p-6">
        <div className="h-8 w-48 animate-pulse rounded bg-gray-100" />
        <div className="h-64 animate-pulse rounded-lg bg-gray-50" />
      </div>
    );
  }

  if (error) {
    return (
      <div className="mx-auto max-w-3xl p-6">
        <div className="rounded-lg border border-red-200 bg-red-50 p-4">
          <p className="text-sm text-red-700">{error}</p>
        </div>
      </div>
    );
  }

  return (
    <div className="mx-auto max-w-3xl space-y-8 p-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <div className="flex items-center gap-2 text-sm text-gray-500">
            <Link to={`/projects/${project}`} className="hover:text-gray-700">
              {project}
            </Link>
            <span>/</span>
            <span>Settings</span>
          </div>
          <h1 className="mt-1 text-2xl font-semibold text-gray-900">
            Project settings
          </h1>
        </div>
        <Link
          to={`/projects/${project}`}
          className="rounded-lg border border-gray-300 px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50"
        >
          Back to project
        </Link>
      </div>

      {/* Environments */}
      <div className="rounded-lg border border-gray-200 bg-white">
        <div className="flex items-center justify-between border-b border-gray-100 px-6 py-4">
          <div>
            <h2 className="text-sm font-medium text-gray-900">Environments</h2>
            <p className="mt-0.5 text-xs text-gray-500">
              Inherited from org defaults · Project-level overrides and additions shown below.
            </p>
          </div>
          <button
            onClick={() => setShowAdd(true)}
            className="rounded-lg bg-gray-900 px-3 py-1.5 text-xs font-medium text-white hover:bg-gray-700"
          >
            Add environment
          </button>
        </div>

        {envs.length === 0 ? (
          <div className="px-6 py-10 text-center">
            <p className="text-sm text-gray-400">No environments configured.</p>
            <p className="mt-1 text-xs text-gray-400">
              Add org-level environments in{" "}
              <Link to="/settings/org" className="text-indigo-600 hover:underline">
                Organization settings
              </Link>
              .
            </p>
          </div>
        ) : (
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-100 text-left text-xs font-medium uppercase tracking-wider text-gray-500">
                <th className="px-6 py-3">Environment</th>
                <th className="px-6 py-3">Cluster</th>
                <th className="px-6 py-3">Base domain</th>
                <th className="px-6 py-3">Namespace</th>
                <th className="px-6 py-3" />
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {envs.map((env) => (
                <tr
                  key={env.name}
                  className={
                    env.origin === "org"
                      ? "bg-gray-50/60 hover:bg-gray-50"
                      : "hover:bg-gray-50"
                  }
                >
                  <td className="px-6 py-3">
                    <div className="flex items-center gap-2">
                      <span className="text-sm font-medium text-gray-900">
                        {env.displayName || env.name}
                      </span>
                      {env.displayName && (
                        <span className="font-mono text-xs text-gray-400">
                          {env.name}
                        </span>
                      )}
                      <OriginBadge origin={env.origin} />
                    </div>
                  </td>
                  <td className="px-6 py-3 font-mono text-xs text-gray-600">
                    {env.clusterRef || <span className="text-gray-300">—</span>}
                  </td>
                  <td className="px-6 py-3 font-mono text-xs text-gray-600">
                    {env.baseDomain || <span className="text-gray-300">—</span>}
                  </td>
                  <td className="px-6 py-3 font-mono text-xs text-gray-500">
                    {env.namespace}
                  </td>
                  <td className="px-6 py-3 text-right text-xs">
                    {env.origin === "org" ? (
                      /* Inherited: offer to create a project override */
                      <button
                        onClick={() => openEdit(env)}
                        className="text-indigo-600 hover:text-indigo-800"
                      >
                        Override
                      </button>
                    ) : env.origin === "override" ? (
                      /* Override: edit or reset to org defaults */
                      <span className="flex items-center justify-end gap-3">
                        <button
                          onClick={() => openEdit(env)}
                          className="text-indigo-600 hover:text-indigo-800"
                        >
                          Edit
                        </button>
                        <button
                          onClick={() => handleDelete(env)}
                          className="text-amber-600 hover:text-amber-800"
                        >
                          Reset
                        </button>
                      </span>
                    ) : (
                      /* Project-only: edit or remove */
                      <span className="flex items-center justify-end gap-3">
                        <button
                          onClick={() => openEdit(env)}
                          className="text-indigo-600 hover:text-indigo-800"
                        >
                          Edit
                        </button>
                        <button
                          onClick={() => handleDelete(env)}
                          className="text-red-500 hover:text-red-700"
                        >
                          Remove
                        </button>
                      </span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {/* Legend */}
      <div className="flex items-center gap-4 text-xs text-gray-500">
        <span className="flex items-center gap-1.5">
          <span className="inline-block rounded-full bg-blue-50 px-2 py-0.5 font-medium text-blue-700">
            inherited
          </span>
          from org defaults
        </span>
        <span className="flex items-center gap-1.5">
          <span className="inline-block rounded-full bg-amber-50 px-2 py-0.5 font-medium text-amber-700">
            override
          </span>
          project-level fields applied on top
        </span>
        <span className="flex items-center gap-1.5">
          <span className="inline-block rounded-full bg-gray-100 px-2 py-0.5 font-medium text-gray-600">
            project-only
          </span>
          not in org defaults
        </span>
      </div>

      {/* Project namespace naming pattern */}
      <ProjectNamespaceSection projectName={project} />

      {/* Project-level environment variables */}
      <EnvConfigEditor
        title="Project-level variables"
        description="Applied to every app in this project. Overrides org defaults; overridden by app-level values."
        fetchFn={fetchProjectEnvConfig}
        saveFn={saveProjectEnvConfig}
      />
      <SecretEditor
        title="Project-level secrets"
        description="Secrets shared across every app in this project. Overrides org defaults; overridden by app-level secrets."
        fetchFn={() => listProjectSecretKeys(project)}
        upsertFn={(entries) => upsertProjectSecrets(project, entries)}
        deleteFn={(key) => deleteProjectSecretKey(project, key)}
      />

      {/* Modals */}
      {showAdd && (
        <EnvForm
          projectName={project}
          clusters={clusters}
          onClose={() => setShowAdd(false)}
          onSaved={reload}
        />
      )}
      {editEnv && (
        <EnvForm
          projectName={project}
          clusters={clusters}
          initial={editEnv}
          isFirstOverride={isFirstOverride}
          onClose={() => {
            setEditEnv(null);
            setIsFirstOverride(false);
          }}
          onSaved={reload}
        />
      )}
    </div>
  );
}
