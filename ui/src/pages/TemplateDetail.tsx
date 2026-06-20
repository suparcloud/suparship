import { Suspense, lazy, useEffect, useState } from "react";
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
import { parseYamlOverlay, stringifyOverlay } from "../lib/yamlDoc";
import type {
  TemplateDetail as TemplateDetailType,
  TemplateImage,
  TemplateOverride,
  TemplateSecretInput,
} from "../types";

// CodeMirror is heavy; only the override editor needs it.
const ValuesEditor = lazy(() => import("../components/ValuesEditor"));

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
// Scope values are "__all__", "env:<name>", or "cluster:<ref>".
const envScope = (name: string) => `env:${name}`;
const clusterScope = (ref: string) => `cluster:${ref}`;

// PlatformOverridesEditor lets a platform engineer (org_admin) layer org-specific
// Helm values on top of a template's own defaults — per env AND per cluster —
// WITHOUT forking the upstream template. Stored separately from the template so an
// external sync can't clobber it. The editor holds the override layer; a read-only
// pane shows the effective values (chart ⊕ template ⊕ override). Hidden for
// non-admins.
function PlatformOverridesEditor({ templateName }: { templateName: string }) {
  const { user } = useAuth();
  const isAdmin = user?.role === "org_admin";

  const [envs, setEnvs] = useState<string[]>([]);
  const [clusters, setClusters] = useState<Cluster[]>([]);
  const [scope, setScope] = useState<string>(ALL_ENVS);
  const [allText, setAllText] = useState("");
  const [envTexts, setEnvTexts] = useState<Record<string, string>>({});
  const [clusterTexts, setClusterTexts] = useState<Record<string, string>>({});
  const [yamlError, setYamlError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [preview, setPreview] = useState("");
  const [chartAvailable, setChartAvailable] = useState(true);
  const [configVars, setConfigVars] = useState<ConfigVariables | null>(null);

  // Seed from the saved override + load env/cluster lists (admins only).
  useEffect(() => {
    if (!isAdmin) return;
    let cancelled = false;
    fetchTemplateOverride(templateName)
      .then((ov) => {
        if (cancelled) return;
        setAllText(stringifyOverlay(ov.defaultValues));
        const envNext: Record<string, string> = {};
        for (const [env, vals] of Object.entries(ov.envValues ?? {})) {
          envNext[env] = stringifyOverlay(vals as Record<string, unknown>);
        }
        setEnvTexts(envNext);
        const clNext: Record<string, string> = {};
        for (const [c, vals] of Object.entries(ov.clusterValues ?? {})) {
          clNext[c] = stringifyOverlay(vals as Record<string, unknown>);
        }
        setClusterTexts(clNext);
      })
      .catch(() => {
        /* no override yet */
      });
    listOrgEnvironments()
      .then((res) =>
        setEnvs((res.environments ?? []).map((e) => e.name).filter((n) => n !== "preview")),
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

  // Build the full override from the live editor text across all scopes.
  function buildOverride(): { override: TemplateOverride; error: string | null } {
    const all = parseYamlOverlay(allText);
    if (all.error) return { override: {}, error: `All-envs: ${all.error}` };
    const collect = (texts: Record<string, string>, label: string) => {
      const out: Record<string, Record<string, unknown>> = {};
      for (const [key, text] of Object.entries(texts)) {
        const p = parseYamlOverlay(text);
        if (p.error) return { out, error: `${label} ${key}: ${p.error}` };
        if (p.value && Object.keys(p.value).length > 0) out[key] = p.value;
      }
      return { out, error: null as string | null };
    };
    const envs = collect(envTexts, "env");
    if (envs.error) return { override: {}, error: envs.error };
    const cls = collect(clusterTexts, "cluster");
    if (cls.error) return { override: {}, error: cls.error };
    return {
      override: {
        defaultValues: all.value ?? {},
        envValues: Object.keys(envs.out).length > 0 ? envs.out : undefined,
        clusterValues: Object.keys(cls.out).length > 0 ? cls.out : undefined,
      },
      error: null,
    };
  }

  // Which (env, cluster) to preview for the active scope.
  const previewEnv =
    scope.startsWith("env:") ? scope.slice(4) : scope === ALL_ENVS ? (envs[0] ?? "") : "";
  const previewCluster = scope.startsWith("cluster:") ? scope.slice(8) : "";

  // Debounced effective preview (chart ⊕ template ⊕ live override).
  useEffect(() => {
    if (!isAdmin || (!previewEnv && !previewCluster)) return;
    const { override, error } = buildOverride();
    if (error) return; // keep last good preview
    const handle = setTimeout(() => {
      previewTemplateEffectiveValues(templateName, previewEnv, previewCluster, override)
        .then((res) => {
          setPreview(stringifyOverlay(res.values));
          setChartAvailable(res.chartDefaultsAvailable);
        })
        .catch(() => {
          /* keep last good preview */
        });
    }, 400);
    return () => clearTimeout(handle);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isAdmin, templateName, previewEnv, previewCluster, allText, envTexts, clusterTexts]);

  if (!isAdmin) return null;

  const activeText =
    scope === ALL_ENVS
      ? allText
      : scope.startsWith("cluster:")
        ? (clusterTexts[scope.slice(8)] ?? "")
        : (envTexts[scope.slice(4)] ?? "");
  function setActiveText(text: string) {
    if (scope === ALL_ENVS) setAllText(text);
    else if (scope.startsWith("cluster:"))
      setClusterTexts((cur) => ({ ...cur, [scope.slice(8)]: text }));
    else setEnvTexts((cur) => ({ ...cur, [scope.slice(4)]: text }));
  }

  const clusterLabel = (c: Cluster) => c.displayName || c.name;
  const activeLabel =
    scope === ALL_ENVS
      ? "All-envs overrides"
      : scope.startsWith("cluster:")
        ? `cluster ${scope.slice(8)} overrides`
        : `${scope.slice(4)} overrides`;

  async function save() {
    const { override, error } = buildOverride();
    if (error) {
      setYamlError(error);
      toast.error(`Invalid YAML — ${error}`);
      return;
    }
    setSaving(true);
    try {
      await updateTemplateOverride(templateName, override);
      toast.success("Platform overrides saved — applies on next publish.");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to save overrides");
    } finally {
      setSaving(false);
    }
  }

  return (
    <div>
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-lg font-semibold text-gray-900">Platform overrides</h2>
          <p className="mt-0.5 mb-4 max-w-2xl text-sm text-gray-500">
            Org-specific Helm values layered on top of this template's defaults,
            per environment and per cluster — applied to every app using it. Sits
            below per-app overrides (developers can still override). Stored
            separately from the template, so an external sync won't clobber it.
            Simple per-cluster values can use{" "}
            <code className="font-mono">{"{platform.cluster}"}</code> or
            cluster-scoped <code className="font-mono">{"{vars.*}"}</code>; these
            blocks are for complex cluster-specific (e.g. cloud) annotations.
          </p>
        </div>
        <button
          onClick={save}
          disabled={saving || yamlError !== null}
          className="shrink-0 rounded-md bg-gray-900 px-3 py-1.5 text-sm font-medium text-white hover:bg-gray-700 disabled:opacity-50"
        >
          {saving ? "Saving…" : "Save overrides"}
        </button>
      </div>

      <div className="mb-3 flex items-center gap-2">
        <span className="text-xs font-medium uppercase tracking-wide text-gray-500">
          Scope
        </span>
        <select
          value={scope}
          onChange={(e) => {
            setScope(e.target.value);
            setYamlError(null);
          }}
          className="rounded-md border border-gray-300 px-2 py-1 text-xs"
        >
          <option value={ALL_ENVS}>All environments</option>
          {envs.length > 0 && (
            <optgroup label="Environments">
              {envs.map((env) => (
                <option key={env} value={envScope(env)}>
                  {env}
                </option>
              ))}
            </optgroup>
          )}
          {clusters.length > 0 && (
            <optgroup label="Clusters">
              {clusters.map((c) => (
                <option key={c.name} value={clusterScope(c.name)}>
                  {clusterLabel(c)}
                </option>
              ))}
            </optgroup>
          )}
        </select>
      </div>

      <Suspense
        fallback={
          <div className="rounded-lg border border-gray-200 p-4 text-xs text-gray-400">
            Loading editor…
          </div>
        }
      >
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          <ValuesEditor
            label={activeLabel}
            value={activeText}
            configVars={configVars}
            height="26rem"
            placeholder={"# e.g.\nresources:\n  requests:\n    cpu: 500m"}
            onChange={setActiveText}
            onValidChange={(_, err) => setYamlError(err)}
          />
          <div>
            <ValuesEditor
              label={
                previewCluster
                  ? `Effective — cluster ${previewCluster}`
                  : previewEnv
                    ? `Effective — ${previewEnv}`
                    : "Effective"
              }
              value={preview}
              height="26rem"
              readOnly
            />
            {!chartAvailable && (
              <p className="mt-1 text-xs text-gray-400">
                Chart defaults aren't readable for this template; preview shows
                template + overrides only.
              </p>
            )}
            <p className="mt-1 text-xs text-gray-400">
              Preview omits per-app overrides and{" "}
              <code className="font-mono">{"{…}"}</code> token resolution — applied
              at deploy.
            </p>
          </div>
        </div>
      </Suspense>
      {yamlError && (
        <p className="mt-2 text-xs text-red-600">Invalid YAML: {yamlError}</p>
      )}
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

  function reset() {
    setTitle(template.title);
    setCategory(template.category);
    setDescription(template.description ?? "");
    setPassthrough(template.injectCanonicalValues === false);
    setEditing(false);
  }

  async function save() {
    setSaving(true);
    try {
      const updated = await updateTemplateMetadata(template.name, {
        title,
        category,
        description,
        // The passthrough toggle only applies to in-place edits; the override
        // path is display-metadata only and rejects injectCanonicalValues.
        ...(inPlace ? { injectCanonicalValues: !passthrough } : {}),
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
