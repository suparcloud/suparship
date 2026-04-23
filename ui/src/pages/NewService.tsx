import { type FormEvent, useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";

import { ApiError } from "../lib/api";
import { createApp } from "../lib/apps";
import { fetchTemplate, fetchTemplates } from "../lib/templates";
import type {
  TemplateSummary,
  TemplateDetail,
  TemplateInput,
  TemplateSecretInput,
  SecretRefInput,
} from "../types";

type Step = "template" | "configure";

// ---------------------------------------------------------------------------
// Main page component
// ---------------------------------------------------------------------------

export function NewService() {
  const { project } = useParams<{ project: string }>();
  const navigate = useNavigate();

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
    <div className="mx-auto max-w-3xl space-y-6">
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
  onBack,
  navigate,
}: {
  project: string;
  template: TemplateDetail;
  onBack: () => void;
  navigate: ReturnType<typeof useNavigate>;
}) {
  const [appName, setAppName] = useState("");
  const [values, setValues] = useState<Record<string, unknown>>(() =>
    buildDefaults(template),
  );
  const [secretRefs, setSecretRefs] = useState<Record<string, string>>(() =>
    buildDefaultSecretRefs(template),
  );
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [namespaceScope, setNamespaceScope] = useState<"app" | "project">("app");
  const [namespacePattern, setNamespacePattern] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [selectedPreset, setSelectedPreset] = useState<string | null>(null);

  function applyPreset(presetName: string) {
    const preset = template.presets.find((p) => p.name === presetName);
    if (!preset) return;
    setSelectedPreset(presetName);
    setValues((prev) => ({ ...prev, ...preset.values }));
  }

  function updateValue(name: string, val: unknown) {
    setValues((prev) => ({ ...prev, [name]: val }));
    setFieldErrors((prev) => {
      const next = { ...prev };
      delete next[name];
      return next;
    });
    setSelectedPreset(null);
  }

  function updateSecretRef(name: string, ref: string) {
    setSecretRefs((prev) => ({ ...prev, [name]: ref }));
    setFieldErrors((prev) => {
      const next = { ...prev };
      delete next[name];
      return next;
    });
  }

  function validate(): boolean {
    const errors: Record<string, string> = {};

    if (!appName.trim()) {
      errors._name = "App name is required.";
    } else if (!/^[a-z][a-z0-9-]{0,61}[a-z0-9]$/.test(appName)) {
      errors._name =
        "Must be lowercase letters, numbers, and hyphens (2-63 chars).";
    }

    for (const inp of template.inputs) {
      const val = values[inp.name];
      const err = validateField(inp, val);
      if (err) errors[inp.name] = err;
    }

    setFieldErrors(errors);
    return Object.keys(errors).length === 0;
  }

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    if (!validate()) return;

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
        values,
        secretRefs: secretRefList,
        namespaceScope: namespaceScope !== "app" ? namespaceScope : undefined,
        namespacePattern: namespacePattern.trim() || undefined,
      });
      navigate(`/projects/${project}/apps/${appName}`);
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.message);
      } else {
        setError("Could not reach the server.");
      }
    } finally {
      setSubmitting(false);
    }
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

      {/* Component info note */}
      <div className="rounded-lg border border-blue-100 bg-blue-50 px-4 py-3">
        <p className="text-xs text-blue-700">
          Most apps start with one runtime component. Some templates include
          multiple components such as <span className="font-medium">web</span>{" "}
          and <span className="font-medium">worker</span>. Components are
          configured automatically from the template — no manual setup needed.
        </p>
      </div>

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
        </p>
        <input
          id="app-name"
          type="text"
          placeholder="e.g. api, web, worker"
          value={appName}
          onChange={(e) => {
            setAppName(e.target.value);
            setFieldErrors((prev) => {
              const next = { ...prev };
              delete next._name;
              return next;
            });
          }}
          className={`mt-2 block w-full rounded-md border px-3 py-2 text-sm shadow-sm transition-colors focus:outline-none focus:ring-1 ${
            fieldErrors._name
              ? "border-red-300 focus:border-red-500 focus:ring-red-500"
              : "border-gray-300 focus:border-gray-900 focus:ring-gray-900"
          }`}
        />
        {fieldErrors._name && (
          <p className="mt-1 text-xs text-red-600">{fieldErrors._name}</p>
        )}
      </div>

      {/* Presets */}
      {template.presets.length > 0 && (
        <div>
          <p className="text-sm font-medium text-gray-700">
            Start from a preset
          </p>
          <p className="mt-0.5 text-xs text-gray-400">
            Presets fill in recommended defaults. You can customize them below.
          </p>
          <div className="mt-3 flex flex-wrap gap-2">
            {template.presets.map((p) => (
              <button
                key={p.name}
                type="button"
                onClick={() => applyPreset(p.name)}
                className={`rounded-lg border px-4 py-2 text-sm font-medium transition-all ${
                  selectedPreset === p.name
                    ? "border-gray-900 bg-gray-900 text-white shadow-sm"
                    : "border-gray-200 bg-white text-gray-700 hover:border-gray-300 hover:shadow-sm"
                }`}
              >
                {p.title}
                {p.description && (
                  <span className="ml-1.5 font-normal text-gray-400">
                    — {p.description.slice(0, 50)}
                    {p.description.length > 50 ? "…" : ""}
                  </span>
                )}
              </button>
            ))}
          </div>
        </div>
      )}

      {/* Inputs */}
      {template.inputs.length > 0 && (
        <FormSection title="Configuration">
          <div className="space-y-5">
            {template.inputs.map((inp) => (
              <DynamicField
                key={inp.name}
                input={inp}
                value={values[inp.name]}
                error={fieldErrors[inp.name]}
                onChange={(val) => updateValue(inp.name, val)}
              />
            ))}
          </div>
        </FormSection>
      )}

      {/* Advanced inputs */}
      {template.advancedInputs.length > 0 && (
        <div>
          <button
            type="button"
            onClick={() => setShowAdvanced(!showAdvanced)}
            className="flex items-center gap-2 text-sm font-medium text-gray-500 transition-colors hover:text-gray-700"
          >
            <svg
              className={`h-4 w-4 transition-transform ${showAdvanced ? "rotate-90" : ""}`}
              fill="none"
              viewBox="0 0 24 24"
              stroke="currentColor"
              strokeWidth={2}
            >
              <path strokeLinecap="round" strokeLinejoin="round" d="m8.25 4.5 7.5 7.5-7.5 7.5" />
            </svg>
            Advanced settings ({template.advancedInputs.length})
          </button>
          {showAdvanced && (
            <div className="mt-4 space-y-5 rounded-lg border border-gray-100 bg-gray-50/50 p-5">
              {template.advancedInputs.map((inp) => (
                <DynamicField
                  key={inp.name}
                  input={inp}
                  value={values[inp.name]}
                  error={fieldErrors[inp.name]}
                  onChange={(val) => updateValue(inp.name, val)}
                />
              ))}
            </div>
          )}
        </div>
      )}

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
                className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
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
                error={fieldErrors[si.name]}
                onChange={(val) => updateSecretRef(si.name, val)}
              />
            ))}
          </div>
        </FormSection>
      )}

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
          disabled={submitting}
          className="rounded-md bg-gray-900 px-6 py-2 text-sm font-medium text-white transition-colors hover:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-gray-900 focus:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {submitting ? "Creating…" : "Create app"}
        </button>
      </div>
    </form>
  );
}

