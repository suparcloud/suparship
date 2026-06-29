import { useState } from "react";
import { Link } from "react-router-dom";

import { deleteAppPreview } from "../lib/previews";
import { StatusBadge } from "./StatusBadge";
import type { PreviewGroup } from "../types";

// PreviewGroupCard renders one PR-level preview (collapsible) with its per-app
// previews nested. Shared by the global Previews page and the stack page. The
// group may be pre-filtered (e.g. to a stack's member apps) by the caller.
// onAppDeleted fires after an app-preview is removed so the parent can update.
export function PreviewGroupCard({
  group,
  onAppDeleted,
  defaultOpen = true,
}: {
  group: PreviewGroup;
  onAppDeleted: (appName: string) => void;
  defaultOpen?: boolean;
}) {
  const [open, setOpen] = useState(defaultOpen);

  return (
    <div className="overflow-hidden rounded-xl border border-gray-200 bg-white">
      <div className="flex items-center justify-between px-5 py-3">
        <button
          onClick={() => setOpen((v) => !v)}
          className="flex items-center gap-2 text-sm"
        >
          <svg
            className={`h-3.5 w-3.5 text-gray-400 transition-transform ${open ? "" : "-rotate-90"}`}
            fill="none"
            viewBox="0 0 24 24"
            stroke="currentColor"
            strokeWidth={2}
          >
            <path strokeLinecap="round" strokeLinejoin="round" d="m19.5 8.25-7.5 7.5-7.5-7.5" />
          </svg>
          <span className="font-semibold text-gray-900">{group.name}</span>
        </button>
        <div className="flex items-center gap-3">
          <Link
            to={`/projects/${encodeURIComponent(group.project)}`}
            className="text-xs text-gray-500 hover:text-gray-800"
          >
            {group.project}
          </Link>
          {group.baseEnv && (
            <span className="text-xs text-gray-400">from {group.baseEnv}</span>
          )}
          <StatusBadge status={group.health} />
          <span className="rounded-full bg-gray-100 px-2 py-0.5 text-xs text-gray-500">
            {group.apps.length} app{group.apps.length === 1 ? "" : "s"}
          </span>
        </div>
      </div>

      {open && (
        <div className="divide-y divide-gray-50 border-t border-gray-100">
          {group.apps.map((a) => (
            <AppPreviewRow
              key={a.appName}
              project={group.project}
              previewName={group.name}
              appName={a.appName}
              phase={a.status.phase}
              url={a.urls?.[0]}
              onDeleted={() => onAppDeleted(a.appName)}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function AppPreviewRow({
  project,
  previewName,
  appName,
  phase,
  url,
  onDeleted,
}: {
  project: string;
  previewName: string;
  appName: string;
  phase: string;
  url?: string;
  onDeleted: () => void;
}) {
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);

  async function remove() {
    setBusy(true);
    try {
      await deleteAppPreview(project, appName, previewName);
      onDeleted();
    } catch {
      setBusy(false);
      setConfirming(false);
    }
  }

  return (
    <div className="flex items-center justify-between px-5 py-2.5 pl-10">
      <Link
        to={`/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(appName)}`}
        className="font-mono text-sm text-gray-900 hover:text-indigo-600"
      >
        {appName}
      </Link>
      <div className="flex items-center gap-3">
        <StatusBadge status={phase} />
        {url ? (
          <a
            href={url}
            target="_blank"
            rel="noreferrer"
            className="max-w-[18rem] truncate text-xs text-indigo-600 hover:text-indigo-800"
            title={url}
          >
            {url.replace(/^https?:\/\//, "")} ↗
          </a>
        ) : (
          <span className="text-xs text-gray-300">no endpoint</span>
        )}
        {confirming ? (
          <span className="flex items-center gap-1.5 text-xs">
            <span className="text-gray-500">Delete?</span>
            <button
              onClick={remove}
              disabled={busy}
              className="rounded bg-red-600 px-2 py-0.5 font-medium text-white hover:bg-red-700 disabled:opacity-50"
            >
              {busy ? "…" : "Yes"}
            </button>
            <button
              onClick={() => setConfirming(false)}
              className="rounded border border-gray-200 px-2 py-0.5 text-gray-600 hover:bg-gray-50"
            >
              No
            </button>
          </span>
        ) : (
          <button
            onClick={() => setConfirming(true)}
            className="text-xs font-medium text-gray-400 hover:text-red-600"
          >
            Delete
          </button>
        )}
      </div>
    </div>
  );
}
