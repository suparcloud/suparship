import { useMemo, useState } from "react";
import type { ReactNode } from "react";
import { Link } from "react-router-dom";
import type { AppSummary, AppEnvironmentSummary } from "../types";
import type { Stack } from "../lib/stacks";
import { StatusBadge } from "./StatusBadge";

// AppTable is the unified, compact app list shared by the Dashboard, Project
// detail, and Stack detail pages. Apps that belong to a stack are grouped under
// a collapsible stack header; standalone apps follow. Each row shows per-env
// status, links to the app, and exposes URLs. Search + pagination are
// client-side over the already-loaded apps.

type Row =
  | { kind: "stack"; name: string; displayName: string; count: number }
  | { kind: "app"; app: AppSummary; inStack: boolean };

const PAGE_SIZE = 12;

function stableEnv(app: AppSummary, envName: string): AppEnvironmentSummary | undefined {
  return app.environments?.find((e) => e.envName === envName && e.envType !== "preview");
}

function previewCount(app: AppSummary): number {
  return (app.environments ?? []).filter((e) => e.envType === "preview").length;
}

function firstUrl(app: AppSummary): string | undefined {
  return app.urls?.[0];
}

// hostLabel renders a URL as its hostname (+path when not root) for a compact,
// recognizable endpoint label in the list.
function hostLabel(url: string): string {
  try {
    const u = new URL(url);
    return u.host + (u.pathname && u.pathname !== "/" ? u.pathname : "");
  } catch {
    return url.replace(/^https?:\/\//, "");
  }
}

export function AppTable({
  project,
  apps,
  stacks = [],
  emptyText = "No apps yet.",
  rowAction,
}: {
  project: string;
  apps: AppSummary[];
  stacks?: Stack[];
  emptyText?: string;
  // Optional trailing per-row action (e.g. a Remove button on the stack page).
  rowAction?: (app: AppSummary) => ReactNode;
}) {
  const [query, setQuery] = useState("");
  const [page, setPage] = useState(1);
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());

  // Stable env columns: union across apps, in promotion order.
  const envCols = useMemo(() => {
    const order = new Map<string, number>();
    for (const app of apps) {
      for (const e of app.environments ?? []) {
        if (e.envType === "preview") continue;
        if (!order.has(e.envName) || e.order < (order.get(e.envName) ?? 0)) {
          order.set(e.envName, e.order);
        }
      }
    }
    return [...order.entries()].sort((a, b) => a[1] - b[1]).map(([n]) => n);
  }, [apps]);
  const hasEnvData = envCols.length > 0;

  const stackOf = useMemo(() => {
    const m = new Map<string, string>();
    for (const s of stacks) for (const a of s.apps ?? []) m.set(a, s.name);
    return m;
  }, [stacks]);

  // Filter, group, and flatten into rows (with collapse).
  const rows = useMemo<Row[]>(() => {
    const q = query.trim().toLowerCase();
    const match = (a: AppSummary) =>
      !q ||
      a.name.toLowerCase().includes(q) ||
      (a.displayName ?? "").toLowerCase().includes(q) ||
      a.template.name.toLowerCase().includes(q);
    const filtered = apps.filter(match);

    const sortedStacks = [...stacks].sort((a, b) =>
      (a.displayName || a.name).localeCompare(b.displayName || b.name),
    );
    const out: Row[] = [];
    for (const s of sortedStacks) {
      const members = filtered.filter((a) => stackOf.get(a.name) === s.name);
      if (members.length === 0) continue; // hide empty / non-matching stacks
      out.push({ kind: "stack", name: s.name, displayName: s.displayName || s.name, count: members.length });
      // Expand all while searching so matches are visible.
      const isCollapsed = collapsed.has(s.name) && !q;
      if (!isCollapsed) for (const m of members) out.push({ kind: "app", app: m, inStack: true });
    }
    for (const a of filtered.filter((a) => !stackOf.has(a.name))) {
      out.push({ kind: "app", app: a, inStack: false });
    }
    return out;
  }, [apps, stacks, stackOf, query, collapsed]);

  const pageCount = Math.max(1, Math.ceil(rows.length / PAGE_SIZE));
  const curPage = Math.min(page, pageCount);
  const pageRows = rows.slice((curPage - 1) * PAGE_SIZE, curPage * PAGE_SIZE);
  const appCount = apps.length;

  const colSpan = 2 + (hasEnvData ? envCols.length : 1) + 1 + (rowAction ? 1 : 0);

  return (
    <div>
      <div className="mb-3 flex items-center gap-3">
        <input
          value={query}
          onChange={(e) => {
            setQuery(e.target.value);
            setPage(1);
          }}
          placeholder="Search apps…"
          className="w-56 rounded-lg border border-gray-300 px-3 py-1.5 text-sm focus:border-gray-400 focus:outline-none"
        />
        <span className="text-xs text-gray-400">
          {query ? `${rows.filter((r) => r.kind === "app").length} of ${appCount}` : `${appCount}`} apps
        </span>
      </div>

      {appCount === 0 ? (
        <p className="py-6 text-center text-sm text-gray-400">{emptyText}</p>
      ) : (
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-gray-100 text-left text-xs font-medium uppercase tracking-wider text-gray-500">
              <th className="py-2">App</th>
              <th className="py-2">Template</th>
              {hasEnvData ? (
                envCols.map((c) => (
                  <th key={c} className="py-2 capitalize">
                    {c}
                  </th>
                ))
              ) : (
                <th className="py-2">Status</th>
              )}
              <th className="py-2">URLs</th>
              {rowAction && <th className="py-2 text-right"></th>}
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-50">
            {pageRows.map((row) =>
              row.kind === "stack" ? (
                <tr key={`stack-${row.name}`} className="bg-gray-50/60">
                  <td colSpan={colSpan} className="py-2">
                    <div className="flex items-center gap-2">
                      <button
                        onClick={() =>
                          setCollapsed((prev) => {
                            const next = new Set(prev);
                            if (next.has(row.name)) next.delete(row.name);
                            else next.add(row.name);
                            return next;
                          })
                        }
                        className="flex items-center gap-1.5 text-sm font-medium text-gray-700"
                        title={collapsed.has(row.name) ? "Expand" : "Collapse"}
                      >
                        <svg
                          className={`h-3.5 w-3.5 text-gray-400 transition-transform ${collapsed.has(row.name) ? "-rotate-90" : ""}`}
                          fill="none"
                          viewBox="0 0 24 24"
                          stroke="currentColor"
                          strokeWidth={2}
                        >
                          <path strokeLinecap="round" strokeLinejoin="round" d="m19.5 8.25-7.5 7.5-7.5-7.5" />
                        </svg>
                        {row.displayName}
                      </button>
                      <span className="rounded-full bg-gray-200 px-2 py-0.5 text-xs font-medium text-gray-600">
                        stack · {row.count}
                      </span>
                      <Link
                        to={`/projects/${encodeURIComponent(project)}/stacks/${encodeURIComponent(row.name)}`}
                        className="text-xs font-medium text-indigo-600 hover:text-indigo-800"
                      >
                        Open stack →
                      </Link>
                    </div>
                  </td>
                </tr>
              ) : (
                <AppRowView
                  key={`app-${row.app.name}`}
                  project={project}
                  app={row.app}
                  inStack={row.inStack}
                  envCols={envCols}
                  hasEnvData={hasEnvData}
                  rowAction={rowAction}
                />
              ),
            )}
          </tbody>
        </table>
      )}

      {pageCount > 1 && (
        <div className="mt-3 flex items-center justify-end gap-2 text-xs text-gray-500">
          <button
            onClick={() => setPage(curPage - 1)}
            disabled={curPage <= 1}
            className="rounded border border-gray-200 px-2 py-1 hover:bg-gray-50 disabled:opacity-40"
          >
            ‹ Prev
          </button>
          <span className="tabular-nums">
            {curPage} / {pageCount}
          </span>
          <button
            onClick={() => setPage(curPage + 1)}
            disabled={curPage >= pageCount}
            className="rounded border border-gray-200 px-2 py-1 hover:bg-gray-50 disabled:opacity-40"
          >
            Next ›
          </button>
        </div>
      )}
    </div>
  );
}

