import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";

import { createPreview } from "../lib/previews";
import { fetchServiceDetail, promoteService } from "../lib/services";
import type {
  ServiceDetailInfo,
  ServiceEnv,
  RuntimeInfo,
  PromoteResponse,
} from "../types";

// ---------------------------------------------------------------------------
// Status
// ---------------------------------------------------------------------------

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

const statusStyles: Record<string, StatusStyle> = {
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

function StatusBadge({
  status,
  size = "sm",
}: {
  status: string;
  size?: "sm" | "lg";
}) {
  const cfg = statusStyles[status] ?? fallbackStatus;
  const cls =
    size === "lg"
      ? "px-3 py-1 text-sm font-medium"
      : "px-2.5 py-0.5 text-xs font-medium";
  return (
    <span
      className={`inline-flex items-center gap-1.5 rounded-full ${cls} ${cfg.bg}`}
    >
      <span
        className={`rounded-full ${cfg.dot} ${size === "lg" ? "h-2 w-2" : "h-1.5 w-1.5"}`}
      />
      {cfg.label}
    </span>
  );
}

function overallStatus(envs: ServiceEnv[]): string {
  if (envs.length === 0) return "not_deployed";
  const statuses = envs.map((e) => e.runtime.status);
  if (statuses.every((s) => s === "healthy")) return "healthy";
  if (statuses.some((s) => s === "degraded")) return "degraded";
  if (statuses.some((s) => s === "progressing")) return "progressing";
  if (statuses.every((s) => s === "not_deployed")) return "not_deployed";
  return "degraded";
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function formatImage(image?: string): { short: string; tag: string } {
  if (!image) return { short: "—", tag: "" };
  const colonIdx = image.lastIndexOf(":");
  const repo = colonIdx > 0 ? image.slice(0, colonIdx) : image;
  const tag = colonIdx > 0 ? image.slice(colonIdx + 1) : "latest";
  const shortRepo = repo.split("/").pop() ?? repo;
  return { short: shortRepo, tag };
}

function formatTime(iso?: string): string {
  if (!iso) return "—";
  return new Date(iso).toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

function allIngressUrls(envs: ServiceEnv[]): string[] {
  const urls: string[] = [];
  const seen = new Set<string>();
  for (const env of envs) {
    for (const url of env.runtime.ingressUrls) {
      if (!seen.has(url)) {
        seen.add(url);
        urls.push(url);
      }
    }
  }
  return urls;
}

function latestImage(envs: ServiceEnv[]): RuntimeInfo | undefined {
  for (const env of envs) {
    if (env.runtime.image) return env.runtime;
  }
  return undefined;
}

// ---------------------------------------------------------------------------
// SVG Icons
// ---------------------------------------------------------------------------

const icons = {
  externalLink: (
    <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M13.5 6H5.25A2.25 2.25 0 0 0 3 8.25v10.5A2.25 2.25 0 0 0 5.25 21h10.5A2.25 2.25 0 0 0 18 18.75V10.5m-10.5 6L21 3m0 0h-5.25M21 3v5.25" />
    </svg>
  ),
  rocket: (
    <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M15.59 14.37a6 6 0 0 1-5.84 7.38v-4.8m5.84-2.58a14.98 14.98 0 0 0 6.16-12.12A14.98 14.98 0 0 0 9.631 8.41m5.96 5.96a14.926 14.926 0 0 1-5.841 2.58m-.119-8.54a6 6 0 0 0-7.381 5.84h4.8m2.581-5.84a14.927 14.927 0 0 0-2.58 5.84m2.699 2.7c-.103.021-.207.041-.311.06a15.09 15.09 0 0 1-2.448-2.448 14.9 14.9 0 0 1 .06-.312m-2.24 2.39a4.493 4.493 0 0 0-1.757 4.306 4.493 4.493 0 0 0 4.306-1.758M16.5 9a1.5 1.5 0 1 1-3 0 1.5 1.5 0 0 1 3 0Z" />
    </svg>
  ),
  arrowUp: (
    <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M3 4.5h14.25M3 9h9.75M3 13.5h5.25m5.25-.75L17.25 9m0 0L21 12.75M17.25 9v12" />
    </svg>
  ),
  terminal: (
    <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
      <path strokeLinecap="round" strokeLinejoin="round" d="m6.75 7.5 3 2.25-3 2.25m4.5 0h3m-9 8.25h13.5A2.25 2.25 0 0 0 21 18V6a2.25 2.25 0 0 0-2.25-2.25H5.25A2.25 2.25 0 0 0 3 6v12a2.25 2.25 0 0 0 2.25 2.25Z" />
    </svg>
  ),
  branch: (
    <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M7.217 10.907a2.25 2.25 0 1 0 0 2.186m0-2.186c.18.324.283.696.283 1.093s-.103.77-.283 1.093m0-2.186 9.566-5.314m-9.566 7.5 9.566 5.314m0 0a2.25 2.25 0 1 0 3.935 2.186 2.25 2.25 0 0 0-3.935-2.186Zm0-12.814a2.25 2.25 0 1 0 3.933-2.185 2.25 2.25 0 0 0-3.933 2.185Z" />
    </svg>
  ),
  lock: (
    <svg className="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M16.5 10.5V6.75a4.5 4.5 0 1 0-9 0v3.75m-.75 11.25h10.5a2.25 2.25 0 0 0 2.25-2.25v-6.75a2.25 2.25 0 0 0-2.25-2.25H6.75a2.25 2.25 0 0 0-2.25 2.25v6.75a2.25 2.25 0 0 0 2.25 2.25Z" />
    </svg>
  ),
  clock: (
    <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M12 6v6h4.5m4.5 0a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z" />
    </svg>
  ),
};

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function ServiceDetail() {
  const { project, service } = useParams<{
    project: string;
    service: string;
  }>();
  const navigate = useNavigate();
  const [data, setData] = useState<ServiceDetailInfo | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [showPreviewForm, setShowPreviewForm] = useState(false);
  const [previewName, setPreviewName] = useState("");
  const [previewError, setPreviewError] = useState<string | null>(null);
  const [previewSubmitting, setPreviewSubmitting] = useState(false);

  const [showPromoteModal, setShowPromoteModal] = useState(false);
  const [promoteTarget, setPromoteTarget] = useState("");
  const [promoteSubmitting, setPromoteSubmitting] = useState(false);
  const [promoteError, setPromoteError] = useState<string | null>(null);
  const [promoteResult, setPromoteResult] = useState<PromoteResponse | null>(
    null,
  );

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

  const status = overallStatus(data.environments);
  const current = latestImage(data.environments);
  const img = formatImage(current?.image);
  const urls = allIngressUrls(data.environments);
  const primaryUrl = urls[0];

  return (
    <div className="space-y-6">
      {/* Breadcrumb */}
      <nav className="flex items-center gap-1.5 text-sm text-gray-400">
        <Link to="/" className="hover:text-gray-600">Dashboard</Link>
        <span>/</span>
        <Link to={`/projects/${project}`} className="hover:text-gray-600">
          {project}
        </Link>
        <span>/</span>
        <span className="text-gray-600">{service}</span>
      </nav>

      {/* ---- Header + Actions ---- */}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0">
          <div className="flex items-center gap-3">
            <h1 className="truncate text-2xl font-semibold text-gray-900">
              {data.name}
            </h1>
            <StatusBadge status={status} size="lg" />
          </div>
          <div className="mt-1.5 flex flex-wrap items-center gap-x-4 gap-y-1 text-sm text-gray-500">
            <Link
              to={`/templates/${data.template.name}`}
              className="inline-flex items-center gap-1 font-mono text-gray-600 hover:text-gray-900"
            >
              {data.template.name}
              {data.template.version && (
                <span className="text-gray-400">v{data.template.version}</span>
              )}
            </Link>
            {current?.image && (
              <span className="inline-flex items-center gap-1 font-mono">
                {img.short}
                {img.tag && (
                  <span className="rounded bg-gray-100 px-1.5 py-0.5 text-xs text-gray-500">
                    {img.tag}
                  </span>
                )}
              </span>
            )}
          </div>
        </div>

        {/* Actions */}
        <div className="flex flex-shrink-0 flex-wrap gap-2">
          {primaryUrl && (
            <a
              href={primaryUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1.5 rounded-lg border border-gray-200 bg-white px-3.5 py-2 text-sm font-medium text-gray-700 shadow-sm transition-colors hover:bg-gray-50"
            >
              {icons.externalLink}
              Open app
            </a>
          )}
          <button
            className="inline-flex items-center gap-1.5 rounded-lg border border-gray-200 bg-white px-3.5 py-2 text-sm font-medium text-gray-700 shadow-sm transition-colors hover:bg-gray-50"
            title="View logs (coming soon)"
          >
            {icons.terminal}
            Logs
          </button>
          <button
            onClick={() => {
              setShowPreviewForm(true);
              setPreviewName("");
              setPreviewError(null);
            }}
            className="inline-flex items-center gap-1.5 rounded-lg border border-gray-200 bg-white px-3.5 py-2 text-sm font-medium text-gray-700 shadow-sm transition-colors hover:bg-gray-50"
            title="Create preview environment"
          >
            {icons.branch}
            Preview
          </button>
          <button
            onClick={() => {
              if (!data) return;
              const envs = data.environments
                .slice()
                .sort((a, b) => a.environment.localeCompare(b.environment));
              const last = envs[envs.length - 1] as { environment: string } | undefined;
              const highestEnv = last?.environment ?? "";
              setPromoteTarget(highestEnv);
              setPromoteError(null);
              setPromoteResult(null);
              setShowPromoteModal(true);
            }}
            className="inline-flex items-center gap-1.5 rounded-lg bg-gray-900 px-3.5 py-2 text-sm font-medium text-white shadow-sm transition-colors hover:bg-gray-700"
            title="Promote to next environment"
          >
            {icons.arrowUp}
            Promote
          </button>
        </div>
      </div>

      {/* ---- Create Preview Form ---- */}
      {showPreviewForm && (
        <div className="rounded-xl border border-indigo-200 bg-indigo-50/50 p-5">
          <div className="flex items-start gap-3">
            <div className="flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-lg bg-indigo-100 text-indigo-600">
              {icons.branch}
            </div>
            <div className="min-w-0 flex-1">
              <h3 className="text-sm font-medium text-gray-900">
                Create preview environment
              </h3>
              <p className="mt-0.5 text-xs text-gray-500">
                Deploy <span className="font-medium">{service}</span> to an
                isolated preview namespace.
              </p>
              <form
                className="mt-3 flex items-end gap-2"
                onSubmit={async (e) => {
                  e.preventDefault();
                  if (!previewName.trim() || !project || !service) return;
                  setPreviewSubmitting(true);
                  setPreviewError(null);
                  try {
                    await createPreview({
                      name: previewName.trim(),
                      project,
                      service,
                    });
                    setShowPreviewForm(false);
                    navigate("/previews");
                  } catch (err) {
                    setPreviewError(
                      err instanceof Error ? err.message : "Failed to create",
                    );
                  } finally {
                    setPreviewSubmitting(false);
                  }
                }}
              >
                <div className="flex-1">
                  <label
                    htmlFor="preview-name"
                    className="mb-1 block text-xs font-medium text-gray-700"
                  >
                    Preview name
                  </label>
                  <input
                    id="preview-name"
                    type="text"
                    value={previewName}
                    onChange={(e) => setPreviewName(e.target.value)}
                    placeholder="pr-42"
                    pattern="[a-z][a-z0-9-]*[a-z0-9]"
                    required
                    className="w-full rounded-md border border-gray-300 bg-white px-3 py-1.5 text-sm text-gray-900 placeholder-gray-400 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
                  />
                </div>
                <button
                  type="submit"
                  disabled={previewSubmitting || !previewName.trim()}
                  className="rounded-md bg-indigo-600 px-4 py-1.5 text-sm font-medium text-white transition-colors hover:bg-indigo-700 disabled:opacity-50"
                >
                  {previewSubmitting ? "Creating…" : "Create"}
                </button>
                <button
                  type="button"
                  onClick={() => setShowPreviewForm(false)}
                  className="rounded-md border border-gray-300 bg-white px-3 py-1.5 text-sm font-medium text-gray-600 transition-colors hover:bg-gray-50"
                >
                  Cancel
                </button>
              </form>
              {previewError && (
                <p className="mt-2 text-xs text-red-600">{previewError}</p>
              )}
            </div>
          </div>
        </div>
      )}

      {/* ---- Promote Modal ---- */}
      {showPromoteModal && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
          <div className="mx-4 w-full max-w-md rounded-2xl bg-white p-6 shadow-xl">
            {promoteResult ? (
              <>
                <div className="mb-4 flex h-10 w-10 items-center justify-center rounded-full bg-emerald-100">
                  <svg className="h-5 w-5 text-emerald-600" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
                    <path strokeLinecap="round" strokeLinejoin="round" d="m4.5 12.75 6 6 9-13.5" />
                  </svg>
                </div>
                <h3 className="text-lg font-semibold text-gray-900">
                  Promotion initiated
                </h3>
                <p className="mt-1 text-sm text-gray-500">
                  {promoteResult.message}
                </p>
                <dl className="mt-4 space-y-2 text-sm">
                  <div className="flex justify-between">
                    <dt className="text-gray-400">Source</dt>
                    <dd className="font-medium text-gray-900">{promoteResult.source}</dd>
                  </div>
                  <div className="flex justify-between">
                    <dt className="text-gray-400">Destination</dt>
                    <dd className="font-medium text-gray-900">{promoteResult.destination}</dd>
                  </div>
                  <div className="flex justify-between">
                    <dt className="text-gray-400">Namespace</dt>
                    <dd className="font-mono text-gray-600">{promoteResult.namespace}</dd>
                  </div>
                </dl>
                <button
                  onClick={() => setShowPromoteModal(false)}
                  className="mt-6 w-full rounded-lg bg-gray-900 py-2 text-sm font-medium text-white transition-colors hover:bg-gray-700"
                >
                  Done
                </button>
              </>
            ) : (
              <>
                <div className="mb-4 flex h-10 w-10 items-center justify-center rounded-full bg-gray-100 text-gray-600">
                  {icons.arrowUp}
                </div>
                <h3 className="text-lg font-semibold text-gray-900">
                  Promote {service}
                </h3>
                <p className="mt-1 text-sm text-gray-500">
                  Promote this service to a higher environment. This will sync
                  the configuration from the previous stage.
                </p>

                <div className="mt-4">
                  <label
                    htmlFor="promote-target"
                    className="mb-1.5 block text-sm font-medium text-gray-700"
                  >
                    Target environment
                  </label>
                  <select
                    id="promote-target"
                    value={promoteTarget}
                    onChange={(e) => setPromoteTarget(e.target.value)}
                    className="w-full rounded-md border border-gray-300 bg-white px-3 py-2 text-sm text-gray-900 focus:border-gray-500 focus:outline-none focus:ring-1 focus:ring-gray-500"
                  >
                    {data.environments
                      .slice()
                      .sort((a, b) => {
                        const orderA = a.environment.localeCompare(b.environment);
                        return orderA;
                      })
                      .map((env) => (
                        <option key={env.environment} value={env.environment}>
                          {env.environment}
                        </option>
                      ))}
                  </select>
                </div>

                {promoteError && (
                  <div className="mt-3 rounded-md bg-red-50 px-3 py-2">
                    <p className="text-sm text-red-700">{promoteError}</p>
                  </div>
                )}

                <div className="mt-6 flex gap-3">
                  <button
                    onClick={async () => {
                      if (!project || !service || !promoteTarget) return;
                      setPromoteSubmitting(true);
                      setPromoteError(null);
                      try {
                        const result = await promoteService(project, service, {
                          targetEnvironment: promoteTarget,
                        });
                        setPromoteResult(result);
                      } catch (err) {
                        setPromoteError(
                          err instanceof Error
                            ? err.message
                            : "Promotion failed",
                        );
                      } finally {
                        setPromoteSubmitting(false);
                      }
                    }}
                    disabled={promoteSubmitting || !promoteTarget}
                    className="flex-1 rounded-lg bg-gray-900 py-2 text-sm font-medium text-white transition-colors hover:bg-gray-700 disabled:opacity-50"
                  >
                    {promoteSubmitting ? "Promoting…" : "Promote"}
                  </button>
                  <button
                    onClick={() => setShowPromoteModal(false)}
                    disabled={promoteSubmitting}
                    className="flex-1 rounded-lg border border-gray-300 bg-white py-2 text-sm font-medium text-gray-700 transition-colors hover:bg-gray-50 disabled:opacity-50"
                  >
                    Cancel
                  </button>
                </div>
              </>
            )}
          </div>
        </div>
      )}

      {/* ---- Quick stats row ---- */}
      <div className="grid gap-4 sm:grid-cols-4">
        <QuickStat label="Environments" value={String(data.environments.length)} />
        <QuickStat
          label="Replicas"
          value={
            current
              ? `${current.available}/${current.replicas}`
              : "—"
          }
        />
        <QuickStat label="Image tag" value={img.tag || "—"} mono />
        <QuickStat
          label="Last deployed"
          value={
            current?.lastDeployed
              ? formatTime(current.lastDeployed)
              : "—"
          }
        />
      </div>

      {/* ---- URLs ---- */}
      {urls.length > 0 && (
        <div className="rounded-xl border border-gray-200 bg-white">
          <div className="border-b border-gray-100 px-5 py-3">
            <h2 className="text-xs font-medium uppercase tracking-wider text-gray-400">
              Endpoints
            </h2>
          </div>
          <div className="divide-y divide-gray-50">
            {urls.map((url) => (
              <div
                key={url}
                className="flex items-center justify-between px-5 py-2.5"
              >
                <a
                  href={url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="text-sm text-blue-600 hover:text-blue-800 hover:underline"
                >
                  {url}
                </a>
                <a
                  href={url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="text-gray-400 hover:text-gray-600"
                >
                  {icons.externalLink}
                </a>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* ---- Environments ---- */}
      <div className="rounded-xl border border-gray-200 bg-white">
        <div className="border-b border-gray-100 px-5 py-3">
          <h2 className="text-xs font-medium uppercase tracking-wider text-gray-400">
            Environments
          </h2>
        </div>
        {data.environments.length === 0 ? (
          <div className="px-5 py-10 text-center">
            <p className="text-sm text-gray-400">
              No environments configured for this project.
            </p>
          </div>
        ) : (
          <div className="grid gap-px bg-gray-100 sm:grid-cols-2 lg:grid-cols-3">
            {data.environments.map((env) => (
              <EnvCard key={env.environment} env={env} />
            ))}
          </div>
        )}
      </div>

      {/* ---- Configuration ---- */}
      {Object.keys(data.values).length > 0 && (
        <div className="rounded-xl border border-gray-200 bg-white">
          <div className="border-b border-gray-100 px-5 py-3">
            <h2 className="text-xs font-medium uppercase tracking-wider text-gray-400">
              Configuration
            </h2>
          </div>
          <dl className="divide-y divide-gray-50">
            {Object.entries(data.values).map(([key, val]) => (
              <div key={key} className="flex items-center justify-between px-5 py-2.5">
                <dt className="font-mono text-sm text-gray-500">{key}</dt>
                <dd className="text-sm text-gray-900">{String(val)}</dd>
              </div>
            ))}
          </dl>
        </div>
      )}

      {/* ---- Secrets ---- */}
      {data.secretRefs.length > 0 && (
        <div className="rounded-xl border border-gray-200 bg-white">
          <div className="border-b border-gray-100 px-5 py-3">
            <h2 className="text-xs font-medium uppercase tracking-wider text-gray-400">
              Secrets
            </h2>
          </div>
          <dl className="divide-y divide-gray-50">
            {data.secretRefs.map((ref) => (
              <div key={ref.name} className="flex items-center justify-between px-5 py-2.5">
                <dt className="font-mono text-sm text-gray-500">{ref.name}</dt>
                <dd className="flex items-center gap-1.5 text-sm text-gray-600">
                  {icons.lock}
                  <span className="font-mono">{ref.secretRef}</span>
                </dd>
              </div>
            ))}
          </dl>
        </div>
      )}

      {/* ---- Deployment History (placeholder) ---- */}
      <div className="rounded-xl border border-gray-200 bg-white">
        <div className="border-b border-gray-100 px-5 py-3">
          <h2 className="text-xs font-medium uppercase tracking-wider text-gray-400">
            Deployment history
          </h2>
        </div>
        <div className="px-5 py-10 text-center">
          <div className="mx-auto mb-3 flex h-10 w-10 items-center justify-center rounded-full bg-gray-100 text-gray-400">
            {icons.clock}
          </div>
          <p className="text-sm font-medium text-gray-500">
            No deployment history available yet
          </p>
          <p className="mt-1 text-xs text-gray-400">
            History will appear here once promotions are tracked via ArgoCD.
          </p>
        </div>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Subcomponents
// ---------------------------------------------------------------------------

function QuickStat({
  label,
  value,
  mono,
}: {
  label: string;
  value: string;
  mono?: boolean;
}) {
  return (
    <div className="rounded-xl border border-gray-200 bg-white px-4 py-3">
      <p className="text-xs text-gray-400">{label}</p>
      <p
        className={`mt-0.5 text-lg font-semibold text-gray-900 ${mono ? "font-mono" : ""}`}
      >
        {value}
      </p>
    </div>
  );
}

function EnvCard({ env }: { env: ServiceEnv }) {
  const rt = env.runtime;
  const img = formatImage(rt.image);
  const deployed = formatTime(rt.lastDeployed);

  return (
    <div className="bg-white p-5">
      <div className="flex items-center justify-between">
        <span className="text-sm font-semibold capitalize text-gray-900">
          {env.environment}
        </span>
        <StatusBadge status={rt.status} />
      </div>

      <dl className="mt-3 space-y-1.5 text-xs">
        <div className="flex justify-between">
          <dt className="text-gray-400">Image</dt>
          <dd className="font-mono text-gray-600" title={rt.image}>
            {img.short}
            {img.tag && (
              <span className="ml-1 rounded bg-gray-100 px-1 py-0.5 text-gray-500">
                {img.tag}
              </span>
            )}
          </dd>
        </div>
        {rt.status !== "not_deployed" && (
          <div className="flex justify-between">
            <dt className="text-gray-400">Instances</dt>
            <dd className="text-gray-600">
              {rt.available}
              <span className="text-gray-400">/{rt.replicas}</span>
            </dd>
          </div>
        )}
        <div className="flex justify-between">
          <dt className="text-gray-400">Namespace</dt>
          <dd className="font-mono text-gray-600">{env.namespace}</dd>
        </div>
        <div className="flex justify-between">
          <dt className="text-gray-400">Deployed</dt>
          <dd className="text-gray-600">{deployed}</dd>
        </div>
      </dl>

      {rt.ingressUrls.length > 0 && (
        <div className="mt-3 flex flex-wrap gap-1.5">
          {rt.ingressUrls.map((url) => (
            <a
              key={url}
              href={url}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1 rounded-md border border-gray-200 px-2 py-1 text-xs text-blue-600 transition-colors hover:border-blue-300 hover:bg-blue-50"
            >
              {url.replace(/^https?:\/\//, "")}
              <svg className="h-3 w-3" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
                <path strokeLinecap="round" strokeLinejoin="round" d="M13.5 6H5.25A2.25 2.25 0 0 0 3 8.25v10.5A2.25 2.25 0 0 0 5.25 21h10.5A2.25 2.25 0 0 0 18 18.75V10.5m-10.5 6L21 3m0 0h-5.25M21 3v5.25" />
              </svg>
            </a>
          ))}
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Skeleton
// ---------------------------------------------------------------------------

function DetailSkeleton() {
  return (
    <div className="space-y-6">
      <div className="h-4 w-40 animate-pulse rounded bg-gray-100" />
      <div className="flex items-start justify-between">
        <div className="space-y-2">
          <div className="h-8 w-56 animate-pulse rounded bg-gray-100" />
          <div className="h-5 w-72 animate-pulse rounded bg-gray-50" />
        </div>
        <div className="flex gap-2">
          {[1, 2, 3, 4].map((n) => (
            <div key={n} className="h-9 w-24 animate-pulse rounded-lg bg-gray-100" />
          ))}
        </div>
      </div>
      <div className="grid gap-4 sm:grid-cols-4">
        {[1, 2, 3, 4].map((n) => (
          <div key={n} className="h-16 animate-pulse rounded-xl bg-gray-50" />
        ))}
      </div>
      <div className="h-40 animate-pulse rounded-xl bg-gray-50" />
      <div className="h-32 animate-pulse rounded-xl bg-gray-50" />
    </div>
  );
}