// ---------------------------------------------------------------------------
// Dynamic form field
// ---------------------------------------------------------------------------

function DynamicField({
  input,
  value,
  error,
  onChange,
}: {
  input: TemplateInput;
  value: unknown;
  error?: string;
  onChange: (val: unknown) => void;
}) {
  const id = `input-${input.name}`;
  const borderCls = error
    ? "border-red-300 focus:border-red-500 focus:ring-red-500"
    : "border-gray-300 focus:border-gray-900 focus:ring-gray-900";
  const baseCls = `mt-1.5 block w-full rounded-md border px-3 py-2 text-sm shadow-sm transition-colors focus:outline-none focus:ring-1 ${borderCls}`;

  return (
    <div>
      <div className="flex items-center gap-2">
        <label htmlFor={id} className="block text-sm font-medium text-gray-700">
          {input.title}
        </label>
        {input.required && (
          <span className="text-[10px] font-semibold uppercase tracking-wide text-red-500">
            Required
          </span>
        )}
      </div>
      {input.description && (
        <p className="mt-0.5 text-xs text-gray-400">{input.description}</p>
      )}

      {input.type === "boolean" ? (
        <div className="mt-2 flex items-center gap-3">
          <button
            type="button"
            role="switch"
            aria-checked={value === true}
            onClick={() => onChange(!(value === true))}
            className={`relative inline-flex h-6 w-11 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors ${
              value === true ? "bg-gray-900" : "bg-gray-200"
            }`}
          >
            <span
              className={`pointer-events-none inline-block h-5 w-5 rounded-full bg-white shadow-sm ring-0 transition-transform ${
                value === true ? "translate-x-5" : "translate-x-0"
              }`}
            />
          </button>
          <span className="text-sm text-gray-600">
            {value === true ? "Enabled" : "Disabled"}
          </span>
        </div>
      ) : input.type === "enum" ? (
        <select
          id={id}
          value={String(value ?? "")}
          onChange={(e) => onChange(e.target.value)}
          className={baseCls}
        >
          <option value="" disabled>
            Select an option…
          </option>
          {input.options.map((opt) => (
            <option key={opt} value={opt}>
              {opt}
            </option>
          ))}
        </select>
      ) : input.type === "number" ? (
        <input
          id={id}
          type="number"
          value={value !== undefined && value !== null ? String(value) : ""}
          min={input.min}
          max={input.max}
          onChange={(e) => {
            const n = e.target.valueAsNumber;
            onChange(Number.isNaN(n) ? undefined : n);
          }}
          placeholder={
            input.default !== undefined ? String(input.default) : undefined
          }
          className={baseCls}
        />
      ) : (
        <input
          id={id}
          type="text"
          value={String(value ?? "")}
          onChange={(e) => onChange(e.target.value)}
          placeholder={
            input.default !== undefined ? String(input.default) : undefined
          }
          className={baseCls}
        />
      )}

      {error && <p className="mt-1 text-xs text-red-600">{error}</p>}
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
        className={`mt-1.5 block w-full rounded-md border px-3 py-2 font-mono text-sm shadow-sm transition-colors focus:outline-none focus:ring-1 ${borderCls}`}
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

function buildDefaults(tmpl: TemplateDetail): Record<string, unknown> {
  const vals: Record<string, unknown> = {};
  for (const inp of [...tmpl.inputs, ...tmpl.advancedInputs]) {
    if (inp.default !== undefined && inp.default !== null) {
      vals[inp.name] = inp.default;
    }
  }
  return vals;
}

function buildDefaultSecretRefs(tmpl: TemplateDetail): Record<string, string> {
  const refs: Record<string, string> = {};
  for (const si of tmpl.secretInputs) {
    refs[si.name] = si.secretRef;
  }
  return refs;
}

function validateField(inp: TemplateInput, val: unknown): string | undefined {
  if (inp.required && (val === undefined || val === null || val === "")) {
    return `${inp.title} is required.`;
  }

  if (val === undefined || val === null || val === "") return undefined;

  switch (inp.type) {
    case "number": {
      const n = typeof val === "number" ? val : Number(val);
      if (Number.isNaN(n)) return "Must be a number.";
      if (inp.min !== undefined && n < inp.min) return `Must be at least ${inp.min}.`;
      if (inp.max !== undefined && n > inp.max) return `Must be at most ${inp.max}.`;
      break;
    }
    case "enum":
      if (!inp.options.includes(String(val)))
        return `Must be one of: ${inp.options.join(", ")}.`;
      break;
    case "string":
      if (inp.pattern) {
        try {
          if (!new RegExp(inp.pattern).test(String(val)))
            return `Does not match the required format.`;
        } catch {
          /* invalid regex — skip client check */
        }
      }
      break;
  }
  return undefined;
}
