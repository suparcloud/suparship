import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { toast } from "sonner";

import { ApiError } from "../lib/api";
import {
  fetchTemplateRegistry,
  syncAllSources,
  syncSource,
  updateTemplateRegistry,
} from "../lib/templates";
import type {
  ExternalTemplateRepo,
  TemplateRegistry,
  TemplateSyncResult,
} from "../types";

// emptyRepo seeds the inline "add source" form. Lives outside the component
// because react re-creates it on every render otherwise, which would reset
// the form mid-typing if we ever passed it as a memoized default.
const emptyRepo: ExternalTemplateRepo = {
  name: "",
  repoURL: "",
  ref: "main",
  path: "",
  existingSecret: "",
};

const inputClass =
  "block w-full rounded-md border border-gray-300 px-3 py-2 text-sm shadow-sm focus:border-gray-500 focus:ring-1 focus:ring-gray-500";

// formatTimestamp turns an ISO timestamp into a human-friendly relative-ish
// string. We don't need a full date library for this — coarse-grained is
// enough for "recently synced" UX.
function formatTimestamp(iso?: string): string {
  if (!iso) return "never";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const diffMs = Date.now() - d.getTime();
  const diffMin = Math.floor(diffMs / 60_000);
  if (diffMin < 1) return "just now";
  if (diffMin < 60) return `${diffMin}m ago`;
  const diffHr = Math.floor(diffMin / 60);
  if (diffHr < 24) return `${diffHr}h ago`;
  return d.toLocaleString();
}

// extractError normalises ApiError + Error + unknown into a string the
// user can read. Identical pattern to TemplateImport — kept inline here
// rather than factored to avoid a one-call-site shared helper.
function extractError(err: unknown, fallback: string): string {
  if (err instanceof ApiError) return err.message;
  if (err instanceof Error) return err.message;
  return fallback;
}

