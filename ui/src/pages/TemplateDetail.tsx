import { Suspense, lazy, useCallback, useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { toast } from "sonner";

import { ApiError } from "../lib/api";
import { useAuth } from "../lib/AuthContext";
import {
  deleteTemplate,
  fetchTemplate,
  fetchTemplateOverride,
  previewTemplateEffectiveValues,
  updateTemplateMetadata,
  updateTemplateOverride,
} from "../lib/templates";
import { listOrgEnvironments } from "../lib/settings";
import { listClusters } from "../lib/clusters";
import type { Cluster } from "../lib/clusters";
import { listPlatformConfigVariables } from "../lib/configVars";
import type { ConfigVariables } from "../lib/configVars";
import type {
  TemplateDetail as TemplateDetailType,
  TemplateImage,
  TemplateOverride,
  TemplateSecretInput,
} from "../types";

// CodeMirror is heavy; only the override editor needs it.
const ScopedValuesEditor = lazy(() => import("../components/ScopedValuesEditor"));
import type {
  ScopeBase,
  ValuesScope,
} from "../components/ScopedValuesEditor";

function SecretInputCard({ input }: { input: TemplateSecretInput }) {
  return (
    <div className="rounded-lg border border-amber-200 bg-amber-50/50 p-4">
      <div className="flex items-center gap-2">
        <svg className="h-4 w-4 text-amber-500" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
          <path strokeLinecap="round" strokeLinejoin="round" d="M16.5 10.5V6.75a4.5 4.5 0 1 0-9 0v3.75m-.75 11.25h10.5a2.25 2.25 0 0 0 2.25-2.25v-6.75a2.25 2.25 0 0 0-2.25-2.25H6.75a2.25 2.25 0 0 0-2.25 2.25v6.75a2.25 2.25 0 0 0 2.25 2.25Z" />
        </svg>
        <h4 className="text-sm font-semibold text-gray-900">{input.title}</h4>
      </div>
      {input.description && (
        <p className="mt-1.5 text-sm text-gray-500">{input.description}</p>
      )}
      <div className="mt-2 text-xs text-gray-400">
        <span className="font-mono">{input.name}</span>
        <span className="mx-2">&middot;</span>
        <span>
          Ref: <code className="rounded bg-white px-1 py-0.5 font-mono text-gray-600">{input.secretRef}</code>
        </span>
      </div>
    </div>
  );
}

export function TemplateDetail() {
  const { name } = useParams<{ name: string }>();
  const navigate = useNavigate();
  const [template, setTemplate] = useState<TemplateDetailType | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [deleting, setDeleting] = useState(false);

  useEffect(() => {
    if (!name) return;
    let cancelled = false;

    async function load() {
      try {
        const data = await fetchTemplate(name!);
        if (cancelled) return;
        setTemplate(data);
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
  }, [name]);

  if (loading) {
    return (
      <div className="space-y-6">
        <div className="h-5 w-32 animate-pulse rounded bg-gray-100" />
        <div className="space-y-2">
          <div className="h-8 w-64 animate-pulse rounded bg-gray-100" />
          <div className="h-5 w-96 animate-pulse rounded bg-gray-50" />
        </div>
        <div className="space-y-3">
          {[1, 2, 3].map((n) => (
            <div key={n} className="h-24 animate-pulse rounded-lg bg-gray-50" />
          ))}
        </div>
      </div>
    );
  }

  if (error || !template) {
    return (
      <div className="space-y-4">
        <Link
          to="/templates"
          className="inline-flex items-center gap-1 text-sm text-gray-500 hover:text-gray-700"
        >
          &larr; Back to templates
        </Link>
        <div className="rounded-lg border border-red-200 bg-red-50 p-4">
          <p className="text-sm text-red-700">
            {error ?? "Template not found"}
          </p>
        </div>
      </div>
    );
  }

  // handleDelete drops the template's cluster ConfigMap. We confirm
  // explicitly because the action is destructive and not reversible
  // via the UI (operators would re-import or wait for a sync to
  // restore an external-source template). 409s on built-ins are
  // surfaced via ApiError.message — the user finds out why.
  async function handleDelete() {
    if (!template) return;
    const confirmed = window.confirm(
      `Delete template "${template.name}"?\n\n` +
        "Existing apps deployed from it will keep running, but you " +
        "won't be able to create new ones until the template is " +
        "re-imported. If this template comes from an external source, " +
        "the next sync will re-create it — remove the source path " +
        "instead to make the deletion stick.",
    );
    if (!confirmed) return;
    setDeleting(true);
    try {
      await deleteTemplate(template.name);
      toast.success(`Deleted ${template.name}`);
      navigate("/templates");
    } catch (err) {
      if (err instanceof ApiError) {
        toast.error(err.message);
      } else if (err instanceof Error) {
        toast.error(err.message);
      } else {
        toast.error("Delete failed");
      }
    } finally {
      setDeleting(false);
    }
  }

  return (
    <div className="space-y-8">
      {/* Breadcrumb */}
      <Link
        to="/templates"
        className="inline-flex items-center gap-1 text-sm text-gray-500 transition-colors hover:text-gray-700"
      >
        &larr; Back to templates
      </Link>

      {/* Header */}
      <div className="flex items-start justify-between gap-4">
        <div>
          <div className="flex items-center gap-3">
            <h1 className="text-2xl font-semibold text-gray-900">
              {template.title}
            </h1>
            <span className="rounded-full bg-gray-100 px-2.5 py-0.5 text-xs font-medium text-gray-500">
              v{template.version}
            </span>
          </div>
          {template.description && (
            <p className="mt-2 max-w-2xl text-sm leading-relaxed text-gray-500">
              {template.description}
            </p>
          )}
        </div>
        <button
          type="button"
          onClick={handleDelete}
          disabled={deleting}
          className="shrink-0 rounded-md border border-red-200 bg-white px-3 py-1.5 text-sm font-medium text-red-700 hover:bg-red-50 disabled:opacity-50"
        >
          {deleting ? "Deleting…" : "Delete template"}
        </button>
      </div>

      {/* Metadata — editable for org_admins on imported templates */}
      <MetadataSection template={template} onUpdated={setTemplate} />

      {/* Image mappings — drive external-CD (Kargo) wiring */}
      <ImagesSection template={template} onUpdated={setTemplate} />

      {/* Template inputs are deprecated and not shown — apps are configured via
          the values editor, and the effective-values preview is the real "what
          deploys" reference. */}

      {/* Secret inputs */}
      {template.secretInputs.length > 0 && (
        <Section title="Secrets" subtitle="References to Kubernetes Secrets — values are never stored in Git.">
          <div className="space-y-3">
            {template.secretInputs.map((si) => (
              <SecretInputCard key={si.name} input={si} />
            ))}
          </div>
        </Section>
      )}

      {/* Platform overrides — org_admin only */}
      <PlatformOverridesEditor templateName={template.name} />
    </div>
  );
}

const ALL_ENVS = "__all__";
const PREVIEW_SCOPE = "__preview__";
// Scope values are "__all__", "__preview__", "env:<name>", or "cluster:<ref>".
const envScope = (name: string) => `env:${name}`;
const clusterScope = (ref: string) => `cluster:${ref}`;

// PlatformOverridesEditor lets a platform engineer (org_admin) curate a concise,
// opinionated override on top of a template's chart defaults — per env AND per
// cluster — WITHOUT forking the upstream template. Stored separately so an external
// sync can't clobber it. It uses the shared ScopedValuesEditor: the PE edits the
// resolved values in place and only the diff is persisted into the TemplateOverride.
// Hidden for non-admins.
function PlatformOverridesEditor({ templateName }: { templateName: string }) {
  const { user } = useAuth();
  const isAdmin = user?.role === "org_admin";

  const [envs, setEnvs] = useState<string[]>([]);
  const [clusters, setClusters] = useState<Cluster[]>([]);
  const [override, setOverride] = useState<TemplateOverride>({});
  const [configVars, setConfigVars] = useState<ConfigVariables | null>(null);

  useEffect(() => {
    if (!isAdmin) return;
    let cancelled = false;
    fetchTemplateOverride(templateName)
      .then((ov) => {
        if (!cancelled) setOverride(ov ?? {});
      })
      .catch(() => {
        /* no override yet */
      });
    listOrgEnvironments()
      .then((res) =>
        setEnvs(
          (res.environments ?? [])
            .map((e) => e.name)
            .filter((n) => n !== "preview"),
        ),
      )
      .catch(() => setEnvs([]));
    listClusters()
      .then(setClusters)
      .catch(() => setClusters([]));
    listPlatformConfigVariables()
      .then(setConfigVars)
      .catch(() => setConfigVars({ platform: [], vars: [] }));
    return () => {
      cancelled = true;
    };
  }, [isAdmin, templateName]);

  const clusterLabel = (c: Cluster) => c.displayName || c.name;

  // The persisted override (diff) stored for a scope, read from the TemplateOverride.
  const storedFor = useCallback(
    (id: string): Record<string, unknown> | undefined => {
      if (id === ALL_ENVS) return override.defaultValues;
      if (id === PREVIEW_SCOPE) return override.previewDefaultValues;
      if (id.startsWith("env:")) return override.envValues?.[id.slice(4)];
      if (id.startsWith("cluster:")) return override.clusterValues?.[id.slice(8)];
      return undefined;
    },
    [override],
  );

  const scopes: ValuesScope[] = useMemo(() => {
    const nonEmpty = (id: string) => {
      const v = storedFor(id);
      return !!v && Object.keys(v).length > 0;
    };
    return [
      { id: ALL_ENVS, label: "All environments", hasOverride: nonEmpty(ALL_ENVS) },
      {
        id: PREVIEW_SCOPE,
        label: "Preview defaults",
        hasOverride: nonEmpty(PREVIEW_SCOPE),
      },
      ...envs.map((e) => ({
        id: envScope(e),
        label: e,
        hasOverride: nonEmpty(envScope(e)),
      })),
      ...clusters.map((c) => ({
        id: clusterScope(c.name),
        label: clusterLabel(c),
        hasOverride: nonEmpty(clusterScope(c.name)),
      })),
    ];
  }, [envs, clusters, storedFor]);

  // Reassemble a TemplateOverride from per-scope diffs, dropping the excluded scope
  // (used to fetch a scope's base = resolved values with its own override removed).
  function overrideFromDiffs(
    diffs: Record<string, Record<string, unknown>>,
    exclude: string,
  ): TemplateOverride {
    const o: TemplateOverride = {};
    const envValues: Record<string, Record<string, unknown>> = {};
    const clusterValues: Record<string, Record<string, unknown>> = {};
    for (const [id, d] of Object.entries(diffs)) {
      if (id === exclude || !d || Object.keys(d).length === 0) continue;
      if (id === ALL_ENVS) o.defaultValues = d;
      else if (id === PREVIEW_SCOPE) o.previewDefaultValues = d;
      else if (id.startsWith("env:")) envValues[id.slice(4)] = d;
      else if (id.startsWith("cluster:")) clusterValues[id.slice(8)] = d;
    }
    if (Object.keys(envValues).length) o.envValues = envValues;
    if (Object.keys(clusterValues).length) o.clusterValues = clusterValues;
    return o;
  }

  const getBase = useCallback(
    async (
      scopeId: string,
      diffs: Record<string, Record<string, unknown>>,
    ): Promise<ScopeBase> => {
      const ov = overrideFromDiffs(diffs, scopeId);
      const env = scopeId.startsWith("env:") ? scopeId.slice(4) : "";
      const cluster = scopeId.startsWith("cluster:") ? scopeId.slice(8) : "";
      const res = await previewTemplateEffectiveValues(
        templateName,
        env,
        cluster,
        ov,
        scopeId === PREVIEW_SCOPE,
      );
      return {
        values: res.values,
        chartDefaultsAvailable: res.chartDefaultsAvailable,
      };
    },
    [templateName],
  );

  const saveOverride = useCallback(
    async (scopeId: string, diff: Record<string, unknown>) => {
      const next: TemplateOverride = structuredClone(override);
      const empty = Object.keys(diff).length === 0;
      if (scopeId === ALL_ENVS) next.defaultValues = empty ? undefined : diff;
      else if (scopeId === PREVIEW_SCOPE)
        next.previewDefaultValues = empty ? undefined : diff;
      else if (scopeId.startsWith("env:")) {
        const e = { ...(next.envValues ?? {}) };
        if (empty) delete e[scopeId.slice(4)];
        else e[scopeId.slice(4)] = diff;
        next.envValues = Object.keys(e).length ? e : undefined;
      } else if (scopeId.startsWith("cluster:")) {
        const c = { ...(next.clusterValues ?? {}) };
        if (empty) delete c[scopeId.slice(8)];
        else c[scopeId.slice(8)] = diff;
        next.clusterValues = Object.keys(c).length ? c : undefined;
      }
      try {
        const saved = await updateTemplateOverride(templateName, next);
        setOverride(saved ?? next);
        toast.success("Platform overrides saved — applies on next publish.");
      } catch (err) {
        toast.error(
          err instanceof Error ? err.message : "Failed to save overrides",
        );
      }
    },
    [override, templateName],
  );

  const scopeHelp = (scopeId: string): ReactNode =>
    scopeId === PREVIEW_SCOPE
      ? "Applied to every preview of apps using this template, below each app's own preview override. Preview-only — stable envs are unaffected."
      : scopeId.startsWith("cluster:")
        ? "Cluster-specific platform values (e.g. cloud annotations), applied to every app on this cluster."
        : "Applied to every app using this template, below per-app overrides.";

  if (!isAdmin) return null;

  return (
    <div>
      <h2 className="text-lg font-semibold text-gray-900">Platform overrides</h2>
      <p className="mt-0.5 mb-4 max-w-2xl text-sm text-gray-500">
        Curate a concise, opinionated override on top of this template's chart
        defaults — applied to every app using it, below per-app overrides. Edit the
        resolved values in place; only your diff is saved. Stored separately from
        the template, so an external sync won't clobber it.
      </p>
      <Suspense
        fallback={
          <div className="rounded-lg border border-gray-200 p-4 text-xs text-gray-400">
            Loading editor…
          </div>
        }
      >
        <ScopedValuesEditor
          scopes={scopes}
          configVars={configVars}
          getBase={getBase}
          getStoredOverride={storedFor}
          saveOverride={saveOverride}
          scopeHelp={scopeHelp}
        />
      </Suspense>
    </div>
  );
}

function valuesModeLabel(t: TemplateDetailType): string {
  return t.injectCanonicalValues === false
    ? "Passthrough (BYO chart)"
    : "Canonical values";
}

const IMAGE_STRATEGIES = ["", "NewestBuild", "SemVer", "Digest", "Lexical"];

// ImagesSection shows + edits the template's per-service image mapping, which
// drives external-CD (Kargo): which repository each service watches and which
// Helm values key holds its tag. Editable in place only for imported/BYO
// templates (synced/built-in mappings come from the source's template.yaml).
function ImagesSection({
  template,
  onUpdated,
}: {
  template: TemplateDetailType;
  onUpdated: (t: TemplateDetailType) => void;
}) {
  const { user } = useAuth();
  const isOrgAdmin = user?.role === "org_admin";
  const inPlace = template.editable === true;

  const [editing, setEditing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [rows, setRows] = useState<TemplateImage[]>(template.images ?? []);

  function reset() {
    setRows(template.images ?? []);
    setEditing(false);
  }

  function setRow(i: number, patch: Partial<TemplateImage>) {
    setRows((cur) => cur.map((r, idx) => (idx === i ? { ...r, ...patch } : r)));
  }

  async function save() {
    setSaving(true);
    try {
      const cleaned = rows.map((r) => ({
        name: r.name.trim(),
        repository: r.repository.trim(),
        tagKey: r.tagKey.trim(),
        tagPattern: r.tagPattern?.trim() || undefined,
        selectionStrategy: r.selectionStrategy || undefined,
      }));
      const updated = await updateTemplateMetadata(template.name, {
        images: cleaned,
      });
      onUpdated(updated);
      toast.success("Image mappings updated");
      setEditing(false);
    } catch (err) {
      toast.error(
        err instanceof ApiError ? err.message : "Failed to update image mappings",
      );
    } finally {
      setSaving(false);
    }
  }

  const images = template.images ?? [];

  return (
    <div className="rounded-xl border border-gray-200 bg-white p-5">
      <div className="mb-3 flex items-center justify-between">
        <div>
          <h2 className="text-sm font-semibold text-gray-900">Images</h2>
          <p className="text-xs text-gray-400">
            Per-service image mapping for continuous delivery (Kargo): the repo
            to watch and the values key holding each tag.
          </p>
        </div>
        {isOrgAdmin && !editing && (
          <button
            onClick={() => setEditing(true)}
            className="rounded-md px-3 py-1 text-xs font-medium text-gray-500 hover:bg-gray-50"
          >
            Edit
          </button>
        )}
        {editing && (
          <div className="flex items-center gap-2">
            <button
              onClick={reset}
              disabled={saving}
              className="rounded-md px-3 py-1 text-xs font-medium text-gray-500 hover:bg-gray-50"
            >
              Cancel
            </button>
            <button
              onClick={save}
              disabled={saving}
              className="rounded-md bg-gray-900 px-3 py-1 text-xs font-medium text-white hover:bg-gray-700 disabled:opacity-50"
            >
              {saving ? "Saving…" : "Save"}
            </button>
          </div>
        )}
      </div>

      {!inPlace && (
        <p className="mb-3 rounded-md bg-blue-50 px-3 py-2 text-xs text-blue-700">
          This template is managed by its source. Image mappings you set here are
          saved as a local override (sync-safe) — a re-sync won't clobber them.
        </p>
      )}

      {!editing ? (
        images.length === 0 ? (
          <p className="text-xs text-gray-400">
            No image mappings.{" "}
            {isOrgAdmin && "Add one to wire up Kargo-driven CD."}
          </p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-xs">
              <thead className="text-left text-gray-400">
                <tr>
                  <th className="py-1 pr-3 font-medium">Service</th>
                  <th className="py-1 pr-3 font-medium">Repository</th>
                  <th className="py-1 pr-3 font-medium">Tag key</th>
                  <th className="py-1 pr-3 font-medium">Tag pattern</th>
                  <th className="py-1 font-medium">Strategy</th>
                </tr>
              </thead>
              <tbody className="font-mono text-gray-700">
                {images.map((im) => (
                  <tr key={im.name} className="border-t border-gray-100">
                    <td className="py-1 pr-3">{im.name}</td>
                    <td className="py-1 pr-3">{im.repository}</td>
                    <td className="py-1 pr-3">{im.tagKey}</td>
                    <td className="py-1 pr-3">{im.tagPattern || "—"}</td>
                    <td className="py-1">{im.selectionStrategy || "SemVer"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )
      ) : (
        <div className="space-y-3">
          {rows.map((r, i) => (
            <div
              key={i}
              className="grid grid-cols-1 gap-2 rounded-lg border border-gray-200 p-3 sm:grid-cols-2"
            >
              <input
                className="rounded-md border border-gray-300 px-2 py-1 text-xs"
                placeholder="service name (e.g. agent)"
                value={r.name}
                onChange={(e) => setRow(i, { name: e.target.value })}
              />
              <input
                className="rounded-md border border-gray-300 px-2 py-1 font-mono text-xs"
                placeholder="repository (e.g. acr.io/org/agent)"
                value={r.repository}
                onChange={(e) => setRow(i, { repository: e.target.value })}
              />
              <input
                className="rounded-md border border-gray-300 px-2 py-1 font-mono text-xs"
                placeholder="tag key (e.g. image.tag)"
                value={r.tagKey}
                onChange={(e) => setRow(i, { tagKey: e.target.value })}
              />
              <input
                className="rounded-md border border-gray-300 px-2 py-1 font-mono text-xs"
                placeholder="tag pattern (regex, optional)"
                value={r.tagPattern ?? ""}
                onChange={(e) => setRow(i, { tagPattern: e.target.value })}
              />
              <select
                className="rounded-md border border-gray-300 px-2 py-1 text-xs"
                value={r.selectionStrategy ?? ""}
                onChange={(e) => setRow(i, { selectionStrategy: e.target.value })}
              >
                {IMAGE_STRATEGIES.map((s) => (
                  <option key={s} value={s}>
                    {s === "" ? "SemVer (default)" : s}
                  </option>
                ))}
              </select>
              <button
                onClick={() => setRows((cur) => cur.filter((_, idx) => idx !== i))}
                className="justify-self-start rounded-md px-2 py-1 text-xs font-medium text-red-600 hover:bg-red-50"
              >
                Remove
              </button>
            </div>
          ))}
          <button
            onClick={() =>
              setRows((cur) => [
                ...cur,
                { name: "", repository: "", tagKey: "", tagPattern: "", selectionStrategy: "" },
              ])
            }
            className="rounded-md border border-dashed border-gray-300 px-3 py-1 text-xs font-medium text-gray-500 hover:bg-gray-50"
          >
            + Add image
          </button>
        </div>
      )}
    </div>
  );
}

// MetadataSection shows category/engine + values-mode and lets an org_admin edit
// the metadata. Imported/BYO templates are edited IN PLACE (title, category,
// description, passthrough). Synced/built-in templates have a read-only body, so
// their title/category/description are saved as a sync-safe override (the source
// isn't touched and a re-sync won't clobber it); the passthrough toggle is only
// available for in-place edits.
function MetadataSection({
  template,
  onUpdated,
}: {
  template: TemplateDetailType;
  onUpdated: (t: TemplateDetailType) => void;
}) {
  const { user } = useAuth();
  const isOrgAdmin = user?.role === "org_admin";
  // inPlace edits rewrite the template body (imported/BYO). Otherwise the edit
  // is persisted as a sync-safe metadata override (synced/built-in).
  const inPlace = template.editable === true;

  const [editing, setEditing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [title, setTitle] = useState(template.title);
  const [category, setCategory] = useState(template.category);
  const [description, setDescription] = useState(template.description ?? "");
  const [passthrough, setPassthrough] = useState(
    template.injectCanonicalValues === false,
  );
  const [deliveryMode, setDeliveryMode] = useState(
    template.deliveryMode === "direct" ? "direct" : "pipeline",
  );

  function reset() {
    setTitle(template.title);
    setCategory(template.category);
    setDescription(template.description ?? "");
    setPassthrough(template.injectCanonicalValues === false);
    setDeliveryMode(template.deliveryMode === "direct" ? "direct" : "pipeline");
    setEditing(false);
  }

  async function save() {
    setSaving(true);
    try {
      const updated = await updateTemplateMetadata(template.name, {
        title,
        category,
        description,
        // The passthrough toggle only applies to in-place edits (it rewrites the
        // chart body). Delivery mode is supported on both paths — in place for
        // imported templates, as a sync-safe override for synced/built-in ones.
        ...(inPlace ? { injectCanonicalValues: !passthrough } : {}),
        deliveryMode,
      });
      onUpdated(updated);
      toast.success(
        inPlace ? "Template metadata updated" : "Metadata override saved",
      );
      setEditing(false);
    } catch (err) {
      toast.error(
        err instanceof ApiError ? err.message : "Failed to update metadata",
      );
    } finally {
      setSaving(false);
    }
  }

  if (editing) {
    return (
      <div className="rounded-xl border border-gray-200 bg-white p-5">
        <div className="mb-4 flex items-center justify-between">
          <h2 className="text-sm font-semibold text-gray-900">
            {inPlace ? "Edit metadata" : "Override metadata"}
          </h2>
          <div className="flex items-center gap-2">
            <button
              onClick={reset}
              disabled={saving}
              className="rounded-md px-3 py-1 text-xs font-medium text-gray-500 hover:bg-gray-50"
            >
              Cancel
            </button>
            <button
              onClick={save}
              disabled={saving}
              className="rounded-md bg-gray-900 px-3 py-1 text-xs font-medium text-white hover:bg-gray-700 disabled:opacity-50"
            >
              {saving ? "Saving…" : "Save"}
            </button>
          </div>
        </div>
        {!inPlace && (
          <p className="mb-3 rounded-md bg-amber-50 px-3 py-2 text-xs text-amber-700">
            {template.source?.origin === "synced"
              ? "This template is managed by its source repo. Your edits are saved as a local override — the source isn't changed, and a re-sync won't overwrite them. Leave a field blank to fall back to the source value."
              : "This is a built-in template. Your edits are saved as a local override that won't be lost on upgrade. Leave a field blank to fall back to the built-in value."}
          </p>
        )}
        <div className="space-y-3">
          <label className="block">
            <span className="text-xs font-medium text-gray-600">Title</span>
            <input
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              className="mt-1 w-full max-w-xl rounded-md border border-gray-300 px-3 py-1.5 text-sm"
            />
          </label>
          <label className="block">
            <span className="text-xs font-medium text-gray-600">Category</span>
            <input
              value={category}
              onChange={(e) => setCategory(e.target.value)}
              placeholder="e.g. web, worker, voiceai"
              className="mt-1 w-full max-w-xs rounded-md border border-gray-300 px-3 py-1.5 text-sm"
            />
          </label>
          <label className="block">
            <span className="text-xs font-medium text-gray-600">Description</span>
            <textarea
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              rows={3}
              className="mt-1 w-full max-w-2xl rounded-md border border-gray-300 px-3 py-1.5 text-sm"
            />
          </label>
          {inPlace && (
            <label className="flex items-start gap-2">
              <input
                type="checkbox"
                checked={passthrough}
                onChange={(e) => setPassthrough(e.target.checked)}
                className="mt-0.5"
              />
              <span className="text-sm text-gray-700">
                Passthrough — this chart brings its own values (no canonical
                schema injected).{" "}
                <span className="text-xs text-gray-400">
                  Turning this on clears the auto-generated chart parameters.
                </span>
              </span>
            </label>
          )}
          <label className="block">
            <span className="text-xs font-medium text-gray-500">
              Delivery mode
            </span>
            <select
              value={deliveryMode}
              onChange={(e) => setDeliveryMode(e.target.value)}
              className="mt-1 w-full max-w-2xl rounded-md border border-gray-300 px-3 py-1.5 text-sm"
            >
              <option value="pipeline">Pipeline (Kargo + promotion)</option>
              <option value="direct">Direct (deploy each env from values)</option>
            </select>
            <span className="mt-1 block text-xs text-gray-400">
              Apps created from this template default to this. "Direct" suits
              off-the-shelf software (valkey, redis, postgres) with a pinned
              image — no Kargo, no promotion.
              {!inPlace && " Saved as a sync-safe override."}
            </span>
          </label>
        </div>
      </div>
    );
  }

  return (
    <div>
      <div className="grid grid-cols-2 gap-4">
        <StatCard label="Category" value={template.category} />
        <StatCard label="Engine" value={template.engine} />
      </div>
      <div className="mt-2 flex items-center justify-between gap-3">
        <span className="text-xs text-gray-400">
          {valuesModeLabel(template)}
          {template.deliveryMode === "direct"
            ? " · direct delivery"
            : " · pipeline delivery"}
          {template.source?.origin === "synced" && template.source.externalRepo
            ? ` · managed by ${template.source.externalRepo}`
            : template.source?.origin === "builtin"
              ? " · built-in"
              : ""}
        </span>
        {isOrgAdmin && (
          <button
            onClick={() => setEditing(true)}
            className="text-xs font-medium text-indigo-600 hover:text-indigo-800"
          >
            {inPlace ? "Edit metadata" : "Override metadata"}
          </button>
        )}
      </div>
    </div>
  );
}

function StatCard({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-lg border border-gray-200 bg-white px-4 py-3">
      <p className="text-xs font-medium uppercase tracking-wider text-gray-400">
        {label}
      </p>
      <p className="mt-0.5 text-lg font-semibold capitalize text-gray-900">
        {value}
      </p>
    </div>
  );
}

function Section({
  title,
  subtitle,
  children,
}: {
  title: string;
  subtitle: string;
  children: React.ReactNode;
}) {
  return (
    <div>
      <h2 className="text-lg font-semibold text-gray-900">{title}</h2>
      <p className="mt-0.5 mb-4 text-sm text-gray-500">{subtitle}</p>
      {children}
    </div>
  );
}
