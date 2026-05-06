import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { toast } from "sonner";

import { ApiError } from "../lib/api";
import {
  fetchTemplateRegistry,
  setSourceCredentials,
  syncAllSources,
  syncSource,
  testSourceConnection,
  updateTemplateRegistry,
} from "../lib/templates";
import type {
  ExternalTemplateRepo,
  TemplateCredentialsRequest,
  TemplateRegistry,
  TemplateRepoProvider,
  TemplateSyncResult,
  TemplateTestConnectionResult,
} from "../types";

// Deterministic prefix used by credstore.SecretNameFor on the backend.
// Knowing this lets us label rows as "managed" vs "hand-wired" at a
// glance without an extra API call.
const MANAGED_SECRET_PREFIX = "suparship-tpl-credentials-";

const PROVIDERS: ReadonlyArray<{ value: TemplateRepoProvider; label: string }> =
  [
    { value: "github", label: "GitHub" },
    { value: "gitlab", label: "GitLab" },
    { value: "gitea", label: "Gitea" },
    { value: "bitbucket", label: "Bitbucket" },
    { value: "generic", label: "Generic (HTTPS Basic)" },
  ];

// usesToken returns true when the provider authenticates via a single
// PAT (token field), false when it needs username + password.
function usesToken(provider: TemplateRepoProvider | "" | undefined): boolean {
  return provider === "github" || provider === "gitlab" || provider === "gitea";
}

function isManagedSecret(repo: ExternalTemplateRepo): boolean {
  return repo.existingSecret === `${MANAGED_SECRET_PREFIX}${repo.name}`;
}

// emptyRepo seeds the inline "add source" form. Lives outside the component
// because react re-creates it on every render otherwise, which would reset
// the form mid-typing if we ever passed it as a memoized default.
const emptyRepo: ExternalTemplateRepo = {
  name: "",
  type: "git",
  repoURL: "",
  ref: "main",
  path: "",
  chart: "",
  version: "",
  existingSecret: "",
};

// Source-type dropdown options. Each maps to a fetcher in registrysync.
const SOURCE_TYPES: ReadonlyArray<{
  value: "git" | "oci" | "chartmuseum" | "gittgz";
  label: string;
  description: string;
}> = [
  {
    value: "git",
    label: "Git templates repo",
    description: "Clone a repo and discover templates under a path. Mixes inline charts and external (registry-ref) wrapper templates.",
  },
  {
    value: "oci",
    label: "OCI chart",
    description: "Pull a single chart from an OCI registry by name+version. The chart's bundled template.yaml drives metadata when present.",
  },
  {
    value: "chartmuseum",
    label: "Helm HTTP repo",
    description: "Pull a single chart from a classic Helm HTTP repository (ChartMuseum, GitHub Pages-served, JFrog).",
  },
  {
    value: "gittgz",
    label: "Chart .tgz in git",
    description: "Read a packaged chart .tgz at a known path inside a git repo. Useful when charts are version-controlled rather than published to a registry.",
  },
];

// usesGit returns true when ref+path apply (git-type sources).
function usesGit(t: ExternalTemplateRepo["type"]): boolean {
  return !t || t === "git";
}

// usesChart returns true when chart+version apply (registry-type sources).
function usesChart(t: ExternalTemplateRepo["type"]): boolean {
  return t === "oci" || t === "chartmuseum";
}