function AppRowView({
  project,
  app,
  inStack,
  envCols,
  hasEnvData,
  rowAction,
}: {
  project: string;
  app: AppSummary;
  inStack: boolean;
  envCols: string[];
  hasEnvData: boolean;
  rowAction?: (app: AppSummary) => ReactNode;
}) {
  const url = firstUrl(app);
  const previews = previewCount(app);
  return (
    <tr className="hover:bg-gray-50/50">
      <td className={`py-2 ${inStack ? "pl-6" : ""}`}>
        <Link
          to={`/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app.name)}`}
          className="font-medium text-gray-900 hover:text-indigo-600"
        >
          {app.displayName || app.name}
        </Link>
        {previews > 0 && (
          <span className="ml-2 rounded-full bg-indigo-50 px-1.5 py-0.5 text-xs font-medium text-indigo-600">
            {previews} preview{previews > 1 ? "s" : ""}
          </span>
        )}
      </td>
      <td className="py-2">
        <span className="rounded bg-gray-100 px-2 py-0.5 font-mono text-xs text-gray-600">
          {app.template.name}
        </span>
      </td>
      {hasEnvData ? (
        envCols.map((c) => {
          const env = stableEnv(app, c);
          if (!env) return <td key={c} className="py-2 text-gray-300">—</td>;
          // Direct apps that opt out of an env never deploy there.
          const phase = env.deploy ? env.status.phase : "not_deployed";
          return (
            <td key={c} className="py-2">
              <StatusBadge status={phase} />
            </td>
          );
        })
      ) : (
        <td className="py-2">
          <StatusBadge status={app.status.phase} />
        </td>
      )}
      <td className="py-2">
        {url ? (
          <span className="inline-flex items-center gap-1">
            <a
              href={url}
              target="_blank"
              rel="noreferrer"
              className="max-w-[16rem] truncate text-indigo-600 hover:text-indigo-800"
              title={(app.urls ?? []).join("\n")}
            >
              {hostLabel(url)} ↗
            </a>
            {(app.urls?.length ?? 0) > 1 && (
              <span className="rounded-full bg-gray-100 px-1.5 py-0.5 text-xs text-gray-500">
                +{(app.urls?.length ?? 0) - 1}
              </span>
            )}
          </span>
        ) : (
          <span className="text-gray-300">—</span>
        )}
      </td>
      {rowAction && <td className="py-2 text-right">{rowAction(app)}</td>}
    </tr>
  );
}
