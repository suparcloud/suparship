import { useEffect, useState } from "react";
import { Link } from "react-router-dom";

import { fetchPreviews } from "../lib/previews";
import { fetchOrg, fetchProjects } from "../lib/settings";
import { fetchEnvironments, fetchProjectServices } from "../lib/services";
import type {
  OrgInfo,
  Project,
  EnvironmentInfo,
  ServiceRuntime,
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
  servicesByProject: Map<string, ServiceRuntime[]>;
  previewCount: number;
}

async function loadDashboard(): Promise<DashboardData> {
  const [orgData, projectsData, envsData, previewsData] = await Promise.all([
    fetchOrg().catch(() => null),
    fetchProjects().catch(() => ({ projects: [] as Project[] })),
    fetchEnvironments().catch(() => ({ environments: [] as EnvironmentInfo[] })),
    fetchPreviews().catch(() => ({ previews: [] })),
  ]);

  const servicesByProject = new Map<string, ServiceRuntime[]>();

  const results = await Promise.allSettled(
    projectsData.projects.map((p) => fetchProjectServices(p.name)),
  );
  for (const result of results) {
    if (result.status === "fulfilled") {
      servicesByProject.set(result.value.project, result.value.services);
    }
  }

  return {
    org: orgData,
    projects: projectsData.projects,
    environments: envsData.environments,
    servicesByProject,
    previewCount: previewsData.previews.length,
  };
}

// --- Component ---

export function Dashboard() {
  const [data, setData] = useState<DashboardData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

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

  const totalServices = Array.from(data.servicesByProject.values()).reduce(
    (sum, svcs) => sum + svcs.length,
    0,
  );

  const uniqueEnvNames = [
    ...new Set(data.environments.map((e) => e.name)),
  ];

  return (
    <div className="space-y-8">
      {/* Header */}
      <div>
        <h1 className="text-2xl font-semibold text-gray-900">
          {data.org?.displayName ?? "Dashboard"}
        </h1>
        <p className="mt-1 text-sm text-gray-500">
          Platform overview — environments, projects, and services at a glance.
        </p>
      </div>

      {/* Stats */}
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard label="Projects" value={data.projects.length} />
        <StatCard label="Environments" value={uniqueEnvNames.length} />
        <StatCard label="Services" value={totalServices} />
        <StatCard label="Previews" value={data.previewCount} />
      </div>

      {/* Environments */}
      {data.environments.length > 0 && (
        <div>
          <h2 className="mb-3 text-sm font-medium uppercase tracking-wider text-gray-400">
            Environments
          </h2>
          <div className="flex flex-wrap gap-2">
            {data.environments.map((env) => (
              <div
                key={`${env.project}-${env.name}`}
                className="rounded-lg border border-gray-200 bg-white px-3.5 py-2"
              >
                <span className="text-sm font-medium text-gray-900">
                  {env.displayName || env.name}
                </span>
                <span className="ml-2 text-xs text-gray-400">
                  {env.project}
                </span>
                <span className="ml-2 font-mono text-xs text-gray-400">
                  {env.namespace}
                </span>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Projects & Services */}
      {data.projects.length === 0 ? (
        <EmptyState />
      ) : (
        <div className="space-y-6">
          <h2 className="text-sm font-medium uppercase tracking-wider text-gray-400">
            Projects
          </h2>
          {data.projects.map((p) => {
            const services = data.servicesByProject.get(p.name) ?? [];
            return (
              <ProjectCard
                key={p.name}
                project={p}
                services={services}
              />
            );
          })}
        </div>
      )}
    </div>
  );
}

// --- Project card ---

function ProjectCard({
  project,
  services,
}: {
  project: Project;
  services: ServiceRuntime[];
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
            {services.length} {services.length === 1 ? "service" : "services"}
          </span>
          <Link
            to={`/projects/${project.name}/apps/new`}
            className="rounded-md bg-gray-900 px-3 py-1.5 text-xs font-medium text-white transition-colors hover:bg-gray-700"
          >
            Add app
          </Link>
        </div>
      </div>

      {services.length === 0 ? (
        <div className="px-5 py-10 text-center">
          <p className="text-sm text-gray-400">No services configured yet.</p>
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
              <th className="px-5 py-2.5">Service</th>
              <th className="px-5 py-2.5">Template</th>
              <th className="px-5 py-2.5">Status</th>
              <th className="px-5 py-2.5">Image</th>
              <th className="px-5 py-2.5">Replicas</th>
              <th className="px-5 py-2.5">URLs</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-50">
            {services.map((svc) => (
              <ServiceRow
                key={svc.name}
                project={project}
                service={svc}
              />
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

// --- Service row ---

function ServiceRow({
  project,
  service,
}: {
  project: string;
  service: ServiceRuntime;
}) {
  const rt = service.runtime;
  const imageShort = rt.image
    ? rt.image.replace(/^[^/]*\/[^/]*\//, "")
    : "—";

  return (
    <tr className="transition-colors hover:bg-gray-50">
      <td className="px-5 py-3">
        <Link
          to={`/projects/${project}/services/${service.name}`}
          className="text-sm font-medium text-gray-900 hover:text-gray-600"
        >
          {service.name}
        </Link>
      </td>
      <td className="px-5 py-3">
        <span className="rounded bg-gray-100 px-2 py-0.5 font-mono text-xs text-gray-600">
          {service.template.name}
        </span>
      </td>
      <td className="px-5 py-3">
        <StatusBadge status={rt.status} />
      </td>
      <td className="px-5 py-3">
        <span className="font-mono text-xs text-gray-500" title={rt.image}>
          {imageShort}
        </span>
      </td>
      <td className="px-5 py-3 text-sm text-gray-600">
        {rt.status === "not_deployed" ? (
          "—"
        ) : (
          <span>
            {rt.available}
            <span className="text-gray-400">/{rt.replicas}</span>
          </span>
        )}
      </td>
      <td className="px-5 py-3">
        {rt.ingressUrls.length > 0 ? (
          <div className="flex flex-col gap-0.5">
            {rt.ingressUrls.map((url) => (
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

function EmptyState() {
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
        Get started by browsing available templates or running the onboarding
        checklist to set up your first project and service.
      </p>
      <div className="mt-6 flex items-center justify-center gap-3">
        <Link
          to="/templates"
          className="rounded-md bg-gray-900 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-gray-700"
        >
          Browse templates
        </Link>
        <Link
          to="/onboarding"
          className="rounded-md border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 transition-colors hover:bg-gray-50"
        >
          Onboarding checklist
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
