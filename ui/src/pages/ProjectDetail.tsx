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
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!project) return;
    listStacks(project)
      .then((r) => setStacks(r.stacks))
      .catch(() => setStacks([]));
  }, [project]);

  useEffect(() => {
    if (!project) return;
    let cancelled = false;

    // listApps now returns per-env status inline, so no per-app enrichment.
    listApps(project)
      .then((data) => {
        if (cancelled) return;
        setApps(data.apps);
        setLoading(false);
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

      {/* Unified apps + stacks table */}
      {apps.length === 0 ? (
        <EmptyApps project={project!} />
      ) : (
        <div className="rounded-xl border border-gray-200 bg-white p-5">
          <AppTable project={project!} apps={apps} stacks={stacks} />
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
