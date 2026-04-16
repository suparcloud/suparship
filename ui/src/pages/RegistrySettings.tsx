import { useEffect, useState } from "react";
import { toast } from "sonner";

import {
  fetchRegistryConfig,
  updateRegistryConfig,
  type RegistryConfig,
} from "../lib/registry";

const inputClass =
  "block w-full rounded-md border border-gray-300 px-3 py-2 text-sm shadow-sm focus:border-blue-500 focus:ring-1 focus:ring-blue-500";

function Field({
  label,
  children,
  help,
}: {
  label: string;
  children: React.ReactNode;
  help?: string;
}) {
  return (
    <label className="block">
      <span className="text-sm font-medium text-gray-700">{label}</span>
      <div className="mt-1">{children}</div>
      {help && <p className="mt-1 text-xs text-gray-400">{help}</p>}
    </label>
  );
}

export function RegistrySettings() {
  const [config, setConfig] = useState<RegistryConfig>({
    enabled: false,
    url: "",
  });
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    async function load() {
      try {
        const res = await fetchRegistryConfig();
        if (!cancelled) setConfig(res.config);
      } catch (err) {
        if (!cancelled)
          setError(err instanceof Error ? err.message : "Failed to load");
      } finally {
        if (!cancelled) setLoading(false);
      }
    }
    load();
    return () => { cancelled = true; };
  }, []);

  const handleSave = async () => {
    setSaving(true);
    try {
      const res = await updateRegistryConfig(config);
      setConfig(res.config);
      toast.success("Registry configuration saved");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Save failed");
    } finally {
      setSaving(false);
    }
  };

  const update = (partial: Partial<RegistryConfig>) => {
    setConfig((prev) => ({ ...prev, ...partial }));
  };

  if (loading) {
    return (
      <div className="space-y-4">
        <div className="h-8 w-48 animate-pulse rounded bg-gray-100" />
        <div className="h-48 animate-pulse rounded-lg bg-gray-50" />
      </div>
    );
  }

  if (error) {
    return (
      <div className="rounded-lg border border-red-200 bg-red-50 p-4">
        <p className="text-sm text-red-700">Failed to load config: {error}</p>
      </div>
    );
  }

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-semibold text-gray-900">Container Registry</h1>
        <p className="mt-1 text-sm text-gray-500">
          Configure a private container registry for pulling app images.
        </p>
      </div>

      <div className="max-w-xl space-y-6 rounded-lg border border-gray-200 bg-white p-6">
        <div className="flex items-center gap-3">
          <label className="relative inline-flex cursor-pointer items-center">
            <input
              type="checkbox"
              className="peer sr-only"
              checked={config.enabled}
              onChange={(e) => update({ enabled: e.target.checked })}
            />
            <div className="peer h-6 w-11 rounded-full bg-gray-200 after:absolute after:left-[2px] after:top-[2px] after:h-5 after:w-5 after:rounded-full after:border after:border-gray-300 after:bg-white after:transition-all after:content-[''] peer-checked:bg-blue-600 peer-checked:after:translate-x-full peer-checked:after:border-white" />
          </label>
          <span className="text-sm font-medium text-gray-700">
            {config.enabled ? "Enabled" : "Disabled"}
          </span>
        </div>

        {config.enabled && (
          <>
            <Field label="Registry URL">
              <input
                className={inputClass}
                value={config.url}
                onChange={(e) => update({ url: e.target.value })}
                placeholder="ghcr.io"
              />
            </Field>

            <Field label="Username" help="Non-secret registry username">
              <input
                className={inputClass}
                value={config.username ?? ""}
                onChange={(e) =>
                  update({ username: e.target.value || undefined })
                }
                placeholder="robot-account"
              />
            </Field>

            <Field
              label="Auth Secret Reference"
              help="K8s Secret name with registry credentials (key: password or .dockerconfigjson)"
            >
              <input
                className={inputClass}
                value={config.authSecretRef ?? ""}
                onChange={(e) =>
                  update({ authSecretRef: e.target.value || undefined })
                }
                placeholder="suparship-registry-credentials"
              />
            </Field>

            <Field
              label="Environments"
              help="Comma-separated list of environments needing imagePullSecrets. Leave empty for all."
            >
              <input
                className={inputClass}
                value={(config.environments ?? []).join(", ")}
                onChange={(e) =>
                  update({
                    environments: e.target.value
                      ? e.target.value.split(",").map((s) => s.trim()).filter(Boolean)
                      : undefined,
                  })
                }
                placeholder="staging, prod"
              />
            </Field>
          </>
        )}

        <div className="pt-2">
          <button
            onClick={handleSave}
            disabled={saving || (config.enabled && !config.url)}
            className="rounded-md bg-blue-600 px-4 py-2 text-sm font-medium text-white shadow-sm hover:bg-blue-700 disabled:opacity-50"
          >
            {saving ? "Saving..." : "Save"}
          </button>
        </div>
      </div>
    </div>
  );
}
