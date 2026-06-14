import { useEffect, useMemo, useState } from "react";

import type {
  ComponentConfig,
  ComponentScaling,
  KEDATrigger,
} from "../types";

// ComponentConfigEditor edits per-component resources / envFrom / KEDA scaling /
// env overrides, at the app level and per environment.
//
// Two modes:
//   - onSave (app-detail): shows a "Save component config" button.
//   - onChange (create form): controlled — emits the pruned maps on every edit
//     and hides the Save button so the parent submits with the rest of the form.

interface Props {
  components: string[];
  environments: string[];
  componentConfigs: Record<string, ComponentConfig>;
  envComponents: Record<string, Record<string, ComponentConfig>>;
  saving?: boolean;
  onSave?: (
    componentConfigs: Record<string, ComponentConfig>,
    envComponents: Record<string, Record<string, ComponentConfig>>,
  ) => void;
  onChange?: (
    componentConfigs: Record<string, ComponentConfig>,
    envComponents: Record<string, Record<string, ComponentConfig>>,
  ) => void;
}

const APP_SCOPE = "__app__";

export function ComponentConfigEditor({
  components,
  environments,
  componentConfigs,
  envComponents,
  saving,
  onSave,
  onChange,
}: Props) {
  const [appCfg, setAppCfg] = useState<Record<string, ComponentConfig>>(
    () => structuredClone(componentConfigs ?? {}),
  );
  const [envCfg, setEnvCfg] = useState<
    Record<string, Record<string, ComponentConfig>>
  >(() => structuredClone(envComponents ?? {}));
  const [scope, setScope] = useState<string>(APP_SCOPE);
  const [component, setComponent] = useState<string>(components[0] ?? "");

  // Controlled mode: emit pruned maps to the parent on every edit.
  useEffect(() => {
    if (onChange) onChange(prune(appCfg), pruneEnv(envCfg));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [appCfg, envCfg]);

  // The config object for the current (scope, component); undefined when unset.
  const current: ComponentConfig = useMemo(() => {
    if (scope === APP_SCOPE) return appCfg[component] ?? {};
    return envCfg[scope]?.[component] ?? {};
  }, [scope, component, appCfg, envCfg]);

  function update(next: ComponentConfig) {
    if (scope === APP_SCOPE) {
      setAppCfg((p) => ({ ...p, [component]: next }));
    } else {
      setEnvCfg((p) => ({
        ...p,
        [scope]: { ...(p[scope] ?? {}), [component]: next },
      }));
    }
  }

  if (components.length === 0) return null;

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        <label className="text-xs font-medium text-gray-500">Scope</label>
        <select
          value={scope}
          onChange={(e) => setScope(e.target.value)}
          className="rounded-md border border-gray-300 px-2 py-1 text-sm"
        >
          <option value={APP_SCOPE}>App default</option>
          {environments.map((e) => (
            <option key={e} value={e}>
              {e} (override)
            </option>
          ))}
        </select>
        <label className="text-xs font-medium text-gray-500">Component</label>
        <select
          value={component}
          onChange={(e) => setComponent(e.target.value)}
          className="rounded-md border border-gray-300 px-2 py-1 text-sm"
        >
          {components.map((c) => (
            <option key={c} value={c}>
              {c}
            </option>
          ))}
        </select>
        {scope !== APP_SCOPE && (
          <span className="text-xs text-gray-400">
            Only set fields override the app default for {scope}.
          </span>
        )}
      </div>

      <div className="space-y-5 rounded-lg border border-gray-200 p-4">
        <ResourcesEditor cfg={current} onChange={update} />
        <EnvFromEditor cfg={current} onChange={update} />
        <ScalingEditor cfg={current} onChange={update} />
        <EnvEditor cfg={current} onChange={update} />
      </div>

      {onSave && !onChange && (
        <div className="flex justify-end">
          <button
            type="button"
            disabled={saving}
            onClick={() => onSave(prune(appCfg), pruneEnv(envCfg))}
            className="rounded-md bg-indigo-600 px-4 py-2 text-sm font-medium text-white hover:bg-indigo-700 disabled:opacity-50"
          >
            {saving ? "Saving…" : "Save component config"}
          </button>
        </div>
      )}
    </div>
  );
}

// ── sub-editors ───────────────────────────────────────────────────────────────

function Section({
  title,
  children,
}: {
  title: string;
  children: React.ReactNode;
}) {
  return (
    <div>
      <h4 className="mb-2 text-xs font-semibold uppercase tracking-wider text-gray-500">
        {title}
      </h4>
      {children}
    </div>
  );
}

