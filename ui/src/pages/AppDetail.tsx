import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";

import { getApp, getAppEnvironment } from "../lib/apps";
import { createPreview } from "../lib/previews";
import { promoteService } from "../lib/services";
import type {
  AppDetail as AppDetailType,
  AppEnvironmentSummary,
  ComponentSummary,
  PromoteResponse,
} from "../types";

// ---------------------------------------------------------------------------
// Tab types
// ---------------------------------------------------------------------------

type TabId = "overview" | "deployments" | "previews" | "logs" | "traffic";

const TABS: { id: TabId; label: string }[] = [
  { id: "overview", label: "Overview" },
  { id: "deployments", label: "Deployments" },
  { id: "previews", label: "Previews" },
  { id: "logs", label: "Logs" },
  { id: "traffic", label: "Traffic" },
];

// ---------------------------------------------------------------------------
// Status helpers
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

function overallPhase(envs: AppEnvironmentSummary[]): string {
  const active = envs.filter((e) => e.envType !== "preview");
  if (active.length === 0) return "not_deployed";
  const phases = active.map((e) => e.status.phase);
  if (phases.every((p) => p === "healthy")) return "healthy";
  if (phases.some((p) => p === "degraded")) return "degraded";
  if (phases.some((p) => p === "progressing")) return "progressing";
  if (phases.every((p) => p === "not_deployed")) return "not_deployed";
  return "degraded";
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function formatTime(iso?: string): string {
  if (!iso) return "—";
  return new Date(iso).toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

// ---------------------------------------------------------------------------
// Icons
// ---------------------------------------------------------------------------

const icons = {
  externalLink: (
    <svg
      className="h-4 w-4"
      fill="none"
      viewBox="0 0 24 24"
      stroke="currentColor"
      strokeWidth={1.5}
    >
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M13.5 6H5.25A2.25 2.25 0 0 0 3 8.25v10.5A2.25 2.25 0 0 0 5.25 21h10.5A2.25 2.25 0 0 0 18 18.75V10.5m-10.5 6L21 3m0 0h-5.25M21 3v5.25"
      />
    </svg>
  ),
  rocket: (
    <svg
      className="h-4 w-4"
      fill="none"
      viewBox="0 0 24 24"
      stroke="currentColor"
      strokeWidth={1.5}
    >
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M15.59 14.37a6 6 0 0 1-5.84 7.38v-4.8m5.84-2.58a14.98 14.98 0 0 0 6.16-12.12A14.98 14.98 0 0 0 9.631 8.41m5.96 5.96a14.926 14.926 0 0 1-5.841 2.58m-.119-8.54a6 6 0 0 0-7.381 5.84h4.8m2.581-5.84a14.927 14.927 0 0 0-2.58 5.84m2.699 2.7c-.103.021-.207.041-.311.06a15.09 15.09 0 0 1-2.448-2.448 14.9 14.9 0 0 1 .06-.312m-2.24 2.39a4.493 4.493 0 0 0-1.757 4.306 4.493 4.493 0 0 0 4.306-1.758M16.5 9a1.5 1.5 0 1 1-3 0 1.5 1.5 0 0 1 3 0Z"
      />
    </svg>
  ),
  branch: (
    <svg
      className="h-4 w-4"
      fill="none"
      viewBox="0 0 24 24"
      stroke="currentColor"
      strokeWidth={1.5}
    >
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M7.217 10.907a2.25 2.25 0 1 0 0 2.186m0-2.186c.18.324.283.696.283 1.093s-.103.77-.283 1.093m0-2.186 9.566-5.314m-9.566 7.5 9.566 5.314m0 0a2.25 2.25 0 1 0 3.935 2.186 2.25 2.25 0 0 0-3.935-2.186Zm0-12.814a2.25 2.25 0 1 0 3.933-2.185 2.25 2.25 0 0 0-3.933 2.185Z"
      />
    </svg>
  ),
  terminal: (
    <svg
      className="h-4 w-4"
      fill="none"
      viewBox="0 0 24 24"
      stroke="currentColor"
      strokeWidth={1.5}
    >
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="m6.75 7.5 3 2.25-3 2.25m4.5 0h3m-9 8.25h13.5A2.25 2.25 0 0 0 21 18V6a2.25 2.25 0 0 0-2.25-2.25H5.25A2.25 2.25 0 0 0 3 6v12a2.25 2.25 0 0 0 2.25 2.25Z"
      />
    </svg>
  ),
  lock: (
    <svg
      className="h-3.5 w-3.5"
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
  ),
  clock: (
    <svg
      className="h-4 w-4"
      fill="none"
      viewBox="0 0 24 24"
      stroke="currentColor"
      strokeWidth={1.5}
    >
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M12 6v6h4.5m4.5 0a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z"
      />
    </svg>
  ),
};

// ---------------------------------------------------------------------------
// AppDetail page
// ---------------------------------------------------------------------------

export function AppDetail() {
  const { project, app: appName } = useParams<{
    project: string;
    app: string;
  }>();
  const navigate = useNavigate();

  const [data, setData] = useState<AppDetailType | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [activeTab, setActiveTab] = useState<TabId>("overview");
  const [selectedEnvName, setSelectedEnvName] = useState<string | null>(null);
  // Freshly-fetched detail for the selected environment; falls back to the
  // summary embedded in the app detail response when unavailable.
  const [selectedEnvDetail, setSelectedEnvDetail] =
    useState<AppEnvironmentSummary | null>(null);

  // Preview form
  const [showPreviewForm, setShowPreviewForm] = useState(false);
  const [previewName, setPreviewName] = useState("");
  const [previewError, setPreviewError] = useState<string | null>(null);
  const [previewSubmitting, setPreviewSubmitting] = useState(false);

  // Promote modal
  const [showPromoteModal, setShowPromoteModal] = useState(false);
  const [promoteTarget, setPromoteTarget] = useState("");
  const [promoteSubmitting, setPromoteSubmitting] = useState(false);
  const [promoteError, setPromoteError] = useState<string | null>(null);
  const [promoteResult, setPromoteResult] = useState<PromoteResponse | null>(
    null,
  );

  useEffect(() => {
    if (!project || !appName) return;
    let cancelled = false;

    getApp(project, appName)
      .then((res) => {
        if (cancelled) return;
        setData(res.app);
        const staging = res.app.environments.find(
          (e) => e.envType === "staging",
        );
        const first = res.app.environments[0];
        setSelectedEnvName(staging?.envName ?? first?.envName ?? null);
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
  }, [project, appName]);

  // When the user switches environments, fetch the specific env detail for
  // fresh runtime data. Errors are silently swallowed; the embedded summary
  // from the app detail response is used as fallback.
  useEffect(() => {
    if (!project || !appName || !selectedEnvName) {
      setSelectedEnvDetail(null);
      return;
    }
    let cancelled = false;

    getAppEnvironment(project, appName, selectedEnvName)
      .then((res) => {
        if (!cancelled) setSelectedEnvDetail(res.environment);
      })
      .catch(() => {
        if (!cancelled) setSelectedEnvDetail(null);
      });

    return () => {
      cancelled = true;
    };
  }, [project, appName, selectedEnvName]);

  if (loading) return <DetailSkeleton />;

  if (error) {
    return (
      <div className="rounded-lg border border-red-200 bg-red-50 p-4">
        <p className="text-sm text-red-700">Failed to load app: {error}</p>
      </div>
    );
  }

  if (!data) return null;

  const nonPreviewEnvs = data.environments.filter(
    (e) => e.envType !== "preview",
  );
  const previewEnvs = data.environments.filter((e) => e.envType === "preview");
  // The embedded summary from the app response; used as a fallback.
  const currentEnvSummary =
    data.environments.find((e) => e.envName === selectedEnvName) ?? null;
  // Prefer the freshly-fetched env detail when available; fall back to summary.
  const currentEnv: AppEnvironmentSummary | null =
    selectedEnvDetail ?? currentEnvSummary;
  const overallStatus = overallPhase(data.environments);
  const primaryUrl =
    currentEnv?.urls[0] ??
    data.environments.find((e) => e.urls.length > 0)?.urls[0];

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
        <span className="text-gray-600">{appName}</span>
      </nav>

      {/* App header */}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0">
          <div className="flex items-center gap-3">
            <h1 className="truncate text-2xl font-semibold text-gray-900">
              {data.displayName ?? data.name}
            </h1>
            <StatusBadge status={overallStatus} size="lg" />
          </div>
          <div className="mt-1.5 flex flex-wrap items-center gap-x-4 gap-y-1 text-sm text-gray-500">
            <Link
              to={`/templates/${data.template.name}`}
              className="inline-flex items-center gap-1 font-mono text-gray-600 hover:text-gray-900"
            >
              {data.template.name}
              {data.template.version && (
                <span className="text-gray-400">
                  v{data.template.version}
                </span>
              )}
            </Link>
            {data.description && (
              <span className="text-gray-400">{data.description}</span>
            )}
          </div>
        </div>

        {/* Quick actions */}
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
            onClick={() => setActiveTab("logs")}
            className="inline-flex items-center gap-1.5 rounded-lg border border-gray-200 bg-white px-3.5 py-2 text-sm font-medium text-gray-700 shadow-sm transition-colors hover:bg-gray-50"
            title="View logs"
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
              const envs = nonPreviewEnvs
                .slice()
                .sort((a, b) => a.envName.localeCompare(b.envName));
              const last = envs[envs.length - 1];
              setPromoteTarget(last?.envName ?? "");
              setPromoteError(null);
              setPromoteResult(null);
              setShowPromoteModal(true);
            }}
            className="inline-flex items-center gap-1.5 rounded-lg bg-gray-900 px-3.5 py-2 text-sm font-medium text-white shadow-sm transition-colors hover:bg-gray-700"
            title="Promote to next environment"
          >
            {icons.rocket}
            Promote
          </button>
        </div>
      </div>

      {/* Preview form */}
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
                Deploy{" "}
                <span className="font-medium">{appName}</span> to an isolated
                preview namespace.
              </p>
              <form
                className="mt-3 flex items-end gap-2"
                onSubmit={async (e) => {
                  e.preventDefault();
                  if (!previewName.trim() || !project || !appName) return;
                  setPreviewSubmitting(true);
                  setPreviewError(null);
                  try {
                    await createPreview({
                      name: previewName.trim(),
                      project,
                      service: appName,
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

      {/* Promote modal */}
      {showPromoteModal && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
          <div className="mx-4 w-full max-w-md rounded-2xl bg-white p-6 shadow-xl">
            {promoteResult ? (
              <>
                <div className="mb-4 flex h-10 w-10 items-center justify-center rounded-full bg-emerald-100">
                  <svg
                    className="h-5 w-5 text-emerald-600"
                    fill="none"
                    viewBox="0 0 24 24"
                    stroke="currentColor"
                    strokeWidth={2}
                  >
                    <path
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      d="m4.5 12.75 6 6 9-13.5"
                    />
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
                    <dd className="font-medium text-gray-900">
                      {promoteResult.source}
                    </dd>
                  </div>
                  <div className="flex justify-between">
                    <dt className="text-gray-400">Destination</dt>
                    <dd className="font-medium text-gray-900">
                      {promoteResult.destination}
                    </dd>
                  </div>
                  <div className="flex justify-between">
                    <dt className="text-gray-400">Namespace</dt>
                    <dd className="font-mono text-gray-600">
                      {promoteResult.namespace}
                    </dd>
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
                  {icons.rocket}
                </div>
                <h3 className="text-lg font-semibold text-gray-900">
                  Promote {appName}
                </h3>
                <p className="mt-1 text-sm text-gray-500">
                  Promote this app to a higher environment.
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
                    {nonPreviewEnvs
                      .slice()
                      .sort((a, b) => a.envName.localeCompare(b.envName))
                      .map((env) => (
                        <option key={env.envName} value={env.envName}>
                          {env.envName}
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
                      if (!project || !appName || !promoteTarget) return;
                      setPromoteSubmitting(true);
                      setPromoteError(null);
                      try {
                        const result = await promoteService(
                          project,
                          appName,
                          { targetEnvironment: promoteTarget },
                        );
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

      {/* Environment switcher */}
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-xs font-medium uppercase tracking-wider text-gray-400">
          Env
        </span>
        {nonPreviewEnvs.map((env) => (
          <button
            key={env.envName}
            onClick={() => setSelectedEnvName(env.envName)}
            className={`inline-flex items-center gap-1.5 rounded-full px-3 py-1 text-xs font-medium capitalize transition-colors ${
              selectedEnvName === env.envName
                ? "bg-gray-900 text-white"
                : "bg-gray-100 text-gray-600 hover:bg-gray-200"
            }`}
          >
            <span
              className={`h-1.5 w-1.5 rounded-full ${
                selectedEnvName === env.envName
                  ? "bg-white/60"
                  : (statusStyles[env.status.phase] ?? fallbackStatus).dot
              }`}
            />
            {env.envName}
          </button>
        ))}
        {previewEnvs.length > 0 && (
          <>
            <span className="text-xs text-gray-300">|</span>
            {previewEnvs.map((env) => (
              <button
                key={env.envName}
                onClick={() => setSelectedEnvName(env.envName)}
                className={`inline-flex items-center gap-1.5 rounded-full px-3 py-1 text-xs font-medium transition-colors ${
                  selectedEnvName === env.envName
                    ? "bg-purple-700 text-white"
                    : "bg-purple-50 text-purple-700 hover:bg-purple-100"
                }`}
              >
                {env.preview?.previewName ?? env.envName}
              </button>
            ))}
          </>
        )}
      </div>

      {/* Tab bar */}
      <div className="border-b border-gray-200">
        <nav className="-mb-px flex gap-6">
          {TABS.map((tab) => (
            <button
              key={tab.id}
              onClick={() => setActiveTab(tab.id)}
              className={`pb-3 text-sm font-medium transition-colors ${
                activeTab === tab.id
                  ? "border-b-2 border-gray-900 text-gray-900"
                  : "text-gray-500 hover:text-gray-700"
              }`}
            >
              {tab.label}
            </button>
          ))}
        </nav>
      </div>

      {/* Tab panels */}
      {activeTab === "overview" && (
        <OverviewTab data={data} currentEnv={currentEnv} />
      )}
      {activeTab === "deployments" && <DeploymentsTab />}
      {activeTab === "previews" && (
        <PreviewsTab previewEnvs={previewEnvs} />
      )}
      {activeTab === "logs" && <LogsTab />}
      {activeTab === "traffic" && <TrafficTab />}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Tab: Overview
// ---------------------------------------------------------------------------

function OverviewTab({
  data,
  currentEnv,
}: {
  data: AppDetailType;
  currentEnv: AppEnvironmentSummary | null;
}) {
  const replicas = currentEnv
    ? `${currentEnv.status.available}/${currentEnv.status.replicas}`
    : "—";
  const releaseTag =
    currentEnv?.release?.tag ??
    (currentEnv?.release?.image
      ? currentEnv.release.image.split(":").pop() ?? "—"
      : "—");
  const lastDeployed = formatTime(currentEnv?.status.lastDeployed);
  const urls = currentEnv?.urls ?? [];
  const nonPreviewCount = data.environments.filter(
    (e) => e.envType !== "preview",
  ).length;

  return (
    <div className="space-y-6">
      {/* Environment context bar: status, primary URL, namespace */}
      {currentEnv ? (
        <div className="flex flex-wrap items-center justify-between gap-y-1.5 rounded-lg border border-gray-100 bg-gray-50/50 px-4 py-2.5">
          <div className="flex flex-wrap items-center gap-3">
            <StatusBadge status={currentEnv.status.phase} />
            {currentEnv.urls[0] && (
              <a
                href={currentEnv.urls[0]}
                target="_blank"
                rel="noopener noreferrer"
                className="flex items-center gap-1 text-sm text-blue-600 hover:underline"
              >
                {currentEnv.urls[0]}
                <span className="inline-flex">{icons.externalLink}</span>
              </a>
            )}
          </div>
          {/* Namespace shown subtly for advanced users; not the focus of the view */}
          <span
            className="font-mono text-xs text-gray-400"
            title="Kubernetes namespace"
          >
            {currentEnv.namespace}
          </span>
        </div>
      ) : (
        <div className="rounded-lg border border-dashed border-gray-200 bg-gray-50/50 px-4 py-8 text-center">
          <p className="text-sm text-gray-500">Not yet deployed</p>
          <p className="mt-1 text-xs text-gray-400">
            No environment data available for this selection.
          </p>
        </div>
      )}

      {/* Quick stats */}
      <div className="grid gap-4 sm:grid-cols-4">
        <QuickStat label="Environments" value={String(nonPreviewCount)} />
        <QuickStat label="Replicas" value={replicas} />
        <QuickStat
          label="Release"
          value={releaseTag !== "—" ? releaseTag : "—"}
          mono
        />
        <QuickStat label="Last deployed" value={lastDeployed} />
      </div>

      {/* Endpoints */}
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
                  <svg
                    className="h-4 w-4"
                    fill="none"
                    viewBox="0 0 24 24"
                    stroke="currentColor"
                    strokeWidth={1.5}
                  >
                    <path
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      d="M13.5 6H5.25A2.25 2.25 0 0 0 3 8.25v10.5A2.25 2.25 0 0 0 5.25 21h10.5A2.25 2.25 0 0 0 18 18.75V10.5m-10.5 6L21 3m0 0h-5.25M21 3v5.25"
                    />
                  </svg>
                </a>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Runtime component summaries for the selected environment */}
      <ComponentsTable components={data.components} currentEnv={currentEnv} />

      {/* Configuration */}
      {Object.keys(data.values).length > 0 && (
        <div className="rounded-xl border border-gray-200 bg-white">
          <div className="border-b border-gray-100 px-5 py-3">
            <h2 className="text-xs font-medium uppercase tracking-wider text-gray-400">
              Configuration
            </h2>
          </div>
          <dl className="divide-y divide-gray-50">
            {Object.entries(data.values).map(([key, val]) => (
              <div
                key={key}
                className="flex items-center justify-between px-5 py-2.5"
              >
                <dt className="font-mono text-sm text-gray-500">{key}</dt>
                <dd className="text-sm text-gray-900">{String(val)}</dd>
              </div>
            ))}
          </dl>
        </div>
      )}

      {/* Secrets */}
      {data.secretRefs.length > 0 && (
        <div className="rounded-xl border border-gray-200 bg-white">
          <div className="border-b border-gray-100 px-5 py-3">
            <h2 className="text-xs font-medium uppercase tracking-wider text-gray-400">
              Secrets
            </h2>
          </div>
          <dl className="divide-y divide-gray-50">
            {data.secretRefs.map((ref) => (
              <div
                key={ref.name}
                className="flex items-center justify-between px-5 py-2.5"
              >
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
    </div>
  );
}

// ---------------------------------------------------------------------------
// Tab: Deployments
// ---------------------------------------------------------------------------

function DeploymentsTab() {
  return (
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
          No deployment history yet
        </p>
        <p className="mt-1 text-xs text-gray-400">
          History will appear once promotions are tracked via ArgoCD.
        </p>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Tab: Previews
// ---------------------------------------------------------------------------

function PreviewsTab({
  previewEnvs,
}: {
  previewEnvs: AppEnvironmentSummary[];
}) {
  if (previewEnvs.length === 0) {
    return (
      <div className="rounded-xl border border-dashed border-gray-200 bg-white px-6 py-12 text-center">
        <p className="text-sm font-medium text-gray-500">
          No active preview environments
        </p>
        <p className="mt-1 text-xs text-gray-400">
          Use the Preview button above to create one.
        </p>
      </div>
    );
  }

  return (
    <div className="divide-y divide-gray-50 rounded-xl border border-gray-200 bg-white">
      {previewEnvs.map((env) => (
        <div
          key={env.envName}
          className="flex items-center justify-between px-5 py-3"
        >
          <div>
            <span className="text-sm font-medium text-gray-900">
              {env.preview?.previewName ?? env.envName}
            </span>
            <span className="ml-2 font-mono text-xs text-gray-400">
              {env.namespace}
            </span>
          </div>
          <div className="flex items-center gap-3">
            <StatusBadge status={env.status.phase} />
            {env.urls[0] && (
              <a
                href={env.urls[0]}
                target="_blank"
                rel="noopener noreferrer"
                className="text-xs text-blue-600 hover:underline"
              >
                Open ↗
              </a>
            )}
          </div>
        </div>
      ))}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Tab: Logs (shell placeholder)
// ---------------------------------------------------------------------------

function LogsTab() {
  return (
    <div className="rounded-xl border border-dashed border-gray-200 bg-white px-6 py-12 text-center">
      <div className="mx-auto mb-3 flex h-10 w-10 items-center justify-center rounded-full bg-gray-100 text-gray-400">
        {icons.terminal}
      </div>
      <p className="text-sm font-medium text-gray-500">
        Log viewer coming soon
      </p>
      <p className="mt-1 text-xs text-gray-400">
        Full streaming logs will be available in a future release.
      </p>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Tab: Traffic (placeholder)
// ---------------------------------------------------------------------------

function TrafficTab() {
  return (
    <div className="rounded-xl border border-dashed border-gray-200 bg-white px-6 py-12 text-center">
      <p className="text-sm font-medium text-gray-500">
        Traffic management coming soon
      </p>
      <p className="mt-1 text-xs text-gray-400">
        Canary and blue/green traffic controls will appear here.
      </p>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Subcomponents
// ---------------------------------------------------------------------------

function ComponentsTable({
  components,
  currentEnv,
}: {
  components: ComponentSummary[];
  currentEnv: AppEnvironmentSummary | null;
}) {
  const phase = currentEnv?.status.phase ?? "not_deployed";
  const totalReplicas = currentEnv?.status.replicas ?? 0;
  const availableReplicas = currentEnv?.status.available ?? 0;

  // Replica counts only make sense for scalable component types (web / worker).
  // When there is exactly one such component, we can attribute the env-level
  // replica count to it without ambiguity.
  const scalableCount = components.filter(
    (c) => c.type === "web" || c.type === "worker",
  ).length;
  const singleScalable = scalableCount === 1;

  return (
    <div className="rounded-xl border border-gray-200 bg-white">
      <div className="border-b border-gray-100 px-5 py-3">
        <h2 className="text-xs font-medium uppercase tracking-wider text-gray-400">
          Runtime units
        </h2>
      </div>

      {components.length === 0 ? (
        <div className="px-5 py-8 text-center">
          <p className="text-sm text-gray-400">No components configured</p>
          <p className="mt-1 text-xs text-gray-400">
            Component topology is derived from the template at deploy time.
          </p>
        </div>
      ) : (
        <div className="divide-y divide-gray-50">
          {components.map((comp) => {
            const isScalable = comp.type === "web" || comp.type === "worker";
            const showReplicas = isScalable && singleScalable && totalReplicas > 0;

            return (
              <div
                key={comp.name}
                className="flex items-center justify-between px-5 py-2.5"
              >
                <div className="flex items-center gap-2.5">
                  <span className="font-mono text-sm text-gray-900">
                    {comp.name}
                  </span>
                  <span className="rounded bg-gray-100 px-1.5 py-0.5 text-xs capitalize text-gray-500">
                    {comp.type}
                  </span>
                </div>

                <div className="flex items-center gap-3">
                  {showReplicas && (
                    <span className="text-xs text-gray-400">
                      {availableReplicas}/{totalReplicas} replicas
                    </span>
                  )}
                  <StatusBadge status={phase} />
                  {!comp.enabledInPreview && (
                    <span className="rounded-full bg-gray-100 px-2 py-0.5 text-xs text-gray-500">
                      no preview
                    </span>
                  )}
                </div>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}

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
            <div
              key={n}
              className="h-9 w-24 animate-pulse rounded-lg bg-gray-100"
            />
          ))}
        </div>
      </div>
      <div className="flex gap-2">
        {[1, 2].map((n) => (
          <div key={n} className="h-7 w-20 animate-pulse rounded-full bg-gray-100" />
        ))}
      </div>
      <div className="h-px w-full bg-gray-200" />
      <div className="grid gap-4 sm:grid-cols-4">
        {[1, 2, 3, 4].map((n) => (
          <div key={n} className="h-16 animate-pulse rounded-xl bg-gray-50" />
        ))}
      </div>
      <div className="h-40 animate-pulse rounded-xl bg-gray-50" />
    </div>
  );
}
