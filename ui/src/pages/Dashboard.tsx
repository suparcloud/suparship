import { useCallback, useEffect, useMemo, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";

import { listApps } from "../lib/apps";
import { StuckAppsBanner } from "../components/StuckAppsBanner";
import { StatusBadge, overallPhase } from "../components/StatusBadge";
import { usePagedSearch } from "../components/Pagination";
import { createProject, fetchOrg, fetchProjects } from "../lib/settings";
import { fetchEnvironments } from "../lib/services";
import type {
  AppSummary,
  AppEnvironmentSummary,
  OrgInfo,
  Project,
  EnvironmentInfo,
} from "../types";

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

// The dashboard loads in two phases so it paints immediately. Phase 1 (base) is
// org/projects/environments — three fast reads that populate the header, stat
// cards, env chips, and project rows. Phase 2 (per-project apps) is the slow
// status-enriched fan-out; it fills in health/app/preview counts as it arrives,
// rather than blocking the whole page behind the slowest project.

interface BaseData {
  org: OrgInfo | null;
  projects: Project[];
  environments: EnvironmentInfo[];
}

async function loadBase(): Promise<BaseData> {
  const [orgData, projectsData, envsData] = await Promise.all([
    fetchOrg().catch(() => null),
    fetchProjects().catch(() => ({ projects: [] as Project[] })),
    fetchEnvironments().catch(() => ({ environments: [] as EnvironmentInfo[] })),
  ]);
  return {
    org: orgData,
    projects: projectsData.projects,
    environments: envsData.environments,
  };
}

async function loadApps(
  projects: Project[],
): Promise<Map<string, AppSummary[]>> {
  const appsByProject = new Map<string, AppSummary[]>();
  const results = await Promise.allSettled(
    projects.map((p) => listApps(p.name)),
  );
  for (const result of results) {
    if (result.status === "fulfilled") {
      appsByProject.set(result.value.project, result.value.apps);
    }
  }
  return appsByProject;
}

// --- Component ---

export function Dashboard() {
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const [base, setBase] = useState<BaseData | null>(null);
  const [appsByProject, setAppsByProject] = useState<Map<string, AppSummary[]>>(
    () => new Map(),
  );
  const [appsLoading, setAppsLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [showNewProject, setShowNewProject] = useState(
    searchParams.get("newProject") === "1",
  );

  // load runs the two phases. setBase paints the page as soon as the fast reads
  // resolve; the per-project apps fill in afterwards. cancelled guards against a
  // late resolve writing to an unmounted component.
  const load = useCallback((cancelled?: () => boolean) => {
    const isCancelled = () => cancelled?.() ?? false;
    setError(null);
    setAppsLoading(true);
    loadBase()
      .then((b) => {
        if (isCancelled()) return;
        setBase(b);
        return loadApps(b.projects).then((m) => {
          if (!isCancelled()) setAppsByProject(m);
        });
      })
      .catch((err) => {
        if (!isCancelled())
          setError(err instanceof Error ? err.message : "Failed to load");
      })
      .finally(() => {
        if (!isCancelled()) setAppsLoading(false);
      });
  }, []);

  function refresh() {
    setBase(null);
    setAppsByProject(new Map());
    load();
  }

  useEffect(() => {
    let cancelled = false;
    load(() => cancelled);
    return () => {
      cancelled = true;
    };
  }, [load]);

  // Skeleton only until the fast base reads land; per-project health then streams
  // in without blocking the frame.
  if (!base && !error) return <DashboardSkeleton />;

  if (error) {
    return (
      <div className="rounded-lg border border-red-200 bg-red-50 p-4">
        <p className="text-sm text-red-700">
          Failed to load dashboard: {error}
        </p>
      </div>
    );
  }

  if (!base) return null;
  const data = base;

  const totalServices = Array.from(appsByProject.values()).reduce(
    (sum, apps) => sum + apps.length,
    0,
  );
  const previewCount = Array.from(appsByProject.values()).reduce(
    (sum, apps) => sum + distinctPreviewCount(apps),
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

      {/* Platform ops: stuck-deleting apps (org_admin only; self-hides otherwise) */}
      <StuckAppsBanner />

      {/* Stats */}
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard label="Projects" value={data.projects.length} />
        <StatCard label="Environments" value={uniqueEnvNames.length} />
        <StatCard label="Apps" value={appsLoading ? "…" : totalServices} />
        <StatCard label="Previews" value={appsLoading ? "…" : previewCount} />
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
                {env.clusterRefs && env.clusterRefs.length > 0 && (
                  <span className="ml-2 font-mono text-xs text-gray-400">
                    {env.activeClusterRef || env.clusterRefs[0]}
                    {env.clusterRefs.length > 1 && (
                      <span className="ml-1 text-gray-300">
                        +{env.clusterRefs.length - 1}
                      </span>
                    )}
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
        <ProjectsSection
          projects={data.projects}
          appsByProject={appsByProject}
          appsLoading={appsLoading}
          environments={data.environments}
        />
      )}
    </div>
    </>
  );
}

// --- Projects section: paginated, searchable list of project summary rows ---

// projectSummary derives a project's health rollup from its already-loaded apps,
// with no extra requests. Per stable env it counts apps that deploy there
// (deployed) and of those how many are healthy/idle.
interface EnvCount {
  healthy: number;
  deployed: number;
}
function projectSummary(apps: AppSummary[], envNames: string[]) {
  const allStableEnvs: AppEnvironmentSummary[] = apps.flatMap((a) =>
    (a.environments ?? []).filter((e) => e.envType !== "preview"),
  );
  const perEnv: Record<string, EnvCount> = {};
  for (const name of envNames) perEnv[name] = { healthy: 0, deployed: 0 };
  for (const a of apps) {
    for (const e of a.environments ?? []) {
      const c = perEnv[e.envName];
      if (!c || e.envType === "preview") continue;
      if (e.deploy && e.status.phase !== "not_deployed") {
        c.deployed++;
        if (e.status.phase === "healthy" || e.status.phase === "idle") c.healthy++;
      }
    }
  }
  return {
    total: apps.length,
    overall: overallPhase(allStableEnvs),
    perEnv,
    previews: distinctPreviewCount(apps),
  };
}

// distinctPreviewCount counts PRs, not app-previews: the number of distinct
// preview names across the apps (a PR creates one app-preview per app, all named
// the same — see the per-PR Previews page).
function distinctPreviewCount(apps: AppSummary[]): number {
  const names = new Set<string>();
  for (const a of apps) {
    for (const e of a.environments ?? []) {
      if (e.envType === "preview") names.add(e.preview?.previewName ?? e.envName);
    }
  }
  return names.size;
}

function ProjectsSection({
  projects,
  appsByProject,
  appsLoading,
  environments,
}: {
  projects: Project[];
  appsByProject: Map<string, AppSummary[]>;
  appsLoading: boolean;
  environments: EnvironmentInfo[];
}) {
  const { query, setQuery, page, setPage, pageItems, pageCount, total } =
    usePagedSearch(
      projects,
      (p, q) =>
        p.name.toLowerCase().includes(q) ||
        (p.displayName ?? "").toLowerCase().includes(q),
      12,
    );

  // Org-level stable environments become the per-env columns, in promotion order.
  const envCols = [...environments]
    .filter((e) => !e.project)
    .sort((a, b) => a.order - b.order);

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <h2 className="text-sm font-medium uppercase tracking-wider text-gray-400">
          Projects
        </h2>
        {projects.length > 6 && (
          <input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search projects…"
            className="w-56 rounded-lg border border-gray-300 px-3 py-1.5 text-sm focus:border-gray-400 focus:outline-none"
          />
        )}
      </div>

      <div className="overflow-hidden rounded-xl border border-gray-200 bg-white">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-gray-100 text-left text-xs font-medium uppercase tracking-wider text-gray-400">
              <th className="px-5 py-2.5">Project</th>
              <th className="px-5 py-2.5">Health</th>
              <th className="px-5 py-2.5">Apps</th>
              {envCols.map((e) => (
                <th key={e.name} className="px-5 py-2.5">
                  {e.displayName || e.name}
                </th>
              ))}
              <th className="px-5 py-2.5">Previews</th>
              <th className="px-5 py-2.5 text-right">Actions</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-50">
            {pageItems.map((p) => (
              <ProjectSummaryRow
                key={p.name}
                project={p}
                apps={appsByProject.get(p.name) ?? []}
                loading={appsLoading && !appsByProject.has(p.name)}
                envCols={envCols}
              />
            ))}
            {query && total === 0 && (
              <tr>
                <td
                  colSpan={5 + envCols.length}
                  className="px-5 py-6 text-center text-sm text-gray-400"
                >
                  No projects match “{query}”.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      {pageCount > 1 && (
        <div className="flex items-center justify-end gap-2 text-xs text-gray-500">
          <button
            onClick={() => setPage(page - 1)}
            disabled={page <= 1}
            className="rounded border border-gray-200 px-2 py-1 hover:bg-gray-50 disabled:opacity-40"
          >
            ‹ Prev
          </button>
          <span className="tabular-nums">
            {page} / {pageCount}
          </span>
          <button
            onClick={() => setPage(page + 1)}
            disabled={page >= pageCount}
            className="rounded border border-gray-200 px-2 py-1 hover:bg-gray-50 disabled:opacity-40"
          >
            Next ›
          </button>
        </div>
      )}
    </div>
  );
}

// --- Project summary row ---

function EnvHealthCell({ count }: { count: EnvCount | undefined }) {
  if (!count || count.deployed === 0) {
    return <span className="text-xs text-gray-300">not deployed</span>;
  }
  const allHealthy = count.healthy === count.deployed;
  return (
    <span
      className={`inline-flex items-center gap-1.5 text-xs font-medium ${
        allHealthy ? "text-emerald-700" : "text-amber-700"
      }`}
    >
      <span
        className={`h-1.5 w-1.5 rounded-full ${
          allHealthy ? "bg-emerald-500" : "bg-amber-500"
        }`}
      />
      {count.healthy}/{count.deployed} healthy
    </span>
  );
}

function ProjectSummaryRow({
  project,
  apps,
  loading,
  envCols,
}: {
  project: Project;
  apps: AppSummary[];
  loading: boolean;
  envCols: EnvironmentInfo[];
}) {
  // Recompute the rollup only when this project's apps (or the env columns)
  // change — not on every dashboard re-render (search, pagination, modal).
  const envNames = useMemo(() => envCols.map((e) => e.name), [envCols]);
  const summary = useMemo(
    () => projectSummary(apps, envNames),
    [apps, envNames],
  );

  // A muted pulse stands in for the health/counts until this project's apps
  // arrive, so the row is visible immediately without showing a false "0".
  const pending = <span className="inline-block h-3 w-8 animate-pulse rounded bg-gray-100" />;

  return (
    <tr className="hover:bg-gray-50/50">
      <td className="px-5 py-3">
        <Link
          to={`/projects/${project.name}`}
          className="font-medium text-gray-900 hover:text-indigo-600"
        >
          {project.displayName ?? project.name}
        </Link>
        {project.description && (
          <p className="mt-0.5 max-w-xs truncate text-xs text-gray-400">
            {project.description}
          </p>
        )}
      </td>
      <td className="px-5 py-3">
        {loading ? pending : <StatusBadge status={summary.overall} />}
      </td>
      <td className="px-5 py-3 text-gray-600 tabular-nums">
        {loading ? pending : summary.total}
      </td>
      {envCols.map((e) => (
        <td key={e.name} className="px-5 py-3">
          {loading ? pending : <EnvHealthCell count={summary.perEnv[e.name]} />}
        </td>
      ))}
      <td className="px-5 py-3 text-gray-600 tabular-nums">
        {loading ? (
          pending
        ) : summary.previews > 0 ? (
          summary.previews
        ) : (
          <span className="text-gray-300">—</span>
        )}
      </td>
      <td className="px-5 py-3">
        <div className="flex items-center justify-end gap-3 text-xs font-medium">
          <Link
            to={`/projects/${project.name}/settings`}
            className="text-gray-500 hover:text-gray-800"
          >
            Settings
          </Link>
          <Link
            to={`/projects/${project.name}/apps/new`}
            className="text-indigo-600 hover:text-indigo-800"
          >
            Add app
          </Link>
        </div>
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
