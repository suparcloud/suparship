import { useCallback, useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";

import { listClusters } from "../lib/clusters";
import type { Cluster } from "../lib/clusters";
import {
  createProjectEnvironment,
  deleteProjectEnvironment,
  listProjectEnvironments,
  updateProjectEnvironment,
} from "../lib/projects";
import type { ProjectEnvironment } from "../lib/projects";

// ── Environment form modal ────────────────────────────────────────────────────

interface EnvFormProps {
  projectName: string;
  clusters: Cluster[];
  initial?: ProjectEnvironment;
  onClose: () => void;
  onSaved: () => void;
}

function EnvForm({
  projectName,
  clusters,
  initial,
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
          displayName,
          clusterRef: clusterRef || undefined,
          baseDomain: baseDomain || undefined,
          namespacePattern: namespacePattern || undefined,
          order: order || undefined,
        });
      } else {
        await createProjectEnvironment(projectName, {
          name,
          displayName,
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

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/30 backdrop-blur-sm">
      <div className="w-full max-w-lg rounded-xl bg-white p-8 shadow-xl">
        <h2 className="text-lg font-semibold text-gray-900">
          {isEdit ? `Edit "${initial!.name}"` : "Add environment"}
        </h2>
        <p className="mt-1 text-sm text-gray-500">
          Environments define where apps are deployed (e.g. staging, prod).
        </p>

        <form onSubmit={handleSubmit} className="mt-6 space-y-4">
          {!isEdit && (
            <div>
              <label className="block text-sm font-medium text-gray-700">
                Name <span className="text-red-500">*</span>
              </label>
              <input
                type="text"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="staging"
                required
                className="mt-1 block w-full rounded-md border border-gray-300 px-3 py-2 text-sm shadow-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
              />
              <p className="mt-1 text-xs text-gray-400">
                Lowercase letters, digits, and hyphens. Cannot be changed.
              </p>
            </div>
          )}

          <div>
            <label className="block text-sm font-medium text-gray-700">
              Display name
            </label>
            <input
              type="text"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              placeholder="Staging"
              className="mt-1 block w-full rounded-md border border-gray-300 px-3 py-2 text-sm shadow-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
            />
          </div>

          <div>
            <label className="block text-sm font-medium text-gray-700">
              Cluster
            </label>
            <select
              value={clusterRef}
              onChange={(e) => setClusterRef(e.target.value)}
              className="mt-1 block w-full rounded-md border border-gray-300 px-3 py-2 text-sm shadow-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
            >
              <option value="">— none (unbound) —</option>
              {clusters.map((c) => (
                <option key={c.name} value={c.name}>
                  {c.displayName || c.name}
                </option>
              ))}
            </select>
            <p className="mt-1 text-xs text-gray-400">
              The registered cluster this environment deploys to via ArgoCD.
            </p>
          </div>

          <div>
            <label className="block text-sm font-medium text-gray-700">
              Base domain
            </label>
            <input
              type="text"
              value={baseDomain}
              onChange={(e) => setBaseDomain(e.target.value)}
              placeholder="staging.example.com"
              className="mt-1 block w-full rounded-md border border-gray-300 px-3 py-2 text-sm shadow-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
            />
            <p className="mt-1 text-xs text-gray-400">
              Ingress base domain. App URLs will be{" "}
              <code>http://&#123;app&#125;.&lt;baseDomain&gt;</code>.
            </p>
          </div>

          <div>
            <label className="block text-sm font-medium text-gray-700">
              Namespace pattern
            </label>
            <input
              type="text"
              value={namespacePattern}
              onChange={(e) => setNamespacePattern(e.target.value)}
              placeholder="{app}-{env}"
              className="mt-1 block w-full rounded-md border border-gray-300 px-3 py-2 text-sm shadow-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
            />
            <p className="mt-1 text-xs text-gray-400">
              Use <code>{"{"}</code>app<code>{"}"}</code>,{" "}
              <code>{"{"}</code>env<code>{"}"}</code>,{" "}
              <code>{"{"}</code>project<code>{"}"}</code>. Defaults to{" "}
              <code>{"{app}-{env}"}</code> (safe for shared clusters). Use{" "}
              <code>{"{app}"}</code> alone when each environment has a dedicated
              cluster.
            </p>
          </div>

          <div>
            <label className="block text-sm font-medium text-gray-700">
              Order
            </label>
            <input
              type="number"
              value={order || ""}
              onChange={(e) => setOrder(parseInt(e.target.value, 10) || 0)}
              placeholder="1"
              min={1}
              className="mt-1 block w-full rounded-md border border-gray-300 px-3 py-2 text-sm shadow-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
            />
            <p className="mt-1 text-xs text-gray-400">
              Promotion order (lower = earlier in the pipeline).
            </p>
          </div>

          {error && (
            <p className="rounded-md bg-red-50 px-3 py-2 text-sm text-red-700">
              {error}
            </p>
          )}

          <div className="flex justify-end gap-3 pt-2">
            <button
              type="button"
              onClick={onClose}
              className="rounded-md px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50"
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={submitting}
              className="rounded-md bg-indigo-600 px-4 py-2 text-sm font-medium text-white hover:bg-indigo-700 disabled:opacity-50"
            >
              {submitting ? "Saving…" : isEdit ? "Save changes" : "Add environment"}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

// ── Main page ─────────────────────────────────────────────────────────────────

export function ProjectSettings() {
  const { project } = useParams<{ project: string }>();

  const [envs, setEnvs] = useState<ProjectEnvironment[]>([]);
  const [clusters, setClusters] = useState<Cluster[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [showAdd, setShowAdd] = useState(false);
  const [editEnv, setEditEnv] = useState<ProjectEnvironment | null>(null);
  const [deletingName, setDeletingName] = useState<string | null>(null);

  const load = useCallback(async () => {
    if (!project) return;
    try {
      const [e, c] = await Promise.all([
        listProjectEnvironments(project),
        listClusters().catch(() => [] as Cluster[]),
      ]);
      setEnvs(e);
      setClusters(c);
      setError(null);
    } catch (err) {
      setError(
        err instanceof Error ? err.message : "Failed to load environments",
      );
    } finally {
      setLoading(false);
    }
  }, [project]);

  useEffect(() => {
    load();
  }, [load]);

  async function handleDelete(envName: string) {
    if (
      !confirm(
        `Remove environment "${envName}"? This does not delete running workloads.`,
      )
    )
      return;
    setDeletingName(envName);
    try {
      await deleteProjectEnvironment(project!, envName);
      await load();
    } catch (err) {
      alert(err instanceof Error ? err.message : "Failed to remove");
    } finally {
      setDeletingName(null);
    }
  }

  function clusterLabel(ref?: string) {
    if (!ref) return <span className="text-gray-400">—</span>;
    const c = clusters.find((c) => c.name === ref);
    return (
      <span className="inline-flex items-center gap-1.5">
        <span
          className={`h-1.5 w-1.5 rounded-full ${c?.status === "ready" ? "bg-green-500" : "bg-gray-400"}`}
        />
        <span className="font-mono text-xs">{c?.displayName || ref}</span>
      </span>
    );
  }

  return (
    <div className="space-y-6">
      {(showAdd || editEnv) && (
        <EnvForm
          projectName={project!}
          clusters={clusters}
          initial={editEnv ?? undefined}
          onClose={() => {
            setShowAdd(false);
            setEditEnv(null);
          }}
          onSaved={load}
        />
      )}

      {/* Breadcrumb */}
      <div>
        <nav className="mb-2 flex items-center gap-1.5 text-sm text-gray-400">
          <Link to="/" className="hover:text-gray-600">
            Dashboard
          </Link>
          <span>/</span>
          <Link
            to={`/projects/${project}`}
            className="hover:text-gray-600"
          >
            {project}
          </Link>
          <span>/</span>
          <span className="text-gray-600">Settings</span>
        </nav>
        <div className="flex items-start justify-between">
          <div>
            <h1 className="text-2xl font-semibold text-gray-900">
              Project settings
            </h1>
            <p className="mt-1 text-sm text-gray-500">
              Manage environments, cluster assignments, and routing for{" "}
              <span className="font-medium text-gray-700">{project}</span>.
            </p>
          </div>
          <Link
            to={`/projects/${project}`}
            className="text-sm text-gray-500 hover:text-gray-700"
          >
            ← Back to project
          </Link>
        </div>
      </div>

      {/* Environments section */}
      <div>
        <div className="mb-3 flex items-center justify-between">
          <div>
            <h2 className="text-base font-semibold text-gray-900">
              Environments
            </h2>
            <p className="text-sm text-gray-500">
              Ordered deployment targets. Each environment can be bound to a
              registered cluster.
            </p>
          </div>
          <button
            onClick={() => setShowAdd(true)}
            className="rounded-md bg-indigo-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-indigo-700"
          >
            Add environment
          </button>
        </div>

        {loading && (
          <div className="space-y-2">
            {[1, 2].map((i) => (
              <div key={i} className="h-16 animate-pulse rounded-lg bg-gray-100" />
            ))}
          </div>
        )}

        {!loading && error && (
          <div className="rounded-lg border border-red-200 bg-red-50 p-4">
            <p className="text-sm text-red-700">{error}</p>
          </div>
        )}

        {!loading && !error && envs.length === 0 && (
          <div className="rounded-lg border border-dashed border-gray-300 py-12 text-center">
            <p className="text-sm text-gray-500">No environments yet.</p>
            <button
              onClick={() => setShowAdd(true)}
              className="mt-3 rounded-md bg-indigo-600 px-4 py-2 text-sm font-medium text-white hover:bg-indigo-700"
            >
              Add your first environment
            </button>
          </div>
        )}

        {!loading && !error && envs.length > 0 && (
          <div className="overflow-hidden rounded-lg border border-gray-200 bg-white">
            <table className="w-full">
              <thead>
                <tr className="border-b border-gray-100 text-left text-xs font-medium uppercase tracking-wider text-gray-500">
                  <th className="px-5 py-3">Environment</th>
                  <th className="px-5 py-3">Cluster</th>
                  <th className="px-5 py-3">Base domain</th>
                  <th className="px-5 py-3">Namespace pattern</th>
                  <th className="px-5 py-3" />
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-50">
                {envs.map((env) => (
                  <tr key={env.name} className="hover:bg-gray-50">
                    <td className="px-5 py-3">
                      <p className="text-sm font-medium text-gray-900">
                        {env.displayName || env.name}
                      </p>
                      {env.displayName && (
                        <p className="text-xs text-gray-400">{env.name}</p>
                      )}
                    </td>
                    <td className="px-5 py-3">{clusterLabel(env.clusterRef)}</td>
                    <td className="px-5 py-3 font-mono text-xs text-gray-600">
                      {env.baseDomain || <span className="text-gray-400">localhost</span>}
                    </td>
                    <td className="px-5 py-3 font-mono text-xs text-gray-600">
                      {env.namespacePattern || (
                        <span className="text-gray-400">{"{app}-{env}"}</span>
                      )}
                    </td>
                    <td className="px-5 py-3 text-right">
                      <div className="flex items-center justify-end gap-3">
                        <button
                          onClick={() => setEditEnv(env)}
                          className="text-sm text-indigo-600 hover:text-indigo-700"
                        >
                          Edit
                        </button>
                        <button
                          onClick={() => handleDelete(env.name)}
                          disabled={deletingName === env.name}
                          className="text-sm text-red-600 hover:text-red-700 disabled:opacity-50"
                        >
                          {deletingName === env.name ? "Removing…" : "Remove"}
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {/* Info banner */}
      <div className="rounded-lg bg-blue-50 p-4">
        <h3 className="text-sm font-medium text-blue-800">
          Cluster assignment
        </h3>
        <ul className="mt-2 space-y-1 text-sm text-blue-700">
          <li>
            • Environments without a cluster assignment cannot be deployed to
            until a cluster is registered and linked.
          </li>
          <li>
            • Use <code className="font-mono text-xs">{"{app}"}</code> as the
            namespace pattern when each environment has its own dedicated
            cluster.
          </li>
          <li>
            • Use{" "}
            <code className="font-mono text-xs">{"{app}-{env}"}</code> (default)
            when multiple environments share the same cluster.
          </li>
          <li>
            • Register clusters in{" "}
            <Link to="/settings/clusters" className="underline hover:text-blue-900">
              Settings → Clusters
            </Link>
            .
          </li>
        </ul>
      </div>
    </div>
  );
}
