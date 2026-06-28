// Compact previous/next pager for client-side paginated lists. Renders nothing
// when there is only one page.
export function Pagination({
  page,
  pageCount,
  onPage,
  className = "",
}: {
  page: number;
  pageCount: number;
  onPage: (p: number) => void;
  className?: string;
}) {
  if (pageCount <= 1) return null;
  return (
    <div className={`flex items-center justify-end gap-2 text-xs text-gray-500 ${className}`}>
      <button
        onClick={() => onPage(page - 1)}
        disabled={page <= 1}
        className="rounded border border-gray-200 px-2 py-1 hover:bg-gray-50 disabled:opacity-40"
      >
        ‹ Prev
      </button>
      <span className="tabular-nums">
        {page} / {pageCount}
      </span>
      <button
        onClick={() => onPage(page + 1)}
        disabled={page >= pageCount}
        className="rounded border border-gray-200 px-2 py-1 hover:bg-gray-50 disabled:opacity-40"
      >
        Next ›
      </button>
    </div>
  );
}

// usePagedSearch filters items by a query (via matchFn) and paginates the result.
// Page auto-clamps when the filtered set shrinks. Generic so it works for apps,
// projects, stacks, etc.
import { useMemo, useState } from "react";

export function usePagedSearch<T>(
  items: T[],
  matchFn: (item: T, q: string) => boolean,
  pageSize: number,
) {
  const [query, setQuery] = useState("");
  const [page, setPage] = useState(1);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return items;
    return items.filter((it) => matchFn(it, q));
  }, [items, query, matchFn]);

  const pageCount = Math.max(1, Math.ceil(filtered.length / pageSize));
  const clampedPage = Math.min(page, pageCount);
  const start = (clampedPage - 1) * pageSize;
  const pageItems = filtered.slice(start, start + pageSize);

  return {
    query,
    setQuery: (q: string) => {
      setQuery(q);
      setPage(1);
    },
    page: clampedPage,
    setPage,
    pageItems,
    pageCount,
    total: filtered.length,
  };
}
