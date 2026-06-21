import { type FormEvent, Suspense, lazy, useEffect, useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";

import { ApiError } from "../lib/api";
import { createApp } from "../lib/apps";
import { setAppStack } from "../lib/stacks";
import { listConfigVariables } from "../lib/configVars";
import type { ConfigVariables } from "../lib/configVars";
import { listOrgEnvironments } from "../lib/settings";
import type { OrgEnvironment } from "../lib/settings";
import {
  fetchTemplate,
  fetchTemplateEffectiveValues,
  fetchTemplates,
} from "../lib/templates";
import { mergeOverlay, stringifyOverlay } from "../lib/yamlDoc";
import type {
  TemplateSummary,
  TemplateDetail,
  TemplateSecretInput,
  SecretRefInput,
  EffectiveValuesResponse,
} from "../types";

// CodeMirror is heavy; only the create/detail flows need it.
const ValuesEditor = lazy(() => import("../components/ValuesEditor"));

type Step = "template" | "configure";

// ---------------------------------------------------------------------------
// Main page component
// ---------------------------------------------------------------------------

export function NewService() {
  const { project } = useParams<{ project: string }>();
  const navigate = useNavigate();
  // When launched from a stack ("/apps/new?stack=voiceai"), the new app joins
  // that stack and its name defaults to the "{stack}-" prefix.
  const [searchParams] = useSearchParams();
  const stack = searchParams.get("stack") ?? undefined;

  const [step, setStep] = useState<Step>("template");
  const [templates, setTemplates] = useState<TemplateSummary[]>([]);
  const [selectedTemplate, setSelectedTemplate] =
    useState<TemplateDetail | null>(null);
  const [loadingTemplates, setLoadingTemplates] = useState(true);
  const [loadingDetail, setLoadingDetail] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    async function load() {
      try {
        const data = await fetchTemplates();
        if (!cancelled) setTemplates(data.templates);
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : "Failed to load templates");
      } finally {
        if (!cancelled) setLoadingTemplates(false);
      }
    }
    load();
    return () => { cancelled = true; };
  }, []);

  async function handleSelectTemplate(name: string) {
    setLoadingDetail(true);
    setError(null);
    try {
      const detail = await fetchTemplate(name);
      setSelectedTemplate(detail);
      setStep("configure");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load template");
    } finally {
      setLoadingDetail(false);
    }
  }

  if (!project) {
    return (
      <div className="rounded-lg border border-red-200 bg-red-50 p-4">
        <p className="text-sm text-red-700">Project name is required.</p>
      </div>
    );
  }

  return (
    <div className="mx-auto max-w-5xl space-y-6">
      {/* Breadcrumb */}
      <Link
        to={`/projects/${project}`}
        className="inline-flex items-center gap-1 text-sm text-gray-500 transition-colors hover:text-gray-700"
      >
        &larr; Back to {project}
      </Link>

      {/* Header */}
      <div>
        <h1 className="text-2xl font-semibold text-gray-900">
          Create a new app
        </h1>
        <p className="mt-1 text-sm text-gray-500">
          Choose a template and configure your app for{" "}
          <span className="font-medium text-gray-700">{project}</span>.
        </p>
      </div>

      {/* Steps indicator */}
      <StepIndicator current={step} />

      {/* Error */}
      {error && (
        <div className="rounded-lg border border-red-200 bg-red-50 px-4 py-3">
          <p className="text-sm text-red-700">{error}</p>
        </div>
      )}

      {/* Step content */}
      {step === "template" && (
        <TemplateStep
          templates={templates}
          loading={loadingTemplates || loadingDetail}
          onSelect={handleSelectTemplate}
        />
      )}

      {step === "configure" && selectedTemplate && (
        <ConfigureStep
          project={project}
          template={selectedTemplate}
          stack={stack}
          onBack={() => setStep("template")}
          navigate={navigate}
        />
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Step indicator
// ---------------------------------------------------------------------------

function StepIndicator({ current }: { current: Step }) {
  const steps = [
    { key: "template", label: "Choose template" },
    { key: "configure", label: "Configure app" },
  ] as const;

  return (
    <div className="flex items-center gap-3">
      {steps.map((s, i) => {
        const isActive = s.key === current;
        const isPast =
          current === "configure" && s.key === "template";
        return (
          <div key={s.key} className="flex items-center gap-3">
            {i > 0 && (
              <div
                className={`h-px w-8 ${isPast ? "bg-gray-900" : "bg-gray-200"}`}
              />
            )}
            <div className="flex items-center gap-2">
              <div
                className={`flex h-6 w-6 items-center justify-center rounded-full text-xs font-semibold ${
                  isActive
                    ? "bg-gray-900 text-white"
                    : isPast
                      ? "bg-gray-900 text-white"
                      : "bg-gray-100 text-gray-400"
                }`}
              >
                {isPast ? (
                  <svg className="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={3}>
                    <path strokeLinecap="round" strokeLinejoin="round" d="m4.5 12.75 6 6 9-13.5" />
                  </svg>
                ) : (
                  i + 1
                )}
              </div>
              <span
                className={`text-sm font-medium ${
                  isActive || isPast ? "text-gray-900" : "text-gray-400"
                }`}
              >
                {s.label}
              </span>
            </div>
          </div>
        );
      })}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Step 1: Template selection
// ---------------------------------------------------------------------------

const categoryStyle: Record<string, string> = {
  web: "bg-blue-50 text-blue-600",
  worker: "bg-purple-50 text-purple-600",
  api: "bg-emerald-50 text-emerald-600",
  data: "bg-amber-50 text-amber-600",
};

function TemplateStep({
  templates,
  loading,
  onSelect,
}: {
  templates: TemplateSummary[];
  loading: boolean;
  onSelect: (name: string) => void;
}) {
  if (loading) {
    return (
      <div className="grid gap-3 sm:grid-cols-2">
        {[1, 2, 3, 4].map((n) => (
          <div key={n} className="h-28 animate-pulse rounded-xl bg-gray-50" />
        ))}
      </div>
    );
  }

  if (templates.length === 0) {
    return (
      <div className="rounded-xl border border-dashed border-gray-300 bg-white px-6 py-12 text-center">
        <p className="text-sm text-gray-500">
          No templates available. Start the server with{" "}
          <code className="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono">
            --templates-dir
          </code>{" "}
          to load templates.
        </p>
      </div>
    );
  }

  return (
    <div className="grid gap-3 sm:grid-cols-2">
      {templates.map((t) => {
        const catCls = categoryStyle[t.category] ?? "bg-gray-50 text-gray-500";
        return (
          <button
            key={t.name}
            type="button"
            onClick={() => onSelect(t.name)}
            className="group rounded-xl border border-gray-200 bg-white p-4 text-left transition-all hover:border-gray-300 hover:shadow-md"
          >
            <div className="flex items-center justify-between">
              <span
                className={`inline-block rounded-full px-2.5 py-0.5 text-xs font-medium capitalize ${catCls}`}
              >
                {t.category}
              </span>
              <span className="text-xs text-gray-400">v{t.version}</span>
            </div>
            <h3 className="mt-2 text-sm font-semibold text-gray-900 group-hover:text-gray-700">
              {t.title}
            </h3>
            {t.description && (
              <p className="mt-1 text-xs leading-relaxed text-gray-500 line-clamp-2">
                {t.description}
              </p>
            )}
          </button>
        );
      })}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Step 2: Configure app
// ---------------------------------------------------------------------------

function ConfigureStep({
  project,
  template,
  stack,
  onBack,
  navigate,
}: {
  project: string;
  template: TemplateDetail;
  stack?: string;
  onBack: () => void;
  navigate: ReturnType<typeof useNavigate>;
}) {
  // In a stack, default to the "{stack}-" prefix so member app names stay
  // distinct across stacks (app names are project-unique, not stack-scoped).
  const [appName, setAppName] = useState(stack ? `${stack}-` : "");
  const [secretRefs, setSecretRefs] = useState<Record<string, string>>(() =>
    buildDefaultSecretRefs(template),
  );
  const [namespaceScope, setNamespaceScope] = useState<"app" | "project">("app");
  const [namespacePattern, setNamespacePattern] = useState("");
  const [cdManaged, setCdManaged] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [nameError, setNameError] = useState<string | null>(null);
  const [configVars, setConfigVars] = useState<ConfigVariables | null>(null);

  // The values editor holds ONLY the developer's app-level override layer
  // (rawValues). Empty = inherit chart + platform defaults entirely.
  const [overlayText, setOverlayText] = useState("");
  const [overlay, setOverlay] = useState<Record<string, unknown>>({});
  const [overlayError, setOverlayError] = useState<string | null>(null);
  // Read-only effective base (chart ⊕ platform defaults) for the preview pane.
  const [base, setBase] = useState<EffectiveValuesResponse | null>(null);
  // Org environments — drives the read-only "Deployment targets" panel.
  const [orgEnvs, setOrgEnvs] = useState<OrgEnvironment[]>([]);

  useEffect(() => {
    listConfigVariables(project)
      .then(setConfigVars)
      .catch(() => setConfigVars({ platform: [], vars: [] }));
    listOrgEnvironments()
      .then((res) =>
        setOrgEnvs(
          (res.environments ?? []).filter((e) => e.name !== "preview"),
        ),
      )
      .catch(() => setOrgEnvs([]));
  }, [project]);

  useEffect(() => {
    let cancelled = false;
    fetchTemplateEffectiveValues(template.name)
      .then((res) => {
        if (!cancelled) setBase(res);
      })
      .catch(() => {
        if (!cancelled) setBase(null);
      });
    return () => {
      cancelled = true;
    };
  }, [template.name]);

  function updateSecretRef(name: string, ref: string) {
    setSecretRefs((prev) => ({ ...prev, [name]: ref }));
  }

  // Effective preview = base (chart ⊕ platform defaults) ⊕ live overlay.
  const effectivePreview = stringifyOverlay(
    mergeOverlay(base?.values ?? {}, overlay),
  );

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();

    if (!appName.trim()) {
      setNameError("App name is required.");
      return;
    }
    if (!/^[a-z][a-z0-9-]{0,61}[a-z0-9]$/.test(appName)) {
      setNameError(
        "Must be lowercase letters, numbers, and hyphens (2-63 chars).",
      );
      return;
    }
    if (overlayError) {
      setError("Fix the values YAML before creating the app.");
      return;
    }

    setSubmitting(true);
    setError(null);

    const secretRefList: SecretRefInput[] = [];
    for (const si of template.secretInputs) {
      const ref = secretRefs[si.name]?.trim();
      if (ref) secretRefList.push({ name: si.name, secretRef: ref });
    }

    try {
      await createApp(project, {
        name: appName,
        template: template.name,
        values: {},
        secretRefs: secretRefList,
        namespaceScope: namespaceScope !== "app" ? namespaceScope : undefined,
        namespacePattern: namespacePattern.trim() || undefined,
        rawValues: Object.keys(overlay).length > 0 ? overlay : undefined,
        cd: cdManaged ? { managed: true } : undefined,
      });
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Could not reach the server.");
      setSubmitting(false);
      return;
    }

    // App created. If launched from a stack, attach it (best-effort so a
    // membership hiccup doesn't strand the user on a form for an app that now
    // exists), then return to the stack so the new member is visible.
    if (stack) {
      try {
        await setAppStack(project, appName, stack);
      } catch {
        // Leave unattached; the user can add it from the stack page.
      }
    }
    navigate(
      stack
        ? `/projects/${project}/stacks/${encodeURIComponent(stack)}`
        : `/projects/${project}/apps/${appName}`,
    );
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-8">
      {/* Template badge */}
      <div className="flex items-center gap-3 rounded-lg border border-gray-200 bg-gray-50 px-4 py-3">
        <div className="flex-1">
          <p className="text-xs font-medium uppercase tracking-wider text-gray-400">
            Template
          </p>
          <p className="text-sm font-semibold text-gray-900">
            {template.title}{" "}
            <span className="font-normal text-gray-400">
              v{template.version}
            </span>
          </p>
        </div>
        <button
          type="button"
          onClick={onBack}
          className="rounded-md px-3 py-1.5 text-xs font-medium text-gray-500 transition-colors hover:bg-gray-200 hover:text-gray-700"
        >
          Change
        </button>
      </div>

      {/* Deployment targets (read-only) */}
      <DeploymentTargets envs={orgEnvs} />

      {/* App name */}
      <div>
        <label
          htmlFor="app-name"
          className="block text-sm font-medium text-gray-700"
        >
          App name
        </label>
        <p className="mt-0.5 text-xs text-gray-400">
          A unique name for this app within the project. Used in Kubernetes resource names.
          {stack && (
            <>
              {" "}
              Joining the{" "}
              <span className="font-medium text-gray-600">{stack}</span> stack — keep
              the{" "}
              <code className="font-mono">{stack}-</code> prefix so member names stay
              distinct across stacks.
            </>
          )}
        </p>
        <input
          id="app-name"
          type="text"
          placeholder="e.g. api, web, worker"
          value={appName}
          onChange={(e) => {
            setAppName(e.target.value);
            setNameError(null);
          }}
          className={`mt-2 block w-full max-w-2xl rounded-md border px-3 py-2 text-sm shadow-sm transition-colors focus:outline-none focus:ring-1 ${
            nameError
              ? "border-red-300 focus:border-red-500 focus:ring-red-500"
              : "border-gray-300 focus:border-gray-900 focus:ring-gray-900"
          }`}
        />
        {nameError && <p className="mt-1 text-xs text-red-600">{nameError}</p>}
      </div>

      {/* Namespace settings */}
      <FormSection title="Namespace">
        <p className="mb-3 text-xs text-gray-400">
          Control where this app's workloads are deployed. Leave blank to inherit
          org/project defaults.
        </p>
        <div className="space-y-3">
          {/* Scope toggle */}
          <div className="flex gap-3">
            {(["app", "project"] as const).map((scope) => (
              <label
                key={scope}
                className={`flex flex-1 cursor-pointer items-center gap-2 rounded-lg border p-3 text-sm transition-colors ${
                  namespaceScope === scope
                    ? "border-indigo-200 bg-indigo-50"
                    : "border-gray-200 hover:bg-gray-50"
                }`}
              >
                <input
                  type="radio"
                  name="ns-scope"
                  value={scope}
                  checked={namespaceScope === scope}
                  onChange={() => {
                    setNamespaceScope(scope);
                    setNamespacePattern("");
                  }}
                  className="mt-0.5"
                />
                <div>
                  <span className="font-medium text-gray-900">
                    {scope === "app" ? "Dedicated namespace" : "Project namespace"}
                  </span>
                  <p className="text-xs text-gray-400">
                    {scope === "app"
                      ? "Each app+env gets its own isolated namespace (default)"
                      : "Share the project's namespace across apps"}
                  </p>
                </div>
              </label>
            ))}
          </div>

          {/* Pattern override — only when scope=app */}
          {namespaceScope === "app" && (
            <div>
              <label className="mb-1 block text-xs font-medium text-gray-700">
                Namespace pattern{" "}
                <span className="font-normal text-gray-400">
                  (optional — overrides org/project default)
                </span>
              </label>
              <input
                type="text"
                className="w-full max-w-2xl rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
                placeholder="e.g. {project}-{app}-{env}  or  {project}-{app}"
                value={namespacePattern}
                onChange={(e) => setNamespacePattern(e.target.value)}
              />
              <p className="mt-1 text-xs text-gray-400">
                Tokens: <code className="font-mono">{"{org}"}</code>,{" "}
                <code className="font-mono">{"{project}"}</code>,{" "}
                <code className="font-mono">{"{app}"}</code>,{" "}
                <code className="font-mono">{"{env}"}</code>
              </p>
              {namespacePattern.trim() && (
                <p className="mt-1 text-xs text-gray-400">
                  Preview:{" "}
                  <code className="font-mono text-gray-700">
                    {namespacePattern
                      .replace("{org}", "myorg")
                      .replace("{project}", project)
                      .replace("{app}", appName || "myapp")
                      .replace("{env}", "staging")}
                  </code>
                </p>
              )}
            </div>
          )}
        </div>
      </FormSection>

      {/* Continuous delivery */}
      <FormSection title="Continuous delivery">
        <label className="flex items-center gap-2 text-sm font-medium text-gray-700">
          <input
            type="checkbox"
            checked={cdManaged}
            onChange={(e) => setCdManaged(e.target.checked)}
            className="h-4 w-4 rounded border-gray-300"
          />
          Image tag managed by Kargo
        </label>
        <p className="mt-1 text-xs text-gray-400">
          When enabled, Kargo owns the image tag: it commits the
          discovered/promoted tag and re-publishing preserves it instead of
          resetting to your overrides. The tag you set in values acts only as the
          initial seed. Which images Kargo watches (and the values keys it writes)
          comes from the template's image mapping.
        </p>
      </FormSection>

      {/* Secret inputs */}
      {template.secretInputs.length > 0 && (
        <FormSection title="Secrets">
          <p className="mb-4 text-xs text-gray-400">
            Reference existing Kubernetes Secrets. Values are never stored in Git.
          </p>
          <div className="space-y-5">
            {template.secretInputs.map((si) => (
              <SecretField
                key={si.name}
                input={si}
                value={secretRefs[si.name] ?? ""}
                onChange={(val) => updateSecretRef(si.name, val)}
              />
            ))}
          </div>
        </FormSection>
      )}

      {/* Values — the developer edits ONLY their override layer; the right pane
          shows the effective document (chart + platform defaults ⊕ overrides). */}
      <FormSection title="Values">
        <p className="mb-3 text-xs text-gray-400">
          Edit only what you want to override — leave empty to inherit the chart
          and platform defaults entirely. Deep-merged on top of those at deploy.
          Reference platform metadata / env vars with{" "}
          <code className="font-mono">{"{platform.*}"}</code> /{" "}
          <code className="font-mono">{"{vars.*}"}</code> tokens. No secrets.
        </p>
        <Suspense
          fallback={
            <div className="rounded-lg border border-gray-200 p-4 text-xs text-gray-400">
              Loading editor…
            </div>
          }
        >
          <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
            <ValuesEditor
              label="Your overrides"
              value={overlayText}
              configVars={configVars}
              height="26rem"
              placeholder={
                "# e.g.\nresources:\n  requests:\n    cpu: 200m\nenv:\n  LOG_LEVEL: debug"
              }
              onChange={setOverlayText}
              onValidChange={(parsed, err) => {
                setOverlayError(err);
                if (parsed) setOverlay(parsed);
              }}
            />
            <div>
              <ValuesEditor
                label={
                  base?.chartDefaultsAvailable
                    ? "Effective (chart + platform ⊕ overrides)"
                    : "Effective (platform ⊕ overrides)"
                }
                value={effectivePreview}
                height="26rem"
                readOnly
              />
              {base && !base.chartDefaultsAvailable && (
                <p className="mt-1 text-xs text-gray-400">
                  Chart defaults aren't readable for this template; the preview
                  shows platform defaults + your overrides only.
                </p>
              )}
              <p className="mt-1 text-xs text-gray-400">
                Preview omits per-env values and{" "}
                <code className="font-mono">{"{…}"}</code> token resolution —
                applied at deploy.
              </p>
            </div>
          </div>
        </Suspense>
      </FormSection>

      {/* Error */}
      {error && (
        <div className="rounded-lg border border-red-200 bg-red-50 px-4 py-3">
          <p className="text-sm text-red-700">{error}</p>
        </div>
      )}

      {/* Actions */}
      <div className="flex items-center justify-between border-t border-gray-200 pt-6">
        <button
          type="button"
          onClick={onBack}
          className="rounded-md px-4 py-2 text-sm font-medium text-gray-600 transition-colors hover:bg-gray-100 hover:text-gray-900"
        >
          Back
        </button>
        <button
          type="submit"
          disabled={submitting || overlayError !== null}
          className="rounded-md bg-gray-900 px-6 py-2 text-sm font-medium text-white transition-colors hover:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-gray-900 focus:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {submitting ? "Creating…" : "Create app"}
        </button>
      </div>
    </form>
  );
}

// ---------------------------------------------------------------------------
// Deployment targets (read-only)
// ---------------------------------------------------------------------------

// effectiveClusters resolves which cluster(s) an environment deploys to: every
// clusterRef in "all" (fan-out) mode, else the active one (falling back to the
// first). Mirrors the server's EffectiveClusterRef resolution.
function effectiveClusters(env: OrgEnvironment): string[] {
  if ((env.deployMode ?? "active") === "all") {
    return env.clusterRefs ?? [];
  }
  const active = env.activeClusterRef || env.clusterRefs?.[0] || "";
  return active ? [active] : [];
}

// DeploymentTargets shows, read-only, where the app will land: every org
// environment (the app is created across all of them), which env gets the first
// deploy (lowest order), and the cluster(s) each env maps to. The env→cluster
// binding is owned by platform engineers in org settings, not chosen per app.
function DeploymentTargets({ envs }: { envs: OrgEnvironment[] }) {
  if (envs.length === 0) return null;
  const sorted = [...envs].sort((a, b) => a.order - b.order);

  return (
    <div className="rounded-lg border border-gray-200 bg-white">
      <div className="border-b border-gray-100 px-4 py-2.5">
        <h2 className="text-xs font-medium uppercase tracking-wider text-gray-400">
          Deployment targets
        </h2>
      </div>
      <ul className="divide-y divide-gray-50">
        {sorted.map((env, i) => {
          const clusters = effectiveClusters(env);
          return (
            <li
              key={env.name}
              className="flex items-center justify-between gap-3 px-4 py-2.5"
            >
              <div className="flex items-center gap-2">
                <span className="text-sm font-medium text-gray-800">
                  {env.displayName || env.name}
                </span>
                <span
                  className={`rounded px-1.5 py-0.5 text-[10px] font-medium ${
                    i === 0
                      ? "bg-indigo-50 text-indigo-600"
                      : "bg-gray-100 text-gray-500"
                  }`}
                >
                  {i === 0 ? "first deploy" : "via promotion"}
                </span>
                {(env.deployMode ?? "active") === "all" && (
                  <span className="rounded bg-amber-50 px-1.5 py-0.5 text-[10px] font-medium text-amber-700">
                    fan-out
                  </span>
                )}
              </div>
              <span className="font-mono text-xs text-gray-500">
                {clusters.length > 0 ? (
                  clusters.join(", ")
                ) : (
                  <span className="text-amber-600">no cluster bound</span>
                )}
              </span>
            </li>
          );
        })}
      </ul>
      <p className="border-t border-gray-100 px-4 py-2 text-xs text-gray-400">
        The app is created in every environment; the lowest one deploys first and
        you promote to advance it. Clusters are bound per environment in org
        settings — not chosen per app.
      </p>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Secret ref field
// ---------------------------------------------------------------------------

function SecretField({
  input,
  value,
  error,
  onChange,
}: {
  input: TemplateSecretInput;
  value: string;
  error?: string;
  onChange: (val: string) => void;
}) {
  const id = `secret-${input.name}`;
  const borderCls = error
    ? "border-red-300 focus:border-red-500 focus:ring-red-500"
    : "border-gray-300 focus:border-gray-900 focus:ring-gray-900";

  return (
    <div>
      <div className="flex items-center gap-2">
        <svg className="h-4 w-4 text-amber-500" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
          <path strokeLinecap="round" strokeLinejoin="round" d="M16.5 10.5V6.75a4.5 4.5 0 1 0-9 0v3.75m-.75 11.25h10.5a2.25 2.25 0 0 0 2.25-2.25v-6.75a2.25 2.25 0 0 0-2.25-2.25H6.75a2.25 2.25 0 0 0-2.25 2.25v6.75a2.25 2.25 0 0 0 2.25 2.25Z" />
        </svg>
        <label htmlFor={id} className="block text-sm font-medium text-gray-700">
          {input.title}
        </label>
      </div>
      {input.description && (
        <p className="mt-0.5 text-xs text-gray-400">{input.description}</p>
      )}
      <input
        id={id}
        type="text"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={input.secretRef || "secret-name.key"}
        className={`mt-1.5 block w-full max-w-2xl rounded-md border px-3 py-2 font-mono text-sm shadow-sm transition-colors focus:outline-none focus:ring-1 ${borderCls}`}
      />
      <p className="mt-1 text-xs text-gray-400">
        Format: <code className="rounded bg-gray-100 px-1 py-0.5">secret-name.key</code>
      </p>
      {error && <p className="mt-1 text-xs text-red-600">{error}</p>}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Form section wrapper
// ---------------------------------------------------------------------------

function FormSection({
  title,
  children,
}: {
  title: string;
  children: React.ReactNode;
}) {
  return (
    <div>
      <h2 className="text-lg font-semibold text-gray-900">{title}</h2>
      <div className="mt-4">{children}</div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function buildDefaultSecretRefs(tmpl: TemplateDetail): Record<string, string> {
  const refs: Record<string, string> = {};
  for (const si of tmpl.secretInputs) {
    refs[si.name] = si.secretRef;
  }
  return refs;
}