function ResourcesEditor({
  cfg,
  onChange,
}: {
  cfg: ComponentConfig;
  onChange: (c: ComponentConfig) => void;
}) {
  const req = cfg.resources?.requests ?? {};
  const lim = cfg.resources?.limits ?? {};
  function set(kind: "requests" | "limits", m: Record<string, string>) {
    const resources = { ...cfg.resources, [kind]: m };
    onChange({ ...cfg, resources });
  }
  return (
    <Section title="Resources (raw)">
      <div className="grid grid-cols-2 gap-4">
        <div>
          <p className="mb-1 text-xs text-gray-400">requests</p>
          <KVRows value={req} onChange={(m) => set("requests", m)} keyPlaceholder="cpu" valuePlaceholder="1700m" />
        </div>
        <div>
          <p className="mb-1 text-xs text-gray-400">limits</p>
          <KVRows value={lim} onChange={(m) => set("limits", m)} keyPlaceholder="memory" valuePlaceholder="5260Mi" />
        </div>
      </div>
    </Section>
  );
}

function EnvFromEditor({
  cfg,
  onChange,
}: {
  cfg: ComponentConfig;
  onChange: (c: ComponentConfig) => void;
}) {
  return (
    <Section title="envFrom">
      <div className="grid grid-cols-2 gap-4">
        <div>
          <p className="mb-1 text-xs text-gray-400">Secret names</p>
          <StringList
            value={cfg.envFromSecrets ?? []}
            onChange={(v) => onChange({ ...cfg, envFromSecrets: v })}
            placeholder="my-secret"
          />
        </div>
        <div>
          <p className="mb-1 text-xs text-gray-400">ConfigMap names</p>
          <StringList
            value={cfg.envFromConfigMaps ?? []}
            onChange={(v) => onChange({ ...cfg, envFromConfigMaps: v })}
            placeholder="my-config"
          />
        </div>
      </div>
    </Section>
  );
}

function ScalingEditor({
  cfg,
  onChange,
}: {
  cfg: ComponentConfig;
  onChange: (c: ComponentConfig) => void;
}) {
  const s: ComponentScaling = cfg.scaling ?? {};
  function setScaling(next: ComponentScaling) {
    onChange({ ...cfg, scaling: next });
  }
  const triggers = s.triggers ?? [];
  return (
    <Section title="Autoscaling (KEDA)">
      <div className="mb-3 flex gap-4">
        <NumField
          label="minReplicas"
          value={s.minReplicas}
          onChange={(n) => setScaling({ ...s, minReplicas: n })}
        />
        <NumField
          label="maxReplicas"
          value={s.maxReplicas}
          onChange={(n) => setScaling({ ...s, maxReplicas: n })}
        />
      </div>
      <p className="mb-1 text-xs text-gray-400">
        Triggers (empty = chart defaults)
      </p>
      <div className="space-y-3">
        {triggers.map((t, i) => (
          <div key={i} className="rounded border border-gray-200 p-2">
            <div className="mb-2 flex items-center gap-2">
              <input
                value={t.type}
                onChange={(e) => {
                  const next = [...triggers];
                  next[i] = { ...t, type: e.target.value };
                  setScaling({ ...s, triggers: next });
                }}
                placeholder="type (cpu, prometheus, cron…)"
                className="flex-1 rounded border border-gray-300 px-2 py-1 text-xs"
              />
              <input
                value={t.metricType ?? ""}
                onChange={(e) => {
                  const next = [...triggers];
                  next[i] = { ...t, metricType: e.target.value || undefined };
                  setScaling({ ...s, triggers: next });
                }}
                placeholder="metricType (optional)"
                className="w-40 rounded border border-gray-300 px-2 py-1 text-xs"
              />
              <button
                type="button"
                onClick={() =>
                  setScaling({ ...s, triggers: triggers.filter((_, j) => j !== i) })
                }
                className="text-xs text-red-600 hover:underline"
              >
                Remove
              </button>
            </div>
            <p className="mb-1 text-[10px] uppercase tracking-wide text-gray-400">
              metadata
            </p>
            <KVRows
              value={t.metadata ?? {}}
              onChange={(m) => {
                const next = [...triggers];
                next[i] = { ...t, metadata: m };
                setScaling({ ...s, triggers: next });
              }}
              keyPlaceholder="value"
              valuePlaceholder="80"
            />
          </div>
        ))}
        <button
          type="button"
          onClick={() =>
            setScaling({
              ...s,
              triggers: [...triggers, { type: "cpu", metadata: {} } as KEDATrigger],
            })
          }
          className="text-xs font-medium text-indigo-600 hover:underline"
        >
          + Add trigger
        </button>
      </div>
    </Section>
  );
}

function EnvEditor({
  cfg,
  onChange,
}: {
  cfg: ComponentConfig;
  onChange: (c: ComponentConfig) => void;
}) {
  return (
    <Section title="Env overrides">
      <KVRows
        value={cfg.env ?? {}}
        onChange={(m) => onChange({ ...cfg, env: m })}
        keyPlaceholder="LOG_LEVEL"
        valuePlaceholder="info"
      />
    </Section>
  );
}

// ── primitives ──────────────────────────────────────────────────────────────

