import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";

import { fetchProjectServices } from "../lib/services";
import type { ServiceRuntime } from "../types";

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

// --- Component ---

export function ProjectDetail() {
  const { project } = useParams<{ project: string }>();
  const [services, setServices] = useState<ServiceRuntime[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!project) return;
    let cancelled = false;

    fetchProjectServices(project)
      .then((data) => {
        if (!cancelled) setServices(data.services);
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
  }, [project]);

  if (loading) return <ProjectSkeleton />;

  if (error) {
    return (
      <div className="rounded-lg border border-red-200 bg-red-50 p-4">
        <p className="text-sm text-red-700">
          Failed to load project: {error}
        </p>
      </div>
    );
  }

  const healthyCt = services.filter((s) => s.runtime.status === "healthy").length;
  const totalCt = services.length;

  return (
    <div className="space-y-6">
      {/* Breadcrumb + header */}
      <div>
        <nav className="mb-2 flex items-center gap-1.5 text-sm text-gray-400">
          <Link to="/" className="hover:text-gray-600">
            Dashboard
          </Link>
          <span>/</span>
          <span className="text-gray-600">{project}</span>
        </nav>
        <div className="flex items-center justify-between">
          <div>
            <h1 className="text-2xl font-semibold text-gray-900">{project}</h1>
            <p className="mt-1 text-sm text-gray-500">
              {totalCt} {totalCt === 1 ? "service" : "services"}
              {healthyCt > 0 && (
                <span className="text-emerald-600">
                  {" "}
                  &middot; {healthyCt} healthy
                </span>
              )}
            </p>
          </div>
          <Link
            to={`/projects/${project}/services/new`}
            className="rounded-lg bg-gray-900 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-gray-700"
          >
            Add service
          </Link>
        </div>
      </div>

      {/* Services */}
      {services.length === 0 ? (
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
                d="m21 7.5-9-5.25L3 7.5m18 0-9 5.25m9-5.25v9l-9 5.25M3 7.5l9 5.25M3 7.5v9l9 5.25"
              />
            </svg>
          </div>
          <h3 className="text-sm font-medium text-gray-900">
            No services yet
          </h3>
          <p className="mt-1 text-sm text-gray-500">
            Deploy your first service to get started.
          </p>
          <Link
            to={`/projects/${project}/services/new`}
            className="mt-4 inline-block rounded-lg bg-gray-900 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-gray-700"
          >
            Create service
          </Link>
        </div>
      ) : (
        <div className="overflow-hidden rounded-xl border border-gray-200 bg-white">
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-100 text-left text-xs font-medium uppercase tracking-wider text-gray-400">
                <th className="px-5 py-3">Service</th>
                <th className="px-5 py-3">Template</th>
                <th className="px-5 py-3">Status</th>
                <th className="px-5 py-3">Image</th>
                <th className="px-5 py-3">Replicas</th>
                <th className="px-5 py-3">Namespace</th>
                <th className="px-5 py-3">URLs</th>
                <th className="px-5 py-3">Last deployed</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {services.map((svc) => (
                <ServiceRow
                  key={svc.name}
                  project={project!}
                  service={svc}
                />
              ))}
            </tbody>
          </table>
        </div>
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

  const deployed = rt.lastDeployed
    ? new Date(rt.lastDeployed).toLocaleDateString(undefined, {
        month: "short",
        day: "numeric",
        hour: "2-digit",
        minute: "2-digit",
      })
    : "—";

  return (
    <tr className="transition-colors hover:bg-gray-50">
      <td className="px-5 py-3.5">
        <Link
          to={`/projects/${project}/services/${service.name}`}
          className="text-sm font-medium text-gray-900 hover:text-gray-600"
        >
          {service.name}
        </Link>
      </td>
      <td className="px-5 py-3.5">
        <span className="rounded bg-gray-100 px-2 py-0.5 font-mono text-xs text-gray-600">
          {service.template.name}
        </span>
        {service.template.version && (
          <span className="ml-1 text-xs text-gray-400">
            v{service.template.version}
          </span>
        )}
      </td>
      <td className="px-5 py-3.5">
        <StatusBadge status={rt.status} />
      </td>
      <td className="px-5 py-3.5">
        <span className="font-mono text-xs text-gray-500" title={rt.image}>
          {imageShort}
        </span>
      </td>
      <td className="px-5 py-3.5 text-sm text-gray-600">
        {rt.status === "not_deployed" ? (
          "—"
        ) : (
          <span>
            {rt.available}
            <span className="text-gray-400">/{rt.replicas}</span>
          </span>
        )}
      </td>
      <td className="px-5 py-3.5">
        {rt.namespace ? (
          <span className="font-mono text-xs text-gray-500">
            {rt.namespace}
          </span>
        ) : (
          <span className="text-xs text-gray-400">—</span>
        )}
      </td>
      <td className="px-5 py-3.5">
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
      <td className="px-5 py-3.5 text-xs text-gray-500">{deployed}</td>
    </tr>
  );
}

// --- Skeleton ---

function ProjectSkeleton() {
  return (
    <div className="space-y-6">
      <div className="space-y-2">
        <div className="h-4 w-32 animate-pulse rounded bg-gray-100" />
        <div className="h-8 w-48 animate-pulse rounded bg-gray-100" />
        <div className="h-5 w-64 animate-pulse rounded bg-gray-50" />
      </div>
      <div className="h-64 animate-pulse rounded-xl bg-gray-50" />
    </div>
  );
}
