import { useEffect, useState } from "react";
import { Link } from "react-router-dom";

import { fetchTemplates } from "../lib/templates";
import type { TemplateSummary } from "../types";

const categoryStyle: Record<string, { bg: string; icon: string }> = {
  web: { bg: "bg-blue-50 text-blue-600", icon: "globe" },
  worker: { bg: "bg-purple-50 text-purple-600", icon: "cpu" },
  api: { bg: "bg-emerald-50 text-emerald-600", icon: "zap" },
  data: { bg: "bg-amber-50 text-amber-600", icon: "database" },
};

function CategoryIcon({ category }: { category: string }) {
  const style = categoryStyle[category] ?? {
    bg: "bg-gray-50 text-gray-500",
    icon: "box",
  };

  return (
    <div
      className={`flex h-10 w-10 items-center justify-center rounded-lg ${style.bg}`}
    >
      <CategorySVG type={style.icon} />
    </div>
  );
}

function CategorySVG({ type }: { type: string }) {
  const cls = "h-5 w-5";
  switch (type) {
    case "globe":
      return (
        <svg className={cls} fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
          <path strokeLinecap="round" strokeLinejoin="round" d="M12 21a9.004 9.004 0 0 0 8.716-6.747M12 21a9.004 9.004 0 0 1-8.716-6.747M12 21c2.485 0 4.5-4.03 4.5-9S14.485 3 12 3m0 18c-2.485 0-4.5-4.03-4.5-9S9.515 3 12 3m0 0a8.997 8.997 0 0 1 7.843 4.582M12 3a8.997 8.997 0 0 0-7.843 4.582m15.686 0A11.953 11.953 0 0 1 12 10.5c-2.998 0-5.74-1.1-7.843-2.918m15.686 0A8.959 8.959 0 0 1 21 12c0 .778-.099 1.533-.284 2.253m0 0A17.919 17.919 0 0 1 12 16.5a17.92 17.92 0 0 1-8.716-2.247m0 0A8.966 8.966 0 0 1 3 12c0-1.97.633-3.794 1.708-5.276" />
        </svg>
      );
    case "cpu":
      return (
        <svg className={cls} fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
          <path strokeLinecap="round" strokeLinejoin="round" d="M8.25 3v1.5M4.5 8.25H3m18 0h-1.5M4.5 12H3m18 0h-1.5m-15 3.75H3m18 0h-1.5M8.25 19.5V21M12 3v1.5m0 15V21m3.75-18v1.5m0 15V21m-9-1.5h10.5a2.25 2.25 0 0 0 2.25-2.25V6.75a2.25 2.25 0 0 0-2.25-2.25H6.75A2.25 2.25 0 0 0 4.5 6.75v10.5a2.25 2.25 0 0 0 2.25 2.25Zm7.5-12h-6v6h6v-6Z" />
        </svg>
      );
    case "zap":
      return (
        <svg className={cls} fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
          <path strokeLinecap="round" strokeLinejoin="round" d="m3.75 13.5 10.5-11.25L12 10.5h8.25L9.75 21.75 12 13.5H3.75Z" />
        </svg>
      );
    case "database":
      return (
        <svg className={cls} fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
          <path strokeLinecap="round" strokeLinejoin="round" d="M20.25 6.375c0 2.278-3.694 4.125-8.25 4.125S3.75 8.653 3.75 6.375m16.5 0c0-2.278-3.694-4.125-8.25-4.125S3.75 4.097 3.75 6.375m16.5 0v11.25c0 2.278-3.694 4.125-8.25 4.125s-8.25-1.847-8.25-4.125V6.375m16.5 0v3.75c0 2.278-3.694 4.125-8.25 4.125s-8.25-1.847-8.25-4.125v-3.75" />
        </svg>
      );
    default:
      return (
        <svg className={cls} fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
          <path strokeLinecap="round" strokeLinejoin="round" d="m21 7.5-9-5.25L3 7.5m18 0-9 5.25m9-5.25v9l-9 5.25M3 7.5l9 5.25M3 7.5v9l9 5.25" />
        </svg>
      );
  }
}