function NumField({
  label,
  value,
  onChange,
}: {
  label: string;
  value?: number;
  onChange: (n: number | undefined) => void;
}) {
  return (
    <label className="text-xs text-gray-500">
      {label}
      <input
        type="number"
        value={value ?? ""}
        onChange={(e) =>
          onChange(e.target.value === "" ? undefined : Number(e.target.value))
        }
        className="mt-1 block w-28 rounded border border-gray-300 px-2 py-1 text-sm"
      />
    </label>
  );
}

function StringList({
  value,
  onChange,
  placeholder,
}: {
  value: string[];
  onChange: (v: string[]) => void;
  placeholder?: string;
}) {
  return (
    <div className="space-y-2">
      {value.map((s, i) => (
        <div key={i} className="flex gap-2">
          <input
            value={s}
            onChange={(e) => {
              const next = [...value];
              next[i] = e.target.value;
              onChange(next);
            }}
            placeholder={placeholder}
            className="flex-1 rounded border border-gray-300 px-2 py-1 text-xs"
          />
          <button
            type="button"
            onClick={() => onChange(value.filter((_, j) => j !== i))}
            className="text-xs text-red-600 hover:underline"
          >
            ✕
          </button>
        </div>
      ))}
      <button
        type="button"
        onClick={() => onChange([...value, ""])}
        className="text-xs font-medium text-indigo-600 hover:underline"
      >
        + Add
      </button>
    </div>
  );
}

function KVRows({
  value,
  onChange,
  keyPlaceholder,
  valuePlaceholder,
}: {
  value: Record<string, string>;
  onChange: (m: Record<string, string>) => void;
  keyPlaceholder?: string;
  valuePlaceholder?: string;
}) {
  // Maintain stable row order by working with entries.
  const rows = Object.entries(value);
  function setRows(next: [string, string][]) {
    const m: Record<string, string> = {};
    for (const [k, v] of next) if (k) m[k] = v;
    onChange(m);
  }
  return (
    <div className="space-y-2">
      {rows.map(([k, v], i) => (
        <div key={i} className="flex gap-2">
          <input
            value={k}
            onChange={(e) => {
              const next = [...rows];
              next[i] = [e.target.value, v];
              setRows(next);
            }}
            placeholder={keyPlaceholder}
            className="w-1/2 rounded border border-gray-300 px-2 py-1 text-xs"
          />
          <input
            value={v}
            onChange={(e) => {
              const next = [...rows];
              next[i] = [k, e.target.value];
              setRows(next);
            }}
            placeholder={valuePlaceholder}
            className="w-1/2 rounded border border-gray-300 px-2 py-1 text-xs"
          />
          <button
            type="button"
            onClick={() => setRows(rows.filter((_, j) => j !== i))}
            className="text-xs text-red-600 hover:underline"
          >
            ✕
          </button>
        </div>
      ))}
      <button
        type="button"
        onClick={() => setRows([...rows, ["", ""]])}
        className="text-xs font-medium text-indigo-600 hover:underline"
      >
        + Add
      </button>
    </div>
  );
}

// ── pruning (drop empty objects so the payload stays clean) ───────────────────

function pruneConfig(c: ComponentConfig): ComponentConfig | undefined {
  const out: ComponentConfig = {};
  if (c.resources) {
    const r = c.resources;
    const res: { requests?: Record<string, string>; limits?: Record<string, string> } = {};
    if (r.requests && Object.keys(r.requests).length) res.requests = r.requests;
    if (r.limits && Object.keys(r.limits).length) res.limits = r.limits;
    if (res.requests || res.limits) out.resources = res;
  }
  if (c.envFromSecrets?.filter(Boolean).length)
    out.envFromSecrets = c.envFromSecrets.filter(Boolean);
  if (c.envFromConfigMaps?.filter(Boolean).length)
    out.envFromConfigMaps = c.envFromConfigMaps.filter(Boolean);
  if (c.scaling) {
    const s: ComponentScaling = {};
    if (c.scaling.minReplicas !== undefined) s.minReplicas = c.scaling.minReplicas;
    if (c.scaling.maxReplicas !== undefined) s.maxReplicas = c.scaling.maxReplicas;
    const trig = (c.scaling.triggers ?? []).filter((t) => t.type);
    if (trig.length) s.triggers = trig;
    if (s.minReplicas !== undefined || s.maxReplicas !== undefined || s.triggers)
      out.scaling = s;
  }
  if (c.env && Object.keys(c.env).length) out.env = c.env;
  return Object.keys(out).length ? out : undefined;
}

function prune(
  m: Record<string, ComponentConfig>,
): Record<string, ComponentConfig> {
  const out: Record<string, ComponentConfig> = {};
  for (const [name, cfg] of Object.entries(m)) {
    const p = pruneConfig(cfg);
    if (p) out[name] = p;
  }
  return out;
}

function pruneEnv(
  m: Record<string, Record<string, ComponentConfig>>,
): Record<string, Record<string, ComponentConfig>> {
  const out: Record<string, Record<string, ComponentConfig>> = {};
  for (const [env, byComp] of Object.entries(m)) {
    const p = prune(byComp);
    if (Object.keys(p).length) out[env] = p;
  }
  return out;
}