// usesGitTgz returns true when ref+path-to-tgz apply.
function usesGitTgz(t: ExternalTemplateRepo["type"]): boolean {
  return t === "gittgz";
}

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
  const [credsFor, setCredsFor] = useState<ExternalTemplateRepo | null>(null);

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

  // handleAdd persists the source. When `thenSetCredentials` is true the
  // credentials dialog opens against the freshly saved row — that's the
  // path operators take for private repos so they don't have to find the
  // per-row "Set credentials" button after saving.
  async function handleAdd(thenSetCredentials: boolean) {
    if (!draft.name.trim() || !draft.repoURL.trim()) {
      toast.error("Name and Repo URL are required");
      return;
    }
    if (usesChart(draft.type) && (!draft.chart?.trim() || !draft.version?.trim())) {
      toast.error("Chart and Version are required for registry-pull sources");
      return;
    }
    if (usesGitTgz(draft.type) && !draft.path.trim()) {
      toast.error("Path to the .tgz file is required for git-tgz sources");
      return;
    }
    if ((registry.external ?? []).some((r) => r.name === draft.name)) {
      toast.error(`A source named ${draft.name} already exists`);
      return;
    }
    setSavingRepo(true);
    try {
      const saved: ExternalTemplateRepo = { ...draft };
      const next: TemplateRegistry = {
        ...registry,
        external: [...(registry.external ?? []), saved],
      };
      const res = await updateTemplateRegistry(next);
      setRegistry(res.registry);
      setDraft(emptyRepo);
      setShowAdd(false);
      toast.success(`Added source ${saved.name}`);
      if (thenSetCredentials) {
        // Use the saved draft directly; the registry response also
        // contains the row but lookup-by-name would have to handle the
        // case where the server normalised something. Direct is simpler.
        setCredsFor(saved);
      }
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
          onSubmit={() => handleAdd(false)}
          onSubmitWithCredentials={() => handleAdd(true)}
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
                        {usesChart(repo.type) ? (
                          <span>
                            {repo.chart || "?"}@{repo.version || "?"}
                            {" · "}{repo.type}
                          </span>
                        ) : usesGitTgz(repo.type) ? (
                          <span>
                            {repo.ref || "main"} · {repo.path || "?"}
                            {" · gittgz"}
                          </span>
                        ) : (
                          <span>
                            {repo.ref || "main"} · {repo.path || "/"}
                          </span>
                        )}
                        {repo.existingSecret ? (
                          isManagedSecret(repo) ? (
                            <span> · auth: managed (sealed)</span>
                          ) : (
                            <span> · auth: {repo.existingSecret}</span>
                          )
                        ) : null}
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
                        onClick={() => setCredsFor(repo)}
                        className="ml-2 rounded-md border border-gray-300 bg-white px-3 py-1.5 text-xs font-medium text-gray-700 hover:bg-gray-50"
                      >
                        {repo.existingSecret ? "Update credentials" : "Set credentials"}
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

      {credsFor && (
        <CredentialsDialog
          source={credsFor}
          onClose={() => setCredsFor(null)}
          onSaved={(updated) => {
            // Optimistic local update so the row reflects the new
            // existingSecret immediately; followed by a refetch so the
            // sources list (next sync result) is authoritative.
            setRegistry((reg) => ({
              ...reg,
              external: (reg.external ?? []).map((r) =>
                r.name === updated.name ? updated : r,
              ),
            }));
            fetchTemplateRegistry()
              .then((res) => setRegistry(res.registry))
              .catch((err) => toast.error(extractError(err, "Refresh failed")));
          }}
        />
      )}
    </div>
  );
}