function CategoryBadge({ category }: { category: string }) {
  const style = categoryStyle[category] ?? {
    bg: "bg-gray-50 text-gray-500",
  };
  return (
    <span
      className={`inline-block rounded-full px-2.5 py-0.5 text-xs font-medium capitalize ${style.bg}`}
    >
      {category}
    </span>
  );
}

export function Templates() {
  const [templates, setTemplates] = useState<TemplateSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;

    async function load() {
      try {
        const data = await fetchTemplates();
        if (cancelled) return;
        setTemplates(data.templates);
      } catch (err) {
        if (cancelled) return;
        setError(err instanceof Error ? err.message : "Failed to load");
      } finally {
        if (!cancelled) setLoading(false);
      }
    }

    load();
    return () => {
      cancelled = true;
    };
  }, []);

  if (loading) {
    return (
      <div className="space-y-6">
        <div className="space-y-1">
          <div className="h-8 w-40 animate-pulse rounded bg-gray-100" />
          <div className="h-5 w-72 animate-pulse rounded bg-gray-50" />
        </div>
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {[1, 2, 3].map((n) => (
            <div
              key={n}
              className="h-44 animate-pulse rounded-xl bg-gray-50"
            />
          ))}
        </div>
      </div>
    );
  }

  if (error) {
    return (
      <div className="rounded-lg border border-red-200 bg-red-50 p-4">
        <p className="text-sm text-red-700">
          Failed to load templates: {error}
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900">Templates</h1>
          <p className="mt-1 text-sm text-gray-500">
            Golden paths for deploying apps. Pick a template to see what it
            offers.
          </p>
        </div>
        <div className="flex shrink-0 gap-2">
          <Link
            to="/templates/sources"
            className="rounded-md border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50"
          >
            External sources
          </Link>
          <Link
            to="/templates/import"
            className="rounded-md bg-gray-900 px-4 py-2 text-sm font-medium text-white hover:bg-gray-800"
          >
            Import Helm chart
          </Link>
        </div>
      </div>

      {templates.length === 0 ? (
        <div className="rounded-xl border border-dashed border-gray-300 bg-white px-6 py-16 text-center">
          <div className="mx-auto mb-4 flex h-12 w-12 items-center justify-center rounded-full bg-gray-100">
            <svg className="h-6 w-6 text-gray-400" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
              <path strokeLinecap="round" strokeLinejoin="round" d="m21 7.5-9-5.25L3 7.5m18 0-9 5.25m9-5.25v9l-9 5.25M3 7.5l9 5.25M3 7.5v9l9 5.25" />
            </svg>
          </div>
          <h3 className="text-sm font-medium text-gray-900">
            No templates yet
          </h3>
          <p className="mt-1 text-sm text-gray-500">
            Import a Helm chart with the button above, or start the server
            with <code className="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono">--templates-dir</code>.
          </p>
        </div>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {templates.map((t) => (
            <Link
              key={t.name}
              to={`/templates/${t.name}`}
              className="group rounded-xl border border-gray-200 bg-white p-5 transition-all hover:border-gray-300 hover:shadow-md"
            >
              <div className="flex items-start justify-between">
                <CategoryIcon category={t.category} />
                <CategoryBadge category={t.category} />
              </div>

              <h3 className="mt-4 text-base font-semibold text-gray-900 group-hover:text-gray-700">
                {t.title}
              </h3>
              {t.description && (
                <p className="mt-1.5 text-sm leading-relaxed text-gray-500 line-clamp-2">
                  {t.description}
                </p>
              )}

              <div className="mt-4 flex items-center justify-between">
                <span className="text-xs text-gray-400">
                  v{t.version}
                  {t.disabled && (
                    <span className="ml-2 rounded-full bg-amber-50 px-2 py-0.5 font-medium text-amber-700">
                      disabled
                    </span>
                  )}
                </span>
                <span className="text-xs font-medium text-gray-400 transition-colors group-hover:text-gray-600">
                  View details &rarr;
                </span>
              </div>
            </Link>
          ))}
        </div>
      )}
    </div>
  );
}
