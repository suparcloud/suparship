import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";

import { fetchServiceDetail } from "../lib/services";
import type { ServiceDetailInfo } from "../types";

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

export function ServiceDetail() {
  const { project, service } = useParams<{
    project: string;
    service: string;
  }>();
  const [data, setData] = useState<ServiceDetailInfo | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!project || !service) return;
    let cancelled = false;

    fetchServiceDetail(project, service)
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
  }, [project, service]);

  if (loading) return <DetailSkeleton />;

  if (error) {
    return (
      <div className="rounded-lg border border-red-200 bg-red-50 p-4">
        <p className="text-sm text-red-700">
          Failed to load service: {error}
        </p>
      </div>
    );
  }

  if (!data) return null;

  return (
    <div className="space-y-6">
      {/* Breadcrumb */}
      <nav className="flex items-center gap-1.5 text-sm text-gray-400">
        <Link to="/" className="hover:text-gray-600">
          Dashboard
        </Link>
        <span>/</span>
        <Link to={`/projects/${project}`} className="hover:text-gray-600">
          {project}
        </Link>
        <span>/</span>
        <span className="text-gray-600">{service}</span>
      </nav>

      {/* Header */}
      <div className="flex items-start justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900">{data.name}</h1>
          <p className="mt-1 text-sm text-gray-500">
            Template:{" "}
            <Link
              to={`/templates/${data.template.name}`}
              className="font-mono text-gray-600 hover:text-gray-900"
            >
              {data.template.name}
            </Link>
            {data.template.version && (
              <span className="text-gray-400">
                {" "}
                v{data.template.version}
              </span>
            )}
          </p>
        </div>
      </div>

      {/* Config */}
      {Object.keys(data.values).length > 0 && (
        <div className="rounded-xl border border-gray-200 bg-white">
          <div className="border-b border-gray-100 px-5 py-3.5">
            <h2 className="text-sm font-medium text-gray-500">
              Configuration
            </h2>
          </div>
          <dl className="divide-y divide-gray-50 px-5">
            {Object.entries(data.values).map(([key, val]) => (
              <div key={key} className="flex justify-between py-2.5">
                <dt className="font-mono text-sm text-gray-500">{key}</dt>
                <dd className="text-sm text-gray-900">{String(val)}</dd>
              </div>
            ))}
          </dl>
        </div>
      )}

      {/* Secret refs */}
      {data.secretRefs.length > 0 && (
        <div className="rounded-xl border border-gray-200 bg-white">
          <div className="border-b border-gray-100 px-5 py-3.5">
            <h2 className="text-sm font-medium text-gray-500">
              Secret References
            </h2>
          </div>
          <dl className="divide-y divide-gray-50 px-5">
            {data.secretRefs.map((ref) => (
              <div key={ref.name} className="flex justify-between py-2.5">
                <dt className="font-mono text-sm text-gray-500">{ref.name}</dt>
                <dd className="flex items-center gap-1.5 text-sm text-gray-900">
                  <svg
                    className="h-3.5 w-3.5 text-gray-400"
                    fill="none"
                    viewBox="0 0 24 24"
                    stroke="currentColor"
                    strokeWidth={2}
                  >
                    <path
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      d="M16.5 10.5V6.75a4.5 4.5 0 1 0-9 0v3.75m-.75 11.25h10.5a2.25 2.25 0 0 0 2.25-2.25v-6.75a2.25 2.25 0 0 0-2.25-2.25H6.75a2.25 2.25 0 0 0-2.25 2.25v6.75a2.25 2.25 0 0 0 2.25 2.25Z"
                    />
                  </svg>
                  <span className="font-mono">{ref.secretRef}</span>
                </dd>
              </div>
            ))}
          </dl>
        </div>
      )}

      {/* Environments */}
      <div className="rounded-xl border border-gray-200 bg-white">
        <div className="border-b border-gray-100 px-5 py-3.5">
          <h2 className="text-sm font-medium text-gray-500">
            Environments
          </h2>
        </div>
        {data.environments.length === 0 ? (
          <div className="px-5 py-10 text-center">
            <p className="text-sm text-gray-400">
              No environments configured.
            </p>
          </div>
        ) : (
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-100 text-left text-xs font-medium uppercase tracking-wider text-gray-400">
                <th className="px-5 py-2.5">Environment</th>
                <th className="px-5 py-2.5">Status</th>
                <th className="px-5 py-2.5">Image</th>
                <th className="px-5 py-2.5">Replicas</th>
                <th className="px-5 py-2.5">Namespace</th>
                <th className="px-5 py-2.5">URLs</th>
                <th className="px-5 py-2.5">Last deployed</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {data.environments.map((env) => {
                const rt = env.runtime;
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
                  <tr key={env.environment} className="hover:bg-gray-50">
                    <td className="px-5 py-3 text-sm font-medium text-gray-900">
                      {env.environment}
                    </td>
                    <td className="px-5 py-3">
                      <StatusBadge status={rt.status} />
                    </td>
                    <td className="px-5 py-3">
                      <span
                        className="font-mono text-xs text-gray-500"
                        title={rt.image}
                      >
                        {imageShort}
                      </span>
                    </td>
                    <td className="px-5 py-3 text-sm text-gray-600">
                      {rt.status === "not_deployed" ? (
                        "—"
                      ) : (
                        <span>
                          {rt.available}
                          <span className="text-gray-400">
                            /{rt.replicas}
                          </span>
                        </span>
                      )}
                    </td>
                    <td className="px-5 py-3">
                      <span className="font-mono text-xs text-gray-500">
                        {env.namespace}
                      </span>
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
                    <td className="px-5 py-3 text-xs text-gray-500">
                      {deployed}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

// --- Skeleton ---

function DetailSkeleton() {
  return (
    <div className="space-y-6">
      <div className="h-4 w-40 animate-pulse rounded bg-gray-100" />
      <div className="space-y-2">
        <div className="h-8 w-48 animate-pulse rounded bg-gray-100" />
        <div className="h-5 w-64 animate-pulse rounded bg-gray-50" />
      </div>
      <div className="h-32 animate-pulse rounded-xl bg-gray-50" />
      <div className="h-48 animate-pulse rounded-xl bg-gray-50" />
    </div>
  );
}