export function TemplateSources() {
  const [registry, setRegistry] = useState<TemplateRegistry>({
    builtIn: [],
    external: [],
    sources: [],
  });
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [savingRepo, setSavingRepo] = useState(false);
  const [syncingAll, setSyncingAll] = useState(false);
  const [syncingOne, setSyncingOne] = useState<string | null>(null);
  const [draft, setDraft] = useState<ExternalTemplateRepo>(emptyRepo);
  const [showAdd, setShowAdd] = useState(false);

  // sourceState maps repo name → { lastSynced, templateCount } derived from
  // registry.sources. Recomputed when the registry changes; useMemo means
  // the per-row render isn't doing the lookup work repeatedly.
  const sourceState = useMemo(() => {
    const map = new Map<string, { lastSynced?: string; count: number }>();
    for (const s of registry.sources ?? []) {
      if (!s.externalRepo) continue;
      const cur = map.get(s.externalRepo) ?? { count: 0 };
      cur.count += 1;
      if (!cur.lastSynced || (s.syncedAt && s.syncedAt > cur.lastSynced)) {
        cur.lastSynced = s.syncedAt;
      }
      map.set(s.externalRepo, cur);
    }
    return map;
  }, [registry.sources]);

  useEffect(() => {
    let cancelled = false;
    async function load() {
      try {
        const res = await fetchTemplateRegistry();
        if (!cancelled) setRegistry(res.registry);
      } catch (err) {
        if (!cancelled) setError(extractError(err, "Failed to load registry"));
      } finally {
        if (!cancelled) setLoading(false);
      }
    }
    load();
    return () => {
      cancelled = true;
    };
  }, []);

  // applyResults folds sync responses into local state so timestamps + counts
  // refresh without a separate fetch round-trip. Errors from individual
  // sources are surfaced via toast since the request itself returned 200.
  function applyResults(results: TemplateSyncResult[]) {
    const failures = results.filter((r) => r.error);
    const successes = results.filter((r) => !r.error);
    if (successes.length > 0) {
      const total = successes.reduce((acc, r) => acc + r.templates.length, 0);
      toast.success(
        `Synced ${total} template${total === 1 ? "" : "s"} from ${successes.length} source${successes.length === 1 ? "" : "s"}`,
      );
    }
    for (const f of failures) {
      toast.error(`${f.sourceName}: ${f.error}`);
    }
    // Re-fetch so derived state (sources list) is authoritative — the sync
    // endpoint persists it, but our local copy might race with /sources.
    fetchTemplateRegistry()
      .then((res) => setRegistry(res.registry))
      .catch((err) => toast.error(extractError(err, "Refresh failed")));
  }

  async function handleSyncAll() {
    setSyncingAll(true);
    try {
      const res = await syncAllSources();
      applyResults(res.results);
    } catch (err) {
      toast.error(extractError(err, "Sync failed"));
    } finally {
      setSyncingAll(false);
    }
  }

  async function handleSyncOne(name: string) {
    setSyncingOne(name);
    try {
      const res = await syncSource(name);
      applyResults(res.results);
    } catch (err) {
      toast.error(extractError(err, `Sync failed for ${name}`));
    } finally {
      setSyncingOne(null);
    }
  }

  async function handleAdd() {
    if (!draft.name.trim() || !draft.repoURL.trim()) {
      toast.error("Name and Repo URL are required");
      return;
    }
    if ((registry.external ?? []).some((r) => r.name === draft.name)) {
      toast.error(`A source named ${draft.name} already exists`);
      return;
    }
    setSavingRepo(true);
    try {
      const next: TemplateRegistry = {
        ...registry,
        external: [...(registry.external ?? []), { ...draft }],
      };
      const res = await updateTemplateRegistry(next);
      setRegistry(res.registry);
      setDraft(emptyRepo);
      setShowAdd(false);
      toast.success(`Added source ${draft.name}`);
    } catch (err) {
      toast.error(extractError(err, "Save failed"));
    } finally {
      setSavingRepo(false);
    }
  }

  async function handleRemove(name: string) {
    const confirmed = window.confirm(
      `Remove external source "${name}"? Templates already synced from it will remain in the cluster until cleaned up manually.`,
    );
    if (!confirmed) return;
    try {
      const next: TemplateRegistry = {
        ...registry,
        external: (registry.external ?? []).filter((r) => r.name !== name),
      };
      const res = await updateTemplateRegistry(next);
      setRegistry(res.registry);
      toast.success(`Removed ${name}`);
    } catch (err) {
      toast.error(extractError(err, "Remove failed"));
    }
  }

  if (loading) {
    return (
      <div className="space-y-4">
        <div className="h-8 w-48 animate-pulse rounded bg-gray-100" />
        <div className="h-32 animate-pulse rounded-lg bg-gray-50" />
      </div>
    );
  }

  if (error) {
    return (
      <div className="rounded-lg border border-red-200 bg-red-50 p-4">
        <p className="text-sm text-red-700">{error}</p>
      </div>
    );
  }

  const externals = registry.external ?? [];

  return (
    <div className="space-y-6">
      <div>
        <Link
          to="/templates"
          className="text-sm text-gray-500 hover:text-gray-700"
        >
          ← Back to templates
        </Link>
      </div>

      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900">
            External template sources
          </h1>
          <p className="mt-1 text-sm text-gray-500">
            Git-hosted Helm chart repositories that suparship pulls and turns
            into templates. The platform syncs every 5 minutes by default;
            use Sync now to pick up changes immediately.
          </p>
        </div>
        <div className="flex shrink-0 gap-2">
          <button
            type="button"
            onClick={handleSyncAll}
            disabled={syncingAll || externals.length === 0}
            className="rounded-md border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 disabled:opacity-50"
          >
            {syncingAll ? "Syncing…" : "Sync all"}
          </button>
          <button
            type="button"
            onClick={() => setShowAdd((v) => !v)}
            className="rounded-md bg-gray-900 px-4 py-2 text-sm font-medium text-white hover:bg-gray-800"
          >
            {showAdd ? "Cancel" : "Add source"}
          </button>
        </div>
      </div>

      {showAdd && (
        <AddSourceForm
          draft={draft}
          onChange={setDraft}
          onSubmit={handleAdd}
          saving={savingRepo}
        />
      )}

      {externals.length === 0 ? (
        <div className="rounded-xl border border-dashed border-gray-300 bg-white px-6 py-16 text-center">
          <h3 className="text-sm font-medium text-gray-900">
            No external sources yet
          </h3>
          <p className="mt-1 text-sm text-gray-500">
            Add a Git URL pointing at a directory that contains one or more
            Helm charts. suparship will package each chart and import it.
          </p>
        </div>
      ) : (
        <div className="overflow-hidden rounded-xl border border-gray-200 bg-white">
          <table className="min-w-full divide-y divide-gray-200 text-sm">
            <thead className="bg-gray-50 text-xs uppercase tracking-wide text-gray-500">
              <tr>
                <th className="px-4 py-3 text-left">Name</th>
                <th className="px-4 py-3 text-left">Repository</th>
                <th className="px-4 py-3 text-left">Last sync</th>
                <th className="px-4 py-3 text-left">Templates</th>
                <th className="px-4 py-3 text-right">Actions</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              {externals.map((repo) => {
                const state = sourceState.get(repo.name);
                return (
                  <tr key={repo.name}>
                    <td className="px-4 py-3 align-top font-medium text-gray-900">
                      {repo.name}
                    </td>
                    <td className="px-4 py-3 align-top text-gray-700">
                      <div className="font-mono text-xs">{repo.repoURL}</div>
                      <div className="mt-1 text-xs text-gray-400">
                        {repo.ref || "main"} · {repo.path || "/"}
                        {repo.existingSecret ? ` · auth: ${repo.existingSecret}` : ""}
                      </div>
                    </td>
                    <td className="px-4 py-3 align-top text-gray-600">
                      {formatTimestamp(state?.lastSynced)}
                    </td>
                    <td className="px-4 py-3 align-top text-gray-600">
                      {state?.count ?? 0}
                    </td>
                    <td className="px-4 py-3 align-top text-right">
                      <button
                        type="button"
                        onClick={() => handleSyncOne(repo.name)}
                        disabled={syncingOne === repo.name}
                        className="rounded-md border border-gray-300 bg-white px-3 py-1.5 text-xs font-medium text-gray-700 hover:bg-gray-50 disabled:opacity-50"
                      >
                        {syncingOne === repo.name ? "Syncing…" : "Sync now"}
                      </button>
                      <button
                        type="button"
                        onClick={() => handleRemove(repo.name)}
                        className="ml-2 rounded-md border border-red-200 bg-white px-3 py-1.5 text-xs font-medium text-red-700 hover:bg-red-50"
                      >
                        Remove
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function AddSourceForm({
  draft,
  onChange,
  onSubmit,
  saving,
}: {
  draft: ExternalTemplateRepo;
  onChange: (next: ExternalTemplateRepo) => void;
  onSubmit: () => void;
  saving: boolean;
}) {
  const set = (partial: Partial<ExternalTemplateRepo>) =>
    onChange({ ...draft, ...partial });
  return (
    <div className="rounded-xl border border-gray-200 bg-white p-6">
      <h2 className="text-base font-semibold text-gray-900">Add source</h2>
      <p className="mt-1 text-xs text-gray-500">
        suparship clones the repo at <code>ref</code>, walks{" "}
        <code>path</code> for <code>Chart.yaml</code> directories, and imports
        each as a template.
      </p>
      <div className="mt-4 grid gap-4 sm:grid-cols-2">
        <Field label="Name" help="Unique identifier shown in this list.">
          <input
            className={inputClass}
            value={draft.name}
            onChange={(e) => set({ name: e.target.value })}
            placeholder="myorg-charts"
          />
        </Field>
        <Field label="Existing auth secret" help="K8s Secret in suparship-system with username/password keys. Empty for public repos.">
          <input
            className={inputClass}
            value={draft.existingSecret ?? ""}
            onChange={(e) => set({ existingSecret: e.target.value })}
            placeholder="(optional)"
          />
        </Field>
        <Field
          label="Repository URL"
          help="HTTPS or SSH URL for git clone."
        >
          <input
            className={inputClass}
            value={draft.repoURL}
            onChange={(e) => set({ repoURL: e.target.value })}
            placeholder="https://github.com/myorg/charts.git"
          />
        </Field>
        <Field label="Ref" help="Git tag, branch, or commit. Defaults to main.">
          <input
            className={inputClass}
            value={draft.ref}
            onChange={(e) => set({ ref: e.target.value })}
            placeholder="main"
          />
        </Field>
        <Field
          label="Path"
          help="Directory under the repo containing Chart.yaml folders. Empty for repo root."
        >
          <input
            className={inputClass}
            value={draft.path}
            onChange={(e) => set({ path: e.target.value })}
            placeholder="charts"
          />
        </Field>
      </div>
      <div className="mt-5 flex justify-end">
        <button
          type="button"
          onClick={onSubmit}
          disabled={saving}
          className="rounded-md bg-gray-900 px-4 py-2 text-sm font-medium text-white hover:bg-gray-800 disabled:opacity-50"
        >
          {saving ? "Saving…" : "Save source"}
        </button>
      </div>
    </div>
  );
}

function Field({
  label,
  help,
  children,
}: {
  label: string;
  help?: string;
  children: React.ReactNode;
}) {
  return (
    <label className="block">
      <span className="text-sm font-medium text-gray-700">{label}</span>
      <div className="mt-1">{children}</div>
      {help && <p className="mt-1 text-xs text-gray-400">{help}</p>}
    </label>
  );
}
