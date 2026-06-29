import { useCallback, useEffect, useState } from "react";

import { listPreviewGroups } from "../lib/previews";
import { PreviewGroupCard } from "../components/PreviewGroupCard";
import { usePagedSearch } from "../components/Pagination";
import type { PreviewGroup } from "../types";

// The global Previews page lists one item per PR (preview name) — a PR groups the
// per-app previews created for it (identically for single apps and stacks). Each
// row expands to its app-previews.

export function Previews() {
  const [groups, setGroups] = useState<PreviewGroup[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    listPreviewGroups()
      .then((d) => setGroups(d.previews ?? []))
      .catch((err) => setError(err instanceof Error ? err.message : "Failed to load"))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  // Drop one app-preview from its group; remove the group when it empties.
  function onAppDeleted(groupKey: string, appName: string) {
    setGroups((prev) =>
      prev
        .map((g) =>
          `${g.project}/${g.name}` === groupKey
            ? { ...g, apps: g.apps.filter((a) => a.appName !== appName) }
            : g,
        )
        .filter((g) => g.apps.length > 0),
    );
  }

  if (loading) return <PreviewsSkeleton />;
  if (error) {
    return (
      <div className="space-y-6">
        <PageHeader count={0} />
        <div className="rounded-lg border border-red-200 bg-red-50 p-4">
          <p className="text-sm text-red-700">Failed to load previews: {error}</p>
        </div>
      </div>
    );
  }

  return <PreviewsList groups={groups} onAppDeleted={onAppDeleted} />;
}

function PreviewsList({
  groups,
  onAppDeleted,
}: {
  groups: PreviewGroup[];
  onAppDeleted: (groupKey: string, appName: string) => void;
}) {
  const { query, setQuery, page, setPage, pageItems, pageCount, total } =
    usePagedSearch(
      groups,
      (g, q) => g.name.toLowerCase().includes(q) || g.project.toLowerCase().includes(q),
      15,
    );

  return (
    <div className="space-y-6">
      <PageHeader count={groups.length} />
      {groups.length === 0 ? (
        <EmptyState />
      ) : (
        <>
          {groups.length > 8 && (
            <input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search previews…"
              className="w-64 rounded-lg border border-gray-300 px-3 py-1.5 text-sm focus:border-gray-400 focus:outline-none"
            />
          )}
          <div className="space-y-3">
            {pageItems.map((g) => (
              <PreviewGroupCard
                key={`${g.project}/${g.name}`}
                group={g}
                onAppDeleted={(appName) => onAppDeleted(`${g.project}/${g.name}`, appName)}
              />
            ))}
          </div>
          {query && total === 0 && (
            <p className="py-6 text-center text-sm text-gray-400">
              No previews match “{query}”.
            </p>
          )}
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
        </>
      )}
    </div>
  );
}

function PageHeader({ count }: { count: number }) {
  return (
    <div>
      <h1 className="text-2xl font-semibold text-gray-900">Previews</h1>
      <p className="mt-1 text-sm text-gray-500">
        {count === 0
          ? "Ephemeral preview environments — one per pull request."
          : `${count} preview ${count === 1 ? "PR" : "PRs"}`}
      </p>
    </div>
  );
}

function EmptyState() {
  return (
    <div className="rounded-xl border border-dashed border-gray-300 bg-white px-6 py-16 text-center">
      <h3 className="text-sm font-medium text-gray-900">No preview environments</h3>
      <p className="mx-auto mt-1 max-w-md text-sm text-gray-500">
        A preview is an ephemeral copy of an app (or a stack of apps) — usually one
        per pull request — that clones a base env (default: staging). Use an app's{" "}
        <span className="font-medium">Preview</span> button, or automate it from CI
        (see docs/previews.md).
      </p>
    </div>
  );
}

function PreviewsSkeleton() {
  return (
    <div className="space-y-6">
      <div className="space-y-2">
        <div className="h-8 w-32 animate-pulse rounded bg-gray-100" />
        <div className="h-5 w-64 animate-pulse rounded bg-gray-50" />
      </div>
      <div className="space-y-3">
        {[1, 2, 3].map((n) => (
          <div key={n} className="h-16 animate-pulse rounded-xl bg-gray-50" />
        ))}
      </div>
    </div>
  );
}
