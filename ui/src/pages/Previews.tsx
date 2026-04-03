import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";

import { fetchPreviews, deletePreview } from "../lib/previews";
import type { PreviewEnvironment } from "../types";

// ---------------------------------------------------------------------------
// Grouping
// ---------------------------------------------------------------------------

interface AppGroup {
  project: string;
  /** App name — maps to the `service` field while legacy naming is in use. */
  appName: string;
  previews: PreviewEnvironment[];
}

function groupByApp(previews: PreviewEnvironment[]): AppGroup[] {
  const map = new Map<string, AppGroup>();
  for (const p of previews) {
    const key = `${p.project}/${p.service}`;
    if (!map.has(key)) {
      map.set(key, { project: p.project, appName: p.service, previews: [] });
    }
    map.get(key)!.previews.push(p);
  }
  return Array.from(map.values());
}

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
    label: "Building",
  },
};

function StatusBadge({ status }: { status: string }) {
  const cfg = statusStyles[status] ?? fallbackStatus;
  return (
    <span
      className={`inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-medium ${cfg.bg}`}
    >
      <span className={`h-1.5 w-1.5 rounded-full ${cfg.dot}`} />
      {cfg.label}
    </span>
  );
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function timeAgo(iso: string): string {
  const diff = Date.now() - new Date(iso).getTime();
  const mins = Math.floor(diff / 60000);
  if (mins < 1) return "just now";
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  return `${days}d ago`;
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function Previews() {
  const [previews, setPreviews] = useState<PreviewEnvironment[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<string | null>(null);

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    fetchPreviews()
      .then((d) => setPreviews(d.previews))
      .catch((err) =>
        setError(err instanceof Error ? err.message : "Failed to load"),
      )
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  async function handleDelete(name: string) {
    setDeleting(null);
    try {
      await deletePreview(name);
      setPreviews((prev) => prev.filter((p) => p.name !== name));
    } catch (err) {
      setError(
        err instanceof Error ? err.message : "Failed to delete preview",
      );
    }
  }

  if (loading) return <PreviewsSkeleton />;

  if (error) {
    return (
      <div className="space-y-6">
        <PageHeader count={0} />
        <div className="rounded-lg border border-red-200 bg-red-50 p-4">
          <p className="text-sm text-red-700">
            Failed to load previews: {error}
          </p>
        </div>
      </div>
    );
  }

  const groups = groupByApp(previews);

  return (
    <div className="space-y-6">
      <PageHeader count={previews.length} />

      {groups.length === 0 ? (
        <EmptyState />
      ) : (
        <div className="space-y-8">
          {groups.map((group) => (
            <AppGroupSection
              key={`${group.project}/${group.appName}`}
              group={group}
              deletingName={deleting}
              onRequestDelete={(name) => setDeleting(name)}
              onCancelDelete={() => setDeleting(null)}
              onConfirmDelete={handleDelete}
            />
          ))}
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// App group section
// ---------------------------------------------------------------------------

function AppGroupSection({
  group,
  deletingName,
  onRequestDelete,
  onCancelDelete,
  onConfirmDelete,
}: {
  group: AppGroup;
  deletingName: string | null;
  onRequestDelete: (name: string) => void;
  onCancelDelete: () => void;
  onConfirmDelete: (name: string) => void;
}) {
  const appUrl = `/projects/${group.project}/apps/${group.appName}`;
  return (
    <div>
      {/* Group header */}
      <div className="mb-3 flex items-center justify-between">
        <div className="flex items-center gap-2">
          <div className="flex h-7 w-7 items-center justify-center rounded-md bg-indigo-50 text-indigo-600">
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
                d="M6.75 7.5l3 2.25-3 2.25m4.5 0h3m-9 8.25h13.5A2.25 2.25 0 0 0 21 18V6a2.25 2.25 0 0 0-2.25-2.25H5.25A2.25 2.25 0 0 0 3 6v12a2.25 2.25 0 0 0 2.25 2.25Z"
              />
            </svg>
          </div>
          <div className="flex items-center gap-1.5 text-sm">
            <Link
              to={`/projects/${group.project}`}
              className="text-gray-400 hover:text-gray-600"
            >
              {group.project}
            </Link>
            <span className="text-gray-300">/</span>
            <span className="font-semibold text-gray-900">
              {group.appName}
            </span>
            <span className="ml-1 rounded-full bg-gray-100 px-2 py-0.5 text-xs text-gray-500">
              {group.previews.length}{" "}
              {group.previews.length === 1 ? "preview" : "previews"}
            </span>
          </div>
        </div>
        <Link
          to={appUrl}
          className="inline-flex items-center gap-1 rounded-md border border-gray-200 bg-white px-2.5 py-1 text-xs font-medium text-gray-600 transition-colors hover:bg-gray-50"
        >
          View app
          <svg
            className="h-3 w-3"
            fill="none"
            viewBox="0 0 24 24"
            stroke="currentColor"
            strokeWidth={2}
          >
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              d="m8.25 4.5 7.5 7.5-7.5 7.5"
            />
          </svg>
        </Link>
      </div>

      {/* Preview cards */}
      <div className="space-y-2">
        {group.previews.map((p) => (
          <PreviewCard
            key={p.name}
            preview={p}
            appUrl={appUrl}
            isConfirmingDelete={deletingName === p.name}
            onRequestDelete={() => onRequestDelete(p.name)}
            onCancelDelete={onCancelDelete}
            onConfirmDelete={() => onConfirmDelete(p.name)}
          />
        ))}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Preview card
// ---------------------------------------------------------------------------

function PreviewCard({
  preview,
  appUrl,
  isConfirmingDelete,
  onRequestDelete,
  onCancelDelete,
  onConfirmDelete,
}: {
  preview: PreviewEnvironment;
  appUrl: string;
  isConfirmingDelete: boolean;
  onRequestDelete: () => void;
  onCancelDelete: () => void;
  onConfirmDelete: () => void;
}) {
  return (
    <div className="overflow-hidden rounded-xl border border-gray-200 bg-white transition-shadow hover:shadow-sm">
      <div className="flex items-center justify-between px-5 py-3.5">
        {/* Left: preview identity */}
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-sm font-semibold text-gray-900">
              {preview.name}
            </span>
            <StatusBadge status={preview.status} />
            {preview.branch && (
              <span className="inline-flex items-center gap-1 rounded-full bg-purple-50 px-2.5 py-0.5 text-xs font-medium text-purple-700">
                <svg
                  className="h-3 w-3"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                  strokeWidth={2}
                >
                  <path
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    d="M9.75 3.104v5.714a2.25 2.25 0 0 1-.659 1.591L5 14.5M9.75 3.104c-.251.023-.501.05-.75.082m.75-.082a24.301 24.301 0 0 1 4.5 0m0 0v5.714c0 .597.237 1.17.659 1.591L19.8 15.3M14.25 3.104c.251.023.501.05.75.082M19.8 15.3l-1.57.393A9.065 9.065 0 0 1 12 15a9.065 9.065 0 0 1-6.23-.693L5 14.5m14.8.8 1.402 1.402c1.232 1.232.65 3.318-1.067 3.611A48.309 48.309 0 0 1 12 21c-2.773 0-5.491-.235-8.135-.687-1.718-.293-2.3-2.379-1.067-3.61L5 14.5"
                  />
                </svg>
                {preview.branch}
              </span>
            )}
          </div>
          <div className="mt-0.5 text-xs text-gray-400">
            <span title={new Date(preview.createdAt).toLocaleString()}>
              Created {timeAgo(preview.createdAt)}
            </span>
          </div>
        </div>

        {/* Right: actions */}
        <div className="ml-4 flex flex-shrink-0 items-center gap-2">
          {preview.url && (
            <a
              href={preview.url}
              target="_blank"
              rel="noopener noreferrer"
              className="hidden items-center gap-1.5 rounded-lg border border-gray-200 bg-white px-3 py-1.5 text-xs font-medium text-gray-600 transition-colors hover:bg-gray-50 sm:inline-flex"
              title="Open preview"
            >
              <svg
                className="h-3.5 w-3.5"
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
              Visit
            </a>
          )}
          <Link
            to={appUrl}
            className="hidden items-center gap-1 rounded-lg border border-gray-200 bg-white px-3 py-1.5 text-xs font-medium text-gray-600 transition-colors hover:bg-gray-50 sm:inline-flex"
            title="Go to app"
          >
            App ↗
          </Link>

          {isConfirmingDelete ? (
            <div className="flex items-center gap-1.5">
              <span className="text-xs text-gray-500">Delete?</span>
              <button
                onClick={onConfirmDelete}
                className="rounded-md bg-red-600 px-2.5 py-1 text-xs font-medium text-white transition-colors hover:bg-red-700"
              >
                Yes
              </button>
              <button
                onClick={onCancelDelete}
                className="rounded-md border border-gray-200 bg-white px-2.5 py-1 text-xs font-medium text-gray-600 transition-colors hover:bg-gray-50"
              >
                No
              </button>
            </div>
          ) : (
            <button
              onClick={onRequestDelete}
              className="rounded-lg border border-gray-200 bg-white p-1.5 text-gray-400 transition-colors hover:bg-red-50 hover:text-red-600"
              title="Delete preview"
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
                  d="m14.74 9-.346 9m-4.788 0L9.26 9m9.968-3.21c.342.052.682.107 1.022.166m-1.022-.165L18.16 19.673a2.25 2.25 0 0 1-2.244 2.077H8.084a2.25 2.25 0 0 1-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 0 0-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 0 1 3.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 0 0-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.667 48.667 0 0 0-7.5 0"
                />
              </svg>
            </button>
          )}
        </div>
      </div>

      {/* URL bar */}
      {preview.url && (
        <div className="border-t border-gray-100 bg-gray-50 px-5 py-2">
          <a
            href={preview.url}
            target="_blank"
            rel="noopener noreferrer"
            className="block truncate text-xs text-blue-600 hover:underline sm:hidden"
          >
            {preview.url.replace(/^https?:\/\//, "")}
          </a>
          <div className="hidden items-center justify-between sm:flex">
            <span className="font-mono text-xs text-gray-400">
              {preview.namespace}
            </span>
            <a
              href={preview.url}
              target="_blank"
              rel="noopener noreferrer"
              className="truncate text-xs text-blue-500 hover:text-blue-700 hover:underline"
            >
              {preview.url.replace(/^https?:\/\//, "")}
            </a>
          </div>
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Page header
// ---------------------------------------------------------------------------

function PageHeader({ count }: { count: number }) {
  return (
    <div className="flex items-start justify-between">
      <div>
        <h1 className="text-2xl font-semibold text-gray-900">Previews</h1>
        <p className="mt-1 text-sm text-gray-500">
          {count === 0
            ? "Ephemeral preview environments for your apps."
            : `${count} active preview ${count === 1 ? "environment" : "environments"}`}
        </p>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Empty state
// ---------------------------------------------------------------------------

function EmptyState() {
  return (
    <div className="rounded-xl border border-dashed border-gray-300 bg-white px-6 py-16 text-center">
      <div className="mx-auto mb-4 flex h-12 w-12 items-center justify-center rounded-full bg-indigo-50">
        <svg
          className="h-6 w-6 text-indigo-500"
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
      </div>
      <h3 className="text-sm font-medium text-gray-900">
        No preview environments
      </h3>
      <p className="mx-auto mt-1 max-w-sm text-sm text-gray-500">
        Open an app from the dashboard and use the{" "}
        <span className="font-medium">Preview</span> button to deploy an
        isolated preview environment.
      </p>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Skeleton
// ---------------------------------------------------------------------------

function PreviewsSkeleton() {
  return (
    <div className="space-y-6">
      <div className="space-y-2">
        <div className="h-8 w-32 animate-pulse rounded bg-gray-100" />
        <div className="h-5 w-64 animate-pulse rounded bg-gray-50" />
      </div>
      <div className="space-y-3">
        {[1, 2, 3].map((n) => (
          <div
            key={n}
            className="h-20 animate-pulse rounded-xl bg-gray-50"
          />
        ))}
      </div>
    </div>
  );
}
