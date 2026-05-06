import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";

import { getAppEnvironments, listApps } from "../lib/apps";
import type { AppEnvironmentSummary, AppSummary } from "../types";

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

// --- App with enriched env data ---

interface AppWithEnvs extends AppSummary {
  environments: AppEnvironmentSummary[];
  envsLoaded: boolean;
}

// --- Component ---

export function ProjectDetail() {
  const { project } = useParams<{ project: string }>();
  const [apps, setApps] = useState<AppWithEnvs[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!project) return;
    let cancelled = false;

    listApps(project)
      .then(async (data) => {
        if (cancelled) return;

        // Seed apps immediately for a responsive first paint
        const initial: AppWithEnvs[] = data.apps.map((a) => ({
          ...a,
          environments: [],
          envsLoaded: false,
        }));
        setApps(initial);
        setLoading(false);

        // Enrich each app with per-environment status in parallel
        const envResults = await Promise.allSettled(
          data.apps.map((a) => getAppEnvironments(project, a.name)),
        );

        if (cancelled) return;

        setApps((prev) =>
          prev.map((app, i) => {
            const result = envResults[i];
            if (result && result.status === "fulfilled") {
              return {
                ...app,
                environments: result.value.environments,
                envsLoaded: true,
              };
            }
            return { ...app, envsLoaded: true };
          }),
        );
      })
      .catch((err) => {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : "Failed to load");
          setLoading(false);
        }
      });

    return () => {
      cancelled = true;
    };
  }, [project]);

  if (loading) return <ProjectSkeleton />;

  if (error) {
    return (
      <div className="rounded-lg border border-red-200 bg-red-50 p-4">
        <p className="text-sm text-red-700">Failed to load project: {error}</p>
      </div>
    );
  }

  const healthyCt = apps.filter((a) => a.status.phase === "healthy").length;
  const totalCt = apps.length;

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
              {totalCt} {totalCt === 1 ? "app" : "apps"}
              {healthyCt > 0 && (
                <span className="text-emerald-600">
                  {" "}
                  &middot; {healthyCt} healthy
                </span>
              )}
            </p>
          </div>
          <div className="flex items-center gap-2">
            <Link
              to={`/projects/${project}/settings`}
              className="rounded-lg border border-gray-300 px-4 py-2 text-sm font-medium text-gray-700 transition-colors hover:bg-gray-50"
            >
              Settings
            </Link>
            <Link
              to={`/projects/${project}/apps/new`}
              className="rounded-lg bg-gray-900 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-gray-700"
            >
              New app
            </Link>
          </div>
        </div>
      </div>

      {/* App grid */}
      {apps.length === 0 ? (
        <EmptyApps project={project!} />
      ) : (
        <div className="grid gap-4 sm:grid-cols-2">
          {apps.map((app) => (
            <AppCard key={app.name} project={project!} app={app} />
          ))}
        </div>
      )}
    </div>
  );
}

// --- App card ---

function AppCard({ project, app }: { project: string; app: AppWithEnvs }) {
  const stagingEnv = app.environments.find((e) => e.envType === "staging");
  const prodEnv = app.environments.find((e) => e.envType === "prod");
  const previewCount = app.environments.filter(
    (e) => e.envType === "preview",
  ).length;

  // Prefer staging URL for the "Open" quick link, fall back to prod or summary URL
  const firstUrl =
    stagingEnv?.urls[0] ?? prodEnv?.urls[0] ?? app.urls[0] ?? null;

  return (
    <div className="flex flex-col rounded-xl border border-gray-200 bg-white p-5 transition-shadow hover:shadow-sm">
      {/* Name + quick actions */}
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <Link
            to={`/projects/${project}/apps/${app.name}`}
            className="text-sm font-semibold text-gray-900 hover:text-gray-600"
          >
            {app.displayName ?? app.name}
          </Link>
          {app.description && (
            <p className="mt-0.5 truncate text-xs text-gray-400">
              {app.description}
            </p>
          )}
        </div>
        <div className="flex flex-shrink-0 items-center gap-2">
          {firstUrl && (
            <a
              href={firstUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="rounded-md border border-gray-200 px-2.5 py-1 text-xs font-medium text-gray-600 transition-colors hover:bg-gray-50"
            >
              Open ↗
            </a>
          )}
          <Link
            to={`/projects/${project}/apps/${app.name}`}
            className="rounded-md bg-gray-900 px-2.5 py-1 text-xs font-medium text-white transition-colors hover:bg-gray-700"
          >
            Details
          </Link>
        </div>
      </div>

      {/* Template badge */}
      <div className="mt-3">
        <span className="rounded bg-gray-100 px-2 py-0.5 font-mono text-xs text-gray-600">
          {app.template.name}
        </span>
        {app.template.version && (
          <span className="ml-1 text-xs text-gray-400">
            v{app.template.version}
          </span>
        )}
      </div>

      {/* Environment status row */}
      <div className="mt-4 flex flex-wrap items-center gap-3 border-t border-gray-50 pt-4">
        {!app.envsLoaded ? (
          <div className="h-5 w-40 animate-pulse rounded bg-gray-100" />
        ) : (
          <>
            <EnvStatus label="staging" env={stagingEnv} />
            <EnvStatus label="prod" env={prodEnv} />
            {previewCount > 0 && (
              <span className="inline-flex items-center gap-1 rounded-full bg-purple-50 px-2.5 py-0.5 text-xs font-medium text-purple-700">
                <span className="h-1.5 w-1.5 rounded-full bg-purple-500" />
                {previewCount} {previewCount === 1 ? "preview" : "previews"}
              </span>
            )}
          </>
        )}
      </div>
    </div>
  );
}

// --- Environment status pill ---

function EnvStatus({
  label,
  env,
}: {
  label: string;
  env: AppEnvironmentSummary | undefined;
}) {
  const phase = env?.status.phase ?? "not_deployed";
  return (
    <div className="flex items-center gap-1.5">
      <span className="text-xs text-gray-400">{label}</span>
      <StatusBadge status={phase} />
    </div>
  );
}

// --- Empty state ---

function EmptyApps({ project }: { project: string }) {
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
            d="m21 7.5-9-5.25L3 7.5m18 0-9 5.25m9-5.25v9l-9 5.25M3 7.5l9 5.25M3 7.5v9l9 5.25"
          />
        </svg>
      </div>
      <h3 className="text-sm font-medium text-gray-900">No apps yet</h3>
      <p className="mt-1 text-sm text-gray-500">
        Deploy your first app to get started.
      </p>
      <Link
        to={`/projects/${project}/apps/new`}
        className="mt-4 inline-block rounded-lg bg-gray-900 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-gray-700"
      >
        Create app
      </Link>
    </div>
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
      <div className="grid gap-4 sm:grid-cols-2">
        {[1, 2, 3, 4].map((n) => (
          <div key={n} className="h-40 animate-pulse rounded-xl bg-gray-50" />
        ))}
      </div>
    </div>
  );
}
