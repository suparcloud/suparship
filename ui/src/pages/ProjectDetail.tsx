import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { toast } from "sonner";

import { listApps } from "../lib/apps";
import { createStack, listStacks } from "../lib/stacks";
import type { Stack } from "../lib/stacks";
import { ApiError } from "../lib/api";
import { AppTable } from "../components/AppTable";
import type { AppSummary } from "../types";

// --- Component ---

export function ProjectDetail() {
  const { project } = useParams<{ project: string }>();
  const navigate = useNavigate();
  const [apps, setApps] = useState<AppSummary[]>([]);
  const [stacks, setStacks] = useState<Stack[]>([]);
  const [newStack, setNewStack] = useState<string | null>(null); // null = form hidden
  // The page shell (header, actions) needs no data — it paints immediately from
  // the URL. Only the app table waits on the status-enriched listApps, so it gets
  // its own loading flag and streams in rather than blocking the whole page.
  const [appsLoading, setAppsLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!project) return;
    listStacks(project)
      .then((r) => setStacks(r.stacks))
      .catch(() => setStacks([]));
  }, [project]);

  // Two-phase load: the brief list (store reads only) paints every app name,
  // link, and env column immediately so the user can navigate; the enriched
  // list (live per-env status — the slow call) replaces it when it lands,
  // with status cells pulsing in between.
  const [statusPending, setStatusPending] = useState(true);

  useEffect(() => {
    if (!project) return;
    let cancelled = false;

    listApps(project, { brief: true })
      .then((data) => {
        if (cancelled) return;
        // Don't clobber the enriched result if it somehow won the race.
        setApps((prev) => (prev.length > 0 ? prev : data.apps));
        setAppsLoading(false);
      })
      .catch(() => {
        /* the enriched fetch below is the fallback error path */
      });

    listApps(project)
      .then((data) => {
        if (cancelled) return;
        setApps(data.apps);
        setAppsLoading(false);
        setStatusPending(false);
      })
      .catch((err) => {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : "Failed to load");
          setAppsLoading(false);
          setStatusPending(false);
        }
      });

    return () => {
      cancelled = true;
    };
  }, [project]);

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
              {appsLoading ? (
                <span className="inline-block h-4 w-24 animate-pulse rounded bg-gray-100 align-middle" />
              ) : (
                <>
                  {totalCt} {totalCt === 1 ? "app" : "apps"}
                  {!statusPending && healthyCt > 0 && (
                    <span className="text-emerald-600">
                      {" "}
                      &middot; {healthyCt} healthy
                    </span>
                  )}
                </>
              )}
            </p>
          </div>
          <div className="flex items-center gap-2">
            <button
              onClick={() => setNewStack("")}
              className="rounded-lg border border-gray-300 px-4 py-2 text-sm font-medium text-gray-700 transition-colors hover:bg-gray-50"
            >
              New stack
            </button>
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

      {/* New stack inline form */}
      {newStack !== null && (
        <div className="flex items-center gap-2 rounded-xl border border-gray-200 bg-white p-4">
          <input
            autoFocus
            value={newStack}
            onChange={(e) => setNewStack(e.target.value)}
            placeholder="stack-name (e.g. voiceai)"
            className="w-64 rounded-md border border-gray-300 px-3 py-1.5 font-mono text-sm"
          />
          <button
            onClick={async () => {
              const name = (newStack ?? "").trim();
              if (!name || !project) return;
              try {
                await createStack(project, { name });
                navigate(`/projects/${encodeURIComponent(project)}/stacks/${encodeURIComponent(name)}`);
              } catch (err) {
                toast.error(err instanceof ApiError ? err.message : "Failed to create stack");
              }
            }}
            className="rounded-md bg-gray-900 px-3 py-1.5 text-sm font-medium text-white hover:bg-gray-700"
          >
            Create
          </button>
          <button onClick={() => setNewStack(null)} className="px-2 text-sm text-gray-500 hover:text-gray-700">
            Cancel
          </button>
        </div>
      )}

      {/* Unified apps + stacks table — streams in after the shell paints */}
      {appsLoading ? (
        <AppTableSkeleton />
      ) : error ? (
        <div className="rounded-lg border border-red-200 bg-red-50 p-4">
          <p className="text-sm text-red-700">Failed to load apps: {error}</p>
        </div>
      ) : apps.length === 0 ? (
        <EmptyApps project={project!} />
      ) : (
        <div className="rounded-xl border border-gray-200 bg-white p-5">
          <AppTable project={project!} apps={apps} stacks={stacks} statusPending={statusPending} />
        </div>
      )}
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

// AppTableSkeleton stands in for the apps table while the status-enriched listApps
// resolves, so the page shell stays visible instead of a full-page placeholder.
function AppTableSkeleton() {
  return (
    <div className="rounded-xl border border-gray-200 bg-white p-5">
      <div className="space-y-3">
        {[1, 2, 3, 4].map((n) => (
          <div key={n} className="h-10 animate-pulse rounded bg-gray-50" />
        ))}
      </div>
    </div>
  );
}
