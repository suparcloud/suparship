import { useEffect, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";

import { listApps } from "../lib/apps";
import { fetchPreviews } from "../lib/previews";
import { createProject, fetchOrg, fetchProjects } from "../lib/settings";
import { fetchEnvironments } from "../lib/services";
import type {
  AppSummary,
  OrgInfo,
  Project,
  EnvironmentInfo,
} from "../types";

// --- Status helpers ---

interface StatusStyle {
  dot: string;
  bg: string;
  label: string;
}

const fallbackStatus: StatusStyle = {
  dot: "bg-gray-300",
  bg: "bg-gray-100 text-gray-500",
  label: "Unknown",
};

const statusConfig: Record<string, StatusStyle> = {
  healthy: {
    dot: "bg-emerald-500",
    bg: "bg-emerald-50 text-emerald-700",
    label: "Healthy",
  },
  degraded: {
    dot: "bg-amber-500",
    bg: "bg-amber-50 text-amber-700",
    label: "Degraded",
  },
  progressing: {
    dot: "bg-blue-500",
    bg: "bg-blue-50 text-blue-700",
    label: "Syncing",
  },
  not_deployed: {
    dot: "bg-gray-300",
    bg: "bg-gray-100 text-gray-500",
    label: "Not deployed",
  },
};

function StatusBadge({ status }: { status: string }) {
  const cfg = statusConfig[status] ?? fallbackStatus;
  return (
    <span
      className={`inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-medium ${cfg.bg}`}
    >
      <span className={`h-1.5 w-1.5 rounded-full ${cfg.dot}`} />
      {cfg.label}
    </span>
  );
}

// --- New project modal ---

const projectNameRE = /^[a-z][a-z0-9-]{0,47}$/;

function NewProjectModal({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (name: string) => void;
}) {
  const [name, setName] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [description, setDescription] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!projectNameRE.test(name)) {
      setError(
        "Name must start with a lowercase letter and contain only lowercase letters, digits, or hyphens.",
      );
      return;
    }
    setSaving(true);
    setError(null);
    try {
      await createProject({ name, displayName: displayName || undefined, description: description || undefined });
      onCreated(name);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to create project");
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
      <div className="w-full max-w-md rounded-xl bg-white p-6 shadow-xl">
        <h3 className="mb-4 text-base font-semibold text-gray-900">New project</h3>
        <form onSubmit={handleSubmit} className="space-y-4">
          <div>
            <label className="mb-1 block text-xs font-medium text-gray-700">
              Name <span className="text-red-500">*</span>
            </label>
            <input
              required
              autoFocus
              className="w-full rounded-lg border border-gray-300 px-3 py-2 font-mono text-sm focus:border-gray-900 focus:outline-none focus:ring-1 focus:ring-gray-900"
              placeholder="my-project"
              value={name}
              onChange={(e) => setName(e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, ""))}
            />
            <p className="mt-1 text-xs text-gray-400">
              Lowercase letters, digits, hyphens only. Used in namespaces and URLs.
            </p>
          </div>

          <div>
            <label className="mb-1 block text-xs font-medium text-gray-700">Display name</label>
            <input
              className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm focus:border-gray-900 focus:outline-none focus:ring-1 focus:ring-gray-900"
              placeholder="My Project"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
            />
          </div>

          <div>
            <label className="mb-1 block text-xs font-medium text-gray-700">Description</label>
            <textarea
              rows={2}
              className="w-full resize-none rounded-lg border border-gray-300 px-3 py-2 text-sm focus:border-gray-900 focus:outline-none focus:ring-1 focus:ring-gray-900"
              placeholder="What does this project do?"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
            />
          </div>

          {error && (
            <p className="rounded bg-red-50 px-3 py-2 text-xs text-red-700">{error}</p>
          )}

          <div className="flex justify-end gap-2 pt-1">
            <button
              type="button"
              onClick={onClose}
              disabled={saving}
              className="rounded-lg border border-gray-300 px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50"
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={saving || !name}
              className="rounded-lg bg-gray-900 px-4 py-2 text-sm font-medium text-white hover:bg-gray-700 disabled:opacity-50"
            >
              {saving ? "Creating…" : "Create project"}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

// --- Stat card ---

function StatCard({
  label,
  value,
}: {
  label: string;
  value: string | number;
}) {
  return (
    <div className="rounded-xl border border-gray-200 bg-white px-5 py-4">
      <p className="text-sm text-gray-500">{label}</p>
      <p className="mt-1 text-2xl font-semibold text-gray-900">{value}</p>
    </div>
  );
}

// --- Data loading ---

interface DashboardData {
  org: OrgInfo | null;
  projects: Project[];
  environments: EnvironmentInfo[];
  appsByProject: Map<string, AppSummary[]>;
  previewCount: number;
}

async function loadDashboard(): Promise<DashboardData> {
  const [orgData, projectsData, envsData, previewsData] = await Promise.all([
    fetchOrg().catch(() => null),
    fetchProjects().catch(() => ({ projects: [] as Project[] })),
    fetchEnvironments().catch(() => ({ environments: [] as EnvironmentInfo[] })),
    fetchPreviews().catch(() => ({ previews: [] })),
  ]);

  const appsByProject = new Map<string, AppSummary[]>();

  const results = await Promise.allSettled(
    projectsData.projects.map((p) => listApps(p.name)),
  );
  for (const result of results) {
    if (result.status === "fulfilled") {
      appsByProject.set(result.value.project, result.value.apps);
    }
  }

  return {
    org: orgData,
    projects: projectsData.projects,
    environments: envsData.environments,
    appsByProject,
    previewCount: previewsData.previews.length,
  };
}

// --- Component ---

export function Dashboard() {
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const [data, setData] = useState<DashboardData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [showNewProject, setShowNewProject] = useState(
    searchParams.get("newProject") === "1",
  );

  function refresh() {
    setLoading(true);
    loadDashboard()
      .then((d) => setData(d))
      .catch((err) =>
        setError(err instanceof Error ? err.message : "Failed to load"),
      )
      .finally(() => setLoading(false));
  }

  useEffect(() => {
    let cancelled = false;

    loadDashboard()
      .then((d) => {
        if (!cancelled) setData(d);
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

  if (loading) return <DashboardSkeleton />;

  if (error) {
    return (
      <div className="rounded-lg border border-red-200 bg-red-50 p-4">
        <p className="text-sm text-red-700">
          Failed to load dashboard: {error}
        </p>
      </div>
    );
  }

  if (!data) return null;

  const totalServices = Array.from(data.appsByProject.values()).reduce(
    (sum, apps) => sum + apps.length,
    0,
  );

  const uniqueEnvNames = [
    ...new Set(data.environments.map((e) => e.name)),
  ];

  return (
    <>
    {showNewProject && (
      <NewProjectModal
        onClose={() => {
          setShowNewProject(false);
          setSearchParams({});
        }}
        onCreated={(name) => {
          setShowNewProject(false);
          setSearchParams({});
          refresh();
          navigate(`/projects/${name}`);
        }}
      />
    )}
    <div className="space-y-8">
      {/* Header */}
      <div className="flex items-start justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900">
            {data.org?.displayName ?? "Dashboard"}
          </h1>
          <p className="mt-1 text-sm text-gray-500">
            Platform overview — environments, projects, and apps at a glance.
          </p>
        </div>
        <button
          onClick={() => setShowNewProject(true)}
          className="rounded-lg bg-gray-900 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-gray-700"
        >
          New project
        </button>
      </div>

      {/* Stats */}
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard label="Projects" value={data.projects.length} />
        <StatCard label="Environments" value={uniqueEnvNames.length} />
        <StatCard label="Apps" value={totalServices} />
        <StatCard label="Previews" value={data.previewCount} />
      </div>

      {/* Environments */}
      {data.environments.length > 0 && (
        <div>
          <div className="mb-3 flex items-center justify-between">
            <h2 className="text-sm font-medium uppercase tracking-wider text-gray-400">
              Environments
            </h2>
            <span className="text-xs text-gray-400">
              Click an environment to manage it
            </span>
          </div>
          <div className="flex flex-wrap gap-2">
            {data.environments.map((env) => (
              <Link
                key={`${env.project || "org"}-${env.name}`}
                to={env.project ? `/projects/${env.project}/settings` : "/settings/org"}
                className="rounded-lg border border-gray-200 bg-white px-3.5 py-2 transition-colors hover:border-indigo-300 hover:bg-indigo-50"
              >
                <span className="text-sm font-medium text-gray-900">
                  {env.displayName || env.name}
                </span>
                {env.clusterRef && (
                  <span className="ml-2 font-mono text-xs text-gray-400">
                    {env.clusterRef}
                  </span>
                )}
              </Link>
            ))}
          </div>
        </div>
      )}

      {/* Projects & Apps */}
      {data.projects.length === 0 ? (
        <EmptyState onNewProject={() => setShowNewProject(true)} />
      ) : (
        <div className="space-y-6">
          <h2 className="text-sm font-medium uppercase tracking-wider text-gray-400">
            Projects
          </h2>
          {data.projects.map((p) => {
            const apps = data.appsByProject.get(p.name) ?? [];
            return (
              <ProjectCard
                key={p.name}
                project={p}
                apps={apps}
              />
            );
          })}
        </div>
      )}
    </div>
    </>
  );
}

// --- Project card ---

function ProjectCard({
  project,
  apps,
}: {
  project: Project;
  apps: AppSummary[];
}) {
  const displayName = project.displayName ?? project.name;

  return (
    <div className="overflow-hidden rounded-xl border border-gray-200 bg-white">
      <div className="flex items-center justify-between border-b border-gray-100 px-5 py-3.5">
        <div className="min-w-0">
          <Link
            to={`/projects/${project.name}`}
            className="text-sm font-semibold text-gray-900 hover:text-gray-600"
          >
            {displayName}
          </Link>
          {project.description && (
            <p className="mt-0.5 truncate text-xs text-gray-400">
              {project.description}
            </p>
          )}
        </div>
        <div className="flex flex-shrink-0 items-center gap-3">
          <span className="text-xs text-gray-400">
            {apps.length} {apps.length === 1 ? "app" : "apps"}
          </span>
          <Link
            to={`/projects/${project.name}/settings`}
            className="rounded-md border border-gray-200 px-3 py-1.5 text-xs font-medium text-gray-600 transition-colors hover:bg-gray-50"
          >
            Settings
          </Link>
          <Link
            to={`/projects/${project.name}/apps/new`}
            className="rounded-md bg-gray-900 px-3 py-1.5 text-xs font-medium text-white transition-colors hover:bg-gray-700"
          >
            Add app
          </Link>
        </div>
      </div>

      {apps.length === 0 ? (
        <div className="px-5 py-10 text-center">
          <p className="text-sm text-gray-400">No apps yet.</p>
          <Link
            to={`/projects/${project.name}/apps/new`}
            className="mt-2 inline-block text-sm font-medium text-gray-600 hover:text-gray-900"
          >
            Create your first app &rarr;
          </Link>
        </div>
      ) : (
        <table className="w-full">
          <thead>
            <tr className="border-b border-gray-100 text-left text-xs font-medium uppercase tracking-wider text-gray-400">
              <th className="px-5 py-2.5">App</th>
              <th className="px-5 py-2.5">Template</th>
              <th className="px-5 py-2.5">Status</th>
              <th className="px-5 py-2.5">Replicas</th>
              <th className="px-5 py-2.5">URLs</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-50">
            {apps.map((app) => (
              <AppRow
                key={app.name}
                project={project.name}
                app={app}
              />
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

// --- App row ---

function AppRow({
  project,
  app,
}: {
  project: string;
  app: AppSummary;
}) {
  const phase = app.status.phase;
  const urls = app.urls ?? [];

  return (
    <tr className="transition-colors hover:bg-gray-50">
      <td className="px-5 py-3">
        <Link
          to={`/projects/${project}/apps/${app.name}`}
          className="text-sm font-medium text-gray-900 hover:text-gray-600"
        >
          {app.displayName || app.name}
        </Link>
      </td>
      <td className="px-5 py-3">
        <span className="rounded bg-gray-100 px-2 py-0.5 font-mono text-xs text-gray-600">
          {app.template.name}
        </span>
      </td>
      <td className="px-5 py-3">
        <StatusBadge status={phase} />
      </td>
      <td className="px-5 py-3 text-sm text-gray-600">
        {phase === "not_deployed" ? (
          "—"
        ) : (
          <span>
            {app.status.available}
            <span className="text-gray-400">/{app.status.replicas}</span>
          </span>
        )}
      </td>
      <td className="px-5 py-3">
        {urls.length > 0 ? (
          <div className="flex flex-col gap-0.5">
            {urls.map((url) => (
              <a
                key={url}
                href={url}
                target="_blank"
                rel="noopener noreferrer"
                className="text-xs text-blue-600 hover:text-blue-800 hover:underline"
              >
                {url.replace(/^https?:\/\//, "")}
              </a>
            ))}
          </div>
        ) : (
          <span className="text-xs text-gray-400">—</span>
        )}
      </td>
    </tr>
  );
}

// --- Empty / skeleton states ---

function EmptyState({ onNewProject }: { onNewProject: () => void }) {
  return (
    <div className="rounded-xl border border-dashed border-gray-300 bg-white px-6 py-16 text-center">
      <div className="mx-auto mb-4 flex h-12 w-12 items-center justify-center rounded-full bg-gray-100">
        <svg
          className="h-6 w-6 text-gray-400"
          fill="none"
          viewBox="0 0 24 24"
          stroke="currentColor"
          strokeWidth={1.5}
        >
          <path
            strokeLinecap="round"
            strokeLinejoin="round"
            d="M12 4.5v15m7.5-7.5h-15"
          />
        </svg>
      </div>
      <h3 className="text-sm font-medium text-gray-900">No projects yet</h3>
      <p className="mx-auto mt-1 max-w-sm text-sm text-gray-500">
        Create a project, then deploy apps from templates into your environments.
      </p>
      <div className="mt-6 flex items-center justify-center gap-3">
        <button
          onClick={onNewProject}
          className="rounded-md bg-gray-900 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-gray-700"
        >
          New project
        </button>
        <Link
          to="/templates"
          className="rounded-md border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 transition-colors hover:bg-gray-50"
        >
          Browse templates
        </Link>
      </div>
    </div>
  );
}

function DashboardSkeleton() {
  return (
    <div className="space-y-8">
      <div className="space-y-2">
        <div className="h-8 w-56 animate-pulse rounded bg-gray-100" />
        <div className="h-5 w-80 animate-pulse rounded bg-gray-50" />
      </div>
      <div className="grid gap-4 sm:grid-cols-3">
        {[1, 2, 3].map((n) => (
          <div
            key={n}
            className="h-20 animate-pulse rounded-xl bg-gray-50"
          />
        ))}
      </div>
      <div className="space-y-4">
        {[1, 2].map((n) => (
          <div
            key={n}
            className="h-48 animate-pulse rounded-xl bg-gray-50"
          />
        ))}
      </div>
    </div>
  );
}