function AddSourceForm({
  draft,
  onChange,
  onSubmit,
  onSubmitWithCredentials,
  saving,
}: {
  draft: ExternalTemplateRepo;
  onChange: (next: ExternalTemplateRepo) => void;
  onSubmit: () => void;
  onSubmitWithCredentials: () => void;
  saving: boolean;
}) {
  const set = (partial: Partial<ExternalTemplateRepo>) =>
    onChange({ ...draft, ...partial });
  const sourceType = draft.type || "git";
  const showGitFields = usesGit(sourceType);
  const showChartFields = usesChart(sourceType);
  const showGitTgzFields = usesGitTgz(sourceType);
  return (
    <div className="rounded-xl border border-gray-200 bg-white p-6">
      <h2 className="text-base font-semibold text-gray-900">Add source</h2>
      <p className="mt-1 text-xs text-gray-500">
        Pick a source type below; fields adjust to match. Git sources walk a
        repo path for templates; OCI sources pull a single chart by name and
        version.
      </p>
      <div className="mt-4 grid gap-4 sm:grid-cols-2">
        <Field label="Source type" help="Git for a templates repo; OCI for a single Helm-registry chart.">
          <select
            className={inputClass}
            value={sourceType}
            onChange={(e) => set({ type: e.target.value as ExternalTemplateRepo["type"] })}
          >
            {SOURCE_TYPES.map((opt) => (
              <option key={opt.value} value={opt.value}>{opt.label}</option>
            ))}
          </select>
        </Field>
        <Field label="Name" help="Unique identifier shown in this list.">
          <input
            className={inputClass}
            value={draft.name}
            onChange={(e) => set({ name: e.target.value })}
            placeholder={showChartFields ? "web-service-stdlib" : "myorg-charts"}
          />
        </Field>
        <Field
          label="Repository URL"
          help={
            sourceType === "oci"
              ? "OCI registry root, e.g. oci://ghcr.io/myorg/charts"
              : sourceType === "chartmuseum"
                ? "HTTPS root of a Helm HTTP repo, e.g. https://charts.acme.io"
                : "HTTPS or SSH URL for git clone."
          }
        >
          <input
            className={inputClass}
            value={draft.repoURL}
            onChange={(e) => set({ repoURL: e.target.value })}
            placeholder={
              sourceType === "oci"
                ? "oci://ghcr.io/myorg/charts"
                : sourceType === "chartmuseum"
                  ? "https://charts.acme.io"
                  : "https://github.com/myorg/charts.git"
            }
          />
        </Field>
        <Field
          label="Existing auth secret"
          help="K8s Secret in suparship-system with token (or username+password) keys. Empty for public sources."
        >
          <input
            className={inputClass}
            value={draft.existingSecret ?? ""}
            onChange={(e) => set({ existingSecret: e.target.value })}
            placeholder="(optional)"
          />
        </Field>
        {showGitFields && (
          <>
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
              help="Directory under the repo containing template folders. Empty for repo root."
            >
              <input
                className={inputClass}
                value={draft.path}
                onChange={(e) => set({ path: e.target.value })}
                placeholder="templates"
              />
            </Field>
          </>
        )}
        {showGitTgzFields && (
          <>
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
              help="Path to the chart .tgz file inside the repo, e.g. charts/web-service-1.2.0.tgz."
            >
              <input
                className={inputClass}
                value={draft.path}
                onChange={(e) => set({ path: e.target.value })}
                placeholder="charts/web-service-1.2.0.tgz"
              />
            </Field>
          </>
        )}
        {showChartFields && (
          <>
            <Field label="Chart" help="Chart name within the registry.">
              <input
                className={inputClass}
                value={draft.chart ?? ""}
                onChange={(e) => set({ chart: e.target.value })}
                placeholder="web-service"
              />
            </Field>
            <Field label="Version" help="Pinned chart version (SemVer).">
              <input
                className={inputClass}
                value={draft.version ?? ""}
                onChange={(e) => set({ version: e.target.value })}
                placeholder="1.2.0"
              />
            </Field>
          </>
        )}
      </div>
      <div className="mt-5 flex flex-wrap items-center justify-end gap-2">
        <p className="mr-auto text-xs text-gray-500">
          Public source? Save source. Private? Save &amp; set credentials seals
          a token for the source in one step.
        </p>
        <button
          type="button"
          onClick={onSubmit}
          disabled={saving}
          className="rounded-md border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 disabled:opacity-50"
        >
          {saving ? "Saving…" : "Save source"}
        </button>
        <button
          type="button"
          onClick={onSubmitWithCredentials}
          disabled={saving}
          className="rounded-md bg-gray-900 px-4 py-2 text-sm font-medium text-white hover:bg-gray-800 disabled:opacity-50"
        >
          {saving ? "Saving…" : "Save & set credentials"}
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

function CredentialsDialog({
  source,
  onClose,
  onSaved,
}: {
  source: ExternalTemplateRepo;
  onClose: () => void;
  onSaved: (updated: ExternalTemplateRepo) => void;
}) {
  // Default to the source's stored provider when known, else github
  // (most common case for the public-stdlib + private-fork pattern).
  const initialProvider: TemplateRepoProvider =
    (source.provider as TemplateRepoProvider) || "github";
  const [provider, setProvider] = useState<TemplateRepoProvider>(initialProvider);
  const [token, setToken] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState<TemplateTestConnectionResult | null>(null);

  // Build a request payload from current form state. Used by both
  // Test connection and Save so they exercise the same shape.
  function buildReq(): TemplateCredentialsRequest {
    if (usesToken(provider)) {
      return { provider, token };
    }
    return { provider, username, password };
  }

  function hasInputs(): boolean {
    if (usesToken(provider)) return token.trim().length > 0;
    return username.trim().length > 0 || password.trim().length > 0;
  }

  async function handleTest() {
    setTesting(true);
    setTestResult(null);
    try {
      // When the form is empty the backend falls back to credentials
      // already stored for this source — handy for "are my saved
      // credentials still good?" without re-pasting the PAT.
      const req = hasInputs() ? buildReq() : {};
      const res = await testSourceConnection(source.name, req);
      setTestResult(res);
    } catch (err) {
      setTestResult({
        success: false,
        message: extractError(err, "Test failed"),
        durationMs: 0,
      });
    } finally {
      setTesting(false);
    }
  }

  async function handleSave() {
    if (!hasInputs()) {
      toast.error(
        usesToken(provider) ? "Token is required" : "Username and password are required",
      );
      return;
    }
    setSaving(true);
    setTestResult(null);
    try {
      const res = await setSourceCredentials(source.name, buildReq());
      if (res.testResult) setTestResult(res.testResult);
      toast.success(`Credentials sealed for ${source.name}`);
      onSaved(res.source);
      onClose();
    } catch (err) {
      // The backend returns 400 with the test result inline when
      // verification fails. Try to surface the message; fall back to
      // a generic toast.
      if (err instanceof ApiError) {
        toast.error(err.message);
      } else {
        toast.error(extractError(err, "Save failed"));
      }
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
      <div className="w-full max-w-md rounded-xl bg-white p-6 shadow-xl">
        <h3 className="text-base font-semibold text-gray-900">
          Credentials for {source.name}
        </h3>
        <p className="mt-1 text-xs text-gray-500">
          suparship seals the credentials into a SealedSecret in
          suparship-system; the plaintext never lands on the cluster as
          a regular Secret. Verified against{" "}
          <span className="font-mono">{source.repoURL}</span> before saving.
        </p>

        <div className="mt-4 space-y-4">
          <Field label="Provider" help="Determines the credential format and how git authenticates.">
            <select
              className={inputClass}
              value={provider}
              onChange={(e) => {
                setProvider(e.target.value as TemplateRepoProvider);
                setTestResult(null);
              }}
            >
              {PROVIDERS.map((p) => (
                <option key={p.value} value={p.value}>
                  {p.label}
                </option>
              ))}
            </select>
          </Field>

          {usesToken(provider) ? (
            <Field
              label="Personal access token"
              help="Read access to the templates repo is enough. Don't reuse the gitops PAT — that one has write access to your fleet."
            >
              <input
                type="password"
                className={inputClass}
                value={token}
                onChange={(e) => setToken(e.target.value)}
                placeholder="ghp_… / glpat-… / etc."
                autoComplete="off"
              />
            </Field>
          ) : (
            <>
              <Field label="Username">
                <input
                  className={inputClass}
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  autoComplete="off"
                />
              </Field>
              <Field
                label={provider === "bitbucket" ? "App password" : "Password"}
                help={
                  provider === "bitbucket"
                    ? "Bitbucket app password (account settings → app passwords)."
                    : undefined
                }
              >
                <input
                  type="password"
                  className={inputClass}
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  autoComplete="off"
                />
              </Field>
            </>
          )}

          {testResult && (
            <div
              className={`rounded-md border p-3 text-xs ${
                testResult.success
                  ? "border-green-200 bg-green-50 text-green-800"
                  : "border-red-200 bg-red-50 text-red-800"
              }`}
            >
              <div className="font-medium">
                {testResult.success ? "Verified" : "Failed"} · {testResult.durationMs}ms
              </div>
              <div className="mt-1 break-words">{testResult.message}</div>
            </div>
          )}
        </div>

        <div className="mt-6 flex items-center justify-between gap-2">
          <button
            type="button"
            onClick={handleTest}
            disabled={testing || saving}
            className="rounded-md border border-gray-300 bg-white px-3 py-2 text-xs font-medium text-gray-700 hover:bg-gray-50 disabled:opacity-50"
          >
            {testing ? "Testing…" : "Test connection"}
          </button>
          <div className="flex gap-2">
            <button
              type="button"
              onClick={onClose}
              disabled={saving}
              className="rounded-md border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 disabled:opacity-50"
            >
              Cancel
            </button>
            <button
              type="button"
              onClick={handleSave}
              disabled={saving}
              className="rounded-md bg-gray-900 px-4 py-2 text-sm font-medium text-white hover:bg-gray-800 disabled:opacity-50"
            >
              {saving ? "Sealing…" : "Save"}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}
